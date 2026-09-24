package llm

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Provider struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url"`
	Enabled bool   `json:"enabled"`
	Keys    []Key  `json:"keys,omitempty"`
}

type Key struct {
	ID            int64      `json:"id"`
	ProviderID    int64      `json:"provider_id"`
	Label         string     `json:"label"`
	APIKey        string     `json:"api_key,omitempty"` // only inbound; outbound uses Masked
	Masked        string     `json:"masked"`
	Enabled       bool       `json:"enabled"`
	Priority      int        `json:"priority"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
	LastError     string     `json:"last_error"`
	Uses          int64      `json:"uses"`
}

type Model struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	ProviderID    int64    `json:"provider_id"`
	ModelID       string   `json:"model_id"`
	Kind          string   `json:"kind"`
	ContextWindow int      `json:"context_window"`
	SupportsTools bool     `json:"supports_tools"`
	Vision        bool     `json:"vision"` // the model can look at images
	Temperature   *float64 `json:"temperature,omitempty"`
	MaxOutput     int      `json:"max_output"`
}

type List struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	Models []string `json:"models"`
}

// ProviderPresets are the base URLs offered by the UI.
var ProviderPresets = map[string]string{
	"openai":     "https://api.openai.com/v1",
	"gemini":     "https://generativelanguage.googleapis.com/v1beta/openai",
	"grok":       "https://api.x.ai/v1",
	"openrouter": "https://openrouter.ai/api/v1",
	"lmstudio":   "http://localhost:1234/v1",
	"custom":     "",
}

func Mask(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return strings.Repeat("•", len(k))
	}
	return k[:3] + "…" + k[len(k)-4:]
}

// Store persists providers, keys, models and model lists.
type Store struct {
	db     *pgxpool.Pool
	onEdit func()
}

func NewStore(db *pgxpool.Pool, onEdit func()) *Store { return &Store{db: db, onEdit: onEdit} }

func (s *Store) changed() {
	if s.onEdit != nil {
		s.onEdit()
	}
}

func (s *Store) Providers(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.Query(ctx, `SELECT id,name,kind,base_url,enabled FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		var p Provider
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.BaseURL, &p.Enabled); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keys, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		for _, k := range keys {
			if k.ProviderID == out[i].ID {
				out[i].Keys = append(out[i].Keys, k)
			}
		}
	}
	return out, nil
}

func (s *Store) keys(ctx context.Context) ([]Key, error) {
	rows, err := s.db.Query(ctx, `SELECT id,provider_id,label,api_key,enabled,priority,cooldown_until,last_error,uses FROM provider_keys ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		if err := rows.Scan(&k.ID, &k.ProviderID, &k.Label, &k.APIKey, &k.Enabled, &k.Priority, &k.CooldownUntil, &k.LastError, &k.Uses); err != nil {
			return nil, err
		}
		k.Masked = Mask(k.APIKey)
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) SaveProvider(ctx context.Context, p Provider) (int64, error) {
	if p.BaseURL == "" {
		p.BaseURL = ProviderPresets[p.Kind]
	}
	var id int64
	var err error
	if p.ID == 0 {
		err = s.db.QueryRow(ctx, `INSERT INTO providers(name,kind,base_url,enabled) VALUES($1,$2,$3,$4) RETURNING id`,
			p.Name, p.Kind, p.BaseURL, p.Enabled).Scan(&id)
	} else {
		id = p.ID
		_, err = s.db.Exec(ctx, `UPDATE providers SET name=$2,kind=$3,base_url=$4,enabled=$5 WHERE id=$1`,
			p.ID, p.Name, p.Kind, p.BaseURL, p.Enabled)
	}
	if err == nil {
		s.changed()
	}
	return id, err
}

func (s *Store) DeleteProvider(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM providers WHERE id=$1`, id)
	s.changed()
	return err
}

// SaveKey upserts a key; an empty APIKey on update keeps the stored secret.
func (s *Store) SaveKey(ctx context.Context, k Key) (int64, error) {
	var id int64
	var err error
	if k.ID == 0 {
		err = s.db.QueryRow(ctx, `INSERT INTO provider_keys(provider_id,label,api_key,enabled,priority) VALUES($1,$2,$3,$4,$5) RETURNING id`,
			k.ProviderID, k.Label, k.APIKey, k.Enabled, k.Priority).Scan(&id)
	} else {
		id = k.ID
		_, err = s.db.Exec(ctx, `UPDATE provider_keys SET label=$2, api_key=CASE WHEN $3='' THEN api_key ELSE $3 END,
			enabled=$4, priority=$5, cooldown_until=NULL, last_error='' WHERE id=$1`, k.ID, k.Label, k.APIKey, k.Enabled, k.Priority)
	}
	if err == nil {
		s.changed()
	}
	return id, err
}

func (s *Store) DeleteKey(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM provider_keys WHERE id=$1`, id)
	s.changed()
	return err
}

func (s *Store) Models(ctx context.Context) ([]Model, error) {
	rows, err := s.db.Query(ctx, `SELECT id,name,provider_id,model_id,kind,context_window,supports_tools,vision,temperature,max_output FROM models ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		var t *float32
		if err := rows.Scan(&m.ID, &m.Name, &m.ProviderID, &m.ModelID, &m.Kind, &m.ContextWindow, &m.SupportsTools, &m.Vision, &t, &m.MaxOutput); err != nil {
			return nil, err
		}
		if t != nil {
			f := float64(*t)
			m.Temperature = &f
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) SaveModel(ctx context.Context, m Model) (int64, error) {
	if m.Kind == "" {
		m.Kind = "chat"
	}
	if m.ContextWindow <= 0 {
		m.ContextWindow = 32768
	}
	var id int64
	var err error
	if m.ID == 0 {
		err = s.db.QueryRow(ctx, `INSERT INTO models(name,provider_id,model_id,kind,context_window,supports_tools,vision,temperature,max_output)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
			m.Name, m.ProviderID, m.ModelID, m.Kind, m.ContextWindow, m.SupportsTools, m.Vision, m.Temperature, m.MaxOutput).Scan(&id)
	} else {
		id = m.ID
		_, err = s.db.Exec(ctx, `UPDATE models SET name=$2,provider_id=$3,model_id=$4,kind=$5,context_window=$6,supports_tools=$7,vision=$8,temperature=$9,max_output=$10 WHERE id=$1`,
			m.ID, m.Name, m.ProviderID, m.ModelID, m.Kind, m.ContextWindow, m.SupportsTools, m.Vision, m.Temperature, m.MaxOutput)
	}
	if err == nil {
		s.changed()
	}
	return id, err
}

func (s *Store) DeleteModel(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM models WHERE id=$1`, id)
	s.changed()
	return err
}

func (s *Store) Lists(ctx context.Context) ([]List, error) {
	rows, err := s.db.Query(ctx, `SELECT id,name,kind,models FROM model_lists ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []List
	for rows.Next() {
		var l List
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.Models); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) SaveList(ctx context.Context, l List) (int64, error) {
	if l.Kind == "" {
		l.Kind = "chat"
	}
	if l.Models == nil {
		l.Models = []string{}
	}
	var id int64
	var err error
	if l.ID == 0 {
		err = s.db.QueryRow(ctx, `INSERT INTO model_lists(name,kind,models) VALUES($1,$2,$3) RETURNING id`, l.Name, l.Kind, l.Models).Scan(&id)
	} else {
		id = l.ID
		_, err = s.db.Exec(ctx, `UPDATE model_lists SET name=$2,kind=$3,models=$4 WHERE id=$1`, l.ID, l.Name, l.Kind, l.Models)
	}
	if err == nil {
		s.changed()
	}
	return id, err
}

func (s *Store) DeleteList(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM model_lists WHERE id=$1`, id)
	s.changed()
	return err
}
