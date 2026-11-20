package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/danaimone/rex-rollcall/internal/rexcli"
	"github.com/danaimone/rex-rollcall/internal/rows"
)

func TestPaletteListDescribesLiveAndPastRows(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	g := gathered{
		Local:   "pve",
		Clients: map[string]string{"mac": "client:mac-app"},
		Rows: []rows.Row{
			{
				Origin: rows.Origin{Host: "mac"}, Agent: "claude", State: rows.Waiting, User: "me",
				Title: "Fix the\nlogin redirect", Dir: "/Users/me/src/web", Updated: now.Add(-4 * time.Minute),
				Pane: &rows.Pane{Session: "session:1", Window: "window:1", Block: "block:1"},
			},
			{
				Origin: rows.Origin{Host: "pve", Source: "root"}, Agent: "hermes", State: rows.Past, User: "root",
				Title: "Morning brief", Dir: "/root", Updated: now.Add(-3 * time.Hour),
				Past: &rows.PastSession{Key: "hermes:1", Messages: 12, Launch: []string{"sudo", "-u", "root", "hermes", "--resume", "1"}},
			},
		},
	}
	list, err := newPaletteList(g, "me", now)
	if err != nil {
		t.Fatal(err)
	}
	want := []paletteItem{
		{ID: "0", Title: "Fix the login redirect", Subtitle: "claude · mac · /Users/me/src/web", Icon: "terminal", Detail: "waiting · 4m"},
		{ID: "1", Title: "Morning brief", Subtitle: "hermes · pve/root · /root", Icon: "clock", Detail: "3h"},
	}
	if len(list.Items) != len(want) {
		t.Fatalf("got %d items, want %d", len(list.Items), len(want))
	}
	for i, w := range want {
		if list.Items[i] != w {
			t.Errorf("item %d = %+v, want %+v", i, list.Items[i], w)
		}
	}

	var c paletteChoice
	if err := json.Unmarshal([]byte(list.Choices["0"]), &c); err != nil {
		t.Fatal(err)
	}
	if c.Row.Pane == nil || c.Row.Pane.Block != "block:1" {
		t.Errorf("choice 0 names pane %+v, want block:1", c.Row.Pane)
	}
	if want := (rexcli.Target{Host: "mac", AppClient: "client:mac-app"}); c.Target != want {
		t.Errorf("choice 0 targets %+v, want %+v", c.Target, want)
	}
	if err := json.Unmarshal([]byte(list.Choices["1"]), &c); err != nil {
		t.Fatal(err)
	}
	if !c.Target.Local || c.Row.Past == nil || len(c.Row.Past.Launch) != 6 {
		t.Errorf("choice 1 = %+v, want the local root resume command", c)
	}
}

func TestPaletteListFitsWhatRexPickAccepts(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	g := gathered{Local: "pve", Clients: map[string]string{}}
	g.Rows = append(g.Rows, rows.Row{
		Origin: rows.Origin{Host: "pve"}, Agent: "pi", State: rows.Past, Title: "one-shot probe", Updated: now,
		Past: &rows.PastSession{Key: "pi:short", Messages: 2, Launch: []string{"pi"}},
	})
	for i := range 2100 {
		g.Rows = append(g.Rows, rows.Row{
			Origin: rows.Origin{Host: "pve"}, Agent: "pi", State: rows.Past, Title: fmt.Sprint("session ", i), Updated: now,
			Past: &rows.PastSession{Key: fmt.Sprint("pi:", i), Messages: 12, Launch: []string{"pi"}},
		})
	}
	list, err := newPaletteList(g, "me", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2000 {
		t.Fatalf("got %d items, want the 2000 that rex.ui.pick accepts", len(list.Items))
	}
	seen := map[string]bool{}
	for _, it := range list.Items {
		if seen[it.ID] {
			t.Fatalf("id %q repeats; rex.ui.pick rejects a repeated id", it.ID)
		}
		seen[it.ID] = true
		if _, ok := list.Choices[it.ID]; !ok {
			t.Fatalf("item %q has no choice to open", it.ID)
		}
		if it.Title == "one-shot probe" {
			t.Fatal("a past session with fewer than 5 messages is listed")
		}
	}
}

func TestPalettePlaceholderCarriesHostWarnings(t *testing.T) {
	list, err := newPaletteList(gathered{Warnings: []string{"mini: no answer in time"}}, "me", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if want := "Filter sessions · ⚠ mini: no answer in time"; list.Placeholder != want {
		t.Errorf("placeholder = %q, want %q", list.Placeholder, want)
	}
}
