package rows

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/danaimone/rex-rollcall/internal/termtext"
)

type Origin struct {
	Host   string `json:"host"`
	Source string `json:"source,omitempty"`
}

type State int

const (
	Waiting State = iota
	Errored
	Working
	Idle
	Past
)

func (s State) String() string {
	return [...]string{"waiting", "error", "working", "idle", "past"}[s]
}

type Pane struct {
	Session   string `json:"session"`
	Window    string `json:"window"`
	Block     string `json:"block"`
	AppClient string `json:"app_client,omitempty"`
	PID       int    `json:"pid"`
}

type PastSession struct {
	Key      string   `json:"key"`
	Messages int      `json:"messages"`
	Launch   []string `json:"launch"`
}

type Row struct {
	Origin  Origin       `json:"origin"`
	Agent   string       `json:"agent"`
	State   State        `json:"state"`
	ID      string       `json:"id,omitempty"`
	User    string       `json:"user"`
	Title   string       `json:"title"`
	Dir     string       `json:"dir"`
	Updated time.Time    `json:"updated"`
	Pane    *Pane        `json:"pane,omitempty"`
	Past    *PastSession `json:"past,omitempty"`
}

func (r Row) Live() bool { return r.Pane != nil }

type liveKey struct {
	origin Origin
	agent  string
	id     string
}

func Merge(perHost [][]Row) []Row {
	running := map[liveKey]bool{}
	for _, rs := range perHost {
		for _, r := range rs {
			if r.Live() && r.ID != "" {
				running[liveKey{r.Origin, r.Agent, r.ID}] = true
			}
		}
	}
	var out []Row
	for _, rs := range perHost {
		for _, r := range rs {
			if !r.Live() && running[liveKey{r.Origin, r.Agent, r.ID}] {
				continue
			}
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b Row) int {
		switch {
		case Less(a, b):
			return -1
		case Less(b, a):
			return 1
		}
		return 0
	})
	return out
}

func Less(a, b Row) bool {
	if a.State != b.State {
		return a.State < b.State
	}
	if !a.Updated.Equal(b.Updated) {
		return a.Updated.After(b.Updated)
	}
	if a.Origin.Host != b.Origin.Host {
		return a.Origin.Host < b.Origin.Host
	}
	if a.Origin.Source != b.Origin.Source {
		return a.Origin.Source < b.Origin.Source
	}
	return a.ID < b.ID
}

type Scope int

const (
	All Scope = iota
	LiveOnly
	PastOnly
)

func (s Scope) Next() Scope { return (s + 1) % 3 }

func (s Scope) String() string { return [...]string{"all", "live", "past"}[s] }

func (r Row) Brief() bool {
	return !r.Live() && r.Past != nil && r.Past.Messages < briefMessages
}

const briefMessages = 5

func Filter(rs []Row, scope Scope, host string, brief bool) []int {
	var keep []int
	for i, r := range rs {
		if scope == LiveOnly && !r.Live() || scope == PastOnly && r.Live() {
			continue
		}
		if host != "" && r.Origin.Host != host {
			continue
		}
		if !brief && r.Brief() {
			continue
		}
		keep = append(keep, i)
	}
	return keep
}

func Hosts(rs []Row, local string) []string {
	seen := map[string]bool{local: true}
	var others []string
	for _, r := range rs {
		if !seen[r.Origin.Host] {
			seen[r.Origin.Host] = true
			others = append(others, r.Origin.Host)
		}
	}
	slices.Sort(others)
	return append([]string{local}, others...)
}

type Look struct {
	Glyph string
	Color int
}

type Layout struct {
	Width    int
	Self     string
	ShowHost bool
	WhoWidth int
	DirWidth int
}

const (
	dirMax = 18
	whoMax = 16
	agoW   = 3
)

func NewLayout(rs []Row, self string, width int) Layout {
	l := Layout{Width: width, Self: self}
	hosts := map[string]bool{}
	for _, r := range rs {
		hosts[r.Origin.Host] = true
	}
	l.ShowHost = len(hosts) > 1
	for _, r := range rs {
		l.WhoWidth = max(l.WhoWidth, min(whoMax, termtext.Width(l.who(r))))
		l.DirWidth = max(l.DirWidth, min(dirMax, termtext.Width(dirName(r.Dir))))
	}
	if l.titleRoom() < titleMin {
		l.WhoWidth = 0
	}
	if l.titleRoom() < titleMin {
		l.DirWidth = 0
	}
	return l
}

const (
	lead     = 4
	gapW     = 2
	titleMin = 24
)

func (l Layout) metaWidth() int {
	w := agoW
	for _, c := range []int{l.DirWidth, l.WhoWidth} {
		if c > 0 {
			w += gapW + c
		}
	}
	return w
}

func (l Layout) titleRoom() int { return l.Width - lead - gapW - l.metaWidth() }

func (l Layout) who(r Row) string {
	var parts []string
	if l.ShowHost {
		parts = append(parts, r.Origin.Host)
	}
	if r.Origin.Source != "" {
		parts = append(parts, r.Origin.Source)
	} else if r.User != "" && r.User != l.Self {
		parts = append(parts, r.User)
	}
	return strings.Join(parts, "/")
}

func dirName(dir string) string {
	switch d := path.Clean(dir); {
	case dir == "":
		return ""
	case d == "/root", path.Dir(d) == "/home", path.Dir(d) == "/Users":
		return "~"
	default:
		return path.Base(d)
	}
}

const (
	dim   = "\x1b[2m"
	bold  = "\x1b[1m"
	reset = "\x1b[0m"
)

func SGR(color int) string {
	if color >= 8 {
		return fmt.Sprintf("\x1b[%dm", 90+color-8)
	}
	return fmt.Sprintf("\x1b[%dm", 30+color)
}

var stateColor = map[State]int{Waiting: 3, Errored: 1, Working: 4, Idle: 8}

func Render(r Row, now time.Time, look Look, l Layout) string {
	dot := " "
	if r.Live() {
		dot = SGR(stateColor[r.State]) + "●" + reset
	}
	glyph := SGR(look.Color) + look.Glyph + reset
	var meta []string
	meta = append(meta, termtext.PadLeft(Ago(now, r.Updated), agoW))
	if l.DirWidth > 0 {
		meta = append(meta, termtext.Pad(termtext.Truncate(dirName(r.Dir), l.DirWidth), l.DirWidth))
	}
	if l.WhoWidth > 0 {
		meta = append(meta, termtext.Pad(termtext.Truncate(l.who(r), l.WhoWidth), l.WhoWidth))
	}
	tail := strings.TrimRight(strings.Join(meta, strings.Repeat(" ", gapW)), " ")
	room := l.titleRoom()
	title := oneLine(r.Title)
	if room < 1 {
		return fmt.Sprintf("%s %s %s", dot, glyph, styleTitle(r, termtext.Truncate(title, max(1, l.Width-lead))))
	}
	title = termtext.Truncate(title, room)
	gap := strings.Repeat(" ", room-termtext.Width(title)+gapW)
	return fmt.Sprintf("%s %s %s%s%s%s%s", dot, glyph, styleTitle(r, title), gap, dim, tail, reset)
}

func styleTitle(r Row, title string) string {
	if r.Live() {
		return bold + title + reset
	}
	return title
}

func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "(untitled)"
	}
	return s
}

func Ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return "?"
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	return fmt.Sprintf("%dw", int(d/(7*24*time.Hour)))
}
