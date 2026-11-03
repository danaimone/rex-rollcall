package guard

import (
	"errors"
	"fmt"
	"log/syslog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	StampPath   = "/run/rex-rollcall/unlocked-until"
	MaxUnlock   = 30 * time.Minute
	TitleLength = 60
	redacted    = "[redacted]"
)

var tokens = []*regexp.Regexp{
	regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`),
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	regexp.MustCompile(`\btskey-[A-Za-z0-9_-]{10,}`),
}

var keyed = regexp.MustCompile(`(?i)\b((?:password|passwd|secret|token|api[_-]?key)["']?\s*[:=]\s*["']?)[A-Za-z0-9+/_-]{16,}={0,2}`)

func Redact(s string) string {
	for _, re := range tokens {
		s = re.ReplaceAllString(s, redacted)
	}
	return keyed.ReplaceAllString(s, "${1}"+redacted)
}

func Title(s string) string {
	s = Redact(s)
	if utf8.RuneCountInString(s) <= TitleLength {
		return s
	}
	return string([]rune(s)[:TitleLength-1]) + "…"
}

func ParseUnlock(arg string) (time.Duration, error) {
	d, err := time.ParseDuration(arg)
	if err != nil || d <= 0 || d > MaxUnlock {
		return 0, fmt.Errorf("unlock duration must be between 1s and %s, got %q", MaxUnlock, arg)
	}
	return d, nil
}

func WriteStamp(path string, until time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".unlocked-until.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(strconv.FormatInt(until.Unix(), 10) + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var ErrLocked = errors.New("previews locked")

func Unlocked(path string, now time.Time, owner int) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return ErrLocked
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() || int(st.Uid) != owner || fi.Mode().Perm()&0o077 != 0 {
		return ErrLocked
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ErrLocked
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return ErrLocked
	}
	until := time.Unix(secs, 0)
	if !until.After(now) || until.Sub(now) > MaxUnlock {
		return ErrLocked
	}
	return nil
}

func Audit(event string) error {
	who := fmt.Sprintf("uid=%d", os.Geteuid())
	for _, k := range []string{"SUDO_USER", "SSH_CONNECTION"} {
		if v := os.Getenv(k); v != "" {
			who += fmt.Sprintf(" %s=%q", strings.ToLower(k), v)
		}
	}
	w, err := syslog.New(syslog.LOG_AUTHPRIV|syslog.LOG_NOTICE, "rex-rollcall")
	if err != nil {
		return fmt.Errorf("audit log unavailable, refusing: %w", err)
	}
	defer w.Close()
	return w.Notice(event + " " + who)
}

func Excerpt(text string, terms []string, width int) string {
	text = strings.Join(strings.Fields(Redact(text)), " ")
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	lower := foldRunes(runes)
	at := -1
	for _, t := range terms {
		if i := runeIndex(lower, foldRunes([]rune(t))); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	start := max(0, min(at-width/3, len(runes)-width))
	out := string(runes[start : start+width])
	if start > 0 {
		out = "…" + out
	}
	if start+width < len(runes) {
		out += "…"
	}
	return out
}

func foldRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}

func runeIndex(s, sub []rune) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := range sub {
			if s[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
