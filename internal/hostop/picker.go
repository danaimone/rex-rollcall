package hostop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/danaimone/rex-rollcall/internal/agent"
	"github.com/danaimone/rex-rollcall/internal/rows"
)

var ErrNotInstalled = errors.New("rex-rollcall not installed (run rex-rollcall install && rex config reload there)")

const Action = "rollcall_op"

func Ask(ctx context.Context, host string, req Request) (Reply, error) {
	req.Protocol = Protocol
	body, _ := json.Marshal(req)
	argv := []string{"--autostart=false"}
	if host != "" {
		argv = append(argv, "-S", host)
	}
	argv = append(argv, "do", Action, "--args", string(body), "--json")
	out, err := exec.CommandContext(ctx, "rex", argv...).Output()
	if ctx.Err() != nil {
		return Reply{}, errors.New("no answer in time")
	}
	if err != nil {
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		if strings.Contains(msg, "not_found") || strings.Contains(msg, "unknown action") || strings.Contains(msg, "no action") {
			return Reply{}, ErrNotInstalled
		}
		if strings.Contains(msg, "rex-rollcall: unknown op") {
			return Reply{}, fmt.Errorf("host runs an older rex-rollcall without %q: upgrade it there", req.Op)
		}
		return Reply{}, errors.New(firstLine(msg))
	}
	var text string
	if err := json.Unmarshal(out, &text); err != nil {
		return Reply{}, fmt.Errorf("module returned %.80q, want a JSON string: rerun rex-rollcall install", out)
	}
	var rep Reply
	if err := json.Unmarshal([]byte(text), &rep); err != nil {
		return Reply{}, fmt.Errorf("bad reply: %w", err)
	}
	if rep.Protocol != Protocol {
		return Reply{}, fmt.Errorf("host runs rex-rollcall %s (protocol %d), this picker speaks %d", rep.Version, rep.Protocol, Protocol)
	}
	return rep, nil
}

type HostRows struct {
	Label     string
	Local     bool
	ServerID  string
	Rows      []rows.Row
	Hits      []Hit
	Warnings  []string
	AppClient string
	Err       error
}

func OnePerServer(rs []HostRows) []HostRows {
	keep := map[string]int{}
	var out []HostRows
	for _, r := range rs {
		if r.ServerID == "" {
			out = append(out, r)
			continue
		}
		i, seen := keep[r.ServerID]
		switch {
		case !seen:
			keep[r.ServerID] = len(out)
			out = append(out, r)
		case preferred(r, out[i]):
			out[i] = r
		}
	}
	return out
}

func preferred(a, b HostRows) bool {
	if (a.Err == nil) != (b.Err == nil) {
		return a.Err == nil
	}
	if a.Local != b.Local {
		return a.Local
	}
	if len(a.Label) != len(b.Label) {
		return len(a.Label) < len(b.Label)
	}
	return a.Label < b.Label
}

func Rows(ctx context.Context, host, label string, req Request) HostRows {
	rep, err := Ask(ctx, host, req)
	if err != nil {
		failed := HostRows{Label: label, Local: host == "", Err: err}
		if ctx.Err() == nil {
			failed.ServerID = serverID(ctx, host)
		}
		return failed
	}
	res := HostRows{Label: label, Local: host == "", ServerID: rep.ServerID, Warnings: rep.Warnings, AppClient: rep.AppClient}
	for _, r := range rep.Rows {
		if r, ok := res.admit(r, rep.AppClient); ok {
			res.Rows = append(res.Rows, r)
		}
	}
	for _, h := range rep.Hits {
		if r, ok := res.admit(h.Row, rep.AppClient); ok && !r.Live() {
			h.Row = r
			res.Hits = append(res.Hits, h)
		}
	}
	return res
}

func (h *HostRows) admit(r rows.Row, appClient string) (rows.Row, bool) {
	if _, ok := agent.Lookup(r.Agent); !ok {
		h.Warnings = append(h.Warnings, fmt.Sprintf("unknown agent %q", r.Agent))
		return r, false
	}
	if r.State < rows.Waiting || r.State > rows.Past || (r.State == rows.Past) == r.Live() || !r.Live() && (r.Past == nil || len(r.Past.Launch) == 0) {
		h.Warnings = append(h.Warnings, fmt.Sprintf("dropped malformed %s row", r.Agent))
		return r, false
	}
	r.Origin.Host = h.Label
	if r.Pane != nil {
		r.Pane.AppClient = appClient
	}
	return r, true
}

func serverID(ctx context.Context, host string) string {
	argv := []string{"--autostart=false"}
	if host != "" {
		argv = append(argv, "-S", host)
	}
	out, err := exec.CommandContext(ctx, "rex", append(argv, "api", "call", "server.status")...).Output()
	if err != nil {
		return ""
	}
	var st struct {
		HostID string `json:"host_id"`
	}
	_ = json.Unmarshal(out, &st)
	return st.HostID
}
