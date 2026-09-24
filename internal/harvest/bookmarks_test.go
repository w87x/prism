package harvest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"prism/internal/testutil"
)

func fetched(url, title string, status int) string {
	return fmt.Sprintf("URL: %s\nStatus: %d\nTitle: %s\n\nSome page text about %s that goes on and on.", url, status, title, title)
}

// Pages the agents fetched become bookmarks only when they are reusable references: the model chooses, search
// pages, private hosts and error pages never reach it, known bookmarks are not offered again, and a second pass
// with nothing new does not call the model.
func TestBookmarksAreHarvestedFromFetchedPages(t *testing.T) {
	ctx := context.Background()
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, st := testutil.Setup(t, d, fake)
	var sid int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO sessions(agent,kind) VALUES('Scout','task') RETURNING id`).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	add := func(content string) {
		if _, err := d.Pool.Exec(ctx, `INSERT INTO session_messages(session_id,role,content,name) VALUES($1,'tool',$2,'web_fetch')`, sid, content); err != nil {
			t.Fatal(err)
		}
	}
	add(fetched("https://docs.example.com/guide#install", "Example guide", 200))
	add(fetched("https://docs.example.com/guide", "Example guide", 200))
	add(fetched("https://news.example.org/story-1", "A news story", 200))
	add(fetched("https://www.google.com/search?q=cats", "cats - Google", 200))
	add(fetched("http://192.168.1.5/admin", "router", 200))
	add(fetched("https://docs.example.com/missing", "Not found", 404))
	add(fetched("https://protected.example.net/tool", "", 403))
	if _, err := d.Pool.Exec(ctx, `INSERT INTO bookmarks(url,title) VALUES('https://known.example.com/','Known')`); err != nil {
		t.Fatal(err)
	}
	add(fetched("https://known.example.com", "Known", 200))

	calls := 0
	shown := ""
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		calls++
		ms, _ := req["messages"].([]any)
		shown = fmt.Sprint(ms)
		return testutil.Reply{Content: `{"keep":[{"n":1,"title":"Example docs","description":"How to install Example.","keywords":["Example","Docs"]},{"n":3,"title":"Protected tool"},{"n":99,"title":"invented"}]}`}
	}
	var delegated []string
	delegate := func(_ context.Context, title, input string) error { delegated = append(delegated, input); return nil }
	n, err := Bookmarks(ctx, d.Pool, r, st, delegate)
	if err != nil || n != 1 {
		t.Fatalf("added %d err=%v", n, err)
	}
	for _, bad := range []string{"google.com", "192.168", "missing", "known.example"} {
		if strings.Contains(shown, bad) {
			t.Fatalf("%q must not be offered to the model:\n%s", bad, shown)
		}
	}
	if !strings.Contains(shown, "docs.example.com/guide\n") && !strings.Contains(shown, "docs.example.com/guide ") {
		t.Fatalf("the guide (fragment stripped, 2 hits) should be offered: %s", shown)
	}
	if len(delegated) != 1 || !strings.Contains(delegated[0], "protected.example.net/tool") || strings.Contains(delegated[0], "docs.example.com") {
		t.Fatalf("the blocked page must go to an agent, and only it: %v", delegated)
	}
	var title, desc string
	var kw []string
	if err := d.Pool.QueryRow(ctx, `SELECT title,description,keywords FROM bookmarks WHERE url='https://docs.example.com/guide'`).Scan(&title, &desc, &kw); err != nil {
		t.Fatalf("bookmark not saved: %v", err)
	}
	if title != "Example docs" || len(kw) != 2 || kw[0] != "example" {
		t.Fatalf("bookmark = %q %q %v", title, desc, kw)
	}
	if n, err := Bookmarks(ctx, d.Pool, r, st, delegate); err != nil || n != 0 || calls != 1 {
		t.Fatalf("second pass: n=%d err=%v calls=%d", n, err, calls)
	}
}
