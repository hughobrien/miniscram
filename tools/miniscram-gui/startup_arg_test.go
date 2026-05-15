package main

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWriteFile(t *testing.T, p string) string {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestResolveStartupAction_Empty(t *testing.T) {
	a := resolveStartupAction("", nil)
	if a.Kind != "" {
		t.Errorf("Kind = %q, want \"\"", a.Kind)
	}
	if a.Path != "" {
		t.Errorf("Path = %q, want \"\"", a.Path)
	}
}

func TestResolveStartupAction_LoadFlagOnly_File(t *testing.T) {
	f := mustWriteFile(t, filepath.Join(t.TempDir(), "x.cue"))

	a := resolveStartupAction(f, nil)
	if a.Kind != "file" {
		t.Errorf("Kind = %q, want \"file\"", a.Kind)
	}
	if a.Path != f {
		t.Errorf("Path = %q, want %q", a.Path, f)
	}
}

func TestResolveStartupAction_LoadFlagOnly_Dir(t *testing.T) {
	d := t.TempDir()

	a := resolveStartupAction(d, nil)
	if a.Kind != "dir" {
		t.Errorf("Kind = %q, want \"dir\"", a.Kind)
	}
	if a.Path != d {
		t.Errorf("Path = %q, want %q", a.Path, d)
	}
}

func TestResolveStartupAction_DirPositional(t *testing.T) {
	d := t.TempDir()

	a := resolveStartupAction("", []string{d})
	if a.Kind != "dir" {
		t.Errorf("Kind = %q, want \"dir\"", a.Kind)
	}
	if a.Path != d {
		t.Errorf("Path = %q, want %q", a.Path, d)
	}
}

func TestResolveStartupAction_FilePositional(t *testing.T) {
	f := mustWriteFile(t, filepath.Join(t.TempDir(), "x.cue"))

	a := resolveStartupAction("", []string{f})
	if a.Kind != "file" {
		t.Errorf("Kind = %q, want \"file\"", a.Kind)
	}
	if a.Path != f {
		t.Errorf("Path = %q, want %q", a.Path, f)
	}
}

func TestResolveStartupAction_NonexistentPositional(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	a := resolveStartupAction("", []string{missing})
	if a.Kind != "file" {
		t.Errorf("Kind = %q, want \"file\" (fallthrough so load() can surface the error)", a.Kind)
	}
	if a.Path != missing {
		t.Errorf("Path = %q, want %q", a.Path, missing)
	}
}

func TestResolveStartupAction_LoadFlagWinsOverPositional(t *testing.T) {
	flagFile := mustWriteFile(t, filepath.Join(t.TempDir(), "from-flag.cue"))
	positionalDir := t.TempDir()

	a := resolveStartupAction(flagFile, []string{positionalDir})
	if a.Kind != "file" {
		t.Errorf("Kind = %q, want \"file\" (flag should win)", a.Kind)
	}
	if a.Path != flagFile {
		t.Errorf("Path = %q, want %q", a.Path, flagFile)
	}
}

func TestResolveStartupAction_FirstPositionalWins(t *testing.T) {
	first := t.TempDir()
	second := mustWriteFile(t, filepath.Join(t.TempDir(), "second.cue"))

	a := resolveStartupAction("", []string{first, second})
	if a.Kind != "dir" {
		t.Errorf("Kind = %q, want \"dir\" (first arg wins)", a.Kind)
	}
	if a.Path != first {
		t.Errorf("Path = %q, want %q", a.Path, first)
	}
}
