package wake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

type Paths struct {
	CLI string
	DB  string
}

func DefaultPaths(home string) Paths {
	if runtime.GOOS == "darwin" {
		return Paths{
			CLI: "/Applications/Wake.app/Contents/MacOS/wake-cli",
			DB:  filepath.Join(home, "Library", "Application Support", "wake", "wake.db"),
		}
	}
	return Paths{
		CLI: filepath.Join(home, ".local", "opt", "wake", "wake-cli"),
		DB:  filepath.Join(home, ".local", "share", "wake", "wake.db"),
	}
}

type Session struct {
	Key      string
	Agent    string
	NativeID string
	Title    string
	Dir      string
	Updated  time.Time
	Messages int
}

var ErrNoIndex = errors.New("no Wake index")

const topLevel = `parent_key = '' AND spawned = 0 AND archived = 0 AND host = ''`

const sessionColumns = `key, agent_id, native_id, title, project_path, updated_at, message_count`

const query = `SELECT ` + sessionColumns + `
FROM sessions
WHERE ` + topLevel + `
ORDER BY updated_at DESC LIMIT ?`

func open(p Paths) (*sql.DB, error) {
	if _, err := os.Stat(p.DB); err != nil {
		return nil, ErrNoIndex
	}
	dsn := (&url.URL{Scheme: "file", Path: p.DB, RawQuery: "mode=ro&_pragma=busy_timeout(500)"}).String()
	return sql.Open("sqlite", dsn)
}

type scanner interface{ Scan(dest ...any) error }

func scanSession(sc scanner, extra ...any) (Session, error) {
	var s Session
	var ms int64
	err := sc.Scan(append([]any{&s.Key, &s.Agent, &s.NativeID, &s.Title, &s.Dir, &ms, &s.Messages}, extra...)...)
	s.Updated = time.UnixMilli(ms)
	return s, err
}

func Sessions(ctx context.Context, p Paths, limit int) ([]Session, error) {
	db, err := open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rs, err := db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p.DB, err)
	}
	defer rs.Close()
	var out []Session
	for rs.Next() {
		s, err := scanSession(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rs.Err()
}

type Match struct {
	Session
	Seq  int
	Role string
	Text string
}

var ErrShortQuery = errors.New("search needs at least one word of 3 or more characters")

func Terms(query string) []string {
	var out []string
	for _, w := range strings.Fields(query) {
		if utf8.RuneCountInString(w) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

func matchExpr(terms []string) string {
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " ")
}

const searchQuery = `SELECT ` + sessionColumns + `, m.seq, COALESCE(m.role, ''), m.text
FROM messages_fts
JOIN messages m ON m.id = messages_fts.rowid
JOIN sessions ON sessions.key = m.session_key
WHERE messages_fts MATCH ? AND m.sidechain_id IS NULL AND ` + topLevel + `
ORDER BY rank LIMIT ?`

func Search(ctx context.Context, p Paths, terms []string, limit int) ([]Match, error) {
	if len(terms) == 0 {
		return nil, ErrShortQuery
	}
	db, err := open(p)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rs, err := db.QueryContext(ctx, searchQuery, matchExpr(terms), limit)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", p.DB, err)
	}
	defer rs.Close()
	var out []Match
	for rs.Next() {
		var m Match
		if m.Session, err = scanSession(rs, &m.Seq, &m.Role, &m.Text); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rs.Err()
}

func MessageCount(ctx context.Context, p Paths, key string) (int, error) {
	db, err := open(p)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT message_count FROM sessions WHERE key = ?`, key).Scan(&n); err != nil {
		return 0, fmt.Errorf("session %q: %w", key, err)
	}
	return n, nil
}

func Show(ctx context.Context, p Paths, key string, messages, tail int) ([]byte, error) {
	if messages <= 0 {
		n, err := MessageCount(ctx, p, key)
		if err != nil {
			return nil, err
		}
		messages = n
	}
	from := max(0, messages-tail)
	cmd := exec.CommandContext(ctx, p.CLI, "show", "--db", p.DB,
		"--from", strconv.Itoa(from), "--messages", strconv.Itoa(max(1, min(tail, 200))),
		"--message-chars", "1500", "--", key)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("wake-cli show: %s", ee.Stderr)
		}
		return nil, fmt.Errorf("wake-cli show: %w", err)
	}
	return out, nil
}
