package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func setupQueueTest(t *testing.T, fakeMode string) (*model, *queueModel) {
	t.Helper()
	m := newTestModel(t)
	m.queue = newQueueModel()
	m.runner = &actionRunner{
		binary:     os.Args[0],
		done:       make(chan actionResult, 1),
		invalidate: func() {},
	}
	t.Setenv("FAKE_MODE", fakeMode)
	return m, m.queue
}

func TestDrainHappyPath(t *testing.T) {
	m, q := setupQueueTest(t, "json_happy")

	// Fake three ready cues + one skipped (no scram).
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(dir, n+".cue"), []byte(""))
		writeFile(t, filepath.Join(dir, n+".scram"), []byte(""))
	}
	writeFile(t, filepath.Join(dir, "d.cue"), []byte("")) // no scram → skipped
	// Use nil model to suppress the automatic worker kick from addPaths;
	// we drive drain synchronously below.
	q.addPaths(nil, []string{dir})

	if got := len(q.items); got != 4 {
		t.Fatalf("len(items) = %d, want 4", got)
	}

	// Run the drain synchronously in this test (no goroutine).
	q.drain(m)

	states := make(map[queueState]int)
	for _, it := range q.items {
		states[it.State]++
	}
	if states[qDone] != 3 {
		t.Errorf("qDone = %d, want 3", states[qDone])
	}
	if states[qSkipped] != 1 {
		t.Errorf("qSkipped = %d, want 1", states[qSkipped])
	}
	// 3 event rows written, all pack/success.
	if rows := eventsRecent(m.db, 10); len(rows) != 3 {
		t.Errorf("event rows = %d, want 3", len(rows))
	}
	// No toast — queue suppresses.
	if m.toast != nil {
		t.Errorf("toast = %+v, want nil (queue suppresses)", m.toast)
	}
}

func TestStopQueue(t *testing.T) {
	m, q := setupQueueTest(t, "json_long")

	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		writeFile(t, filepath.Join(dir, n+".cue"), []byte(""))
		writeFile(t, filepath.Join(dir, n+".scram"), []byte(""))
	}
	// Use nil model to suppress the automatic worker kick from addPaths;
	// we kick drain explicitly below.
	q.addPaths(nil, []string{dir})

	// Run drain on a goroutine; stop after the first item starts.
	go q.drain(m)
	// Wait for first item to start running.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		running := false
		for _, it := range q.items {
			if it.State == qRunning {
				running = true
			}
		}
		q.mu.Unlock()
		if running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Trigger Stop queue.
	q.mu.Lock()
	q.stopped = true
	q.mu.Unlock()
	m.runner.Cancel()

	// Wait for worker to exit.
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		wr := q.workerRunning
		q.mu.Unlock()
		if !wr {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// First item cancelled, rest also cancelled.
	states := make(map[queueState]int)
	for _, it := range q.items {
		states[it.State]++
	}
	if states[qCancelled] != 5 {
		t.Errorf("qCancelled = %d, want 5; items = %+v", states[qCancelled], q.items)
	}
	// Only one event row (the in-flight item).
	if rows := eventsRecent(m.db, 10); len(rows) != 1 {
		t.Errorf("event rows = %d, want 1 (only the in-flight)", len(rows))
	} else if rows[0].Status != "cancelled" {
		t.Errorf("event row status = %q, want cancelled", rows[0].Status)
	}
}

func TestCancelCurrent(t *testing.T) {
	// First item hits the long fake; subsequent items hit happy.
	// We approximate by running with json_long, cancelling, then flipping FAKE_MODE.
	// Simpler approach: run with json_long, cancel, then verify item 0 is cancelled
	// and the remaining items are still qReady (worker waiting on Start).
	m, q := setupQueueTest(t, "json_long")
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(dir, n+".cue"), []byte(""))
		writeFile(t, filepath.Join(dir, n+".scram"), []byte(""))
	}
	// Use nil model to suppress the automatic worker kick from addPaths;
	// we kick drain explicitly below.
	q.addPaths(nil, []string{dir})

	go q.drain(m)
	// Wait for item 0 to start.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		s := q.items[0].State
		q.mu.Unlock()
		if s == qRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Cancel current ONLY (do NOT set q.stopped).
	m.runner.Cancel()

	// Worker should advance to item 1, which will also be json_long. To avoid
	// running the full test forever, set q.stopped after a short window so the
	// remaining items go to cancelled cleanly.
	time.Sleep(200 * time.Millisecond)
	q.mu.Lock()
	q.stopped = true
	q.mu.Unlock()
	m.runner.Cancel()

	// Wait for worker exit.
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		wr := q.workerRunning
		q.mu.Unlock()
		if !wr {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if q.items[0].State != qCancelled {
		t.Errorf("items[0].State = %q, want %q", q.items[0].State, qCancelled)
	}
	// Item 0 produced one event row, item 1 (also cancelled mid-run) produced
	// another. Item 2 was never spawned (q.stopped). So 2 event rows.
	if rows := eventsRecent(m.db, 10); len(rows) != 2 {
		t.Errorf("event rows = %d, want 2", len(rows))
	}
}

func TestPackPhasesCoverage(t *testing.T) {
	miniscramBin, err := exec.LookPath("miniscram")
	if err != nil {
		t.Skip("miniscram not on PATH; build it and run again to lock packPhases")
	}

	fixtureCue := os.Getenv("MINISCRAM_TEST_CUE")
	if fixtureCue == "" {
		// Scan for fixtures in test-discs
		matches, _ := filepath.Glob(filepath.Join("..", "..", "test-discs", "*", "*.cue"))
		matchesUpper, _ := filepath.Glob(filepath.Join("..", "..", "test-discs", "*", "*.CUE"))
		matches = append(matches, matchesUpper...)
		if len(matches) > 0 {
			fixtureCue = matches[0]
		}
	}
	if fixtureCue == "" {
		t.Skip("no MINISCRAM_TEST_CUE env and no test-discs/*/.cue fixture; skipping")
	}

	// Pack to a temp output to avoid clobbering a real .miniscram next to the cue.
	tmp := t.TempDir()
	out := filepath.Join(tmp, "fixture.miniscram")
	// Note: we run pack without --keep-source to allow it to complete without
	// attempting to delete the .scram file. We just care about the emitted steps.
	cmd := exec.Command(miniscramBin, "pack", "--progress=json", "-o", out, fixtureCue)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// We don't care if pack fails; we just want to check the step labels it emitted.
	_ = cmd.Run()

	seen := map[string]bool{}
	var unknown []string
	for _, line := range strings.Split(stderr.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev progressEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("non-JSON line on --progress=json stderr: %q", line)
			continue
		}
		if ev.Type != "step" {
			continue
		}
		seen[ev.Label] = true
		if _, ok := lookupFraction(ev.Label); !ok {
			unknown = append(unknown, ev.Label)
		}
	}
	if len(unknown) > 0 {
		t.Errorf("emitted step labels with no packPhases match:\n  %s", strings.Join(unknown, "\n  "))
	}
	if len(seen) == 0 {
		t.Error("no step events captured — did --progress=json work?")
	}
}
