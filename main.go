package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/danaimone/rex-rollcall/internal/guard"
	"github.com/danaimone/rex-rollcall/internal/hostop"
	"github.com/danaimone/rex-rollcall/internal/rexcli"
	"github.com/danaimone/rex-rollcall/internal/rows"
	"github.com/danaimone/rex-rollcall/internal/wake"
)

//go:embed lua/rex_rollcall.lua
var luaModule string

var version = "0.4.5"

const usage = `rex-rollcall: one picker for coding-agent sessions on every Rex host

  rex-rollcall                     open the picker (Rex opens it from the rollcall action)
  rex-rollcall --vim               open the picker in Vim-style normal mode
  rex-rollcall --demo              open the picker with made-up sessions, for screenshots
  rex-rollcall list                print the rows the picker would show
  rex-rollcall mcp                 serve session list, search, show and open as an MCP server on stdio
  rex-rollcall install             write ~/.config/rex/rex_rollcall.lua and add setup{} to init.lua
  rex-rollcall install --module-only   write only the module (for init.lua managed elsewhere)
  rex-rollcall doctor [--keys]     check the module, fzf, Wake, and every host
  rex-rollcall unlock [10m]        allow source transcript previews for a while (asks for your sudo password)
  rex-rollcall version
`

type Settings struct {
	DeadlineMS int    `json:"deadline_ms"`
	PastLimit  int    `json:"past_limit"`
	Unlock     bool   `json:"unlock"`
	UnlockHost string `json:"unlock_host"`
	Vim        bool   `json:"vim"`
	Demo       bool   `json:"demo"`
}

func parseSettings(env string) (Settings, error) {
	s := Settings{DeadlineMS: 1000, PastLimit: 200}
	if env == "" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(env), &s); err != nil {
		return s, fmt.Errorf("REX_ROLLCALL_SETTINGS: %w", err)
	}
	if s.DeadlineMS < 100 || s.DeadlineMS > 20000 || s.PastLimit < 1 {
		return s, fmt.Errorf("REX_ROLLCALL_SETTINGS: deadline_ms must be 100-20000 and past_limit at least 1")
	}
	return s, nil
}

func anchorFromEnv() rexcli.Anchor {
	a := rexcli.Anchor{Session: os.Getenv("REX_SESSION"), Block: os.Getenv("REX_BLOCK")}
	if env := os.Getenv("REX_ROLLCALL_ANCHOR"); env != "" {
		_ = json.Unmarshal([]byte(env), &a)
	}
	return a
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rex-rollcall:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := ""
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	ctx := context.Background()
	switch cmd {
	case "_op":
		return hostop.Serve(ctx, version, os.Stdin, os.Stdout, len(args) == 1 && args[0] == "--source-only")
	case "_rows", "_mode", "_preview", "_act":
		if len(args) < 2 {
			return errors.New(cmd + ": missing arguments")
		}
		return internal(ctx, cmd, args)
	case "version", "--version", "-V":
		fmt.Println(version)
		return nil
	case "_unlock":
		if len(args) != 1 {
			return errors.New("_unlock: want one duration, such as 10m")
		}
		return unlockAsRoot(args[0])
	case "unlock":
		d := "10m"
		if len(args) == 1 {
			d = args[0]
		}
		return unlockWithSudo(d)
	case "install":
		if len(args) > 1 || len(args) == 1 && args[0] != "--module-only" {
			return errors.New("usage: rex-rollcall install [--module-only]")
		}
		return install(len(args) == 1)
	case "doctor":
		return doctor(ctx, len(args) == 1 && args[0] == "--keys")
	case "help", "--help", "-h":
		fmt.Print(usage)
		return nil
	}
	s, err := parseSettings(os.Getenv("REX_ROLLCALL_SETTINGS"))
	if err != nil {
		return err
	}
	switch cmd {
	case "", "--vim", "--demo":
		for _, f := range append([]string{cmd}, args...) {
			switch f {
			case "":
			case "--vim":
				s.Vim = true
			case "--demo":
				s.Demo = true
			default:
				return fmt.Errorf("unknown flag %q", f)
			}
		}
		return pick(ctx, s, anchorFromEnv())
	case "list":
		return list(ctx, s)
	case "mcp":
		return serveMCP(ctx, s, os.Stdin, os.Stdout)
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

const rootBin = "/usr/local/bin/rex-rollcall"

func unlockAsRoot(arg string) error {
	if os.Geteuid() != 0 {
		return errors.New("_unlock must run as root; use rex-rollcall unlock")
	}
	d, err := guard.ParseUnlock(arg)
	if err != nil {
		return err
	}
	until := time.Now().Add(d)
	if err := guard.Audit(fmt.Sprintf("unlock previews for %s until %s", d, until.Format(time.RFC3339))); err != nil {
		return err
	}
	if err := guard.WriteStamp(guard.StampPath, until); err != nil {
		return err
	}
	fmt.Printf("source previews unlocked until %s\n", until.Format("15:04"))
	return nil
}

func unlockWithSudo(arg string) error {
	if _, err := guard.ParseUnlock(arg); err != nil {
		return err
	}
	bin := rootBin
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("%s is missing: run contrib/proxmox-sources-setup.sh as root first", rootBin)
	}
	cmd := exec.Command("sudo", bin, "_unlock", arg)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func localLabel() string {
	h, err := os.Hostname()
	if err != nil {
		return "local"
	}
	h, _, _ = strings.Cut(h, ".")
	return h
}

type gathered struct {
	Rows     []rows.Row        `json:"rows"`
	Warnings []string          `json:"warnings"`
	Local    string            `json:"local"`
	Clients  map[string]string `json:"clients"`
}

func gather(ctx context.Context, s Settings) gathered {
	g := gathered{Local: localLabel(), Clients: map[string]string{}}
	req := hostop.Request{Op: hostop.OpRows, PastLimit: s.PastLimit}
	answers, warns := askHosts(ctx, g.Local, "", s.DeadlineMS, req)
	g.Warnings = warns
	var perHost [][]rows.Row
	for _, h := range answers {
		perHost = append(perHost, h.Rows)
		g.Clients[h.Label] = h.AppClient
	}
	g.Rows = rows.Merge(perHost)
	return g
}

func askHosts(ctx context.Context, local, only string, deadlineMS int, req hostop.Request) ([]hostop.HostRows, []string) {
	var warns []string
	type target struct{ host, label string }
	var targets []target
	if only == "" || only == local {
		targets = append(targets, target{"", local})
	}
	if only != local {
		labels, err := rexcli.Hosts(ctx)
		if err != nil {
			warns = append(warns, err.Error())
		}
		for _, l := range labels {
			if l != local && (only == "" || l == only) {
				targets = append(targets, target{l, l})
			}
		}
		if only != "" && len(targets) == 0 {
			return nil, append(warns, fmt.Sprintf("no Rex host labeled %q", only))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(deadlineMS)*time.Millisecond+300*time.Millisecond)
	defer cancel()
	req.DeadlineMS = deadlineMS * 3 / 4
	answers := make([]hostop.HostRows, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = hostop.Rows(ctx, t.host, t.label, req)
		}()
	}
	wg.Wait()
	var ok []hostop.HostRows
	for _, h := range hostop.OnePerServer(answers) {
		if h.Err != nil {
			warns = append(warns, h.Label+": "+h.Err.Error())
			continue
		}
		ok = append(ok, h)
		for _, w := range h.Warnings {
			warns = append(warns, h.Label+": "+w)
		}
	}
	return ok, warns
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func list(ctx context.Context, s Settings) error {
	start := time.Now()
	g := gather(ctx, s)
	took := time.Since(start)
	color := isTerminal(os.Stdout)
	now := time.Now()
	width := 120
	if color {
		width = envInt("COLUMNS", width)
	}
	l := rows.NewLayout(g.Rows, os.Getenv("USER"), width)
	for _, r := range g.Rows {
		line := rows.Render(r, now, look(r.Agent), l)
		if !color {
			line = ansi.ReplaceAllString(line, "")
		}
		fmt.Println(line)
	}
	for _, w := range g.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	fmt.Fprintf(os.Stderr, "%d rows in %s\n", len(g.Rows), took.Round(time.Millisecond))
	return nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "rex")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "rex")
}

func selfPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

func luaString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func install(moduleOnly bool) error {
	bin, err := selfPath()
	if err != nil {
		return err
	}
	dir := configDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mod := strings.NewReplacer(`"@VERSION@"`, luaString(version), `"@BIN@"`, luaString(bin)).Replace(luaModule)
	modPath := filepath.Join(dir, "rex_rollcall.lua")
	if err := writeAtomic(modPath, []byte(mod), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", modPath)
	if moduleOnly {
		return nil
	}
	initPath := filepath.Join(dir, "init.lua")
	cur, err := os.ReadFile(initPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if strings.Contains(string(cur), `"rex_rollcall"`) {
		fmt.Println(initPath, "already loads rex_rollcall")
	} else {
		line := "\nrequire(\"rex_rollcall\").setup{ key = \"cmd+shift+a\" }\n"
		f, err := os.OpenFile(initPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.WriteString(line); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Println("added setup{} to", initPath)
	}
	fmt.Println("now run: rex config reload")
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

const keysRepro = `Key check (run in a Rex pane on the Mac):
  1. Press the binding at a shell prompt. The picker should open.
  2. Run: printf '\e[>31u'   (turns on the kitty keyboard protocol, like Claude Code does)
  3. Press the binding again. If nothing opens, Rex forwards the key to the program.
  4. Run: printf '\e[<u'     (turns it back off)
If step 3 fails, file it in the Rex Beta feedback forum with these steps.
The command palette entry "Agent Sessions" works in every mode.
`

func doctor(ctx context.Context, keys bool) error {
	if keys {
		fmt.Print(keysRepro)
		return nil
	}
	bin, _ := selfPath()
	fmt.Printf("binary   %s %s\n", bin, version)
	mod := filepath.Join(configDir(), "rex_rollcall.lua")
	if b, err := os.ReadFile(mod); err != nil {
		fmt.Printf("module   missing %s: run rex-rollcall install\n", mod)
	} else if !strings.Contains(string(b), "local VERSION = "+luaString(version)) {
		fmt.Printf("module   %s has another version: rerun rex-rollcall install\n", mod)
	} else {
		fmt.Printf("module   %s ok\n", mod)
	}
	if out, err := exec.Command("fzf", "--version").Output(); err != nil {
		fmt.Println("fzf      missing: install fzf 0.58 or newer")
	} else {
		fmt.Printf("fzf      %s", out)
	}
	home, _ := os.UserHomeDir()
	p := wake.DefaultPaths(home)
	for _, f := range []string{p.DB, p.CLI} {
		if _, err := os.Stat(f); err != nil {
			fmt.Printf("wake     missing %s (no past rows here)\n", f)
		} else {
			fmt.Printf("wake     %s ok\n", f)
		}
	}
	s, _ := parseSettings("")
	start := time.Now()
	g := gather(ctx, s)
	counts := map[string]int{}
	for _, r := range g.Rows {
		counts[r.Origin.Host+"/"+r.Origin.Source]++
	}
	fmt.Printf("gather   %d rows in %s\n", len(g.Rows), time.Since(start).Round(time.Millisecond))
	for k, n := range counts {
		fmt.Printf("         %-20s %d rows\n", strings.TrimSuffix(k, "/"), n)
	}
	for _, w := range g.Warnings {
		fmt.Println("warning ", w)
	}
	return nil
}
