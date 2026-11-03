package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danaimone/rex-rollcall/internal/rows"
)

const demoNote = "Demo mode: these sessions are made up, and keys that act do nothing."

type demoSession struct {
	row    rows.Row
	screen string
	turns  []string
}

func demoSessions(now time.Time) []demoSession {
	home, _ := os.UserHomeDir()
	me := os.Getenv("USER")
	at := func(d time.Duration) time.Time { return now.Add(-d) }
	live := func(host, agentID string, state rows.State, title, dir string, ago time.Duration, screen string) demoSession {
		return demoSession{
			row: rows.Row{
				Origin: rows.Origin{Host: host}, Agent: agentID, State: state, User: me,
				Title: title, Dir: dir, Updated: at(ago),
				Pane: &rows.Pane{Session: "session:demo", Window: "window:demo", Block: "block:demo-" + agentID},
			},
			screen: screen,
		}
	}
	past := func(host, source, user, agentID, title, dir string, ago time.Duration, turns ...string) demoSession {
		return demoSession{
			row: rows.Row{
				Origin: rows.Origin{Host: host, Source: source}, Agent: agentID, State: rows.Past, User: user,
				Title: title, Dir: dir, Updated: at(ago),
				Past: &rows.PastSession{Key: "demo", Messages: 12, Launch: []string{agentID}},
			},
			turns: turns,
		}
	}
	return []demoSession{
		live("laptop", "claude", rows.Waiting, "Fix the login redirect after SSO", filepath.Join(home, "src/acme-web"), 0,
			"● Safari drops the session cookie on the SSO redirect because SameSite is Strict.\n\n"+
				"  Update(src/auth/cookie.ts)\n  ⎿  Updated with 1 addition and 1 removal\n"+
				"       sameSite: \"lax\",\n\n"+
				"● Tests pass. Ready to open a PR?\n\n"+
				"╭──────────────────────────────────────────────╮\n"+
				"│ > yes, open it                               │\n"+
				"╰──────────────────────────────────────────────╯"),
		live("devbox", "codex", rows.Working, "Speed up the nightly export job", "/srv/exports", 2*time.Minute,
			"• Profiling export.py on a 1-day sample\n"+
				"  └ 82% of the time is in to_csv(), one row at a time\n\n"+
				"• Rewriting the writer to stream batches of 10k rows\n\n"+
				"◦ Running tests (14s • esc to interrupt)"),
		live("laptop", "opencode", rows.Idle, "Draft release notes for 2.4", filepath.Join(home, "src/acme-cli"), 25*time.Minute,
			"Release notes are in CHANGELOG.md under 2.4.0.\n\n"+
				"  • New: `acme sync --dry-run`\n  • Fixed: config reload on Windows\n\n"+
				"> "),
		past("devbox", "", me, "pi", "Write a primes benchmark", "/srv/bench", 27*time.Minute,
			"User", "Write a quick benchmark for the three prime sieves in bench/.",
			"Assistant", "- 🔧 bash: go test -bench . ./bench/...",
			"Assistant", "Segmented sieve wins above 10^7: **3.1x** faster than the plain sieve, with 40x less memory."),
		past("devbox", "root", "root", "hermes", "Morning brief", "/root", 3*time.Hour,
			"User", "What's on my calendar today, and is anything on fire?",
			"Assistant", "Three meetings, the first at 10:00. Nothing on fire: all 12 services are healthy and backups ran at 03:00."),
		past("laptop", "", me, "claude", "Add dark mode to the settings page", filepath.Join(home, "src/acme-web"), 5*time.Hour,
			"User", "Add a dark mode toggle to settings, following the system theme by default.",
			"Assistant", "- 🔧 Edit: src/settings/Theme.tsx",
			"Assistant", "Done. The toggle has **System**, **Light** and **Dark**, and the choice is saved per user."),
		past("laptop", "", me, "codex", "Migrate the API client to fetch", filepath.Join(home, "src/acme-sdk"), 26*time.Hour,
			"User", "Replace axios with fetch in the SDK and keep the retry behavior.",
			"Assistant", "Migrated all 23 call sites. Retries keep the same backoff, and the bundle is 14 KB smaller."),
		past("devbox", "", me, "opencode", "Investigate flaky checkout test", "/srv/acme-shop", 3*24*time.Hour,
			"User", "checkout.spec fails about 1 in 20 runs on CI. Why?",
			"Assistant", "The test reads the cart before the price request settles. Waiting on the request fixes it: **0 failures in 200 runs**."),
	}
}

func demoGathered() gathered {
	g := gathered{Local: "laptop", Clients: map[string]string{}}
	for _, d := range demoSessions(time.Now()) {
		g.Rows = append(g.Rows, d.row)
	}
	return g
}

func demoPreview(r rows.Row) string {
	for _, d := range demoSessions(r.Updated) {
		if d.row.Title != r.Title {
			continue
		}
		if r.Live() {
			return d.screen
		}
		var b strings.Builder
		stamp := r.Updated.Add(-10 * time.Minute)
		for i := 0; i+1 < len(d.turns); i += 2 {
			stamp = stamp.Add(2 * time.Minute)
			fmt.Fprintf(&b, "### [seq %d] %s · %s\n\n%s\n\n", i/2+1, d.turns[i], stamp.Format("2006-01-02 15:04:05"), d.turns[i+1])
		}
		return b.String()
	}
	return ""
}
