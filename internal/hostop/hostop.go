package hostop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"

	"time"

	"github.com/danaimone/rex-rollcall/internal/agent"
	"github.com/danaimone/rex-rollcall/internal/guard"
	"github.com/danaimone/rex-rollcall/internal/rows"
	"github.com/danaimone/rex-rollcall/internal/wake"
	"golang.org/x/sys/unix"
)

const Protocol = 1

const (
	OpRows   = "rows"
	OpShow   = "show"
	OpSearch = "search"
)

const (
	PreviewsNever  = "never"
	PreviewsUnlock = "unlock"

	NoticeLocked = "locked"
	NoticeOff    = "off"
)

type Source struct {
	Name     string   `json:"name"`
	Wrap     []string `json:"wrap"`
	Bin      string   `json:"bin"`
	User     string   `json:"user,omitempty"`
	Previews string   `json:"previews,omitempty"`
}

type Request struct {
	Protocol   int      `json:"protocol"`
	Version    string   `json:"version"`
	Op         string   `json:"op"`
	PastLimit  int      `json:"past_limit,omitempty"`
	DeadlineMS int      `json:"deadline_ms,omitempty"`
	Source     string   `json:"source,omitempty"`
	Key        string   `json:"key,omitempty"`
	Messages   int      `json:"messages,omitempty"`
	Tail       int      `json:"tail,omitempty"`
	Query      string   `json:"query,omitempty"`
	Agent      string   `json:"agent,omitempty"`
	Limit      int      `json:"limit,omitempty"`
	Blocks     []Block  `json:"blocks,omitempty"`
	Sources    []Source `json:"sources,omitempty"`
	AppClient  string   `json:"app_client,omitempty"`
	ServerID   string   `json:"server_id,omitempty"`
}

type Block struct {
	Session string   `json:"session_id"`
	Window  string   `json:"window_id"`
	Block   string   `json:"block_id"`
	Home    string   `json:"home"`
	Title   string   `json:"title"`
	FG      Process  `json:"fg"`
	Records []Record `json:"records"`
}

type Process struct {
	PID    int    `json:"pid"`
	User   string `json:"user"`
	Cwd    string `json:"cwd"`
	Argv0  string `json:"argv0"`
	Name   string `json:"name"`
	Script string `json:"script"`
	Path   string `json:"path"`
}

type Record struct {
	App       string    `json:"app"`
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
	Owner     struct {
		PID int `json:"pid"`
	} `json:"owner"`
}

type Reply struct {
	Protocol  int        `json:"protocol"`
	Version   string     `json:"version"`
	User      string     `json:"user"`
	AppClient string     `json:"app_client,omitempty"`
	ServerID  string     `json:"server_id,omitempty"`
	Rows      []rows.Row `json:"rows"`
	Hits      []Hit      `json:"hits,omitempty"`
	Warnings  []string   `json:"warnings,omitempty"`
	Text      string     `json:"text,omitempty"`
	Notice    string     `json:"notice,omitempty"`
}

type Hit struct {
	Row      rows.Row  `json:"row"`
	Snippets []Snippet `json:"snippets"`
}

type Snippet struct {
	Seq  int    `json:"seq"`
	Role string `json:"role"`
	Text string `json:"text"`
}

func Serve(ctx context.Context, version string, stdin io.Reader, stdout io.Writer, sourceOnly bool) error {
	var req Request
	if err := json.NewDecoder(io.LimitReader(stdin, 64<<20)).Decode(&req); err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	if !sourceOnly && req.Version != version {
		return fmt.Errorf("module %q does not match binary %q: rerun rex-rollcall install && rex config reload", req.Version, version)
	}
	if req.Protocol != Protocol {
		return fmt.Errorf("picker speaks protocol %d, this binary speaks %d: upgrade rex-rollcall on both", req.Protocol, Protocol)
	}
	if sourceOnly {
		req.Blocks, req.Sources, req.AppClient, req.Source, req.ServerID = nil, nil, "", "", ""
	}
	deadline := time.Duration(min(max(200, req.DeadlineMS), 10000)) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	me, err := user.Current()
	if err != nil {
		return err
	}
	paths := wake.DefaultPaths(me.HomeDir)
	rep := Reply{Protocol: Protocol, Version: version, User: me.Username, AppClient: req.AppClient, ServerID: req.ServerID, Rows: []rows.Row{}}

	switch req.Op {
	case OpRows:
		sources, warns := validSources(req.Sources)
		rep.Warnings = append(rep.Warnings, warns...)
		rep.Rows = append(rep.Rows, liveRows(req.Blocks)...)
		past, err := pastRows(ctx, paths, req.PastLimit, me.Username)
		if err != nil && !errors.Is(err, wake.ErrNoIndex) {
			rep.Warnings = append(rep.Warnings, err.Error())
		}
		if sourceOnly {
			for i := range past {
				past[i].Title = guard.Title(past[i].Title)
			}
		}
		rep.Rows = append(rep.Rows, past...)
		got, warns := gatherSources(ctx, version, sources, req)
		rep.Rows = append(rep.Rows, got...)
		rep.Warnings = append(rep.Warnings, warns...)
	case OpShow:
		if sourceOnly {
			if err := showAsSource(ctx, paths, req, &rep); err != nil {
				return err
			}
			break
		}
		if req.Source != "" {
			sources, _ := validSources(req.Sources)
			s, ok := findSource(sources, req.Source)
			if !ok {
				return fmt.Errorf("unknown source %q", req.Source)
			}
			if s.Previews != PreviewsUnlock {
				rep.Notice = NoticeOff
				break
			}
			sub := Request{Protocol: Protocol, Version: version, Op: OpShow, Key: req.Key, Messages: req.Messages, Tail: req.Tail, DeadlineMS: req.DeadlineMS}
			r, err := runSource(ctx, s, sub)
			if err != nil {
				return err
			}
			rep.Text, rep.Notice = r.Text, r.Notice
			break
		}
		out, err := wake.Show(ctx, paths, req.Key, req.Messages, req.Tail)
		if err != nil {
			return err
		}
		rep.Text = string(out)
	case OpSearch:
		if sourceOnly {
			if err := searchAsSource(ctx, paths, req, me.Username, &rep); err != nil {
				return err
			}
			break
		}
		hits, err := search(ctx, paths, req, me.Username)
		if err != nil && !errors.Is(err, wake.ErrNoIndex) {
			return err
		}
		rep.Hits = hits
		sources, warns := validSources(req.Sources)
		got, more := searchSources(ctx, version, sources, req)
		rep.Hits = append(rep.Hits, got...)
		rep.Warnings = append(append(rep.Warnings, warns...), more...)
	default:
		return fmt.Errorf("unknown op %q", req.Op)
	}
	return json.NewEncoder(stdout).Encode(rep)
}

func showAsSource(ctx context.Context, paths wake.Paths, req Request, rep *Reply) error {
	locked := guard.Unlocked(guard.StampPath, time.Now(), 0) != nil
	if err := guard.Audit(fmt.Sprintf("source show key=%q allowed=%t", req.Key, !locked)); err != nil {
		return err
	}
	if locked {
		rep.Notice = NoticeLocked
		return nil
	}
	out, err := wake.Show(ctx, paths, req.Key, req.Messages, req.Tail)
	if err != nil {
		return err
	}
	rep.Text = guard.Redact(string(out))
	return nil
}

func searchAsSource(ctx context.Context, paths wake.Paths, req Request, username string, rep *Reply) error {
	locked := guard.Unlocked(guard.StampPath, time.Now(), 0) != nil
	if err := guard.Audit(fmt.Sprintf("source search query=%q allowed=%t", req.Query, !locked)); err != nil {
		return err
	}
	if locked {
		rep.Notice = NoticeLocked
		return nil
	}
	hits, err := search(ctx, paths, req, username)
	if err != nil && !errors.Is(err, wake.ErrNoIndex) {
		return err
	}
	for i := range hits {
		hits[i].Row.Title = guard.Title(hits[i].Row.Title)
	}
	rep.Hits = hits
	return nil
}

const (
	matchesPerSearch = 300
	snippetsPerHit   = 3
	snippetWidth     = 240
	defaultHitLimit  = 10
	maxHitLimit      = 50
)

func search(ctx context.Context, paths wake.Paths, req Request, username string) ([]Hit, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = defaultHitLimit
	}
	limit = min(limit, maxHitLimit)
	terms := wake.Terms(req.Query)
	ms, err := wake.Search(ctx, paths, terms, matchesPerSearch)
	if err != nil {
		return nil, err
	}
	var out []Hit
	at := map[string]int{}
	for _, m := range ms {
		i, seen := at[m.Key]
		if !seen {
			r, ok := pastRow(m.Session, username)
			if !ok || len(out) == limit || req.Agent != "" && r.Agent != req.Agent {
				continue
			}
			i = len(out)
			at[m.Key] = i
			out = append(out, Hit{Row: r})
		}
		if len(out[i].Snippets) < snippetsPerHit {
			out[i].Snippets = append(out[i].Snippets, Snippet{Seq: m.Seq, Role: m.Role, Text: guard.Excerpt(m.Text, terms, snippetWidth)})
		}
	}
	return out, nil
}

func searchSources(ctx context.Context, version string, sources []Source, req Request) ([]Hit, []string) {
	var mu sync.Mutex
	var out []Hit
	var warns []string
	var wg sync.WaitGroup
	sub := Request{Protocol: Protocol, Version: version, Op: OpSearch, Query: req.Query, Agent: req.Agent, Limit: req.Limit, DeadlineMS: req.DeadlineMS}
	for _, s := range sources {
		if s.Previews != PreviewsUnlock {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := runSource(ctx, s, sub)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				warns = append(warns, fmt.Sprintf("%s: %v", s.Name, err))
				return
			case r.Notice == NoticeLocked:
				warns = append(warns, s.Name+": search is locked; run rex-rollcall unlock")
				return
			}
			for _, h := range r.Hits {
				if h.Row.Live() || h.Row.Past == nil {
					continue
				}
				h.Row = fromSource(s, h.Row)
				out = append(out, h)
			}
		}()
	}
	wg.Wait()
	return out, warns
}

func fromSource(s Source, row rows.Row) rows.Row {
	row.Origin.Source = s.Name
	if s.User != "" {
		row.User = s.User
	}
	row.Past.Launch = resumeVia(s, row.Dir, row.Past.Launch)
	return row
}

var foldOrder = map[string]rows.State{"blocked": rows.Waiting, "done": rows.Waiting, "error": rows.Errored, "working": rows.Working}

func liveRows(blocks []Block) []rows.Row {
	var out []rows.Row
	for _, b := range blocks {
		state := rows.Idle
		updated := time.Time{}
		names := []string{b.FG.Script, b.FG.Argv0, b.FG.Name, filepath.Base(b.FG.Path)}
		var ids []string
		title := b.Title
		for _, rec := range b.Records {
			if rec.Owner.PID != b.FG.PID {
				continue
			}
			names = append(names, rec.App)
			ids = append(ids, rec.ID)
			if s, ok := foldOrder[rec.State]; ok && s < state {
				state = s
			}
			if rec.UpdatedAt.After(updated) {
				updated = rec.UpdatedAt
			}
			if title == "" {
				title = rec.Title
			}
		}
		a, ok := agent.Detect(agent.Proc{PID: b.FG.PID, Names: names})
		if !ok {
			continue
		}
		if updated.IsZero() {
			updated = time.Now()
		}
		id := ""
		if a.LiveID != nil {
			id = a.LiveID(agent.Proc{PID: b.FG.PID, Home: b.Home, StatusIDs: ids})
		}
		out = append(out, rows.Row{
			Agent: a.ID, State: state, ID: id, User: b.FG.User, Title: title, Dir: b.FG.Cwd, Updated: updated,
			Pane: &rows.Pane{Session: b.Session, Window: b.Window, Block: b.Block, PID: b.FG.PID},
		})
	}
	return out
}

func pastRows(ctx context.Context, p wake.Paths, limit int, username string) ([]rows.Row, error) {
	ss, err := wake.Sessions(ctx, p, max(1, limit))
	if err != nil {
		return nil, err
	}
	var out []rows.Row
	for _, s := range ss {
		if r, ok := pastRow(s, username); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func pastRow(s wake.Session, username string) (rows.Row, bool) {
	a, ok := agent.ByWake(s.Agent)
	if !ok {
		return rows.Row{}, false
	}
	if fi, err := os.Stat(s.Dir); err != nil || !fi.IsDir() || unix.Access(s.Dir, unix.X_OK) != nil {
		return rows.Row{}, false
	}
	return rows.Row{
		Agent: a.ID, State: rows.Past, ID: s.NativeID,
		User: username, Title: s.Title, Dir: s.Dir, Updated: s.Updated,
		Past: &rows.PastSession{Key: s.Key, Messages: s.Messages, Launch: a.Resume(s.NativeID)},
	}, true
}

func validSources(in []Source) ([]Source, []string) {
	var ok []Source
	var warns []string
	seen := map[string]bool{}
	for _, s := range in {
		switch {
		case s.Name == "" || seen[s.Name]:
			warns = append(warns, fmt.Sprintf("source %q: name missing or repeated", s.Name))
		case len(s.Wrap) == 0:
			warns = append(warns, fmt.Sprintf("source %s: empty wrap", s.Name))
		case filepath.Base(s.Wrap[0]) == "ssh":
			warns = append(warns, fmt.Sprintf("source %s: ssh flattens argv; make the machine a Rex host instead", s.Name))
		case !filepath.IsAbs(s.Bin):
			warns = append(warns, fmt.Sprintf("source %s: bin must be an absolute path", s.Name))
		case s.Previews != "" && s.Previews != PreviewsNever && s.Previews != PreviewsUnlock:
			warns = append(warns, fmt.Sprintf("source %s: previews must be %q or %q", s.Name, PreviewsNever, PreviewsUnlock))
		default:
			seen[s.Name] = true
			ok = append(ok, s)
		}
	}
	return ok, warns
}

func findSource(ss []Source, name string) (Source, bool) {
	for _, s := range ss {
		if s.Name == name {
			return s, true
		}
	}
	return Source{}, false
}

func gatherSources(ctx context.Context, version string, sources []Source, req Request) ([]rows.Row, []string) {
	var mu sync.Mutex
	var out []rows.Row
	var warns []string
	var wg sync.WaitGroup
	sub := Request{Protocol: Protocol, Version: version, Op: OpRows, PastLimit: req.PastLimit, DeadlineMS: req.DeadlineMS}
	for _, s := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := runSource(ctx, s, sub)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				warns = append(warns, fmt.Sprintf("%s: %v", s.Name, err))
				return
			}
			for _, w := range r.Warnings {
				warns = append(warns, s.Name+": "+w)
			}
			for _, row := range r.Rows {
				if row.Live() || row.Past == nil {
					continue
				}
				out = append(out, fromSource(s, row))
			}
		}()
	}
	wg.Wait()
	return out, warns
}

func runSource(ctx context.Context, s Source, req Request) (Reply, error) {
	body, _ := json.Marshal(req)
	argv := append(append([]string{}, s.Wrap...), s.Bin, "_op", "--source-only")
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return Reply{}, errors.New("timed out")
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return Reply{}, errors.New(firstLine(msg))
	}
	var r Reply
	if err := json.Unmarshal(out, &r); err != nil {
		return Reply{}, fmt.Errorf("bad reply: %w", err)
	}
	return r, nil
}

const chdirExec = `cd "$1" && shift && if command -v bash >/dev/null 2>&1; then exec bash -lic 'exec "$@"' bash "$@"; fi; exec "$@"`

func resumeVia(s Source, dir string, resume []string) []string {
	argv := append(Interactive(s.Wrap), "sh", "-c", chdirExec, "sh", dir)
	return append(argv, resume...)
}

func Interactive(wrap []string) []string {
	out := []string{}
	if len(wrap) == 0 || filepath.Base(wrap[0]) != "sudo" {
		return append(out, wrap...)
	}
	out = append(out, wrap[0])
	takesValue := map[string]bool{"-u": true, "-g": true, "-C": true, "-D": true, "-h": true, "-p": true, "-r": true, "-t": true, "-U": true, "-T": true}
	inOptions := true
	for i := 1; i < len(wrap); i++ {
		a := wrap[i]
		if inOptions && a == "-n" {
			continue
		}
		out = append(out, a)
		switch {
		case !inOptions:
		case a == "--" || !strings.HasPrefix(a, "-"):
			inOptions = false
		case takesValue[a] && i+1 < len(wrap):
			i++
			out = append(out, wrap[i])
		}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
