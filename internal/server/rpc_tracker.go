package server

import (
	"context"
	"time"

	"prism/internal/tracker"
)

// trackerDetail bundles a tracker with its rows and recent changes for the UI's single-page view — one
// round trip instead of three.
type trackerDetail struct {
	Tracker tracker.Tracker  `json:"tracker"`
	Rows    []tracker.Row    `json:"rows"`
	Changes []tracker.Change `json:"changes"`
}

func (s *Server) registerTrackers() {
	a := s.App
	tk := func() *tracker.Service { return a.Ext.Trackers }

	rpc(s, "trackers.list", func(ctx context.Context, _ none) ([]tracker.Tracker, error) { return tk().List(ctx) })

	rpc(s, "trackers.get", func(ctx context.Context, r struct {
		Name string `json:"name"`
	}) (*trackerDetail, error) {
		t, err := tk().Get(ctx, r.Name)
		if err != nil {
			return nil, err
		}
		rows, err := tk().Rows(ctx, t.ID, "", true, 500)
		if err != nil {
			return nil, err
		}
		changes, err := tk().Changes(ctx, t.ID, time.Time{}, 100)
		if err != nil {
			return nil, err
		}
		return &trackerDetail{Tracker: *t, Rows: rows, Changes: changes}, nil
	})

	rpc(s, "trackers.create", func(ctx context.Context, r struct {
		Name        string           `json:"name"`
		Description string           `json:"description"`
		Columns     []tracker.Column `json:"columns"`
	}) (*tracker.Tracker, error) {
		return tk().Create(ctx, r.Name, r.Description, r.Columns, "user")
	})

	rpc(s, "trackers.delete", func(ctx context.Context, r struct {
		Name string `json:"name"`
	}) (bool, error) {
		return true, tk().Delete(ctx, r.Name)
	})

	rpc(s, "trackers.row_delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, tk().DeleteRow(ctx, r.ID)
	})

	rpc(s, "trackers.row_retire", func(ctx context.Context, r struct {
		Tracker string `json:"tracker"`
		Key     string `json:"key"`
		Reason  string `json:"reason"`
	}) (bool, error) {
		t, err := tk().Get(ctx, r.Tracker)
		if err != nil {
			return false, err
		}
		_, err = tk().RetireRow(ctx, t.ID, r.Key, r.Reason)
		return err == nil, err
	})
}
