package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCueTrio creates the optional sibling files in dir for the given
// base name. When a value is "skip" the file is not written; "" writes
// a zero-byte file; any other value writes those bytes.
func writeCueTrio(t *testing.T, dir, base, cueContent, scramContent, miniscramContent string) (cue, scram, miniscram string) {
	t.Helper()
	cue = filepath.Join(dir, base+".cue")
	scram = filepath.Join(dir, base+".scram")
	miniscram = filepath.Join(dir, base+".miniscram")
	if cueContent != "skip" {
		if err := os.WriteFile(cue, []byte(cueContent), 0o644); err != nil {
			t.Fatalf("write cue: %v", err)
		}
	}
	if scramContent != "skip" {
		if err := os.WriteFile(scram, []byte(scramContent), 0o644); err != nil {
			t.Fatalf("write scram: %v", err)
		}
	}
	if miniscramContent != "skip" {
		if err := os.WriteFile(miniscram, []byte(miniscramContent), 0o644); err != nil {
			t.Fatalf("write miniscram: %v", err)
		}
	}
	return
}

func TestLoad_CueWithMissingScram_RedirectsToMiniscram(t *testing.T) {
	dir := t.TempDir()
	cue, _, miniscram := writeCueTrio(t, dir, "disc", "", "skip", "anything")

	m := newTestModel(t)
	m.load(cue)

	if m.path != miniscram {
		t.Errorf("m.path = %q, want %q (redirect should have switched to .miniscram)", m.path, miniscram)
	}
}

func TestLoad_CueWithEmptyScram_RedirectsToMiniscram(t *testing.T) {
	dir := t.TempDir()
	cue, _, miniscram := writeCueTrio(t, dir, "disc", "", "", "anything")

	m := newTestModel(t)
	m.load(cue)

	if m.path != miniscram {
		t.Errorf("m.path = %q, want %q (zero-byte .scram should be treated as missing)", m.path, miniscram)
	}
}

func TestLoad_CueWithScramAndMiniscram_StaysOnCue(t *testing.T) {
	dir := t.TempDir()
	cue, _, _ := writeCueTrio(t, dir, "disc", "", "scram-bytes", "anything")

	m := newTestModel(t)
	m.load(cue)

	if m.path != cue {
		t.Errorf("m.path = %q, want %q", m.path, cue)
	}
	if m.kind != "cue" {
		t.Errorf("m.kind = %q, want \"cue\"", m.kind)
	}
}

func TestLoad_CueWithNoScramAndNoMiniscram_StaysOnCue(t *testing.T) {
	dir := t.TempDir()
	cue, _, _ := writeCueTrio(t, dir, "disc", "", "skip", "skip")

	m := newTestModel(t)
	m.load(cue)

	if m.path != cue {
		t.Errorf("m.path = %q, want %q", m.path, cue)
	}
	if m.kind != "cue" {
		t.Errorf("m.kind = %q, want \"cue\"", m.kind)
	}
}
