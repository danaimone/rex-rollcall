package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Agent struct {
	ID     string
	Glyph  string
	Color  int
	Wake   string
	Names  []string
	Resume func(id string) []string
	LiveID func(p Proc) string
}

type Proc struct {
	PID       int
	Home      string
	Names     []string
	StatusIDs []string
}

var All = []Agent{
	{
		ID: "claude", Glyph: "✻", Color: 3, Wake: "claude-code", Names: []string{"claude", "claude-code"},
		Resume: func(id string) []string { return []string{"claude", "--resume", id} },
		LiveID: claudeLiveID,
	},
	{
		ID: "codex", Glyph: "◎", Color: 7, Wake: "codex", Names: []string{"codex"},
		Resume: func(id string) []string { return []string{"codex", "resume", id} },
	},
	{
		ID: "opencode", Glyph: "◇", Color: 2, Wake: "opencode", Names: []string{"opencode"},
		Resume: func(id string) []string { return []string{"opencode", "-s", id} },
		LiveID: opencodeLiveID,
	},
	{
		ID: "pi", Glyph: "π", Color: 6, Wake: "pi", Names: []string{"pi"},
		Resume: func(id string) []string { return []string{"pi", "--session", id} },
	},
	{
		ID: "hermes", Glyph: "☿", Color: 5, Wake: "hermes", Names: []string{"hermes"},
		Resume: func(id string) []string { return []string{"hermes", "--resume", id} },
	},
}

func Lookup(id string) (Agent, bool) {
	for _, a := range All {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

func ByWake(wakeID string) (Agent, bool) {
	for _, a := range All {
		if a.Wake == wakeID {
			return a, true
		}
	}
	return Agent{}, false
}

func Detect(p Proc) (Agent, bool) {
	for _, n := range p.Names {
		n = normalize(n)
		for _, a := range All {
			for _, m := range a.Names {
				if n == m {
					return a, true
				}
			}
		}
	}
	return Agent{}, false
}

func normalize(name string) string {
	name = strings.TrimPrefix(filepath.Base(name), "-")
	for _, ext := range []string{".mjs", ".js", ".py"} {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}

func claudeLiveID(p Proc) string {
	b, err := os.ReadFile(filepath.Join(p.Home, ".claude", "sessions", strconv.Itoa(p.PID)+".json"))
	if err != nil {
		return ""
	}
	var s struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s.SessionID
}

func opencodeLiveID(p Proc) string {
	for _, id := range p.StatusIDs {
		if strings.HasPrefix(id, "ses_") {
			return id
		}
	}
	return ""
}
