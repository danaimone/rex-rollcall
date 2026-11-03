package rows

import (
	"fmt"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/danaimone/rex-rollcall/internal/termtext"
)

var t0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func live(host, agent, id string, s State, ago time.Duration) Row {
	return Row{Origin: Origin{Host: host}, Agent: agent, ID: id, State: s, Updated: t0.Add(-ago), Pane: &Pane{Block: "b-" + id}}
}

func past(o Origin, agent, id string, ago time.Duration) Row {
	return Row{Origin: o, Agent: agent, ID: id, State: Past, Updated: t0.Add(-ago), Past: &PastSession{Key: agent + ":" + id}}
}

func ids(rs []Row) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func TestLess(t *testing.T) {
	pve := Origin{Host: "pve"}
	tests := []struct {
		name string
		a, b Row
		want bool
	}{
		{"waiting before error", live("pve", "claude", "w", Waiting, time.Hour), live("pve", "claude", "e", Errored, 0), true},
		{"error before working", live("pve", "claude", "e", Errored, time.Hour), live("pve", "claude", "k", Working, 0), true},
		{"working before idle", live("pve", "claude", "k", Working, time.Hour), live("pve", "claude", "i", Idle, 0), true},
		{"idle live before newer past", live("pve", "claude", "i", Idle, 48*time.Hour), past(pve, "claude", "p", 0), true},
		{"past never before live", past(pve, "claude", "p", 0), live("pve", "claude", "i", Idle, 48*time.Hour), false},
		{"same state: newer first", past(pve, "pi", "new", time.Minute), past(pve, "pi", "old", time.Hour), true},
		{"same state: older not first", past(pve, "pi", "old", time.Hour), past(pve, "pi", "new", time.Minute), false},
		{"tie broken by host", past(Origin{Host: "a"}, "pi", "x", time.Hour), past(Origin{Host: "b"}, "pi", "x", time.Hour), true},
		{"tie broken by source", past(Origin{Host: "pve"}, "pi", "x", time.Hour), past(Origin{Host: "pve", Source: "root"}, "pi", "x", time.Hour), true},
		{"identical rows are not less", past(pve, "pi", "x", time.Hour), past(pve, "pi", "x", time.Hour), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Less(tt.a, tt.b); got != tt.want {
				t.Errorf("Less = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMerge(t *testing.T) {
	pve := Origin{Host: "pve"}
	root := Origin{Host: "pve", Source: "root"}
	tests := []struct {
		name    string
		perHost [][]Row
		want    []string
	}{
		{
			name: "rows from all hosts in state then recency order",
			perHost: [][]Row{
				{past(pve, "claude", "p-old", 72*time.Hour), live("pve", "claude", "idle", Idle, 0)},
				{live("mac", "codex", "working", Working, 5*time.Minute), past(Origin{Host: "mac"}, "pi", "p-new", time.Hour)},
				{live("mini", "opencode", "waiting", Waiting, 2*time.Hour), live("mini", "claude", "err", Errored, 0)},
			},
			want: []string{"waiting", "err", "working", "idle", "p-new", "p-old"},
		},
		{
			name: "past row hidden while the same session runs on that host",
			perHost: [][]Row{
				{live("pve", "claude", "s1", Working, 0), past(pve, "claude", "s1", time.Minute), past(pve, "claude", "s2", time.Hour)},
			},
			want: []string{"s1", "s2"},
		},
		{
			name: "same id on another host or source or agent is a different session",
			perHost: [][]Row{
				{live("pve", "claude", "s1", Idle, 0), past(root, "claude", "s1", time.Minute)},
				{past(Origin{Host: "mac"}, "claude", "s1", 2*time.Minute)},
				{past(pve, "pi", "s1", 3*time.Minute)},
			},
			want: []string{"s1", "s1", "s1", "s1"},
		},
		{
			name: "live row without a native id hides nothing",
			perHost: [][]Row{
				{live("pve", "codex", "", Idle, 0), past(pve, "codex", "", time.Minute)},
			},
			want: []string{"", ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Merge(tt.perHost)
			if !slices.Equal(ids(got), tt.want) {
				t.Errorf("Merge ids = %q, want %q", ids(got), tt.want)
			}
		})
	}
}

func TestMergeKeepsLiveOverPast(t *testing.T) {
	pve := Origin{Host: "pve"}
	got := Merge([][]Row{{past(pve, "claude", "s1", 0), live("pve", "claude", "s1", Idle, time.Hour)}})
	if len(got) != 1 || !got[0].Live() {
		t.Fatalf("Merge = %+v, want only the live row", got)
	}
}

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestRenderFitsWidth(t *testing.T) {
	titles := []string{
		"short",
		"A very long title that keeps going well past any reasonable column budget for a row",
		"日本語のタイトルがとても長い場合でもきちんと切り詰められること",
		"emoji 🔒🚀✨ in the middle of a long title that must be cut cleanly",
		"",
	}
	rs := []Row{}
	for i, title := range titles {
		r := past(Origin{Host: "pve", Source: "root"}, "claude", fmt.Sprint(i), time.Hour)
		r.Title, r.Dir = title, "/home/dan/src/rex-rollcall"
		rs = append(rs, r)
	}
	rs = append(rs, live("mac", "codex", "l", Working, time.Minute))
	for _, width := range []int{40, 80, 120, 200} {
		l := NewLayout(rs, "dan", width)
		for _, r := range rs {
			line := sgr.ReplaceAllString(Render(r, t0, Look{Glyph: "✻", Color: 3}, l), "")
			if w := termtext.Width(line); w > width {
				t.Errorf("width %d: row %q renders %d columns: %q", width, r.Title, w, line)
			}
		}
	}
}

func TestFilterHidesBriefPastSessionsUnlessAsked(t *testing.T) {
	pve := Origin{Host: "pve"}
	brief := past(pve, "pi", "brief", time.Hour)
	brief.Past.Messages = 2
	long := past(pve, "claude", "long", time.Hour)
	long.Past.Messages = 40
	running := live("pve", "pi", "running", Idle, 0)
	rs := []Row{brief, long, running}
	if got := Filter(rs, All, "", false); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("Filter without brief = %v, want [1 2]", got)
	}
	if got := Filter(rs, All, "", true); !slices.Equal(got, []int{0, 1, 2}) {
		t.Errorf("Filter with brief = %v, want [0 1 2]", got)
	}
}
