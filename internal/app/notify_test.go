package app

import (
	"context"
	"testing"

	"prism/internal/hub"
	"prism/internal/notify"
	"prism/internal/settings"
	"prism/internal/testutil"
)

// NotifySeen exists so routine events (e.g. a device reconnecting) don't pile up in the notification
// bell the way Notify's events do — a live tab gets a toast/hover detail instead. Verify that contract
// directly: it must not write to the same store Notify writes to.
func TestNotifySeenDoesNotPersistToTheNotificationBell(t *testing.T) {
	d := testutil.DB(t)
	a := &App{DB: d, Settings: settings.New(d.Pool), Notifs: &notify.Store{DB: d.Pool}, Hub: hub.New()}
	a.ready.Store(true)

	a.NotifySeen("connection", map[string]any{"addr": "10.0.0.5:1234"})

	items, _, err := a.Notifs.List(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("NotifySeen wrote %d item(s) to the notification bell — it's meant to bypass it entirely: %+v", len(items), items)
	}
}

// Contrast case: Notify (used for things that actually deserve a permanent record, like a denied
// connection) still persists as before — this isn't a regression from adding NotifySeen.
func TestNotifyStillPersists(t *testing.T) {
	d := testutil.DB(t)
	a := &App{DB: d, Settings: settings.New(d.Pool), Notifs: &notify.Store{DB: d.Pool}, Hub: hub.New()}
	a.ready.Store(true)

	a.Notify("connection", "warning", "Blocked connection attempt", "bad token — from 10.0.0.5:1234")

	items, _, err := a.Notifs.List(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected Notify to persist exactly 1 item, got %d", len(items))
	}
}
