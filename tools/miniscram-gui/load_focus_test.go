package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeEmptyCue creates a zero-byte .cue in t.TempDir() and returns its path.
// load() reads the file, sets m.cueText, parses zero tracks, and kicks off
// hashCueBins over an empty slice — none of which influences m.view.
func writeEmptyCue(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.cue")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatalf("write cue: %v", err)
	}
	return p
}

func TestLoadAndFocus_FromStats_PivotsToInspect(t *testing.T) {
	m := newTestModel(t)
	m.view = "stats"

	m.loadAndFocus(writeEmptyCue(t))

	if m.view != "file" {
		t.Errorf("view = %q, want %q", m.view, "file")
	}
}

func TestLoadAndFocus_FromInspect_StaysOnInspect(t *testing.T) {
	m := newTestModel(t)
	m.view = "file"

	m.loadAndFocus(writeEmptyCue(t))

	if m.view != "file" {
		t.Errorf("view = %q, want %q", m.view, "file")
	}
}

// Regression lock: bare load() must NEVER touch m.view. The worker-driven
// callers (queue auto-follow, post-pack reload, startup -load) rely on this.
func TestLoad_FromStats_DoesNotPivot(t *testing.T) {
	m := newTestModel(t)
	m.view = "stats"

	m.load(writeEmptyCue(t))

	if m.view != "stats" {
		t.Errorf("view = %q, want %q", m.view, "stats")
	}
}

func TestLoad_FromInspect_StaysOnInspect(t *testing.T) {
	m := newTestModel(t)
	m.view = "file"

	m.load(writeEmptyCue(t))

	if m.view != "file" {
		t.Errorf("view = %q, want %q", m.view, "file")
	}
}
