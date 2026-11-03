package hostop

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestInteractiveDropsOnlySudoNonInteractiveFlag(t *testing.T) {
	tests := []struct {
		wrap, want []string
	}{
		{[]string{"sudo", "-n", "-u", "root"}, []string{"sudo", "-u", "root"}},
		{[]string{"sudo", "-n", "pct", "exec", "101", "--"}, []string{"sudo", "pct", "exec", "101", "--"}},
		{[]string{"/usr/bin/sudo", "-u", "root", "-n", "-H"}, []string{"/usr/bin/sudo", "-u", "root", "-H"}},
		{[]string{"sudo", "-n", "tool", "-n"}, []string{"sudo", "tool", "-n"}},
		{[]string{"doas", "-n", "-u", "root"}, []string{"doas", "-n", "-u", "root"}},
		{[]string{"pct", "exec", "101", "--"}, []string{"pct", "exec", "101", "--"}},
	}
	for _, tt := range tests {
		if got := Interactive(tt.wrap); !slices.Equal(got, tt.want) {
			t.Errorf("Interactive(%q) = %q, want %q", tt.wrap, got, tt.want)
		}
	}
}

func TestOnePerServerKeepsOneLabelPerServer(t *testing.T) {
	h := func(label, id string, local bool) HostRows { return HostRows{Label: label, ServerID: id, Local: local} }
	failed := func(label, id string, local bool) HostRows {
		r := h(label, id, local)
		r.Err = ErrNotInstalled
		return r
	}
	tests := []struct {
		name string
		in   []HostRows
		want []string
	}{
		{"two labels for one remote server keep the shorter", []HostRows{h("mac", "m", true), h("Homelab (danaimone)", "p", false), h("pve", "p", false)}, []string{"mac", "pve"}},
		{"order of answers does not matter", []HostRows{h("pve", "p", false), h("Homelab (danaimone)", "p", false), h("mac", "m", true)}, []string{"pve", "mac"}},
		{"the local server wins over a host entry that reaches it", []HostRows{h("homelab", "p", false), h("pve-long-name", "p", true)}, []string{"pve-long-name"}},
		{"equal length labels pick the first alphabetically", []HostRows{h("pve2", "p", false), h("pve1", "p", false)}, []string{"pve1"}},
		{"replies without a server id are never merged", []HostRows{h("a", "", false), h("b", "", false)}, []string{"a", "b"}},
		{"a label that failed is dropped when another label of its server answered", []HostRows{failed("mini", "n", false), h("mac mini (dan)", "n", false)}, []string{"mac mini (dan)"}},
		{"an answer beats a failure even from the local label", []HostRows{failed("pve", "p", true), h("homelab", "p", false)}, []string{"homelab"}},
		{"a failure stays when no label of its server answered", []HostRows{failed("mini", "n", false), h("mac", "m", true)}, []string{"mini", "mac"}},
		{"a failure without a server id stays", []HostRows{failed("mini", "", false), h("mac mini (dan)", "n", false)}, []string{"mini", "mac mini (dan)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, r := range OnePerServer(tt.in) {
				got = append(got, r.Label)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("OnePerServer labels = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResumeFindsAgentsOnTheInteractivePath(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("needs bash")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "agents")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	rc := "case $- in *i*) ;; *) return ;; esac\nexport PATH=\"$HOME/agents:$PATH\"\n"
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(". \"$HOME/.bashrc\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "fake-agent"), []byte("#!/bin/sh\necho \"resumed $1 in $(pwd)\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	argv := resumeVia(Source{}, dir, []string{"fake-agent", "ID"})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if want := "resumed ID in " + dir; !strings.Contains(string(out), want) {
		t.Fatalf("got %q, want it to contain %q", out, want)
	}
}

func TestSourceAcceptsAnotherVersionWithTheSameProtocol(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	req := `{"protocol":1,"version":"9.9.9","op":"rows","past_limit":5}`
	var out strings.Builder
	if err := Serve(t.Context(), "1.0.0", strings.NewReader(req), &out, true); err != nil {
		t.Fatalf("source refused a host on another version: %v", err)
	}
	if err := Serve(t.Context(), "1.0.0", strings.NewReader(req), &out, false); err == nil {
		t.Fatal("a module on another version was accepted")
	}
	bad := `{"protocol":2,"version":"1.0.0","op":"rows"}`
	if err := Serve(t.Context(), "1.0.0", strings.NewReader(bad), &out, true); err == nil {
		t.Fatal("a source accepted another protocol")
	}
}
