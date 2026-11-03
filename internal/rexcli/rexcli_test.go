package rexcli

import "testing"

func TestSessionLabelUsesTheFirstWordsOfTheTitle(t *testing.T) {
	tests := []struct{ title, agent, want string }{
		{"Fix the login redirect after SSO", "claude", "fix-the-login-redirect"},
		{"OC | CAPTCHA solver comparison: hcaptcha-challenger", "opencode", "oc-captcha-solver-comparison"},
		{"Ünïcode café notes", "pi", "ünïcode-café-notes"},
		{"  ", "claude", "claude"},
		{"!!!", "codex", "codex"},
	}
	for _, tt := range tests {
		if got := SessionLabel(tt.title, tt.agent); got != tt.want {
			t.Errorf("SessionLabel(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}
