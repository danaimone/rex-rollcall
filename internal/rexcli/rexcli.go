package rexcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/danaimone/rex-rollcall/internal/rows"
)

type Anchor struct {
	Session string `json:"session"`
	Block   string `json:"block"`
	Client  string `json:"client"`
}

type Target struct {
	Host      string
	Local     bool
	AppClient string
}

func (t Target) base() []string {
	argv := []string{"--autostart=false"}
	if !t.Local {
		argv = append(argv, "-S", t.Host)
	}
	return argv
}

func run(ctx context.Context, argv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "rex", argv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, errors.New("rex: " + msg)
	}
	return out, nil
}

func Hosts(ctx context.Context) ([]string, error) {
	out, err := run(ctx, append(Target{Local: true}.base(), "hosts", "list", "--json")...)
	if err != nil {
		return nil, err
	}
	var v struct {
		Hosts []struct {
			Host struct {
				Label string `json:"label"`
			} `json:"host"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("rex hosts list: %w", err)
	}
	var labels []string
	for _, h := range v.Hosts {
		if h.Host.Label != "" {
			labels = append(labels, h.Host.Label)
		}
	}
	return labels, nil
}

func selectSession(ctx context.Context, t Target, client, session, window string) error {
	if client == "" {
		return nil
	}
	args := map[string]string{"session_id": session}
	if window != "" {
		args["window_id"] = window
	}
	b, _ := json.Marshal(args)
	_, err := run(ctx, append(t.base(), "-C", client, "do", "session.select", "--args", string(b))...)
	return err
}

func client(t Target, a Anchor) string {
	if t.Local && a.Client != "" {
		return a.Client
	}
	return t.AppClient
}

func Jump(ctx context.Context, t Target, a Anchor, r rows.Row) error {
	p := r.Pane
	if _, err := run(ctx, append(t.base(), "focus", "-s", p.Session, p.Block)...); err != nil {
		return err
	}
	return selectSession(ctx, t, client(t, a), p.Session, p.Window)
}

type Placement int

const (
	Split Placement = iota
	Tab
)

func cwdArgs(r rows.Row) []string {
	if r.Origin.Source == "" && r.Dir != "" {
		return []string{"-c", r.Dir}
	}
	return nil
}

func NewSessionArgv(t Target, r rows.Row) []string {
	argv := append(append([]string{"rex"}, t.base()...), "new", r.Agent)
	argv = append(append(argv, cwdArgs(r)...), "--")
	return append(argv, r.Past.Launch...)
}

func Open(ctx context.Context, t Target, a Anchor, r rows.Row, p Placement) error {
	cwd := cwdArgs(r)
	cmd := append([]string{"--json"}, cwd...)
	cmd = append(append(cmd, "--"), r.Past.Launch...)
	switch {
	case t.Local && p == Split && a.Session != "" && a.Block != "":
		_, err := run(ctx, append(append(t.base(), "split", "-s", a.Session, "-b", a.Block), cmd...)...)
		return err
	case t.Local && a.Session != "":
		out, err := run(ctx, append(append(t.base(), "run", "-s", a.Session), cmd...)...)
		if err != nil {
			return err
		}
		var res struct {
			WindowID string `json:"window_id"`
		}
		_ = json.Unmarshal(out, &res)
		return selectSession(ctx, t, a.Client, a.Session, res.WindowID)
	}
	out, err := run(ctx, append(append(t.base(), "new", r.Agent), cmd...)...)
	if err != nil {
		return err
	}
	var res struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(out, &res); err != nil || res.SessionID == "" {
		return errors.New("rex new: no session id in reply")
	}
	return selectSession(ctx, t, client(t, a), res.SessionID, "")
}

func Do(ctx context.Context, t Target, action string) ([]byte, error) {
	return run(ctx, append(t.base(), "do", action, "--json")...)
}

func Close(ctx context.Context, t Target, r rows.Row) error {
	_, err := run(ctx, append(t.base(), "block", "close", "-s", r.Pane.Session, r.Pane.Block)...)
	return err
}

func Capture(ctx context.Context, t Target, r rows.Row) ([]byte, error) {
	return run(ctx, append(t.base(), "capture", "-s", r.Pane.Session, "-b", r.Pane.Block, "--trim")...)
}

func CommandLine(t Target, r rows.Row, resume []string) string {
	switch {
	case r.Past != nil && r.Origin.Source != "":
		return Quote(r.Past.Launch)
	case r.Past != nil:
		return "cd " + Quote([]string{r.Dir}) + " && " + Quote(r.Past.Launch)
	case len(resume) > 0:
		return "cd " + Quote([]string{r.Dir}) + " && " + Quote(resume)
	}
	return Quote(append(append([]string{"rex"}, t.base()...), "focus", "-s", r.Pane.Session, r.Pane.Block))
}

func Quote(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.Trim(a, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@+,%") == "" {
			out[i] = a
			continue
		}
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}
