package guard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fake(prefix, alphabet string, n int) string {
	return prefix + strings.Repeat(alphabet, n/len(alphabet)+1)[:n]
}

func TestRedactHidesTokens(t *testing.T) {
	pem := "-----BEGIN " + "OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END " + "OPENSSH PRIVATE KEY-----"
	tests := []struct {
		name, in, want string
	}{
		{"anthropic", "use " + fake("sk-"+"ant-api03-", "Ab3_", 40) + " now", "use [redacted] now"},
		{"openai project", fake("sk-"+"proj-", "aB9", 48), "[redacted]"},
		{"github classic", "push " + fake("gh"+"p_", "a1B2", 36), "push [redacted]"},
		{"github oauth", fake("gh"+"o_", "Zx9", 36), "[redacted]"},
		{"github fine-grained", fake("github_"+"pat_", "11AB_cd", 82), "[redacted]"},
		{"gitlab", fake("gl"+"pat-", "xY7-", 20), "[redacted]"},
		{"slack bot", fake("xo"+"xb-", "1234-", 40), "[redacted]"},
		{"aws access key", "key AK" + "IAIOSFODNN7EXAMPLE done", "key [redacted] done"},
		{"google api key", fake("AI"+"za", "Sy9_-", 35), "[redacted]"},
		{"jwt", fake("ey"+"J", "hbGciOi", 20) + "." + fake("ey"+"J", "zdWIiOi", 30) + "." + fake("", "Sfl-KxwR", 43), "[redacted]"},
		{"private key block", "before\n" + pem + "\nafter", "before\n[redacted]\nafter"},
		{"private key cut off", "x " + "-----BEGIN " + "RSA PRIVATE KEY-----\nMIIEow", "x [redacted]"},
		{"tailscale", "auth " + fake("ts"+"key-auth-", "kQ7", 30), "auth [redacted]"},
		{"password value", "password=" + fake("", "a1b2c3d4", 24), "password=[redacted]"},
		{"api_key json", `"api_key": "` + fake("", "deadbeef", 32) + `"`, `"api_key": "[redacted]"`},
		{"token base64", "Token: " + fake("", "QWxhZGRpbjpv", 24) + "==", "Token: [redacted]"},
		{"secret colon", "SECRET:" + fake("", "9f8e7d6c", 40), "SECRET:[redacted]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Redact(tt.in); got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedactKeepsNearMisses(t *testing.T) {
	for _, in := range []string{
		"Fix sk-learn import and the task-runner",
		"risk-free refactor of the desk-sharing page",
		"run the flask-sqlalchemy-integration-tests suite",
		"ghp_short is not a token",
		"github_pat_ too short",
		"glpat- prefix only",
		"xoxo hugs from the team",
		"AKIA lowercase akiaiosfodnn7example",
		"AIza short key",
		"eyJ alone and eyJabc.def.ghi",
		"-----BEGIN PUBLIC KEY----- is fine to show",
		"reset the password for users",
		"password: hunter2",
		"token limit exceeded at 200000 tokens",
		"secret santa list",
		"api_key rotation plan",
		"tskey- in the docs",
		"commit 3f2c9a1b fixed it",
	} {
		if got := Redact(in); got != in {
			t.Errorf("Redact(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestTitleRedactsThenTruncates(t *testing.T) {
	in := "deploy with " + fake("gh"+"p_", "a1B2", 36) + " " + strings.Repeat("word ", 20)
	got := Title(in)
	if strings.Contains(got, "ghp_") || !strings.HasPrefix(got, "deploy with [redacted] ") {
		t.Errorf("Title leaked or lost the prefix: %q", got)
	}
	if n := len([]rune(got)); n != TitleLength {
		t.Errorf("Title length = %d, want %d", n, TitleLength)
	}
}

func stamp(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "unlocked-until")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUnlocked(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	me := os.Getuid()
	at := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) + "\n" }
	tests := []struct {
		name  string
		path  string
		owner int
		open  bool
	}{
		{"future expiry", stamp(t, at(10*time.Minute), 0o600), me, true},
		{"expired", stamp(t, at(-time.Second), 0o600), me, false},
		{"expires now", stamp(t, at(0), 0o600), me, false},
		{"beyond the 30m cap", stamp(t, at(31*time.Minute), 0o600), me, false},
		{"readable by others", stamp(t, at(10*time.Minute), 0o644), me, false},
		{"owned by someone else", stamp(t, at(10*time.Minute), 0o600), me + 1, false},
		{"garbage", stamp(t, "soon", 0o600), me, false},
		{"missing", filepath.Join(t.TempDir(), "nope"), me, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Unlocked(tt.path, now, tt.owner) == nil; got != tt.open {
				t.Errorf("Unlocked = %v, want %v", got, tt.open)
			}
		})
	}
}

func TestWriteStampUnlocksForThatLong(t *testing.T) {
	now := time.Now()
	p := filepath.Join(t.TempDir(), "run", "unlocked-until")
	if err := WriteStamp(p, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := Unlocked(p, now, os.Getuid()); err != nil {
		t.Errorf("fresh stamp: %v, want unlocked", err)
	}
	if err := Unlocked(p, now.Add(11*time.Minute), os.Getuid()); err == nil {
		t.Error("stamp after expiry: unlocked, want locked")
	}
}

func TestParseUnlock(t *testing.T) {
	for in, ok := range map[string]bool{"10m": true, "30m": true, "1s": true, "31m": false, "0s": false, "-5m": false, "1h": false, "ten": false} {
		if _, err := ParseUnlock(in); (err == nil) != ok {
			t.Errorf("ParseUnlock(%q) err = %v, want ok=%v", in, err, ok)
		}
	}
}

func TestExcerptRedactsBeforeCutting(t *testing.T) {
	key := fake("sk-"+"ant-api03-", "Ab3_", 40)
	filler := strings.Repeat("lorem ipsum ", 40)
	text := filler + "the deploy key is " + key + " for the Tautulli webhook " + filler
	got := Excerpt(text, []string{"tautulli"}, 120)
	if strings.Contains(got, key[len(key)-16:]) {
		t.Fatalf("Excerpt leaked the key: %q", got)
	}
	if !strings.Contains(got, "Tautulli webhook") || !strings.Contains(got, "[redacted]") {
		t.Errorf("Excerpt = %q, want the window around the match with the key redacted", got)
	}
	if n := len([]rune(got)); n > 122 {
		t.Errorf("Excerpt is %d runes, want at most 120 plus two ellipses", n)
	}
}
