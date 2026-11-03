package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danaimone/rex-rollcall/internal/agent"
	"github.com/danaimone/rex-rollcall/internal/hostop"
	"github.com/danaimone/rex-rollcall/internal/rexcli"
	"github.com/danaimone/rex-rollcall/internal/rows"
	"github.com/danaimone/rex-rollcall/internal/termtext"
	"github.com/danaimone/rex-rollcall/internal/wake"
)

type snapshot struct {
	gathered
	Anchor   rexcli.Anchor `json:"anchor"`
	Settings Settings      `json:"settings"`
	Scope    rows.Scope    `json:"scope"`
	Host     string        `json:"host"`
	Brief    bool          `json:"brief"`
	Search   bool          `json:"search"`
	Help     bool          `json:"help"`
	Note     string        `json:"note,omitempty"`
}

func envInt(name string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return fallback
}

func listWidth() int {
	cols := envInt("FZF_COLUMNS", 100)
	if left := envInt("FZF_PREVIEW_LEFT", 0); left > cols/4 {
		return left - 6
	}
	return cols - 3
}

func look(id string) rows.Look {
	a, _ := agent.Lookup(id)
	return rows.Look{Glyph: a.Glyph, Color: a.Color}
}

func (s *snapshot) lines() string {
	var b strings.Builder
	now := time.Now()
	l := rows.NewLayout(s.Rows, os.Getenv("USER"), listWidth())
	for _, i := range rows.Filter(s.Rows, s.Scope, s.Host, s.Brief) {
		fmt.Fprintf(&b, "%s\t%d\n", rows.Render(s.Rows[i], now, look(s.Rows[i].Agent), l), i)
	}
	return b.String()
}

const (
	sgrDim    = "\x1b[2m"
	sgrAccent = "\x1b[34m"
	sgrReset  = "\x1b[0m"
)

func (s *snapshot) prompt() string {
	if !s.Settings.Vim {
		return sgrAccent + "❯" + sgrReset + " "
	}
	if s.Search {
		return sgrAccent + "/" + sgrReset
	}
	return sgrDim + "/" + sgrReset
}

func (s *snapshot) label() string {
	name := [...]string{"Sessions", "Live", "Past"}[s.Scope]
	if s.Host != "" {
		name += " on " + s.Host
	}
	label := " " + name + " "
	if hidden := len(rows.Filter(s.Rows, s.Scope, s.Host, true)) - len(rows.Filter(s.Rows, s.Scope, s.Host, false)); hidden > 0 && !s.Brief {
		label += sgrDim + fmt.Sprintf(" %d short hidden  ^s ", hidden) + sgrReset
	} else if s.Brief {
		label += sgrDim + " short shown  ^s " + sgrReset
	}
	return label
}

type hint struct{ key, does string }

var (
	plainHints  = []hint{{"↵", "open"}, {"tab", "live/past"}, {"^/", "keys"}}
	normalHints = []hint{{"↵", "open"}, {"j/k", "move"}, {"/", "search"}, {"tab", "live/past"}, {"?", "keys"}}
	searchHints = []hint{{"↵", "open"}, {"esc", "stop searching"}}
	allHints    = []hint{
		{"↵", "jump or resume"}, {"^t", "resume in a new tab"}, {"^y", "copy command"}, {"^x", "close pane"},
		{"tab", "live/past/all"}, {"^o", "next host"}, {"^s", "short sessions"}, {"^r", "refresh"}, {"^u", "unlock previews"},
	}
	plainHelpHints = []hint{{"esc", "quit"}, {"^/", "fewer keys"}}
	vimHelpHints   = []hint{{"j/k", "move"}, {"d/u", "half page"}, {"g/G", "top/bottom"}, {"/ i", "search"}, {"esc q", "quit"}, {"?", "fewer keys"}}
)

func hintLines(hs []hint, width int) []string {
	var out []string
	var cur string
	curW := 0
	for _, h := range hs {
		w := termtext.Width(h.key) + 1 + termtext.Width(h.does)
		cell := h.key + " " + sgrDim + h.does + sgrReset
		if curW > 0 && curW+3+w > width {
			out = append(out, cur)
			cur, curW = "", 0
		}
		if curW > 0 {
			cur, curW = cur+"   ", curW+3
		}
		cur, curW = cur+cell, curW+w
	}
	return append(out, cur)
}

func (s *snapshot) header() string {
	var hs []hint
	switch {
	case s.Help && s.Settings.Vim && !s.Search:
		hs = append(allHints, vimHelpHints...)
	case s.Help:
		hs = append(allHints, plainHelpHints...)
	case !s.Settings.Vim:
		hs = plainHints
	case s.Search:
		hs = searchHints
	default:
		hs = normalHints
	}
	width := listWidth()
	lines := hintLines(hs, width)
	if s.Note != "" {
		lines = append([]string{sgrDim + termtext.Truncate(s.Note, width) + sgrReset}, lines...)
	}
	if len(s.Warnings) > 0 {
		lines = append([]string{"\x1b[33m" + termtext.Truncate("⚠ "+strings.Join(s.Warnings, "  "), width) + sgrReset}, lines...)
	}
	return strings.Join(lines, "\n")
}

const normalKeys = "j,k,g,G,d,u,q,/,i,?,change"

func (s *snapshot) modeActions() string {
	if !s.Settings.Vim {
		return "change-prompt(" + s.prompt() + ")"
	}
	if s.Search {
		return "unbind(" + normalKeys + ")+change-prompt(" + s.prompt() + ")"
	}
	return "rebind(" + normalKeys + ")+change-prompt(" + s.prompt() + ")"
}

func (s *snapshot) target(r rows.Row) rexcli.Target {
	return rexcli.Target{Host: r.Origin.Host, Local: r.Origin.Host == s.Local, AppClient: s.Clients[r.Origin.Host]}
}

func (s *snapshot) row(arg string) (rows.Row, error) {
	i, err := strconv.Atoi(arg)
	if err != nil || i < 0 || i >= len(s.Rows) {
		return rows.Row{}, fmt.Errorf("no row %q", arg)
	}
	return s.Rows[i], nil
}

func loadSnapshot(path string) (*snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s snapshot
	return &s, json.Unmarshal(b, &s)
}

func (s *snapshot) save(path string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeAtomic(path, b, 0o600)
}

func snapshotPath() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "rex-rollcall")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, strconv.Itoa(os.Getpid())+".json"), nil
}

func pick(ctx context.Context, set Settings, a rexcli.Anchor) error {
	if _, err := exec.LookPath("fzf"); err != nil {
		return errors.New("fzf not found in PATH: install fzf 0.58 or newer")
	}
	self, err := selfCommand()
	if err != nil {
		return err
	}
	snap, err := snapshotPath()
	if err != nil {
		return err
	}
	defer os.Remove(snap)
	setTitle("Rollcall")
	s := &snapshot{gathered: gather(ctx, set), Anchor: a, Settings: set, Search: !set.Vim}
	if err := s.save(snap); err != nil {
		return err
	}
	view := "transform(" + self("_rows", snap, "view") + ")"
	mode := func(m string) string { return "transform(" + self("_mode", snap, m) + ")" }
	args := []string{
		"--ansi", "--delimiter", "\t", "--with-nth", "1", "--no-sort", "--cycle", "--reverse",
		"--height", "100%", "--info", "inline-right", "--no-scrollbar", "--highlight-line",
		"--pointer", "▌", "--marker", " ", "--ellipsis", "…",
		"--color", "16,fg:-1,bg:-1,fg+:-1:regular,bg+:0,gutter:-1,hl:4:underline,hl+:4:underline,query:-1:regular," +
			"prompt:-1,pointer:4,info:8,header:-1,header-bg:-1,border:8,header-border:8,header-label:-1:bold,preview-border:8,preview-fg:-1,preview-bg:-1,spinner:8,separator:8",
		"--no-separator", "--header-border", "bottom", "--header-label-pos", "2:bottom", "--header-label", s.label(),
		"--prompt", s.prompt(), "--header", s.header(),
		"--preview", self("_preview", snap) + " {2}", "--preview-window", "right,50%,border-left,nowrap,noinfo,<60(down,45%,border-top,nowrap,noinfo)",
		"--expect", "ctrl-t",
		"--bind", "start:" + view, "--bind", "resize:" + view,
		"--bind", "ctrl-/:" + mode("help"),
		"--bind", "tab:transform(" + self("_rows", snap, "scope") + ")",
		"--bind", "ctrl-o:transform(" + self("_rows", snap, "host") + ")",
		"--bind", "ctrl-s:transform(" + self("_rows", snap, "brief") + ")",
		"--bind", "ctrl-r:transform(" + self("_rows", snap, "refresh") + ")",
		"--bind", "ctrl-y:execute-silent(" + self("_act", snap) + " {2} copy)+transform-header(" + self("_rows", snap, "header") + ")",
		"--bind", "ctrl-u:execute-silent(" + self("_act", snap) + " {2} unlock)+refresh-preview+transform-header(" + self("_rows", snap, "header") + ")",
		"--bind", "ctrl-x:execute-silent(" + self("_act", snap) + " {2} close)+reload(" + self("_rows", snap, "same") + ")+transform-header(" + self("_rows", snap, "header") + ")",
	}
	if fzfAtLeast(0, 66) {
		args = append(args, "--gutter", " ")
	}
	if set.Vim {
		args = append(args,
			"--bind", "j:down,k:up,g:first,G:last,d:half-page-down,u:half-page-up,ctrl-d:half-page-down,q:abort",
			"--bind", "/:"+mode("search")+",i:"+mode("search")+",change:"+mode("search")+",esc:"+mode("esc"),
			"--bind", "?:"+mode("help"),
		)
	}
	fzf := exec.CommandContext(ctx, "fzf", args...)
	fzf.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "FZF_DEFAULT_OPTS=") || strings.HasPrefix(kv, "FZF_DEFAULT_OPTS_FILE=")
	})
	fzf.Stdin = strings.NewReader("")
	fzf.Stderr = os.Stderr
	out, err := fzf.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && (ee.ExitCode() == 130 || ee.ExitCode() == 1) {
		return nil
	}
	if err != nil {
		return err
	}
	key, line, _ := strings.Cut(strings.TrimRight(string(out), "\n"), "\n")
	_, idx, ok := strings.Cut(line, "\t")
	if !ok {
		return nil
	}
	if s, err = loadSnapshot(snap); err != nil {
		return err
	}
	r, err := s.row(idx)
	if err != nil {
		return err
	}
	if err := act(ctx, s, r, key == "ctrl-t"); err != nil {
		return pause(err)
	}
	return nil
}

func fzfAtLeast(major, minor int) bool {
	out, err := exec.Command("fzf", "--version").Output()
	if err != nil {
		return false
	}
	var ma, mi int
	if _, err := fmt.Sscanf(string(out), "%d.%d", &ma, &mi); err != nil {
		return false
	}
	return ma > major || ma == major && mi >= minor
}

func setTitle(title string) {
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		fmt.Fprintf(tty, "\x1b]2;%s\a", title)
		tty.Close()
	}
}

func selfCommand() (func(args ...string) string, error) {
	bin, err := selfPath()
	if err != nil {
		return nil, err
	}
	return func(args ...string) string { return rexcli.Quote(append([]string{bin}, args...)) }, nil
}

func act(ctx context.Context, s *snapshot, r rows.Row, newTab bool) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	t := s.target(r)
	if r.Live() {
		return rexcli.Jump(ctx, t, s.Anchor, r)
	}
	p := rexcli.Split
	if newTab {
		p = rexcli.Tab
	}
	return rexcli.Open(ctx, t, s.Anchor, r, p)
}

func pause(err error) error {
	fmt.Fprintf(os.Stderr, "rex-rollcall: %v\npress enter to close", err)
	if tty, terr := os.Open("/dev/tty"); terr == nil {
		_, _ = bufio.NewReader(tty).ReadString('\n')
		tty.Close()
	}
	return err
}

func internal(ctx context.Context, cmd string, args []string) error {
	path := args[0]
	s, err := loadSnapshot(path)
	if err != nil {
		return err
	}
	switch cmd {
	case "_rows":
		switch args[1] {
		case "header":
			fmt.Print(s.header())
			return nil
		case "same":
			fmt.Print(s.lines())
			return nil
		case "view":
		case "scope":
			s.Scope = s.Scope.Next()
		case "brief":
			s.Brief = !s.Brief
		case "host":
			hosts := append([]string{""}, rows.Hosts(s.Rows, s.Local)...)
			for i, h := range hosts {
				if h == s.Host {
					s.Host = hosts[(i+1)%len(hosts)]
					break
				}
			}
		case "refresh":
			s.gathered = gather(ctx, s.Settings)
			s.Note = ""
		default:
			return fmt.Errorf("_rows: unknown view %q", args[1])
		}
		if err := s.save(path); err != nil {
			return err
		}
		self, err := selfCommand()
		if err != nil {
			return err
		}
		fmt.Printf("reload(%s)+change-header-label(%s)+transform-header(%s)", self("_rows", path, "same"), s.label(), self("_rows", path, "header"))
		if args[1] == "view" {
			fmt.Print("+" + s.modeActions())
		}
		return nil
	case "_mode":
		switch args[1] {
		case "search":
			s.Search, s.Help = true, false
		case "help":
			s.Help = !s.Help
		case "esc":
			if !s.Search {
				fmt.Print("abort")
				return nil
			}
			s.Search = false
		default:
			return fmt.Errorf("_mode: unknown mode %q", args[1])
		}
		if err := s.save(path); err != nil {
			return err
		}
		self, err := selfCommand()
		if err != nil {
			return err
		}
		fmt.Printf("%s+transform-header(%s)", s.modeActions(), self("_rows", path, "header"))
		return nil
	case "_preview":
		r, err := s.row(args[1])
		if err != nil {
			return err
		}
		return preview(ctx, s, r)
	case "_act":
		if len(args) < 3 {
			return errors.New("_act: missing action")
		}
		r, err := s.row(args[1])
		if err != nil {
			return err
		}
		switch args[2] {
		case "copy":
			s.Note = copyRow(s, r)
		case "close":
			s.Note = closeRow(ctx, s, r, args[1])
		case "unlock":
			s.Note = unlock(ctx, s.Settings)
		default:
			return fmt.Errorf("_act: unknown action %q", args[2])
		}
		return s.save(path)
	}
	return nil
}

func copyRow(s *snapshot, r rows.Row) string {
	var resume []string
	if a, ok := agent.Lookup(r.Agent); ok && r.ID != "" {
		resume = a.Resume(r.ID)
	}
	line := rexcli.CommandLine(s.target(r), r, resume)
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return "copy failed: " + err.Error()
	}
	defer tty.Close()
	fmt.Fprintf(tty, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(line)))
	return "copied: " + line
}

func unlock(ctx context.Context, set Settings) string {
	if !set.Unlock {
		return "unlock is not set up: add unlock = { … } to setup{} (see README)"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	t := rexcli.Target{Host: set.UnlockHost, Local: set.UnlockHost == ""}
	out, err := rexcli.Do(ctx, t, "rollcall_unlock")
	if err != nil {
		return "unlock failed: " + err.Error()
	}
	var msg string
	if json.Unmarshal(out, &msg) != nil || msg == "" {
		msg = "unlock requested"
	}
	return strings.TrimSpace(msg)
}

func closeRow(ctx context.Context, s *snapshot, r rows.Row, idx string) string {
	if !r.Live() {
		return "only live panes can be closed"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rexcli.Close(ctx, s.target(r), r); err != nil {
		return "close failed: " + err.Error()
	}
	i, _ := strconv.Atoi(idx)
	s.Rows = append(s.Rows[:i:i], s.Rows[i+1:]...)
	return "closed " + r.Title
}

func preview(ctx context.Context, s *snapshot, r rows.Row) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	width := envInt("FZF_PREVIEW_COLUMNS", 80) - 1
	height := envInt("FZF_PREVIEW_LINES", 40)
	var out []string
	for _, l := range termtext.Wrap(oneLineTitle(r.Title), width) {
		out = append(out, "\x1b[1m"+l+sgrReset)
	}
	lk := look(r.Agent)
	t := s.target(r)
	where := r.Origin.Host
	if r.Origin.Source != "" {
		where += "/" + r.Origin.Source
	}
	facts := []string{r.Agent, where}
	if r.Dir != "" {
		facts = append(facts, homeRelative(r.Dir, t.Local))
	}
	state := rows.Ago(time.Now(), r.Updated) + " ago"
	if r.Live() {
		state = r.State.String()
	}
	facts = append(facts, state)
	out = append(out, rows.SGR(lk.Color)+lk.Glyph+sgrReset+" "+sgrDim+termtext.Truncate(strings.Join(facts, "  "), width-2)+sgrReset)
	if r.ID != "" {
		out = append(out, "\x1b[90m"+termtext.Truncate(r.ID, width)+sgrReset)
	}
	out = append(out, "\x1b[90m"+strings.Repeat("─", width)+sgrReset)
	var body []byte
	var err error
	switch {
	case r.Live():
		body, err = rexcli.Capture(ctx, t, r)
	case t.Local && r.Origin.Source == "":
		home, _ := os.UserHomeDir()
		body, err = wake.Show(ctx, wake.DefaultPaths(home), r.Past.Key, r.Past.Messages, 6)
	default:
		host := r.Origin.Host
		if t.Local {
			host = ""
		}
		var rep hostop.Reply
		rep, err = hostop.Ask(ctx, host, hostop.Request{Op: hostop.OpShow, Source: r.Origin.Source, Key: r.Past.Key, Messages: r.Past.Messages, Tail: 6, DeadlineMS: 2500})
		switch rep.Notice {
		case hostop.NoticeLocked:
			body = []byte("Previews are locked. Press ctrl-u to unlock them.")
		case hostop.NoticeOff:
			body = []byte("Previews are off for this source.")
		default:
			body = []byte(rep.Text)
		}
	}
	var lines []string
	switch {
	case err != nil:
		lines = termtext.Wrap("Preview unavailable: "+err.Error(), width)
	case r.Live():
		for _, l := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			lines = append(lines, termtext.Wrap(l, width)...)
		}
		lines = lines[max(0, len(lines)-(height-len(out))):]
	default:
		lines = transcript(string(body), r.Agent, lk, width)
	}
	fmt.Println(strings.Join(append(out, lines...), "\n"))
	return nil
}

func homeRelative(dir string, local bool) string {
	home, err := os.UserHomeDir()
	if !local || err != nil || home == "/" {
		return dir
	}
	if rest, ok := strings.CutPrefix(dir, home); ok && (rest == "" || rest[0] == '/') {
		return "~" + rest
	}
	return dir
}

func oneLineTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "(untitled)"
	}
	return s
}

var strongText = regexp.MustCompile(`\*\*([^*]+)\*\*`)

var turnHeading = regexp.MustCompile(`^### \[seq \d+\] (\S+) · (.*)$`)

func transcript(md, agentName string, lk rows.Look, width int) []string {
	lines := strings.Split(md, "\n")
	var out []string
	role := ""
	started, blank, lastTool := false, false, false
	emit := func(line string, tool bool) {
		if blank && len(out) > 0 && !(tool && lastTool) {
			out = append(out, "")
		}
		out = append(out, line)
		blank, lastTool = false, tool
	}
	for _, l := range lines {
		if m := turnHeading.FindStringSubmatch(l); m != nil {
			started = true
			if m[1] == role {
				continue
			}
			role = m[1]
			label := "\x1b[1;34myou" + sgrReset
			if role != "User" {
				label = rows.SGR(lk.Color) + "\x1b[1m" + agentName + sgrReset
			}
			blank = true
			lastTool = false
			emit(label+" "+sgrDim+clock(m[2])+sgrReset, false)
			continue
		}
		switch {
		case !started, l == "—", strings.HasPrefix(l, "Showing seq "), strings.HasPrefix(l, "More follows "):
		case strings.TrimSpace(l) == "":
			blank = true
		default:
			if name, args, ok := toolCall(l); ok {
				emit(sgrDim+termtext.Truncate("  › "+name+" "+strings.Join(strings.Fields(args), " "), width)+sgrReset, true)
				continue
			}
			for _, w := range termtext.Wrap("  "+l, width) {
				emit(strongText.ReplaceAllString(w, "\x1b[1m$1"+sgrReset), false)
			}
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return []string{sgrDim + "No messages to show." + sgrReset}
	}
	return out
}

func toolCall(l string) (string, string, bool) {
	rest, ok := strings.CutPrefix(l, "- 🔧 ")
	if !ok {
		return "", "", false
	}
	name, args, _ := strings.Cut(rest, ": ")
	return name, args, true
}

func clock(stamp string) string {
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", stamp, time.Local); err == nil {
		return rows.Ago(time.Now(), t) + " ago"
	}
	return stamp
}

func tail(b []byte, n int) []byte {
	b = bytes.TrimRight(b, "\n")
	for i, seen := len(b)-1, 0; i >= 0; i-- {
		if b[i] == '\n' {
			if seen++; seen == n {
				return b[i+1:]
			}
		}
	}
	return b
}
