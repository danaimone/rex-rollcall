package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallKeepsAnExistingSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := filepath.Join(home, ".config", "rex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	initPath := filepath.Join(dir, "init.lua")
	initLua := "local ok, rollcall = pcall(require, \"rex_rollcall\")\nif ok then rollcall.setup{ key = \"cmd+s>a\" } end\n"
	if err := os.WriteFile(initPath, []byte(initLua), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := install(false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != initLua {
		t.Fatalf("init.lua changed:\n%s", got)
	}
}
