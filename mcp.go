package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/danaimone/rex-rollcall/internal/agent"
	"github.com/danaimone/rex-rollcall/internal/guard"
	"github.com/danaimone/rex-rollcall/internal/hostop"
	"github.com/danaimone/rex-rollcall/internal/rexcli"
	"github.com/danaimone/rex-rollcall/internal/rows"
	"github.com/danaimone/rex-rollcall/internal/wake"
)

// MCP over stdio: newline-delimited JSON-RPC 2.0.
// https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#stdio
var mcpVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
)

type mcpServer struct {
	settings Settings
	local    string
}

func serveMCP(ctx context.Context, s Settings, in io.Reader, out io.Writer) error {
	m := &mcpServer{settings: s, local: localLabel()}
	r := bufio.NewReaderSize(in, 1<<20)
	enc := json.NewEncoder(out)
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			if resp := m.handle(ctx, line); resp != nil {
				if werr := enc.Encode(resp); werr != nil {
					return werr
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (m *mcpServer) handle(ctx context.Context, line []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{rpcParseError, "parse error: " + err.Error()}}
	}
	if len(req.ID) == 0 {
		return nil
	}
	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" || req.Method == "" {
		resp.Error = &rpcError{rpcInvalidRequest, "want a JSON-RPC 2.0 request"}
		return resp
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		v := mcpVersions[0]
		if slices.Contains(mcpVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "rex-rollcall", "version": version},
			"instructions": "Lists and searches coding-agent sessions on every Rex host. " +
				"Use sessions_search to find past work by content, session_show to read a transcript tail, " +
				"and session_open to get the command that resumes or focuses a session. Secrets are redacted.",
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": mcpTools}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{rpcInvalidParams, err.Error()}
			return resp
		}
		tool, ok := m.tools()[p.Name]
		if !ok {
			resp.Error = &rpcError{rpcInvalidParams, fmt.Sprintf("unknown tool %q", p.Name)}
			return resp
		}
		if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
			p.Arguments = json.RawMessage("{}")
		}
		text, err := tool(ctx, p.Arguments)
		if err != nil {
			resp.Result = toolResult(guard.Redact(err.Error()), true)
		} else {
			resp.Result = toolResult(guard.Redact(text), false)
		}
	default:
		resp.Error = &rpcError{rpcMethodNotFound, fmt.Sprintf("method %q not found", req.Method)}
	}
	return resp
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

type tool func(ctx context.Context, args json.RawMessage) (string, error)

func (m *mcpServer) tools() map[string]tool {
	return map[string]tool{
		"sessions_list":   m.list,
		"sessions_search": m.search,
		"session_show":    m.show,
		"session_open":    m.open,
	}
}

func schema(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func integer(desc string, lo, hi int) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "minimum": lo, "maximum": hi}
}

var (
	hostProp   = str("Rex host label, as returned in each session's host field. Omit for every host.")
	agentProp  = map[string]any{"type": "string", "description": "Agent id.", "enum": agentIDs()}
	keyProp    = str("Wake key of a past session (the key field).")
	sourceProp = str("Source name (the source field), for sessions from another account or container.")
	mcpTools   = []map[string]any{
		{
			"name":        "sessions_list",
			"description": "List live agent panes and past sessions on every Rex host, newest first within each state.",
			"inputSchema": schema(nil, map[string]any{
				"host":          hostProp,
				"agent":         agentProp,
				"state":         map[string]any{"type": "string", "enum": []string{"all", "live", "past"}, "description": "Default all."},
				"query":         str("Case-insensitive words that must all appear in the title."),
				"include_short": map[string]any{"type": "boolean", "description": "Include past sessions with fewer than 5 messages. Default false."},
				"limit":         integer("Maximum sessions. Default 50.", 1, 500),
			}),
		},
		{
			"name":        "sessions_search",
			"description": "Full-text search over past transcripts on every Rex host, through each host's Wake index. Returns sessions with matching excerpts, newest first.",
			"inputSchema": schema([]string{"query"}, map[string]any{
				"query": str("Words to find; every word must appear in one message. Words shorter than 3 characters are ignored."),
				"host":  hostProp,
				"agent": agentProp,
				"limit": integer("Maximum sessions per host. Default 10.", 1, 50),
			}),
		},
		{
			"name":        "session_show",
			"description": "Show the last messages of a past session's transcript as Markdown. Source transcripts show only while that host is unlocked.",
			"inputSchema": schema([]string{"host", "key"}, map[string]any{
				"host":     str("Rex host label of the session."),
				"key":      keyProp,
				"source":   sourceProp,
				"messages": integer("How many messages from the end. Default 20.", 1, 100),
			}),
		},
		{
			"name":        "session_open",
			"description": "Return the shell command that resumes a past session or focuses a live pane. It does not run the command.",
			"inputSchema": schema([]string{"host"}, map[string]any{
				"host":   str("Rex host label of the session."),
				"key":    keyProp,
				"source": sourceProp,
				"block":  str("Block id of a live pane (the pane.block field)."),
			}),
		},
	}
)

func agentIDs() []string {
	var ids []string
	for _, a := range agent.All {
		ids = append(ids, a.ID)
	}
	return ids
}

func decodeArgs(raw json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

type sessionView struct {
	Host     string    `json:"host"`
	Source   string    `json:"source,omitempty"`
	Agent    string    `json:"agent"`
	State    string    `json:"state"`
	Title    string    `json:"title"`
	Dir      string    `json:"dir"`
	User     string    `json:"user"`
	Updated  time.Time `json:"updated"`
	NativeID string    `json:"native_id,omitempty"`
	Key      string    `json:"key,omitempty"`
	Messages int       `json:"messages,omitempty"`
	Pane     *paneView `json:"pane,omitempty"`
}

type paneView struct {
	Session string `json:"session"`
	Block   string `json:"block"`
}

func view(r rows.Row) sessionView {
	v := sessionView{
		Host: r.Origin.Host, Source: r.Origin.Source, Agent: r.Agent, State: r.State.String(),
		Title: guard.Redact(oneLineTitle(r.Title)), Dir: r.Dir, User: r.User, Updated: r.Updated.UTC().Truncate(time.Second), NativeID: r.ID,
	}
	if r.Past != nil {
		v.Key, v.Messages = r.Past.Key, r.Past.Messages
	}
	if r.Pane != nil {
		v.Pane = &paneView{Session: r.Pane.Session, Block: r.Pane.Block}
	}
	return v
}

func jsonText(v any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n"), err
}

func (m *mcpServer) rowsOn(ctx context.Context, host string) ([]rows.Row, []string) {
	answers, warns := askHosts(ctx, m.local, host, m.settings.DeadlineMS, hostop.Request{Op: hostop.OpRows, PastLimit: m.settings.PastLimit})
	var perHost [][]rows.Row
	for _, h := range answers {
		perHost = append(perHost, h.Rows)
	}
	return rows.Merge(perHost), warns
}

func (m *mcpServer) list(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Host         string `json:"host"`
		Agent        string `json:"agent"`
		State        string `json:"state"`
		Query        string `json:"query"`
		IncludeShort bool   `json:"include_short"`
		Limit        int    `json:"limit"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	scope := map[string]rows.Scope{"": rows.All, "all": rows.All, "live": rows.LiveOnly, "past": rows.PastOnly}
	sc, ok := scope[a.State]
	if !ok {
		return "", fmt.Errorf("state must be all, live or past")
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 50
	}
	rs, warns := m.rowsOn(ctx, a.Host)
	words := strings.Fields(strings.ToLower(a.Query))
	out := []sessionView{}
	total := 0
	for _, i := range rows.Filter(rs, sc, "", a.IncludeShort) {
		r := rs[i]
		title := strings.ToLower(r.Title)
		if a.Agent != "" && r.Agent != a.Agent || !containsAll(title, words) {
			continue
		}
		total++
		if len(out) < limit {
			out = append(out, view(r))
		}
	}
	return jsonText(map[string]any{"sessions": out, "total": total, "warnings": nonNil(warns)})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func containsAll(s string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

type hitView struct {
	sessionView
	Excerpts []hostop.Snippet `json:"excerpts"`
}

func (m *mcpServer) slowDeadline() int { return max(m.settings.DeadlineMS, 3000) }

func (m *mcpServer) search(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Query string `json:"query"`
		Host  string `json:"host"`
		Agent string `json:"agent"`
		Limit int    `json:"limit"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if len(wake.Terms(a.Query)) == 0 {
		return "", wake.ErrShortQuery
	}
	answers, warns := askHosts(ctx, m.local, a.Host, m.slowDeadline(), hostop.Request{Op: hostop.OpSearch, Query: a.Query, Agent: a.Agent, Limit: a.Limit})
	hits := []hitView{}
	for _, h := range answers {
		for _, hit := range h.Hits {
			for i := range hit.Snippets {
				hit.Snippets[i].Text = guard.Redact(hit.Snippets[i].Text)
			}
			hits = append(hits, hitView{sessionView: view(hit.Row), Excerpts: hit.Snippets})
		}
	}
	slices.SortStableFunc(hits, func(x, y hitView) int { return y.Updated.Compare(x.Updated) })
	return jsonText(map[string]any{"results": hits, "warnings": nonNil(warns)})
}

func (m *mcpServer) hostArg(label string) (string, error) {
	if label == "" {
		return "", errors.New("host is required")
	}
	if label == m.local {
		return "", nil
	}
	return label, nil
}

func (m *mcpServer) show(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Host     string `json:"host"`
		Key      string `json:"key"`
		Source   string `json:"source"`
		Messages int    `json:"messages"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	host, err := m.hostArg(a.Host)
	if err != nil {
		return "", err
	}
	if a.Key == "" {
		return "", errors.New("key is required")
	}
	tail := a.Messages
	if tail <= 0 {
		tail = 20
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(m.slowDeadline())*time.Millisecond)
	defer cancel()
	rep, err := hostop.Ask(ctx, host, hostop.Request{Op: hostop.OpShow, Source: a.Source, Key: a.Key, Tail: min(tail, 100), DeadlineMS: m.slowDeadline() * 3 / 4})
	if err != nil {
		return "", err
	}
	switch rep.Notice {
	case hostop.NoticeLocked:
		return "Transcripts for this source are locked. Run rex-rollcall unlock on that host, or press ctrl-u in the picker.", nil
	case hostop.NoticeOff:
		return "Transcripts are off for this source (previews = \"never\").", nil
	}
	return rep.Text, nil
}

func (m *mcpServer) open(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Host   string `json:"host"`
		Key    string `json:"key"`
		Source string `json:"source"`
		Block  string `json:"block"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if _, err := m.hostArg(a.Host); err != nil {
		return "", err
	}
	if (a.Key == "") == (a.Block == "") {
		return "", errors.New("give either key (a past session) or block (a live pane)")
	}
	rs, warns := m.rowsOn(ctx, a.Host)
	i := slices.IndexFunc(rs, func(r rows.Row) bool {
		if a.Block != "" {
			return r.Pane != nil && r.Pane.Block == a.Block
		}
		return r.Past != nil && r.Past.Key == a.Key && r.Origin.Source == a.Source
	})
	if i < 0 {
		return "", fmt.Errorf("no such session on %s (warnings: %s)", a.Host, strings.Join(warns, "; "))
	}
	r := rs[i]
	t := rexcli.Target{Host: a.Host, Local: a.Host == m.local}
	res := map[string]any{"session": view(r), "command": rexcli.CommandLine(t, r, nil)}
	if r.Live() {
		res["runs_on"] = "any machine with Rex; it focuses the pane"
	} else {
		res["runs_on"] = a.Host
		res["rex_command"] = rexcli.Quote(rexcli.NewSessionArgv(t, r))
	}
	return jsonText(res)
}
