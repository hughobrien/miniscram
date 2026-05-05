package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestClassify_Ready(t *testing.T) {
	dir := t.TempDir()
	cue := filepath.Join(dir, "disc.cue")
	writeFile(t, cue, []byte("dummy cue"))
	writeFile(t, filepath.Join(dir, "disc.scram"), []byte("dummy scram"))

	item := classify(cue, 7)
	if item.State != qReady {
		t.Errorf("State = %q, want %q", item.State, qReady)
	}
	if item.ID != 7 {
		t.Errorf("ID = %d, want 7", item.ID)
	}
	if item.Basename != "disc.cue" {
		t.Errorf("Basename = %q, want disc.cue", item.Basename)
	}
}

func TestClassify_NoScram(t *testing.T) {
	dir := t.TempDir()
	cue := filepath.Join(dir, "disc.cue")
	writeFile(t, cue, []byte("dummy"))

	item := classify(cue, 0)
	if item.State != qSkipped {
		t.Errorf("State = %q, want %q", item.State, qSkipped)
	}
	if item.Reason != "no sibling .scram" {
		t.Errorf("Reason = %q, want 'no sibling .scram'", item.Reason)
	}
}

func TestClassify_AlreadyPacked(t *testing.T) {
	dir := t.TempDir()
	cue := filepath.Join(dir, "disc.cue")
	writeFile(t, cue, []byte("dummy"))
	writeFile(t, filepath.Join(dir, "disc.scram"), []byte("dummy"))
	writeFile(t, filepath.Join(dir, "disc.miniscram"), []byte("dummy"))

	item := classify(cue, 0)
	if item.State != qSkipped {
		t.Errorf("State = %q, want %q", item.State, qSkipped)
	}
	if item.Reason != "already packed" {
		t.Errorf("Reason = %q, want 'already packed'", item.Reason)
	}
}

func TestWalkForCues_Recursive(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "a.cue"), []byte(""))
	writeFile(t, filepath.Join(root, "b", "deep", "b.cue"), []byte(""))
	writeFile(t, filepath.Join(root, "c.txt"), []byte("")) // ignored
	writeFile(t, filepath.Join(root, ".hidden", "x.cue"), []byte("")) // skipped (dot dir)
	writeFile(t, filepath.Join(root, "B.CUE"), []byte("")) // case-insensitive ext

	cues := walkForCues(root)
	if len(cues) != 3 {
		t.Fatalf("got %d cues, want 3: %v", len(cues), cues)
	}
	// Sorted ascending. B.CUE comes before a/a.cue lexicographically.
	if cues[0] != filepath.Join(root, "B.CUE") {
		t.Errorf("cues[0] = %q", cues[0])
	}
	if cues[1] != filepath.Join(root, "a", "a.cue") {
		t.Errorf("cues[1] = %q", cues[1])
	}
	if cues[2] != filepath.Join(root, "b", "deep", "b.cue") {
		t.Errorf("cues[2] = %q", cues[2])
	}
}

func TestAddPaths_DirAndDedup(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.cue"), []byte(""))
	writeFile(t, filepath.Join(root, "a.scram"), []byte(""))
	writeFile(t, filepath.Join(root, "sub", "b.cue"), []byte(""))
	writeFile(t, filepath.Join(root, "sub", "b.scram"), []byte(""))

	q := &queueModel{}
	// First drop: a directory.
	q.addPaths(nil, []string{root})
	if got := len(q.items); got != 2 {
		t.Fatalf("after dir drop, len(items) = %d, want 2", got)
	}
	// Second drop: same cue via a relative-ish path.
	q.addPaths(nil, []string{filepath.Join(root, "sub", "..", "a.cue")})
	if got := len(q.items); got != 2 {
		t.Errorf("after dedup drop, len(items) = %d, want 2 (dedup failed)", got)
	}
}

func TestAddPaths_NonCueNonDirIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x.txt"), []byte(""))
	q := &queueModel{}
	q.addPaths(nil, []string{filepath.Join(dir, "x.txt")})
	if got := len(q.items); got != 0 {
		t.Errorf("len(items) = %d, want 0 (.txt should be ignored)", got)
	}
}

func TestPrettyProgressLine_Step(t *testing.T) {
	got := prettyProgressLine(`{"type":"step","label":"writing miniscram"}`)
	if got != "step: writing miniscram…" {
		t.Errorf("got %q", got)
	}
}

func TestPrettyProgressLine_Done(t *testing.T) {
	got := prettyProgressLine(`{"type":"done","label":"writing miniscram","msg":"123 bytes"}`)
	if got != "done: writing miniscram ✓" {
		t.Errorf("got %q", got)
	}
}

func TestPrettyProgressLine_Info(t *testing.T) {
	got := prettyProgressLine(`{"type":"info","msg":"detected write offset 0"}`)
	if got != "detected write offset 0" {
		t.Errorf("got %q", got)
	}
}

func TestPrettyProgressLine_Fail(t *testing.T) {
	got := prettyProgressLine(`{"type":"fail","label":"writing miniscram","error":"disk full"}`)
	if got != "disk full" {
		t.Errorf("got %q", got)
	}
}

func TestPrettyProgressLine_NotJSON(t *testing.T) {
	got := prettyProgressLine("plain text line")
	if got != "plain text line" {
		t.Errorf("got %q", got)
	}
}

func TestLookupFraction_KnownLabels(t *testing.T) {
	cases := map[string]float64{
		"resolving cue /tmp/x.cue":            0.02, // prefix-match with suffix
		"detecting write offset":              0.05,
		"checking constant offset":            0.08,
		"hashing tracks":                      0.15,
		"hashing scram":                       0.30,
		"building scram prediction + delta":   0.65,
		"writing container":                   0.95,
	}
	for label, want := range cases {
		got, ok := lookupFraction(label)
		if !ok {
			t.Errorf("lookupFraction(%q) returned !ok", label)
			continue
		}
		if got != want {
			t.Errorf("lookupFraction(%q) = %f, want %f", label, got, want)
		}
	}
}

func TestLookupFraction_UnknownLabel(t *testing.T) {
	if _, ok := lookupFraction("frobnicating"); ok {
		t.Error("lookupFraction unknown label returned ok=true")
	}
}
