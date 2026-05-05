package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestModel returns a *model with an in-memory SQLite (schema applied)
// and just enough wiring for handleActionResult to run.
func newTestModel(t *testing.T) *model {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return &model{db: db}
}

// writeTempBytes writes n bytes to a temp file and returns its path.
func writeTempBytes(t *testing.T, name string, n int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func mustOnly(t *testing.T, m *model) eventRec {
	t.Helper()
	rows := eventsRecent(m.db, 10)
	if len(rows) != 1 {
		t.Fatalf("got %d event rows, want 1", len(rows))
	}
	return rows[0]
}

func TestHandleActionResult_PackSuccess(t *testing.T) {
	m := newTestModel(t)
	out := writeTempBytes(t, "disc.miniscram", 1500)

	m.handleActionResult(actionResult{
		Action: "pack", Input: "/in/disc.cue", Output: out,
		DurationMs: 1234, Status: "success",
	})

	ev := mustOnly(t, m)
	if ev.Action != "pack" || ev.Status != "success" {
		t.Errorf("row mismatch: %+v", ev)
	}
	if ev.MiniscramSize != 1500 {
		t.Errorf("MiniscramSize = %d, want 1500", ev.MiniscramSize)
	}
	// Pack also calls `miniscram inspect` on the output. Our temp file isn't
	// a real container, so the inspect call fails or returns junk — those
	// fields stay zero. That's the intended fallback.
	if ev.ScramSize != 0 {
		t.Errorf("ScramSize = %d, want 0 (inspect of dummy file should fail)", ev.ScramSize)
	}
	if m.toast == nil || m.toast.Action != "pack" || m.toast.OutputSize != 1500 {
		t.Errorf("toast = %+v, want pack/1500", m.toast)
	}
	if m.toast.DurationMs != 1234 {
		t.Errorf("toast.DurationMs = %d, want 1234", m.toast.DurationMs)
	}
}

func TestHandleActionResult_UnpackSuccess(t *testing.T) {
	m := newTestModel(t)
	m.miniscramOnDisk = 1500
	m.meta = &inspectJSON{
		WriteOffsetBytes: -48,
		DeltaRecords:     []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`)},
	}
	m.meta.Scram.Size = 100000

	out := writeTempBytes(t, "disc.scram", 4096)
	m.handleActionResult(actionResult{
		Action: "unpack", Input: "/in/disc.miniscram", Output: out,
		DurationMs: 2345, Status: "success",
	})

	ev := mustOnly(t, m)
	if ev.Action != "unpack" || ev.Status != "success" {
		t.Errorf("row = %+v", ev)
	}
	if ev.ScramSize != 4096 {
		t.Errorf("ScramSize = %d, want 4096", ev.ScramSize)
	}
	if ev.MiniscramSize != 1500 {
		t.Errorf("MiniscramSize = %d, want 1500", ev.MiniscramSize)
	}
	if ev.OverrideRecords != 2 {
		t.Errorf("OverrideRecords = %d, want 2", ev.OverrideRecords)
	}
	if ev.WriteOffset != -48 {
		t.Errorf("WriteOffset = %d, want -48", ev.WriteOffset)
	}
	if m.toast == nil || m.toast.OutputSize != 4096 {
		t.Errorf("toast outputSize = %v, want 4096", m.toast)
	}
}

func TestHandleActionResult_VerifySuccess(t *testing.T) {
	m := newTestModel(t)
	m.miniscramOnDisk = 1500
	m.meta = &inspectJSON{}
	m.meta.Scram.Size = 99999

	m.handleActionResult(actionResult{
		Action: "verify", Input: "/in/disc.miniscram",
		DurationMs: 999, Status: "success",
	})

	ev := mustOnly(t, m)
	if ev.ScramSize != 99999 || ev.MiniscramSize != 1500 {
		t.Errorf("verify row size mismatch: %+v", ev)
	}
	// Verify still emits a toast; size segment is 0 (no output), Reveal
	// button is suppressed at render time when Output == "".
	if m.toast == nil || m.toast.Action != "verify" {
		t.Fatalf("verify toast = %+v, want non-nil verify toast", m.toast)
	}
	if m.toast.Output != "" {
		t.Errorf("verify toast.Output = %q, want empty", m.toast.Output)
	}
	if m.toast.OutputSize != 0 {
		t.Errorf("verify toast.OutputSize = %d, want 0", m.toast.OutputSize)
	}
}

func TestHandleActionResult_Fail(t *testing.T) {
	m := newTestModel(t)
	// Pre-set a toast that should be cleared by the fail.
	m.toast = &toastState{Action: "pack", ExpiresAt: time.Now().Add(time.Hour)}

	m.handleActionResult(actionResult{
		Action: "pack", Input: "/in/disc.cue", Output: "/out/disc.miniscram",
		Status: "fail", Error: "scram not found",
	})

	ev := mustOnly(t, m)
	if ev.Status != "fail" || ev.Error != "scram not found" {
		t.Errorf("fail row = %+v", ev)
	}
	if m.toast != nil {
		t.Errorf("toast should be cleared on fail, got %+v", m.toast)
	}
}

func TestHandleActionResult_Cancelled(t *testing.T) {
	m := newTestModel(t)
	m.toast = &toastState{Action: "pack", ExpiresAt: time.Now().Add(time.Hour)}

	m.handleActionResult(actionResult{
		Action: "unpack", Input: "/in/disc.miniscram", Status: "cancelled",
	})

	ev := mustOnly(t, m)
	if ev.Status != "cancelled" {
		t.Errorf("cancelled row Status = %q", ev.Status)
	}
	if m.toast != nil {
		t.Errorf("toast should be cleared on cancel, got %+v", m.toast)
	}
}

// TestHandleActionResult_TitleFromRedump verifies that when the redump_cache
// has a 'found' entry for the loaded miniscram's first track SHA-1, the
// resolved disc title is stamped onto the event row's Title.
func TestHandleActionResult_TitleFromRedump(t *testing.T) {
	m := newTestModel(t)
	m.miniscramOnDisk = 1500
	m.meta = &inspectJSON{}
	m.meta.Scram.Size = 100000
	// Inject one track with a SHA-1 that we'll pre-populate in redump_cache.
	track := struct {
		Number   int               `json:"number"`
		Mode     string            `json:"mode"`
		FirstLBA int               `json:"first_lba"`
		Filename string            `json:"filename"`
		Size     int64             `json:"size"`
		Hashes   map[string]string `json:"hashes"`
	}{Number: 1, Mode: "MODE1/2352", Hashes: map[string]string{"sha1": "deadbeef"}}
	m.meta.Tracks = append(m.meta.Tracks, track)

	redumpPut(m.db, "deadbeef", &redumpEntry{
		State:       "found",
		URL:         "http://redump.org/disc/12345/",
		Title:       "Test Disc",
		CheckedUnix: time.Now().Unix(),
	})

	m.handleActionResult(actionResult{
		Action: "verify", Input: "/in/disc.miniscram", Status: "success",
	})

	ev := mustOnly(t, m)
	if ev.Title != "Test Disc" {
		t.Errorf("Title = %q, want %q", ev.Title, "Test Disc")
	}
}

func TestBuildEventRec_PackSuccess(t *testing.T) {
	m := newTestModel(t)
	out := writeTempBytes(t, "disc.miniscram", 1500)

	ev := buildEventRec(m, "pack", "/in/disc.cue", out, actionResult{
		Action: "pack", Input: "/in/disc.cue", Output: out,
		DurationMs: 1234, Status: "success",
	})

	if ev.Action != "pack" || ev.Status != "success" {
		t.Errorf("row mismatch: %+v", ev)
	}
	if ev.MiniscramSize != 1500 {
		t.Errorf("MiniscramSize = %d, want 1500", ev.MiniscramSize)
	}
	if ev.InputPath != "/in/disc.cue" {
		t.Errorf("InputPath = %q, want /in/disc.cue", ev.InputPath)
	}
	if ev.DurationMs != 1234 {
		t.Errorf("DurationMs = %d, want 1234", ev.DurationMs)
	}
	// helper must NOT have side effects:
	if rows := eventsRecent(m.db, 10); len(rows) != 0 {
		t.Errorf("buildEventRec must not insert; got %d rows", len(rows))
	}
	if m.toast != nil {
		t.Errorf("buildEventRec must not set toast; got %+v", m.toast)
	}
}

func TestHashCueBin(t *testing.T) {
	// "abc" has well-known md5/sha1/sha256 digests.
	p := writeTempBytes(t, "track.bin", 0)
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	md5h, sha1h, sha256h, err := hashCueBin(p)
	if err != nil {
		t.Fatalf("hashCueBin: %v", err)
	}
	if md5h != "900150983cd24fb0d6963f7d28e17f72" {
		t.Errorf("md5 = %q", md5h)
	}
	if sha1h != "a9993e364706816aba3e25717850c26c9cd0d89d" {
		t.Errorf("sha1 = %q", sha1h)
	}
	if sha256h != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("sha256 = %q", sha256h)
	}
}

func TestHashCueBinMissing(t *testing.T) {
	if _, _, _, err := hashCueBin("/nonexistent/path"); err == nil {
		t.Error("expected error for missing file")
	}
}

// TestStartActionOrSurfaceFailure_BinaryMissing simulates the
// miniscram-not-on-PATH case: Start() returns ENOENT, the helper
// must write a fail event row, set a fail toast, and translate the
// raw exec error into the user-facing "place it next to miniscram-gui"
// message.
func TestStartActionOrSurfaceFailure_BinaryMissing(t *testing.T) {
	m := newTestModel(t)
	m.runner = &actionRunner{
		binary: "/no/such/miniscram-binary-for-test",
		done:   make(chan actionResult, 1),
	}

	m.startActionOrSurfaceFailure("pack", "/in/disc.cue", "/out/disc.miniscram", "pack", "/in/disc.cue")

	ev := mustOnly(t, m)
	if ev.Status != "fail" {
		t.Errorf("event status = %q, want fail", ev.Status)
	}
	if !strings.Contains(ev.Error, "miniscram CLI not found") {
		t.Errorf("event error = %q, want translated user-facing message", ev.Error)
	}
	if m.toast == nil {
		t.Fatal("toast should be set on Start failure")
	}
	if m.toast.Status != "fail" {
		t.Errorf("toast.Status = %q, want fail", m.toast.Status)
	}
	if m.toast.Action != "pack" {
		t.Errorf("toast.Action = %q, want pack", m.toast.Action)
	}
	if !strings.Contains(m.toast.FailMsg, "miniscram CLI not found") {
		t.Errorf("toast.FailMsg = %q", m.toast.FailMsg)
	}
}

// TestStartActionOrSurfaceFailure_AlreadyRunning ensures we don't
// spam the user when the single-flight invariant trips (button click
// during a window where the runner is still busy from a prior click).
func TestStartActionOrSurfaceFailure_AlreadyRunning(t *testing.T) {
	m := newTestModel(t)
	m.runner = &actionRunner{
		binary: "/no/such/binary",
		done:   make(chan actionResult, 1),
		// state non-nil → Start returns errAlreadyRunning before
		// touching the (missing) binary.
		state: &runningState{Action: "pack"},
	}

	m.startActionOrSurfaceFailure("pack", "/in/disc.cue", "/out/disc.miniscram", "pack", "/in/disc.cue")

	if rows := eventsRecent(m.db, 10); len(rows) != 0 {
		t.Errorf("errAlreadyRunning should be silent; got %d event row(s)", len(rows))
	}
	if m.toast != nil {
		t.Errorf("errAlreadyRunning should not show a toast; got %+v", m.toast)
	}
}

// TestResolveMiniscram_FallbackPath confirms resolveMiniscram returns
// "miniscram" (PATH lookup) when no sibling and no two-up candidate
// exists.
func TestResolveMiniscram_FallbackPath(t *testing.T) {
	got := resolveMiniscram()
	// In the test harness, os.Args[0] is the test binary; there is
	// likely no sibling `miniscram` and no `../../miniscram`. We
	// can't assert which path resolveMiniscram returns absolutely,
	// but it should at least produce a non-empty string.
	if got == "" {
		t.Error("resolveMiniscram returned empty string")
	}
}

func TestReadURIList(t *testing.T) {
	body := "# comment\nfile:///tmp/a.cue\nfile:///tmp/b%20space.cue\n\nhttp://example.com/x.cue\n"
	paths := readURIList(strings.NewReader(body))
	if len(paths) != 2 {
		t.Fatalf("got %d paths, want 2: %v", len(paths), paths)
	}
	if paths[0] != "/tmp/a.cue" {
		t.Errorf("paths[0] = %q", paths[0])
	}
	if paths[1] != "/tmp/b space.cue" {
		t.Errorf("paths[1] = %q (percent-decoding broken?)", paths[1])
	}
}
