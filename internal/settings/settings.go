// Package settings is a small typed key/value store on top of the settings
// table. All user-facing configuration that is not relational lives here.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db    *pgxpool.Pool
	mu    sync.RWMutex
	cache map[string]json.RawMessage
	hooks []func(key string)
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db, cache: map[string]json.RawMessage{}}
}

// OnChange registers a callback fired after every Set/Delete.
func (s *Store) OnChange(fn func(key string)) {
	s.mu.Lock()
	s.hooks = append(s.hooks, fn)
	s.mu.Unlock()
}

// Get decodes the stored value into out. It reports whether the key existed;
// when it did not, out is left untouched so callers can pre-fill defaults.
func (s *Store) Get(ctx context.Context, key string, out any) (bool, error) {
	s.mu.RLock()
	raw, ok := s.cache[key]
	s.mu.RUnlock()
	if !ok {
		err := s.db.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		s.mu.Lock()
		s.cache[key] = raw
		s.mu.Unlock()
	}
	return true, json.Unmarshal(raw, out)
}

// Load returns the value of key, or def when unset.
func Load[T any](ctx context.Context, s *Store, key string, def T) T {
	v := def
	if _, err := s.Get(ctx, key, &v); err != nil {
		return def
	}
	return v
}

func (s *Store) Set(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settings(key,value) VALUES($1,$2)
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, key, b); err != nil {
		return err
	}
	s.mu.Lock()
	s.cache[key] = b
	hooks := append([]func(string){}, s.hooks...)
	s.mu.Unlock()
	for _, h := range hooks {
		h(key)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM settings WHERE key=$1`, key); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, key)
	hooks := append([]func(string){}, s.hooks...)
	s.mu.Unlock()
	for _, h := range hooks {
		h(key)
	}
	return nil
}

// All returns every stored setting.
func (s *Store) All(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.db.Query(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var v json.RawMessage
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
