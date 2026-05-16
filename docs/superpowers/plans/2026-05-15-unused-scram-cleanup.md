# Unused .scram cleanup after audio-only reject — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tell users their `.scram` is useless on audio-only cuesheets, and offer a one-step removal via a new CLI flag (`--remove-unused-scram`) and a batched bottom-of-queue button in the GUI.

**Architecture:** Five small changes layered bottom-up. (1) Reporter API gains `UnusedScram(path, size)` with text/json/quiet implementations; the JSON wire shape gains `path` and `size` fields. (2) Pack emits the new event before failing with `ErrAudioOnlyDisc`. (3) CLI grows `--remove-unused-scram` for direct CLI users. (4) GUI captures the new NDJSON event into a deduped accumulator. (5) GUI renders a bottom-of-queue button that deletes the accumulated `.scram` paths in one click.

**Tech Stack:** Go 1.21+, Gio v0.9.0 (`gioui.org`), standard `testing`.

**Spec:** [`docs/superpowers/specs/2026-05-15-unused-scram-cleanup-design.md`](../specs/2026-05-15-unused-scram-cleanup-design.md)

**Branch dependency:** This work is on `docs/unused-scram-cleanup`, which branches off `fix/issue-50-audio-only-pretty-errors` (PR #51). The PR #51 base must merge to `main` before opening this work's PR against `main`; alternatively the PR can target `fix/...` first and rebase later.

---

## File Structure

| Path | Role | Action |
| --- | --- | --- |
| `reporter.go` | Reporter interface + 3 impls + wire shape | Modify (add `UnusedScram`, add fields to `progressEvent`) |
| `reporter_test.go` | Reporter tests | Modify (new test per reporter impl) |
| `pack.go` | Pack pipeline | Modify (emit event before audio-only fail) |
| `pack_test.go` | Pack tests | Modify (capture-reporter test, stat-fail test) |
| `main.go` | CLI entry points | Modify (new `--remove-unused-scram` flag, post-Pack handling) |
| `cli_test.go` | CLI tests | Modify (flag-on test, flag-off test) |
| `tools/miniscram-gui/queue.go` | GUI queue model + wire shape | Modify (add `Path`/`Size` to local `progressEvent`, add `unusedScram` type + `unusedScrams []unusedScram`, `appendUnusedScram`, `clearUnusedScrams`, `snapshotUnusedScrams`, expose in `queueSnapshot`) |
| `tools/miniscram-gui/queue_test.go` | GUI queue tests | Modify (capture + dedupe tests) |
| `tools/miniscram-gui/main.go` | GUI event loop | Modify (NDJSON capture branch for `unused-scram`; wire button click → deletion handler) |
| `tools/miniscram-gui/queue_widget.go` | GUI queue panel layout | Modify (add `unusedScramBar` widget; insert between divider and `Stop queue`) |
| `tools/miniscram-gui/queue_widget_test.go` | GUI widget tests | Modify (assert bar visibility logic via the snapshot) |
| `tools/miniscram-gui/unused_scram_test.go` | Deletion handler tests | **Create** (real-file deletion via `t.TempDir`; partial-failure) |

---

## Task 0: Confirm branch state

**Files:** none (git ops)

- [ ] **Step 1: Verify on `docs/unused-scram-cleanup` with the spec committed**

```bash
cd /home/hugh/miniscram
git branch --show-current
git log --oneline -3
```

Expected: branch is `docs/unused-scram-cleanup`; most recent commit is `docs: design spec for unused-scram cleanup after audio-only reject`. The branch's parent should be the tip of `fix/issue-50-audio-only-pretty-errors` (PR #51).

If you find yourself on a different branch, `git checkout docs/unused-scram-cleanup`.

- [ ] **Step 2: Confirm clean tree (modulo go.sum)**

```bash
git status
```

Expected: clean except possibly `tools/miniscram-gui/go.sum` (pre-existing untracked drift; **do not** commit it).

---

## Task 1: Reporter.UnusedScram + wire-shape fields

**Files:**
- Modify: `reporter.go` (interface + 3 impls + `progressEvent`)
- Modify: `reporter_test.go`

- [ ] **Step 1: Write failing tests**

Append to `/home/hugh/miniscram/reporter_test.go`:

```go
func TestReporterUnusedScram_Text(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, false)
	r.UnusedScram("/disc/x.scram", 765077352)
	got := buf.String()
	if !strings.Contains(got, "/disc/x.scram") {
		t.Errorf("output %q missing path", got)
	}
	if !strings.Contains(got, "765077352") {
		t.Errorf("output %q missing size", got)
	}
	if !strings.Contains(got, "--remove-unused-scram") {
		t.Errorf("output %q missing flag hint", got)
	}
}

func TestReporterUnusedScram_Quiet(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, true)
	r.UnusedScram("/disc/x.scram", 765077352)
	if buf.Len() != 0 {
		t.Errorf("quiet reporter wrote %q; want empty", buf.String())
	}
}

func TestReporterUnusedScram_JSON(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONReporter(&buf)
	r.UnusedScram("/disc/x.scram", 765077352)
	const want = `{"type":"unused-scram","path":"/disc/x.scram","size":765077352}` + "\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
```

These will need `bytes` and `strings` in the test file's imports — `reporter_test.go` already imports both (see `TestJSONReporter` and `TestReporterStep`).

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/hugh/miniscram
go test -run TestReporterUnusedScram -count=1
```

Expected: compile error — `UnusedScram` is not a method on the `Reporter` interface.

- [ ] **Step 3: Add `UnusedScram` to the `Reporter` interface**

Edit `/home/hugh/miniscram/reporter.go`. Change the interface block (lines 12-16):

```go
type Reporter interface {
	Step(label string) StepHandle
	Info(format string, args ...any)
	Warn(format string, args ...any)
}
```

to:

```go
type Reporter interface {
	Step(label string) StepHandle
	Info(format string, args ...any)
	Warn(format string, args ...any)
	// UnusedScram reports a source .scram file whose contents are
	// useless to miniscram (today: only emitted on audio-only cues,
	// ahead of the matching fail event). Carries the path and byte
	// size so downstream consumers can offer cleanup.
	UnusedScram(path string, size int64)
}
```

- [ ] **Step 4: Implement `UnusedScram` on `textReporter`**

In `/home/hugh/miniscram/reporter.go`, immediately after the existing `Warn` method on `textReporter` (around line 51), add:

```go
func (r *textReporter) UnusedScram(path string, size int64) {
	fmt.Fprintf(r.w, "note: unused .scram at %s (%d bytes) — pass --remove-unused-scram to delete\n", path, size)
}
```

- [ ] **Step 5: Implement `UnusedScram` on `quietReporter`**

In the same file, immediately after the existing `quietReporter.Warn` line (around line 101), add:

```go
func (quietReporter) UnusedScram(string, int64) {}
```

- [ ] **Step 6: Extend `progressEvent` with optional `path` + `size` fields**

In the same file, change the `progressEvent` struct (lines 135-140):

```go
type progressEvent struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Msg   string `json:"msg,omitempty"`
	Error string `json:"error,omitempty"`
}
```

to:

```go
type progressEvent struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Msg   string `json:"msg,omitempty"`
	Error string `json:"error,omitempty"`
	Path  string `json:"path,omitempty"`
	Size  int64  `json:"size,omitempty"`
}
```

- [ ] **Step 7: Implement `UnusedScram` on `jsonReporter`**

In the same file, immediately after the existing `jsonReporter.Warn` method (around line 168), add:

```go
func (r *jsonReporter) UnusedScram(path string, size int64) {
	_ = r.enc.Encode(progressEvent{Type: "unused-scram", Path: path, Size: size})
}
```

- [ ] **Step 8: Run tests to verify they pass**

```bash
cd /home/hugh/miniscram
go test -run TestReporterUnusedScram -count=1
```

Expected: PASS (all three subtests).

- [ ] **Step 9: Run the full root-package test suite**

```bash
cd /home/hugh/miniscram
go test ./... -count=1
```

Expected: PASS. No callers of the `Reporter` interface should regress (the only existing impls are in this file and any tests that construct a fake; if a fake exists it now needs the new method — fix in this task if so).

If a test file constructs a struct that implements `Reporter` and is now missing `UnusedScram`, add the no-op:

```go
func (yourFakeReporter) UnusedScram(string, int64) {}
```

- [ ] **Step 10: Commit**

```bash
cd /home/hugh/miniscram
git add reporter.go reporter_test.go
git commit -m "$(cat <<'EOF'
reporter: add UnusedScram event for cleanup hints

New Reporter method emits a structured event signalling that a source
.scram file is useless to miniscram (today: only on audio-only cues).
Text reporter prints a one-line hint pointing at the new
--remove-unused-scram flag; JSON reporter emits {"type":"unused-scram",
"path":..., "size":...}; quiet reporter drops it.

progressEvent gains optional path and size fields with omitempty so
existing event types stay byte-identical on the wire.
EOF
)"
```

If `go test` in Step 9 surfaced extra files needing the no-op, add them to the same commit's `git add` list.

---

## Task 2: Pack emits the event before the audio-only fail

**Files:**
- Modify: `pack.go` (the existing audio-only reject block, currently around lines 95-103)
- Modify: `pack_test.go`

- [ ] **Step 1: Write failing tests**

Append to `/home/hugh/miniscram/pack_test.go`:

```go
// capturingReporter records all calls for assertion. Implements the
// full Reporter interface; Step returns a no-op StepHandle that
// records Fail-with-error calls for completeness.
type capturingReporter struct {
	info, warn []string
	unused     []struct {
		Path string
		Size int64
	}
	fails []error
}

func (c *capturingReporter) Step(label string) StepHandle {
	return &capturingStep{c: c}
}
func (c *capturingReporter) Info(format string, args ...any) {
	c.info = append(c.info, fmt.Sprintf(format, args...))
}
func (c *capturingReporter) Warn(format string, args ...any) {
	c.warn = append(c.warn, fmt.Sprintf(format, args...))
}
func (c *capturingReporter) UnusedScram(path string, size int64) {
	c.unused = append(c.unused, struct {
		Path string
		Size int64
	}{path, size})
}

type capturingStep struct{ c *capturingReporter }

func (s *capturingStep) Done(string, ...any) {}
func (s *capturingStep) Fail(err error)      { s.c.fails = append(s.c.fails, err) }

func TestPackEmitsUnusedScramEvent(t *testing.T) {
	dir := t.TempDir()
	cue := "FILE \"x (Track 1).bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n"
	if err := os.WriteFile(filepath.Join(dir, "x.cue"), []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x (Track 1).bin"), make([]byte, SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	const scramPayload = "junk-scram-content"
	scramPath := filepath.Join(dir, "x.scram")
	if err := os.WriteFile(scramPath, []byte(scramPayload), 0o644); err != nil {
		t.Fatal(err)
	}
	cap := &capturingReporter{}
	err := Pack(PackOptions{
		CuePath:    filepath.Join(dir, "x.cue"),
		ScramPath:  scramPath,
		OutputPath: filepath.Join(dir, "x.miniscram"),
	}, cap)
	if !errors.Is(err, ErrAudioOnlyDisc) {
		t.Fatalf("expected ErrAudioOnlyDisc, got %v", err)
	}
	if len(cap.unused) != 1 {
		t.Fatalf("got %d UnusedScram calls, want 1", len(cap.unused))
	}
	if cap.unused[0].Path != scramPath {
		t.Errorf("path = %q, want %q", cap.unused[0].Path, scramPath)
	}
	if cap.unused[0].Size != int64(len(scramPayload)) {
		t.Errorf("size = %d, want %d", cap.unused[0].Size, len(scramPayload))
	}
}

func TestPackUnusedScramStatFailureIsSwallowed(t *testing.T) {
	dir := t.TempDir()
	cue := "FILE \"x (Track 1).bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n"
	if err := os.WriteFile(filepath.Join(dir, "x.cue"), []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x (Track 1).bin"), make([]byte, SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	cap := &capturingReporter{}
	err := Pack(PackOptions{
		CuePath:    filepath.Join(dir, "x.cue"),
		ScramPath:  filepath.Join(dir, "does-not-exist.scram"),
		OutputPath: filepath.Join(dir, "x.miniscram"),
	}, cap)
	if !errors.Is(err, ErrAudioOnlyDisc) {
		t.Fatalf("expected ErrAudioOnlyDisc, got %v", err)
	}
	if len(cap.unused) != 0 {
		t.Errorf("got %d UnusedScram calls, want 0 (scram missing)", len(cap.unused))
	}
}
```

(`pack_test.go` already imports `errors`, `fmt`, `os`, `path/filepath`, `testing` per `TestPackRejectsAudioOnlyCue`.)

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/hugh/miniscram
go test -run "TestPackEmitsUnusedScramEvent|TestPackUnusedScramStatFailureIsSwallowed" -count=1
```

Expected: FAIL. `TestPackEmitsUnusedScramEvent` fails with `got 0 UnusedScram calls, want 1` because Pack doesn't emit the event yet. (`TestPackUnusedScramStatFailureIsSwallowed` may pass coincidentally — that's OK; the assertion locks in current correct behavior post-fix.)

- [ ] **Step 3: Emit the event from Pack**

Edit `/home/hugh/miniscram/pack.go`. Find the audio-only reject block (currently around lines 95-103):

```go
	// 1b. audio-only short-circuit. Pack has nothing to do when every
	// track is AUDIO — detectWriteOffset would scan the entire scram
	// before failing. Fail fast with a clean sentinel.
	if !anyDataTrack(tracks) {
		st = r.Step("checking disc type")
		st.Fail(ErrAudioOnlyDisc)
		return ErrAudioOnlyDisc
	}
```

Replace with:

```go
	// 1b. audio-only short-circuit. Pack has nothing to do when every
	// track is AUDIO — detectWriteOffset would scan the entire scram
	// before failing. Fail fast with a clean sentinel. Stat the scram
	// first so the reporter can hint at cleanup; stat failure is
	// swallowed (the reject still fires, just without the hint).
	if !anyDataTrack(tracks) {
		if info, err := os.Stat(opts.ScramPath); err == nil {
			r.UnusedScram(opts.ScramPath, info.Size())
		}
		st = r.Step("checking disc type")
		st.Fail(ErrAudioOnlyDisc)
		return ErrAudioOnlyDisc
	}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /home/hugh/miniscram
go test -run "TestPackEmitsUnusedScramEvent|TestPackUnusedScramStatFailureIsSwallowed" -count=1
```

Expected: PASS.

- [ ] **Step 5: Run full root-package suite**

```bash
cd /home/hugh/miniscram
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /home/hugh/miniscram
git add pack.go pack_test.go
git commit -m "$(cat <<'EOF'
pack: emit UnusedScram event on audio-only reject

Stat the source .scram before raising ErrAudioOnlyDisc and forward
the path + size to the reporter. Downstream consumers (CLI flag, GUI
accumulator) use the event to offer cleanup.

Stat failure is swallowed so a missing scram still produces a clean
reject (the cleanup hint is the only thing lost).
EOF
)"
```

---

## Task 3: CLI `--remove-unused-scram` flag

**Files:**
- Modify: `main.go::runPack` (around lines 140-192)
- Modify: `cli_test.go` (or `pack_test.go` if `cli_test.go` doesn't house CLI-flag tests; check first)

- [ ] **Step 1: Check where existing CLI flag tests live**

```bash
cd /home/hugh/miniscram
grep -l "keep-source\|runPack\b" *_test.go
```

If matches include `cli_test.go`, append to that file. Otherwise append to `pack_test.go`. The plan below assumes `cli_test.go`; substitute if needed.

- [ ] **Step 2: Write failing tests**

Append to the chosen test file (importing `bytes` if not already imported):

```go
func TestCLIPack_RemoveUnusedScramFlagDeletesFile(t *testing.T) {
	dir := t.TempDir()
	cue := "FILE \"x (Track 1).bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n"
	cuePath := filepath.Join(dir, "x.cue")
	if err := os.WriteFile(cuePath, []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x (Track 1).bin"), make([]byte, SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	scramPath := filepath.Join(dir, "x.scram")
	if err := os.WriteFile(scramPath, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := runPack([]string{"--progress=json", "--remove-unused-scram", cuePath}, &stderr)
	if code == exitOK {
		t.Fatalf("exit = exitOK, want non-zero for ErrAudioOnlyDisc")
	}
	if _, err := os.Stat(scramPath); !os.IsNotExist(err) {
		t.Errorf("scram still exists (err=%v); want removed", err)
	}
}

func TestCLIPack_NoFlagKeepsFile(t *testing.T) {
	dir := t.TempDir()
	cue := "FILE \"x (Track 1).bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n"
	cuePath := filepath.Join(dir, "x.cue")
	if err := os.WriteFile(cuePath, []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x (Track 1).bin"), make([]byte, SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	scramPath := filepath.Join(dir, "x.scram")
	if err := os.WriteFile(scramPath, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := runPack([]string{"--progress=json", cuePath}, &stderr)
	if code == exitOK {
		t.Fatalf("exit = exitOK, want non-zero")
	}
	if _, err := os.Stat(scramPath); err != nil {
		t.Errorf("scram missing (err=%v); want preserved without flag", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
cd /home/hugh/miniscram
go test -run "TestCLIPack_RemoveUnusedScramFlagDeletesFile|TestCLIPack_NoFlagKeepsFile" -count=1
```

Expected: FAIL — either the flag is unknown (`runPack` returns exitUsage and `os.Stat` finds the file), or the file isn't being removed. The "NoFlag" test may pass coincidentally (the file is preserved today on this path); the "Flag" test fails for sure.

- [ ] **Step 4: Add the flag to `runPack`**

Edit `/home/hugh/miniscram/main.go`. Find the `runPack` function and its flag block (around line 143):

```go
	var keepSource, force, forceLong bool
	positional, common, exit, ok := parseSubcommand("pack", packHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&keepSource, "keep-source", false, "keep .scram after verified pack")
		// ... force flags ...
	})
```

Add `removeUnused` to the declarations and a new `fs.BoolVar` call. Concretely:

Change:

```go
	var keepSource, force, forceLong bool
```

to:

```go
	var keepSource, removeUnused, force, forceLong bool
```

Then inside the `parseSubcommand` callback, immediately after the existing `keep-source` line, add:

```go
		fs.BoolVar(&removeUnused, "remove-unused-scram", false,
			"remove source .scram when miniscram has nothing to pack (audio-only cues)")
```

- [ ] **Step 5: Handle the flag after `Pack` returns**

Still in `main.go::runPack`. Find the post-`Pack` block (around lines 181-191):

```go
	err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath, OutputPath: out,
		LeadinLBA: LBALeadinStart,
	}, rep)
	if err != nil {
		return errToExit(err)
	}
	if !keepSource {
		if err := os.Remove(scramPath); err != nil {
			rep.Warn("source removal skipped: %v", err)
		} else {
			rep.Info("removed source %s", scramPath)
		}
	}
	return exitOK
```

Replace with:

```go
	err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath, OutputPath: out,
		LeadinLBA: LBALeadinStart,
	}, rep)
	if errors.Is(err, ErrAudioOnlyDisc) && removeUnused {
		if rmErr := os.Remove(scramPath); rmErr != nil {
			rep.Warn("could not remove unused source: %v", rmErr)
		} else {
			rep.Info("removed unused source %s", scramPath)
		}
	}
	if err != nil {
		return errToExit(err)
	}
	if !keepSource {
		if err := os.Remove(scramPath); err != nil {
			rep.Warn("source removal skipped: %v", err)
		} else {
			rep.Info("removed source %s", scramPath)
		}
	}
	return exitOK
```

(The `errors` package is already imported; check the import block at the top of `main.go` — if not, add it.)

- [ ] **Step 6: Update `packHelpText` to document the new flag**

```bash
cd /home/hugh/miniscram
grep -n "packHelpText\s*=" main.go
```

The help text lives in a `const` block near the top of `main.go`. Find the `--keep-source` line in `packHelpText` and add a new line immediately after it:

```
  --remove-unused-scram     remove .scram when there's nothing to pack
                            (audio-only cues; default: keep)
```

Match the surrounding indentation exactly.

- [ ] **Step 7: Run tests to verify they pass**

```bash
cd /home/hugh/miniscram
go test -run "TestCLIPack_RemoveUnusedScramFlagDeletesFile|TestCLIPack_NoFlagKeepsFile" -count=1
```

Expected: PASS (both).

- [ ] **Step 8: Run full suite**

```bash
cd /home/hugh/miniscram
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
cd /home/hugh/miniscram
git add main.go cli_test.go pack_test.go
git commit -m "$(cat <<'EOF'
cli: add --remove-unused-scram flag for audio-only cleanup

Opt-in flag (default: keep). When set, runPack removes the source
.scram after Pack returns ErrAudioOnlyDisc. Exit code still reflects
the audio-only failure — the removal is a side-effect on the way out.

Inverse default vs. --keep-source on purpose: the success path is
recoverable from the .miniscram container, the audio-only reject is
not.
EOF
)"
```

(Stage only the files you actually modified — if only `cli_test.go` got new tests, omit `pack_test.go` from the `git add`.)

---

## Task 4: GUI captures `unused-scram` events

**Files:**
- Modify: `tools/miniscram-gui/queue.go` (`progressEvent`, new `unusedScram` type, accumulator on `queueModel`, surface on `queueSnapshot`)
- Modify: `tools/miniscram-gui/queue_test.go`
- Modify: `tools/miniscram-gui/main.go` (NDJSON capture branch)

- [ ] **Step 1: Write failing tests**

Append to `/home/hugh/miniscram/tools/miniscram-gui/queue_test.go`:

```go
func TestQueue_AppendUnusedScramDedupes(t *testing.T) {
	q := newQueueModel()
	q.appendUnusedScram(unusedScram{Path: "/a.scram", Size: 100})
	q.appendUnusedScram(unusedScram{Path: "/a.scram", Size: 100})
	q.appendUnusedScram(unusedScram{Path: "/b.scram", Size: 200})
	got := q.snapshotUnusedScrams()
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Path != "/a.scram" || got[1].Path != "/b.scram" {
		t.Errorf("got %+v, want [/a.scram /b.scram]", got)
	}
}

func TestQueue_ClearUnusedScrams(t *testing.T) {
	q := newQueueModel()
	q.appendUnusedScram(unusedScram{Path: "/a.scram", Size: 1})
	q.appendUnusedScram(unusedScram{Path: "/b.scram", Size: 2})
	q.clearUnusedScrams()
	if got := q.snapshotUnusedScrams(); len(got) != 0 {
		t.Errorf("got %d entries after clear; want 0", len(got))
	}
}

func TestQueue_SnapshotExposesUnusedScrams(t *testing.T) {
	q := newQueueModel()
	q.appendUnusedScram(unusedScram{Path: "/a.scram", Size: 729})
	snap := q.Snapshot()
	if len(snap.UnusedScrams) != 1 || snap.UnusedScrams[0].Path != "/a.scram" {
		t.Errorf("snapshot.UnusedScrams = %+v", snap.UnusedScrams)
	}
}

func TestProgressEvent_ParsesUnusedScram(t *testing.T) {
	const line = `{"type":"unused-scram","path":"/disc/x.scram","size":765077352}`
	var ev progressEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != "unused-scram" || ev.Path != "/disc/x.scram" || ev.Size != 765077352 {
		t.Errorf("got %+v", ev)
	}
}
```

(`queue_test.go` already imports `testing`; `json` may need adding to the import block — check first and add if missing.)

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test -run "TestQueue_AppendUnusedScramDedupes|TestQueue_ClearUnusedScrams|TestQueue_SnapshotExposesUnusedScrams|TestProgressEvent_ParsesUnusedScram" -count=1
```

Expected: FAIL — compile errors (the types and methods don't exist yet, `progressEvent` has no `Path`/`Size` fields).

- [ ] **Step 3: Add `Path` + `Size` to the GUI's `progressEvent`**

Edit `/home/hugh/miniscram/tools/miniscram-gui/queue.go`. Find the `progressEvent` struct around line 183:

```go
type progressEvent struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Msg   string `json:"msg,omitempty"`
	Error string `json:"error,omitempty"`
}
```

Replace with:

```go
type progressEvent struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Msg   string `json:"msg,omitempty"`
	Error string `json:"error,omitempty"`
	Path  string `json:"path,omitempty"`
	Size  int64  `json:"size,omitempty"`
}
```

- [ ] **Step 4: Add `unusedScram` type and accumulator on `queueModel`**

In the same file, around line 42 (the `queueModel` struct), add an `unusedScrams` field and define the helper type. Immediately before the `queueModel` struct, add:

```go
// unusedScram is an entry in the queue's accumulator of .scram files
// flagged as useless by Pack (today: audio-only cues). The GUI's
// bottom-of-queue button drains this slice.
type unusedScram struct {
	Path string
	Size int64
}
```

Then change the `queueModel` struct from:

```go
type queueModel struct {
	mu            sync.Mutex
	items         []queueItem
	nextID        int64
	deleteScram   bool
	stopped       bool
	autoFollow    bool
	workerRunning bool
}
```

to:

```go
type queueModel struct {
	mu            sync.Mutex
	items         []queueItem
	nextID        int64
	deleteScram   bool
	stopped       bool
	autoFollow    bool
	workerRunning bool
	unusedScrams  []unusedScram
}
```

Immediately after the `removeByID` method (around line 432, just before `Snapshot`), add the accumulator methods:

```go
// appendUnusedScram adds an entry unless the same Path is already
// present. Safe to call from any goroutine.
func (q *queueModel) appendUnusedScram(u unusedScram) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, x := range q.unusedScrams {
		if x.Path == u.Path {
			return
		}
	}
	q.unusedScrams = append(q.unusedScrams, u)
}

// clearUnusedScrams drops the entire accumulator. Used after a
// successful batch deletion or a dismiss.
func (q *queueModel) clearUnusedScrams() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.unusedScrams = nil
}

// snapshotUnusedScrams returns a copy of the accumulator suitable for
// the UI goroutine to iterate without the queue mutex held.
func (q *queueModel) snapshotUnusedScrams() []unusedScram {
	q.mu.Lock()
	defer q.mu.Unlock()
	cp := make([]unusedScram, len(q.unusedScrams))
	copy(cp, q.unusedScrams)
	return cp
}
```

- [ ] **Step 5: Surface the accumulator on `queueSnapshot`**

Still in `queue.go`. Change `queueSnapshot` (around line 413):

```go
type queueSnapshot struct {
	Items         []queueItem
	DeleteScram   bool
	WorkerRunning bool
	ReadyCount    int
	SkippedCount  int
}
```

to:

```go
type queueSnapshot struct {
	Items         []queueItem
	DeleteScram   bool
	WorkerRunning bool
	ReadyCount    int
	SkippedCount  int
	UnusedScrams  []unusedScram
}
```

Update the `Snapshot` method body (around line 436) — after the existing `cp := make(...)` for `Items`, also copy `unusedScrams` into the result:

Change:

```go
	cp := make([]queueItem, len(q.items))
	copy(cp, q.items)
	s := queueSnapshot{
		Items:         cp,
		DeleteScram:   q.deleteScram,
		WorkerRunning: q.workerRunning,
	}
```

to:

```go
	cp := make([]queueItem, len(q.items))
	copy(cp, q.items)
	us := make([]unusedScram, len(q.unusedScrams))
	copy(us, q.unusedScrams)
	s := queueSnapshot{
		Items:         cp,
		DeleteScram:   q.deleteScram,
		WorkerRunning: q.workerRunning,
		UnusedScrams:  us,
	}
```

- [ ] **Step 6: Wire the NDJSON capture branch in `main.go`**

Edit `/home/hugh/miniscram/tools/miniscram-gui/main.go`. Find the per-frame NDJSON capture block (around lines 1214-1221):

```go
				if rs := mdl.runner.Snapshot(); rs != nil {
					var ev progressEvent
					if json.Unmarshal([]byte(rs.LastLine), &ev) == nil && ev.Type == "step" && ev.Label != "" {
						if frac, ok := lookupFraction(ev.Label); ok {
							mdl.queue.UpdateRunningProgress(ev.Label, frac)
						}
					}
				}
```

Replace with:

```go
				if rs := mdl.runner.Snapshot(); rs != nil {
					var ev progressEvent
					if json.Unmarshal([]byte(rs.LastLine), &ev) == nil {
						switch {
						case ev.Type == "step" && ev.Label != "":
							if frac, ok := lookupFraction(ev.Label); ok {
								mdl.queue.UpdateRunningProgress(ev.Label, frac)
							}
						case ev.Type == "unused-scram" && ev.Path != "":
							mdl.queue.appendUnusedScram(unusedScram{Path: ev.Path, Size: ev.Size})
						}
					}
				}
```

Note: the NDJSON capture branch fires on every frame using `rs.LastLine` (the most recent stderr line). This means the same `unused-scram` event may be observed many times before being overwritten by a later step event; `appendUnusedScram` dedupes on `Path`, so this is benign.

- [ ] **Step 7: Run tests to verify they pass**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test -run "TestQueue_AppendUnusedScramDedupes|TestQueue_ClearUnusedScrams|TestQueue_SnapshotExposesUnusedScrams|TestProgressEvent_ParsesUnusedScram" -count=1
```

Expected: PASS (all four).

- [ ] **Step 8: Run full GUI suite**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
cd /home/hugh/miniscram
git add tools/miniscram-gui/queue.go tools/miniscram-gui/queue_test.go tools/miniscram-gui/main.go
git commit -m "$(cat <<'EOF'
gui: capture unused-scram events into queue accumulator

queueModel grows an unusedScrams slice plus three methods
(appendUnusedScram, clearUnusedScrams, snapshotUnusedScrams).
appendUnusedScram dedupes on Path so the per-frame NDJSON capture
loop's repeated re-observation of LastLine is benign.

The capture loop in main.go now handles two event kinds: step (per-
row progress, existing) and unused-scram (new accumulator).
queueSnapshot exposes the accumulated slice for layout consumption
in the next task.
EOF
)"
```

---

## Task 5: GUI bottom-of-queue bar + delete handler

**Files:**
- Modify: `tools/miniscram-gui/queue_widget.go` (new `unusedScramBar`, hook into `queuePanel` layout)
- Modify: `tools/miniscram-gui/main.go` (wire button clicks)
- Create: `tools/miniscram-gui/unused_scram_test.go` (deletion handler tests)

- [ ] **Step 1: Write failing tests**

Create `/home/hugh/miniscram/tools/miniscram-gui/unused_scram_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeleteUnusedScrams_AllOK confirms the handler removes every
// path in the accumulator and returns zero failures.
func TestDeleteUnusedScrams_AllOK(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.scram")
	b := filepath.Join(dir, "b.scram")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	q := newQueueModel()
	q.appendUnusedScram(unusedScram{Path: a, Size: 1})
	q.appendUnusedScram(unusedScram{Path: b, Size: 1})

	failed := deleteUnusedScrams(q)
	if len(failed) != 0 {
		t.Errorf("failed = %v, want empty", failed)
	}
	for _, p := range []string{a, b} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err=%v)", p, err)
		}
	}
	if len(q.snapshotUnusedScrams()) != 0 {
		t.Errorf("accumulator not cleared after full success")
	}
}

// TestDeleteUnusedScrams_PartialFailure confirms missing paths are
// reported as failures, but the slice is still cleared and the
// existing file is still removed.
func TestDeleteUnusedScrams_PartialFailure(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.scram")
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "does-not-exist.scram")
	q := newQueueModel()
	q.appendUnusedScram(unusedScram{Path: a, Size: 1})
	q.appendUnusedScram(unusedScram{Path: missing, Size: 1})

	failed := deleteUnusedScrams(q)
	if len(failed) != 1 || failed[0] != missing {
		t.Errorf("failed = %v, want [%s]", failed, missing)
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Errorf("%s still exists (err=%v); want removed", a, err)
	}
	if len(q.snapshotUnusedScrams()) != 0 {
		t.Errorf("accumulator not cleared after partial failure")
	}
}

// TestUnusedScramBar_HiddenWhenEmpty asserts the widget's
// visibility-from-snapshot rule. Pure-data test: the widget itself
// returns zero dims when the snapshot has no entries.
func TestUnusedScramBar_HiddenWhenEmpty(t *testing.T) {
	q := newQueueModel()
	snap := q.Snapshot()
	if len(snap.UnusedScrams) != 0 {
		t.Fatalf("snapshot.UnusedScrams = %+v, want empty", snap.UnusedScrams)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test -run "TestDeleteUnusedScrams|TestUnusedScramBar" -count=1
```

Expected: FAIL — `deleteUnusedScrams` is undeclared (compile error). The third test will incidentally pass once the file compiles, but tests 1 and 2 require the function.

- [ ] **Step 3: Implement `deleteUnusedScrams`**

Create `/home/hugh/miniscram/tools/miniscram-gui/unused_scram.go`:

```go
// tools/miniscram-gui/unused_scram.go
package main

import "os"

// deleteUnusedScrams snapshots the queue's accumulator, removes each
// path, clears the accumulator regardless of outcome, and returns the
// paths that could not be removed. Errors per file are not surfaced;
// the caller decides how to message a partial failure.
func deleteUnusedScrams(q *queueModel) []string {
	snap := q.snapshotUnusedScrams()
	var failed []string
	for _, u := range snap {
		if err := os.Remove(u.Path); err != nil {
			failed = append(failed, u.Path)
		}
	}
	q.clearUnusedScrams()
	return failed
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test -run "TestDeleteUnusedScrams|TestUnusedScramBar" -count=1
```

Expected: PASS (all three).

- [ ] **Step 5: Add buttons to `queuePanelButtons`**

Edit `/home/hugh/miniscram/tools/miniscram-gui/queue_widget.go`. Change the struct (lines 28-35):

```go
type queuePanelButtons struct {
	AddFiles      widget.Clickable
	AddDir        widget.Clickable
	DeleteScramCB widget.Bool
	Stop          widget.Clickable
	rowClick      map[int64]*widget.Clickable
	rowAction     map[int64]*widget.Clickable // × for ready, ⏹ for running
}
```

to:

```go
type queuePanelButtons struct {
	AddFiles            widget.Clickable
	AddDir              widget.Clickable
	DeleteScramCB       widget.Bool
	Stop                widget.Clickable
	DeleteUnusedScrams  widget.Clickable
	DismissUnusedScrams widget.Clickable
	rowClick            map[int64]*widget.Clickable
	rowAction           map[int64]*widget.Clickable // × for ready, ⏹ for running
}
```

- [ ] **Step 6: Add the `unusedScramBar` widget**

In the same file, immediately after `queueStopButton` (around line 148), add:

```go
// unusedScramBar renders a single button + dismiss × at the bottom
// of the queue panel when there are accumulated unused .scram paths.
// Click drains the accumulator (deletion handler), × clears it
// without deleting. Returns zero dims when the accumulator is empty.
func unusedScramBar(th *material.Theme, snap queueSnapshot, btns *queuePanelButtons) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if len(snap.UnusedScrams) == 0 {
			return layout.Dimensions{}
		}
		var total int64
		for _, u := range snap.UnusedScrams {
			total += u.Size
		}
		label := fmt.Sprintf("Delete %d unused .scram (%s)", len(snap.UnusedScrams), humanBytes(total))
		if len(snap.UnusedScrams) != 1 {
			label = fmt.Sprintf("Delete %d unused .scrams (%s)", len(snap.UnusedScrams), humanBytes(total))
		}
		return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					btn := material.Button(th, &btns.DeleteUnusedScrams, label)
					btn.Background = mustRGB("3a1a1a")
					btn.Color = bad
					btn.CornerRadius = unit.Dp(4)
					btn.TextSize = unit.Sp(12)
					btn.Inset = layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}
					return btn.Layout(gtx)
				}),
				layout.Rigid(spacer(6, 0)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					btn := material.Button(th, &btns.DismissUnusedScrams, "×")
					btn.Background = surface2
					btn.Color = text2
					btn.CornerRadius = unit.Dp(4)
					btn.TextSize = unit.Sp(12)
					btn.Inset = layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}
					return btn.Layout(gtx)
				}),
			)
		})
	}
}
```

(`fmt` is already imported by `queue_widget.go`; `humanBytes` lives in `main.go` in the same package.)

- [ ] **Step 7: Hook the bar into `queuePanel` layout**

Still in `queue_widget.go`. Change the `queuePanel` body (lines 76-89). Find:

```go
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(queueHeader(th, snap)),
					layout.Rigid(spacer(0, 10)),
					layout.Rigid(queueAddButtons(th, btns)),
					layout.Rigid(spacer(0, 8)),
					layout.Rigid(queueDeleteScramRow(th, btns)),
					layout.Rigid(spacer(0, 8)),
					layout.Rigid(thinDivider),
					queueItemsList(th, snap, btns, listScroll),
					layout.Rigid(thinDivider),
					layout.Rigid(queueStopButton(th, snap, btns)),
				)
```

Replace with:

```go
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(queueHeader(th, snap)),
					layout.Rigid(spacer(0, 10)),
					layout.Rigid(queueAddButtons(th, btns)),
					layout.Rigid(spacer(0, 8)),
					layout.Rigid(queueDeleteScramRow(th, btns)),
					layout.Rigid(spacer(0, 8)),
					layout.Rigid(thinDivider),
					queueItemsList(th, snap, btns, listScroll),
					layout.Rigid(thinDivider),
					layout.Rigid(unusedScramBar(th, snap, btns)),
					layout.Rigid(queueStopButton(th, snap, btns)),
				)
```

- [ ] **Step 8: Wire button clicks in `main.go`**

Edit `/home/hugh/miniscram/tools/miniscram-gui/main.go`. Find the existing button-click handlers in the event loop. Use the existing `cancelBtn.Clicked(gtx)` handler (around line 1272) as a sibling — add similar handlers for the two new buttons.

Search for where `qBtns.Stop.Clicked(gtx)` is handled (it should be in the same neighbourhood). After that handler, add:

```go
			if qBtns.DeleteUnusedScrams.Clicked(gtx) {
				failed := deleteUnusedScrams(mdl.queue)
				if len(failed) > 0 {
					total := len(failed)
					mdl.toast = &toastState{
						Status:    "fail",
						FailMsg:   fmt.Sprintf("failed to delete %d unused .scram(s)", total),
						ExpiresAt: time.Now().Add(8 * time.Second),
					}
				}
				if mdl.invalidate != nil {
					mdl.invalidate()
				}
			}
			if qBtns.DismissUnusedScrams.Clicked(gtx) {
				mdl.queue.clearUnusedScrams()
				if mdl.invalidate != nil {
					mdl.invalidate()
				}
			}
```

If you can't find `qBtns.Stop.Clicked` — search by `qBtns\.` regex; the queue button handlers live together. Place the new handlers in the same block, in the same style.

- [ ] **Step 9: Run tests to verify they pass**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test -run "TestDeleteUnusedScrams|TestUnusedScramBar" -count=1
```

Expected: PASS.

- [ ] **Step 10: Run full GUI suite**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 11: Smoke-build the GUI binary (do not run)**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go build -o /tmp/miniscram-gui-unused .
```

Expected: clean build. Delete the artifact after: `rm /tmp/miniscram-gui-unused`.

- [ ] **Step 12: Commit**

```bash
cd /home/hugh/miniscram
git add tools/miniscram-gui/queue_widget.go tools/miniscram-gui/main.go tools/miniscram-gui/unused_scram.go tools/miniscram-gui/unused_scram_test.go
git commit -m "$(cat <<'EOF'
gui: bottom-of-queue button to batch-delete unused .scrams

unusedScramBar renders when the queue's accumulator is non-empty. A
single red button drains every accumulated path through
deleteUnusedScrams; a × dismisses without touching disk. Both clear
the accumulator on completion.

Partial failures surface via a brief fail toast; per-failure detail
is dropped on the floor in V1 (the user can re-attempt manually).
EOF
)"
```

---

## Task 6: Push branch (PR deferred)

**Files:** none (git/gh ops)

PR #51 (the audio-only-reject fix) must merge to `main` before this work can target `main`. Until then, push the branch but do not open a PR against `main`.

- [ ] **Step 1: Push the branch**

```bash
cd /home/hugh/miniscram
git push -u origin docs/unused-scram-cleanup
```

- [ ] **Step 2: Decide PR target**

Options:

- **(a) Wait for #51 to merge**, then rebase `docs/unused-scram-cleanup` onto `main` and open the PR against `main`:
  ```bash
  git fetch origin main
  git rebase origin/main
  git push --force-with-lease
  gh pr create --title "..." --body "..."
  ```
- **(b) Open a stacked PR now**, targeting `fix/issue-50-audio-only-pretty-errors`:
  ```bash
  gh pr create --base fix/issue-50-audio-only-pretty-errors --title "..." --body "..."
  ```

Recommendation: (a) unless review momentum on #51 has stalled.

- [ ] **Step 3: Draft PR body when ready**

When opening the PR, use:

```bash
gh pr create --title "feat: --remove-unused-scram + GUI batch cleanup (#50)" --body "$(cat <<'EOF'
## Summary

Follow-up to #51 (audio-only cue reject). Now that audio-only cues fail fast with a clean error, address the second half of the user pain: the leftover 700 MB+ .scram file.

- New Reporter event `unused-scram` carries the path + size from Pack.
- CLI flag `--remove-unused-scram` (opt-in; default keep) deletes the .scram after Pack returns ErrAudioOnlyDisc.
- GUI accumulates events across the queue run; a bottom-of-queue button batch-deletes them all on one click.

Inverse default vs. `--keep-source`: the success path is recoverable from the .miniscram container, the audio-only reject path is not — so deletion is opt-in here.

## Test plan

- [ ] `go test ./... -count=1` at repo root
- [ ] `CC=/usr/bin/clang CGO_ENABLED=1 go test ./... -count=1` in `tools/miniscram-gui`
- [ ] Manual: `miniscram pack --remove-unused-scram AudioCD.cue` — exits non-zero in <1s, .scram is gone
- [ ] Manual: drop two audio-only cues onto the GUI queue, run, observe the "Delete 2 unused .scrams" button at the bottom, click — files removed, button vanishes

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-Review (writer's checklist)

**Spec coverage:**
- Spec §1 (Reporter UnusedScram + progressEvent fields) → Task 1.
- Spec §2 (Pack emission point) → Task 2.
- Spec §3 (CLI --remove-unused-scram flag + help text) → Task 3.
- Spec §4 (GUI accumulator, capture, render, action, lifecycle) → Tasks 4 + 5.
- Spec testing section: reporter (Task 1), pack (Task 2), CLI (Task 3), accumulator + dedup (Task 4), deletion handler + partial failure (Task 5). All covered.

**Placeholder scan:** no TBD / TODO. Every code-changing step shows the exact code. The "check first" step in Task 3 (where existing CLI tests live) is a concrete grep with two fallback options, not a placeholder.

**Type consistency:** `UnusedScram(path string, size int64)` signature consistent across Reporter interface (Task 1), Pack emission (Task 2), CLI tests (Task 3). `unusedScram` struct with `Path string, Size int64` consistent across GUI tasks (4, 5). `appendUnusedScram` / `clearUnusedScrams` / `snapshotUnusedScrams` method names match exactly across the two GUI tasks. `deleteUnusedScrams(q *queueModel) []string` signature matches between Task 5's tests, implementation, and main.go hook.
