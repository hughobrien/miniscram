# miniscram-gui unpack flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the Unpack… button for real, and unify long-running Pack/Verify/Unpack actions behind a shared `actionRunner` + `runningStrip` + `toast` so the UI never freezes during a multi-second subprocess.

**Architecture:** A new `actionRunner` runs `miniscram pack|unpack|verify` as a subprocess on a dedicated goroutine, line-tails its stderr into a mutex-protected `runningState`, and fires an `onDone` callback when the process exits. UI consumes the state via a `runningStrip` widget rendered at the window-frame level, plus a `toast` widget that surfaces successful completions. Cancellation is SIGTERM; the CLI handles its own cleanup.

**Tech Stack:** Go (`tools/miniscram-gui/` own go.mod), Gio v0.9, modernc.org/sqlite, stdlib `os/exec` + `bufio` + `sync` + `context`.

**Spec:** `docs/superpowers/specs/2026-05-04-miniscram-gui-unpack-design.md`

---

## File Structure

- **Create:** `tools/miniscram-gui/runner.go` — `actionRunner`, `runningState`, `actionResult`, `runningStrip` widget, `toast` widget. ~280 lines.
- **Create:** `tools/miniscram-gui/runner_test.go` — unit tests for `actionRunner` using `os.Args[0]` re-exec as a fake `miniscram` binary. ~140 lines.
- **Modify:** `tools/miniscram-gui/main.go` — replace synchronous handler goroutines with `runner.Start`; add `pickSave` + `revealInFolder`; render `runningStrip` and `toast` in the window layout; add `cancelled` branch to the status chip palette in `eventRow`; add `app.DestroyEvent` cleanup.
- **Modify:** `.github/workflows/ci.yml` — add `go test ./...` to the existing `build miniscram-gui` job.

The runner's helpers (`runningStrip`, `toast`) live in the same file because they're the pieces of the running-state mini-system; co-locating them keeps the related state together. `pickSave` and `revealInFolder` live in `main.go` next to existing `pickFile` and `openURL` for symmetry with the pattern already established.

---

## Task 1: `actionRunner` core + unit tests

**Goal:** A self-contained, mutex-safe goroutine-orchestrator that runs `miniscram` as a subprocess, surfaces its stderr line-by-line, supports SIGTERM cancellation, and is single-flight. Has full unit-test coverage via a re-exec'd fake binary, so it lands without any UI dependency.

**Files:**
- Create: `tools/miniscram-gui/runner.go`
- Create: `tools/miniscram-gui/runner_test.go`

**Acceptance Criteria:**
- [ ] `go test ./...` in `tools/miniscram-gui/` passes with the four scenarios (happy, fail, cancel, single-flight).
- [ ] No goroutine leak: `runtime.NumGoroutine()` before/after each test differs by ≤1 (allowing scheduler slop).
- [ ] `go vet ./...` clean.
- [ ] No file references `runningStrip` or `toast` yet — those land in Task 2/3.

**Verify:** `cd tools/miniscram-gui && go test -race -count=1 ./...` → `PASS  ok  miniscram-gui-gio2`

**Steps:**

- [ ] **Step 1: Create `runner.go` with the types and a `Start` shell that doesn't run anything yet.**

```go
// tools/miniscram-gui/runner.go
package main

import (
	"bufio"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// runningState is the snapshot of an in-flight subprocess.
type runningState struct {
	Action     string    // "pack" | "unpack" | "verify"
	Input      string    // source file path
	Output     string    // optional; destination path for pack/unpack
	StartedAt  time.Time
	LastLine   string    // most recent non-empty stderr line
	Cancelling bool      // set between Cancel() and process exit
}

// actionResult is what onDone receives when a subprocess finishes.
type actionResult struct {
	Action     string
	Input      string
	Output     string
	DurationMs int64
	Status     string // "success" | "fail" | "cancelled"
	Error      string // tail of stderr on fail
	OutputSize int64  // os.Stat(output).Size() on success, 0 otherwise
}

// actionRunner orchestrates a single in-flight miniscram subprocess.
// Single-flight: Start refuses while one is running.
type actionRunner struct {
	mu         sync.Mutex
	binary     string // defaults to "miniscram"; tests override
	cmd        *exec.Cmd
	state      *runningState
	onDone     func(actionResult)
	invalidate func()
}

func newActionRunner(invalidate func(), onDone func(actionResult)) *actionRunner {
	return &actionRunner{
		binary:     "miniscram",
		invalidate: invalidate,
		onDone:     onDone,
	}
}

// Snapshot returns a copy of the current state, or nil when idle.
// Safe to call from any goroutine.
func (r *actionRunner) Snapshot() *runningState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		return nil
	}
	s := *r.state
	return &s
}

// Running reports whether a subprocess is in flight.
func (r *actionRunner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state != nil
}

var errAlreadyRunning = errors.New("an action is already running")

// Start spawns miniscram with the given args. action/input/output are
// recorded in runningState for UI display; they do NOT shape the
// command line — caller passes the literal argv via args.
//
// Returns errAlreadyRunning if single-flight is violated.
func (r *actionRunner) Start(action, input, output string, args ...string) error {
	r.mu.Lock()
	if r.state != nil {
		r.mu.Unlock()
		return errAlreadyRunning
	}
	cmd := exec.Command(r.binary, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		r.mu.Unlock()
		return err
	}
	if err := cmd.Start(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.cmd = cmd
	r.state = &runningState{
		Action:    action,
		Input:     input,
		Output:    output,
		StartedAt: time.Now(),
	}
	r.mu.Unlock()

	go r.readStderr(stderr)
	go r.wait()
	return nil
}

// Cancel sends SIGTERM to the in-flight subprocess. No-op if idle.
func (r *actionRunner) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil || r.cmd == nil || r.cmd.Process == nil {
		return
	}
	r.state.Cancelling = true
	_ = r.cmd.Process.Signal(syscall.SIGTERM)
	if r.invalidate != nil {
		r.invalidate()
	}
}

// readStderr line-tails the subprocess stderr into state.LastLine.
// Runs on its own goroutine; exits when the pipe closes.
func (r *actionRunner) readStderr(stderr io.ReadCloser) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		r.mu.Lock()
		if r.state != nil {
			r.state.LastLine = line
		}
		r.mu.Unlock()
		if r.invalidate != nil {
			r.invalidate()
		}
	}
}

// wait blocks until the subprocess exits, fires onDone with the
// classified result, and clears state for the next Start.
func (r *actionRunner) wait() {
	err := r.cmd.Wait()

	r.mu.Lock()
	state := r.state
	r.cmd = nil
	r.state = nil
	r.mu.Unlock()

	res := actionResult{
		Action:     state.Action,
		Input:      state.Input,
		Output:     state.Output,
		DurationMs: time.Since(state.StartedAt).Milliseconds(),
	}
	switch {
	case state.Cancelling:
		res.Status = "cancelled"
	case err != nil:
		res.Status = "fail"
		res.Error = state.LastLine
		if res.Error == "" {
			res.Error = err.Error()
		}
	default:
		res.Status = "success"
	}

	if r.onDone != nil {
		r.onDone(res)
	}
	if r.invalidate != nil {
		r.invalidate()
	}
}
```

- [ ] **Step 2: Run `go vet` to catch compilation issues before tests.**

Run: `cd tools/miniscram-gui && go vet ./...`
Expected: clean (no output)

- [ ] **Step 3: Create `runner_test.go` with the four scenarios.**

The tests re-exec the test binary itself as a fake `miniscram`. `TestMain` checks an env var and behaves like a fake when set; otherwise runs the real test suite. This is portable across Linux/macOS/Windows without writing a separate fake program to disk.

```go
// tools/miniscram-gui/runner_test.go
package main

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestMain re-exec mode: when FAKE_MODE is set, the test binary
// pretends to be `miniscram` and behaves per the env var.
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_MODE") {
	case "":
		os.Exit(m.Run())
	case "happy":
		// emit 3 stderr lines on a 50ms cadence, exit 0
		for i, line := range []string{
			"hashing scram ... OK",
			"building scram prediction ... OK 355586 sector(s)",
			"verifying scram hashes ... OK all three match",
		} {
			fmt.Fprintln(os.Stderr, line)
			if i < 2 {
				time.Sleep(50 * time.Millisecond)
			}
		}
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "hashing scram ... started")
		fmt.Fprintln(os.Stderr, "scram not found: /no/such/file.scram")
		os.Exit(1)
	case "long":
		// run long enough to be cancelled; handle SIGTERM and exit 130
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		fmt.Fprintln(os.Stderr, "applying delta ...")
		select {
		case <-sig:
			os.Exit(130) // standard SIGTERM exit code
		case <-time.After(5 * time.Second):
			os.Exit(0)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown FAKE_MODE")
		os.Exit(2)
	}
}

func newTestRunner(t *testing.T, fakeMode string) (*actionRunner, chan actionResult) {
	t.Helper()
	done := make(chan actionResult, 1)
	r := &actionRunner{
		binary: os.Args[0],
		onDone: func(res actionResult) { done <- res },
	}
	// Pass FAKE_MODE through env. exec.Command inherits the parent
	// environment, so we set it here on the parent (tests run sequentially
	// per t.Setenv semantics).
	t.Setenv("FAKE_MODE", fakeMode)
	return r, done
}

func TestActionRunner_Happy(t *testing.T) {
	gBefore := runtime.NumGoroutine()
	r, done := newTestRunner(t, "happy")

	if err := r.Start("verify", "/in/path.miniscram", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}

	res := waitFor(t, done, 3*time.Second)
	if res.Status != "success" {
		t.Errorf("status = %q, want success (err=%q)", res.Status, res.Error)
	}
	if res.DurationMs <= 0 {
		t.Errorf("duration = %dms, want > 0", res.DurationMs)
	}

	// allow scheduler to clean up
	time.Sleep(100 * time.Millisecond)
	if leaked := runtime.NumGoroutine() - gBefore; leaked > 1 {
		t.Errorf("goroutine leak: %d new goroutines after run", leaked)
	}
}

func TestActionRunner_Fail(t *testing.T) {
	r, done := newTestRunner(t, "fail")

	if err := r.Start("pack", "/in/disc.cue", "/out/disc.miniscram"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	res := waitFor(t, done, 3*time.Second)
	if res.Status != "fail" {
		t.Errorf("status = %q, want fail", res.Status)
	}
	if !strings.Contains(res.Error, "scram not found") {
		t.Errorf("Error = %q, want it to contain 'scram not found'", res.Error)
	}
}

func TestActionRunner_Cancel(t *testing.T) {
	r, done := newTestRunner(t, "long")

	if err := r.Start("pack", "/in/disc.cue", "/out/disc.miniscram"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the subprocess a moment to actually start.
	time.Sleep(100 * time.Millisecond)
	r.Cancel()

	res := waitFor(t, done, 2*time.Second)
	if res.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", res.Status)
	}
}

func TestActionRunner_SingleFlight(t *testing.T) {
	r, done := newTestRunner(t, "long")

	if err := r.Start("pack", "a", "b"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer func() {
		r.Cancel()
		<-done
	}()

	if err := r.Start("verify", "c", ""); err != errAlreadyRunning {
		t.Errorf("second Start err = %v, want errAlreadyRunning", err)
	}
}

// invalidate counter test: stderr line should bump invalidate calls.
func TestActionRunner_InvalidateOnLine(t *testing.T) {
	r, done := newTestRunner(t, "happy")
	var ticks atomic.Int64
	r.invalidate = func() { ticks.Add(1) }

	if err := r.Start("verify", "/in/path.miniscram", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-done

	// 3 lines + 1 final invalidate from wait() = at least 4
	if got := ticks.Load(); got < 4 {
		t.Errorf("invalidate calls = %d, want ≥ 4", got)
	}
}

func waitFor(t *testing.T, done <-chan actionResult, timeout time.Duration) actionResult {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(timeout):
		t.Fatalf("onDone did not fire within %v", timeout)
		return actionResult{}
	}
}
```

- [ ] **Step 4: Run the test suite.**

Run: `cd tools/miniscram-gui && go test -race -count=1 ./...`
Expected: `ok  ...` for all five tests (Happy, Fail, Cancel, SingleFlight, InvalidateOnLine).

If `runtime.NumGoroutine` flakes by 1–2 in CI, that's allowed by the assertion (`> 1`). If it's consistently larger, there's a leak — investigate before commit.

- [ ] **Step 5: Commit.**

```bash
git add tools/miniscram-gui/runner.go tools/miniscram-gui/runner_test.go
git commit -m "miniscram-gui: actionRunner with line-tailed stderr + tests

A goroutine-safe orchestrator for miniscram pack/unpack/verify
subprocesses. Stderr is bufio-scanned line-by-line into a
mutex-protected runningState; SIGTERM cancellation is handled by
the CLI itself (the existing tempfile cleanup paths cover us).
Single-flight: Start refuses while one is in flight.

Tested via TestMain re-exec as a fake miniscram, covering happy,
fail, cancel, single-flight, and invalidate-tick scenarios."
```

---

## Task 2: `runningStrip` widget + Pack/Verify migration + window-close cleanup

**Goal:** Render the running-state visually and migrate the existing synchronous Pack/Verify handlers to use `actionRunner`, so the UI no longer freezes during those actions. Window close cleans up any in-flight subprocess.

**Files:**
- Modify: `tools/miniscram-gui/runner.go` — add `runningStrip` widget.
- Modify: `tools/miniscram-gui/main.go` — wire `runner` into the model, replace handlers, render strip in layout, add `cancelled` branch to status chip in `eventRow`, handle `app.DestroyEvent`.

**Acceptance Criteria:**
- [ ] Clicking Verify on a loaded `.miniscram` shows the strip ("Verifying", current step text, elapsed time, Cancel) instead of freezing the window. The strip disappears on completion; an event row appears in Stats.
- [ ] Clicking Pack on a loaded `.cue` does the same, respecting the existing `deleteScramCB` checkbox by passing `--keep-source` when unchecked.
- [ ] Clicking Cancel on a long-running pack/verify SIGTERMs the subprocess; the event records as `cancelled`; the Stats status chip shows a grey "CANCELLED".
- [ ] Closing the window during a running action cleans up the subprocess (no orphan in `ps`).
- [ ] `go vet ./...` clean. `go test ./...` still passes.

**Verify:**
- Manual: `cd tools/miniscram-gui && nix-shell ../../shell.nix --run 'go build -o /tmp/miniscram-gui .' && /tmp/miniscram-gui -load /home/hugh/miniscram/test-discs/deus-ex/DeusEx_v1002f.miniscram`. Click Verify; observe strip; await event row in Stats.
- Automated: `go test -race -count=1 ./...` (Task 1's tests still green; no new tests in this task).

**Steps:**

- [ ] **Step 1: Append `runningStrip` to `runner.go`.**

The widget is a horizontal strip rendered when `Snapshot()` returns non-nil. Spinner, action verb + input basename, current step in muted mono, elapsed seconds, Cancel button.

```go
// tools/miniscram-gui/runner.go (append)

import (
	// ...existing imports...
	"fmt"
	"path/filepath"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// stripState is the data the runningStrip widget consumes.
// runner.Snapshot() produces one; we render text and elapsed.
type stripStyle struct {
	th       *material.Theme
	state    *runningState
	cancelBtn *widget.Clickable
}

func runningStripWidget(th *material.Theme, state *runningState, cancelBtn *widget.Clickable) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if state == nil {
			return layout.Dimensions{}
		}
		actionVerb := map[string]string{
			"pack":   "Packing",
			"unpack": "Unpacking",
			"verify": "Verifying",
		}[state.Action]
		if actionVerb == "" {
			actionVerb = "Running"
		}
		basename := filepath.Base(state.Input)
		elapsed := time.Since(state.StartedAt).Truncate(time.Second)
		stepText := state.LastLine
		if stepText == "" {
			stepText = "Starting…"
		}
		cancelLabel := "Cancel"
		if state.Cancelling {
			cancelLabel = "Cancelling…"
		}

		// Background fill across the whole strip.
		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(24), Right: unit.Dp(24)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lb := material.Label(th, unit.Sp(13), actionVerb+" "+basename)
						lb.Color = text1
						lb.Font.Weight = font.SemiBold
						return lb.Layout(gtx)
					}),
					layout.Rigid(spacer(12, 0)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						lb := material.Label(th, unit.Sp(12), stepText)
						lb.Color = text2
						lb.Font.Typeface = "Go Mono"
						return lb.Layout(gtx)
					}),
					layout.Rigid(spacer(12, 0)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lb := material.Label(th, unit.Sp(12), fmt.Sprintf("%ds", int(elapsed.Seconds())))
						lb.Color = text3
						lb.Font.Typeface = "Go Mono"
						return lb.Layout(gtx)
					}),
					layout.Rigid(spacer(12, 0)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if state.Cancelling {
							gtx = gtx.Disabled()
						}
						btn := material.Button(th, cancelBtn, cancelLabel)
						btn.Background = surface2
						btn.Color = text1
						btn.CornerRadius = unit.Dp(4)
						btn.TextSize = unit.Sp(12)
						btn.Inset = layout.Inset{Top: 5, Bottom: 5, Left: 12, Right: 12}
						return btn.Layout(gtx)
					}),
				)
			})
		call := macro.Stop()
		bg := mustRGB("13262d") // muted teal-tinted dark
		paint.FillShape(gtx.Ops, bg, clip.Rect{Max: dims.Size}.Op())
		call.Add(gtx.Ops)
		// Re-draw on every animation frame so the elapsed counter ticks.
		// 200ms is plenty for a one-second readout and won't burn battery.
		op.InvalidateOp{At: gtx.Now.Add(200 * time.Millisecond)}.Add(gtx.Ops)
		_ = event.Op // keep the import live for Task 3 toast wiring
		return dims
	}
}
```

Note: `mustRGB`, `text1`/`text2`/`text3`, `surface2`, and `spacer` are defined in `main.go` and accessible because we're in the same package.

- [ ] **Step 2: Edit `main.go` — add `runner` to the model, the cancel button widget, and the `cancelled` branch to the status chip.**

In the model struct:

```go
// tools/miniscram-gui/main.go (in `type model struct`)
type model struct {
	// ...existing fields...
	runner    *actionRunner
	lastEvent eventRec // populated in onDone; used by Task 3's toast
}
```

In `main()` after `mdl := &model{...}`:

```go
mdl.runner = newActionRunner(
	func() {
		// invalidate is set in the Window goroutine; capture via mdl.invalidate
		if mdl.invalidate != nil {
			mdl.invalidate()
		}
	},
	func(res actionResult) {
		// Translate actionResult into an event row. Per-action: pack
		// writes a .miniscram (size on disk, manifest-derived metrics),
		// unpack writes a .scram (size on disk, manifest-derived metrics
		// from the source mdl.meta), verify produces no output (metrics
		// from mdl.meta only). Title comes from the redump cache when
		// the disc was identified.
		ev := eventRec{
			TS:         time.Now(),
			Action:     res.Action,
			InputPath:  res.Input,
			OutputPath: res.Output,
			DurationMs: res.DurationMs,
			Status:     res.Status,
			Error:      res.Error,
		}
		fillTitle := func(meta *inspectJSON) {
			if meta == nil || len(meta.Tracks) == 0 {
				return
			}
			if e, ok := redumpGet(mdl.db, meta.Tracks[0].Hashes["sha1"]); ok && e.State == "found" {
				ev.Title = e.Title
			}
		}
		if res.Status == "success" {
			switch res.Action {
			case "pack":
				// Output is the .miniscram. Size on disk + inspect to get manifest.
				if res.Output != "" {
					if st, err := os.Stat(res.Output); err == nil {
						ev.MiniscramSize = st.Size()
					}
					if raw, err := exec.Command("miniscram", "inspect", res.Output, "--json").Output(); err == nil {
						var meta inspectJSON
						if json.Unmarshal(raw, &meta) == nil {
							ev.ScramSize = meta.Scram.Size
							ev.OverrideRecords = len(meta.DeltaRecords)
							ev.WriteOffset = meta.WriteOffsetBytes
							fillTitle(&meta)
						}
					}
				}
			case "unpack":
				// Output is the .scram. Size on disk + manifest metrics from mdl.meta.
				if res.Output != "" {
					if st, err := os.Stat(res.Output); err == nil {
						ev.ScramSize = st.Size()
					}
				}
				if mdl.meta != nil {
					ev.MiniscramSize = mdl.miniscramOnDisk
					ev.OverrideRecords = len(mdl.meta.DeltaRecords)
					ev.WriteOffset = mdl.meta.WriteOffsetBytes
					fillTitle(mdl.meta)
				}
			case "verify":
				// No output file. Manifest metrics from mdl.meta.
				if mdl.meta != nil {
					ev.ScramSize = mdl.meta.Scram.Size
					ev.MiniscramSize = mdl.miniscramOnDisk
					ev.OverrideRecords = len(mdl.meta.DeltaRecords)
					ev.WriteOffset = mdl.meta.WriteOffsetBytes
					fillTitle(mdl.meta)
				}
			}
		}
		eventInsert(mdl.db, ev)
		mdl.refreshStats()
		mdl.lastEvent = ev // for Task 3's toast — see Task 3 step 4
	},
)
```

In the loop, declare a `cancelBtn`:

```go
// tools/miniscram-gui/main.go (in `loop`, with the other widget declarations)
var cancelBtn widget.Clickable
```

Wire its click below the existing button handlers:

```go
// tools/miniscram-gui/main.go (in `loop`'s event-frame body)
if cancelBtn.Clicked(gtx) {
	mdl.runner.Cancel()
}
```

Replace the synchronous Pack/Verify handlers (currently at main.go:601-609):

```go
// Replace the existing block:
//     if verifyBtn.Clicked(gtx) && mdl.kind == "miniscram" {
//         go func() { mdl.recordVerifyEvent(mdl.path); mdl.invalidate() }()
//     }
//     _ = unpackBtn.Clicked(gtx)
//     if packBtn.Clicked(gtx) && mdl.kind == "cue" {
//         out := strings.TrimSuffix(mdl.path, filepath.Ext(mdl.path)) + ".miniscram"
//         keepSource := !deleteScramCB.Value
//         go func() { mdl.recordPackEvent(mdl.path, out, keepSource); mdl.invalidate() }()
//     }

if verifyBtn.Clicked(gtx) && mdl.kind == "miniscram" && !mdl.runner.Running() {
	_ = mdl.runner.Start("verify", mdl.path, "", "verify", mdl.path)
}
_ = unpackBtn.Clicked(gtx) // wired in Task 3
if packBtn.Clicked(gtx) && mdl.kind == "cue" && !mdl.runner.Running() {
	out := strings.TrimSuffix(mdl.path, filepath.Ext(mdl.path)) + ".miniscram"
	args := []string{"pack", mdl.path}
	if !deleteScramCB.Value {
		args = append(args, "--keep-source")
	}
	_ = mdl.runner.Start("pack", mdl.path, out, args...)
}
```

Disable the action buttons when running. Find the cue view's Pack button render and wrap with the running check:

```go
// In cueView (look for `btn := material.Button(th, packBtn, "Pack")`):
//   change the existing `if !hasScram || !allBinsExist` test to also include `mdl.runner.Running()`.
if !hasScram || !allBinsExist || mdl.runner.Running() {
	gtx = gtx.Disabled()
}
```

Same in `heroRow` for Verify and Unpack — wrap with `if mdl.runner.Running() { gtx = gtx.Disabled() }` before each button's render.

The two now-unused methods `recordPackEvent` and `recordVerifyEvent` should be removed from `main.go` since the runner's `onDone` callback handles their job. Search for them and delete; the test suite has nothing pinned to those names.

Add the `cancelled` branch to the status chip palette in `eventRow` (search for `statusLabel := "PASS"`):

```go
// tools/miniscram-gui/main.go (in eventRow)
statusCol := good
statusLabel := "PASS"
switch ev.Status {
case "fail":
	statusCol = bad
	statusLabel = "FAIL"
case "cancelled":
	statusCol = text3
	statusLabel = "CANCELLED"
}
```

- [ ] **Step 3: Render the strip in the window layout.**

Insert it as a Rigid child between the topBar's divider and the body's Flexed scroller:

```go
// tools/miniscram-gui/main.go (in `loop`'s Frame body, replace the existing layout.Flex children)
layout.Flex{Axis: layout.Vertical}.Layout(gtx,
	layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return topBar(th, mdl, &openBtn, &statsBtn, &fileBtn).Layout(gtx)
	}),
	layout.Rigid(divider),
	layout.Rigid(runningStripWidget(th, mdl.runner.Snapshot(), &cancelBtn)),
	layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		return material.List(th, &listScroll).Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
			return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				switch mdl.view {
				case "stats":
					return statsView(gtx, th, mdl)
				default:
					return body(gtx, th, mdl, &verifyBtn, &unpackBtn, &packBtn, &deleteScramCB, getCopy, getLink)
				}
			})
		})
	}),
	layout.Rigid(divider),
	layout.Rigid(footer(th, mdl)),
)
```

When `Snapshot()` returns nil, the widget's `if state == nil { return layout.Dimensions{} }` collapses it — no divider, no inset, zero height.

- [ ] **Step 4: Handle window close — cancel any in-flight action so the subprocess doesn't outlive the GUI.**

In the `loop` function's event switch, modify the `app.DestroyEvent` case:

```go
// tools/miniscram-gui/main.go (in `loop`)
case app.DestroyEvent:
	if mdl.runner != nil && mdl.runner.Running() {
		mdl.runner.Cancel()
		// Wait a moment for the process to exit. The wait goroutine will
		// drop state to nil; we're racing best-effort cleanup.
		deadline := time.Now().Add(5 * time.Second)
		for mdl.runner.Running() && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return e.Err
```

- [ ] **Step 5: Sanity build + verify the existing test suite still passes.**

Run: `cd tools/miniscram-gui && nix-shell /home/hugh/miniscram/gui-prototypes/shell.nix --run 'go vet ./... && go test -race -count=1 ./... && go build -o /tmp/miniscram-gui .'`
Expected: vet clean, tests `PASS`, binary built.

- [ ] **Step 6: Manual smoke — pack a cleaned half-life, mid-cancel.**

```bash
# delete the existing miniscram from a prior run so pack has work to do
rm -f /home/hugh/miniscram/test-discs/half-life/HALFLIFE.miniscram
PATH=$PATH:$(dirname $(which miniscram)) /tmp/miniscram-gui -load /home/hugh/miniscram/test-discs/half-life/HALFLIFE.cue
```

In the GUI: click **Pack**. Watch the strip appear with stepping stderr lines and an elapsed counter. Wait ~2 s, click **Cancel**. Confirm:
- Strip shows "Cancelling…" briefly then disappears.
- No `HALFLIFE.miniscram` left next to the cue (CLI cleans up its own tempfiles on SIGTERM).
- Switch to Stats; the most recent row is `pack` `CANCELLED` for HALFLIFE.cue.

Then click **Pack** again, let it complete (~5 s). Verify:
- Strip steps through "hashing scram…", "building scram prediction…", "verifying scram hashes…".
- Strip disappears on completion.
- Stats has a `pack` `PASS` row.

- [ ] **Step 7: Manual smoke — window close while running.**

Restart the GUI on a fresh half-life cue. Click Pack. While the strip is showing, close the window. Confirm with `pgrep -af miniscram` that no `miniscram pack` subprocess remains within 5 s.

- [ ] **Step 8: Commit.**

```bash
git add tools/miniscram-gui/runner.go tools/miniscram-gui/main.go
git commit -m "miniscram-gui: migrate Pack/Verify to actionRunner + render strip

Drops the synchronous goroutines that froze the UI during pack and
verify, replacing them with actionRunner.Start. A new runningStrip
widget renders at the window-frame level (between top bar and body)
showing action verb, current stderr step, elapsed, and a Cancel
button. SIGTERM-via-Cancel records as a new 'cancelled' status with
a grey 'CANCELLED' chip in Stats. Window close cleans up any
in-flight subprocess with a 5s grace.

The unpack handler stays a no-op for now — wired in the next task."
```

---

## Task 3: `toast` widget + Unpack handler with `pickSave` + `revealInFolder`

**Goal:** Wire the Unpack… button to actually run `miniscram unpack` after a native save dialog, and surface successful Pack/Verify/Unpack with a 6-second toast that includes a "Reveal in folder" affordance for actions that produced an output file.

**Files:**
- Modify: `tools/miniscram-gui/runner.go` — append `toast` widget + `toastState`.
- Modify: `tools/miniscram-gui/main.go` — add `pickSave` + `revealInFolder` (next to `pickFile` + `openURL`); wire Unpack click handler; render toast in layout; populate `mdl.toast` in the runner's `onDone`.

**Acceptance Criteria:**
- [ ] Clicking Unpack… on a loaded `.miniscram` opens the native save dialog with the default filename `<basename>.scram` in the source's directory.
- [ ] If the user cancels the save dialog, nothing happens.
- [ ] If the user picks the source `.miniscram` path itself, the action is refused (transient error in the strip).
- [ ] On a successful unpack/pack, a toast appears at the bottom of the window with action verb, output basename, size, duration, and a "Reveal in folder" button. It auto-dismisses after 6 s, or earlier on click of ✕.
- [ ] "Reveal in folder" opens the OS file manager pointed at the output's directory.
- [ ] Toast is hidden during a verify (no output file).
- [ ] Toast clears immediately when a new action starts.
- [ ] `go vet ./...` clean. `go test ./...` still passes.

**Verify:**
- Manual: launch the GUI on a packed `.miniscram` (DeusEx), click Unpack…, accept default. Toast appears with size ≈ 856 MiB. Click Reveal in folder → file manager opens the deus-ex directory. Stats has an `unpack` `PASS` row.
- Automated: `go test -race -count=1 ./...` (no new tests in this task).

**Steps:**

- [ ] **Step 1: Append `toastState` and the `toast` widget to `runner.go`.**

```go
// tools/miniscram-gui/runner.go (append)

// toastState is set by the runner's onDone on a successful action.
// The widget hides itself when ExpiresAt < now or Hide is true.
type toastState struct {
	Action     string // "pack" | "unpack" | "verify"
	Output     string // path to the output file; "" for verify
	OutputSize int64
	DurationMs int64
	ExpiresAt  time.Time
	Hide       bool // set when user clicks the ✕
}

func toastWidget(th *material.Theme, ts *toastState, dismissBtn, revealBtn *widget.Clickable) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if ts == nil || ts.Hide || time.Now().After(ts.ExpiresAt) {
			return layout.Dimensions{}
		}
		verb := map[string]string{
			"pack":   "Packed",
			"unpack": "Unpacked",
			"verify": "Verified",
		}[ts.Action]
		if verb == "" {
			verb = "Done"
		}
		basename := filepath.Base(ts.Output)
		if basename == "." || basename == "" {
			basename = ts.Action + " complete"
		}
		summary := verb + "  " + basename
		if ts.OutputSize > 0 {
			summary += "  ·  " + humanBytes(ts.OutputSize)
		}
		summary += "  ·  " + fmt.Sprintf("%.1fs", float64(ts.DurationMs)/1000)

		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(24), Right: unit.Dp(24)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return statusDot(gtx, good)
					}),
					layout.Rigid(spacer(10, 0)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						lb := material.Label(th, unit.Sp(13), summary)
						lb.Color = text1
						return lb.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if ts.Output == "" {
							return layout.Dimensions{}
						}
						btn := material.Button(th, revealBtn, "Reveal in folder")
						btn.Background = surface2
						btn.Color = text2
						btn.CornerRadius = unit.Dp(4)
						btn.TextSize = unit.Sp(11)
						btn.Inset = layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 10}
						return btn.Layout(gtx)
					}),
					layout.Rigid(spacer(8, 0)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						btn := material.Button(th, dismissBtn, "✕")
						btn.Background = bg
						btn.Color = text3
						btn.CornerRadius = unit.Dp(4)
						btn.TextSize = unit.Sp(13)
						btn.Inset = layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}
						return btn.Layout(gtx)
					}),
				)
			})
		call := macro.Stop()
		paint.FillShape(gtx.Ops, mustRGB("17392d"), clip.Rect{Max: dims.Size}.Op())
		call.Add(gtx.Ops)
		// Tick at 250ms so the toast self-expires within ~the second it should.
		op.InvalidateOp{At: gtx.Now.Add(250 * time.Millisecond)}.Add(gtx.Ops)
		return dims
	}
}
```

- [ ] **Step 2: Add `pickSave` to `main.go` (next to existing `pickFile`).**

```go
// tools/miniscram-gui/main.go (next to pickFile)

// pickSave shells out to the platform's native save dialog with overwrite-confirm.
// defaultName is the suggested filename (e.g., "DeusEx_v1002f.scram").
// defaultDir is the suggested starting directory.
func pickSave(defaultName, defaultDir string) (string, error) {
	switch runtime.GOOS {
	case "linux":
		if p, err := exec.LookPath("zenity"); err == nil {
			out, err := exec.Command(p, "--file-selection",
				"--save", "--confirm-overwrite",
				"--title=Save .scram as…",
				"--filename="+filepath.Join(defaultDir, defaultName)).Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		}
		if p, err := exec.LookPath("kdialog"); err == nil {
			out, err := exec.Command(p, "--getsavefilename",
				filepath.Join(defaultDir, defaultName), "*.scram|scram\n*|all files").Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		}
		return "", errors.New("install zenity or kdialog for the native save picker")
	case "darwin":
		script := fmt.Sprintf(
			`POSIX path of (choose file name with prompt "Save .scram as…" default name "%s" default location POSIX file "%s")`,
			defaultName, defaultDir)
		out, err := exec.Command("osascript", "-e", script).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	case "windows":
		ps := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms;`+
			`$f = New-Object System.Windows.Forms.SaveFileDialog;`+
			`$f.FileName = "%s";`+
			`$f.InitialDirectory = "%s";`+
			`$f.Filter = "scram|*.scram|All|*";`+
			`$f.OverwritePrompt = $true;`+
			`if ($f.ShowDialog() -eq 'OK') { $f.FileName }`,
			defaultName, defaultDir)
		out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("no save dialog for %s", runtime.GOOS)
}
```

- [ ] **Step 3: Add `revealInFolder` to `main.go` (next to `openURL`).**

```go
// tools/miniscram-gui/main.go (next to openURL)
func revealInFolder(path string) {
	dir := filepath.Dir(path)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	_ = cmd.Start()
}
```

- [ ] **Step 4: Wire Unpack handler + render toast + handle dismiss/reveal clicks.**

Add to the model:

```go
// tools/miniscram-gui/main.go (in `type model struct`)
type model struct {
	// ...existing fields...
	toast *toastState
}
```

Declare the toast buttons in `loop`:

```go
// tools/miniscram-gui/main.go (in `loop`, with the other widget declarations)
var toastDismissBtn widget.Clickable
var toastRevealBtn widget.Clickable
```

In the runner's `onDone` callback (in `main()`), after `eventInsert` + `refreshStats`, populate the toast on success. The output size for the toast comes from whichever column the action just populated — `MiniscramSize` for pack, `ScramSize` for unpack, neither for verify.

```go
// inside the onDone callback in main(), after eventInsert + refreshStats:
if res.Status == "success" {
	var outputSize int64
	switch res.Action {
	case "pack":
		outputSize = mdl.lastEvent.MiniscramSize
	case "unpack":
		outputSize = mdl.lastEvent.ScramSize
	// verify: outputSize stays 0; toast omits the size segment
	}
	mdl.toast = &toastState{
		Action:     res.Action,
		Output:     res.Output,
		OutputSize: outputSize,
		DurationMs: res.DurationMs,
		ExpiresAt:  time.Now().Add(6 * time.Second),
	}
} else {
	mdl.toast = nil // clear stale toast on fail/cancel
}
```

Wire the Unpack click handler (replacing the `_ = unpackBtn.Clicked(gtx)` placeholder from Task 2):

```go
// tools/miniscram-gui/main.go (replace the placeholder)
if unpackBtn.Clicked(gtx) && mdl.kind == "miniscram" && !mdl.runner.Running() {
	mdl.toast = nil
	srcPath := mdl.path
	defaultName := strings.TrimSuffix(mdl.basename, filepath.Ext(mdl.basename)) + ".scram"
	defaultDir := mdl.dir
	go func() {
		out, err := pickSave(defaultName, defaultDir)
		if err != nil || out == "" {
			return
		}
		if out == srcPath {
			// Surface as a transient strip-style error: spawn a fake actionRunner
			// state? No — simpler: write a fail event row directly so the user
			// sees what happened in Stats. Avoids inventing a new error path.
			eventInsert(mdl.db, eventRec{
				TS:        time.Now(),
				Action:    "unpack",
				InputPath: srcPath,
				Status:    "fail",
				Error:     "refused: output path equals source .miniscram",
			})
			mdl.refreshStats()
			if mdl.invalidate != nil {
				mdl.invalidate()
			}
			return
		}
		_ = mdl.runner.Start("unpack", srcPath, out, "unpack", srcPath, "-o", out)
	}()
}
```

Handle toast button clicks (next to where Pack/Verify clicks are handled):

```go
// tools/miniscram-gui/main.go (in `loop`'s event-frame body)
if toastDismissBtn.Clicked(gtx) && mdl.toast != nil {
	mdl.toast.Hide = true
}
if toastRevealBtn.Clicked(gtx) && mdl.toast != nil && mdl.toast.Output != "" {
	revealInFolder(mdl.toast.Output)
}
```

Render the toast just above the footer divider:

```go
// tools/miniscram-gui/main.go (in `loop`'s Frame body — modify the layout.Flex)
layout.Flex{Axis: layout.Vertical}.Layout(gtx,
	layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return topBar(th, mdl, &openBtn, &statsBtn, &fileBtn).Layout(gtx)
	}),
	layout.Rigid(divider),
	layout.Rigid(runningStripWidget(th, mdl.runner.Snapshot(), &cancelBtn)),
	layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		return material.List(th, &listScroll).Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
			return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				switch mdl.view {
				case "stats":
					return statsView(gtx, th, mdl)
				default:
					return body(gtx, th, mdl, &verifyBtn, &unpackBtn, &packBtn, &deleteScramCB, getCopy, getLink)
				}
			})
		})
	}),
	layout.Rigid(toastWidget(th, mdl.toast, &toastDismissBtn, &toastRevealBtn)),
	layout.Rigid(divider),
	layout.Rigid(footer(th, mdl)),
)
```

- [ ] **Step 5: Sanity build.**

Run: `cd tools/miniscram-gui && nix-shell /home/hugh/miniscram/gui-prototypes/shell.nix --run 'go vet ./... && go test -race -count=1 ./... && go build -o /tmp/miniscram-gui .'`
Expected: clean.

- [ ] **Step 6: Manual smoke — unpack DeusEx end-to-end.**

```bash
# the DeusEx miniscram already exists from earlier rounds
PATH=$PATH:$(dirname $(which miniscram)) /tmp/miniscram-gui -load /home/hugh/miniscram/test-discs/deus-ex/DeusEx_v1002f.miniscram
```

In the GUI:
1. Click **Unpack…**. A native save dialog appears. Default filename: `DeusEx_v1002f.scram`. Default location: `test-discs/deus-ex/`.
2. Click Save. Confirm overwrite if it prompts.
3. Watch the strip step through "verifying bin hashes …", "building scram prediction …", "applying delta …", "verifying scram hashes …".
4. On completion, the toast slides in: "Unpacked DeusEx_v1002f.scram · 856 MiB · 5.2s · [Reveal in folder] [✕]".
5. Click **Reveal in folder** → file manager opens the deus-ex directory.
6. Wait 6 s without clicking; toast disappears on its own.
7. Switch to **Stats**; the most recent row is `unpack PASS` for DeusEx_v1002f.miniscram.

- [ ] **Step 7: Manual smoke — refusal of self-overwrite.**

In the same GUI session, click **Unpack…** again. In the dialog, navigate to and pick the source `.miniscram` itself (rename "DeusEx_v1002f.scram" suggestion to "DeusEx_v1002f.miniscram" in the filename field). Confirm:
- No subprocess runs.
- Stats gains an `unpack FAIL` row with error "refused: output path equals source .miniscram".

- [ ] **Step 8: Commit.**

```bash
git add tools/miniscram-gui/runner.go tools/miniscram-gui/main.go
git commit -m "miniscram-gui: wire Unpack with pickSave + toast + revealInFolder

Unpack… now opens a native save dialog (zenity / osascript /
PowerShell) defaulted to <basename>.scram next to the source. On a
successful pack/unpack, a 6s toast slides in with size, duration,
and a Reveal in folder button that opens the OS file manager at the
output directory. Toast self-dismisses on timer or ✕.

Refuses the case where the user picks the source .miniscram itself
as the output path (records as a fail event with a clear error)."
```

---

## Task 4: CI — run `go test` for the GUI module

**Goal:** The existing CI's `build miniscram-gui` job builds and vets but doesn't run tests. With Task 1 introducing real tests, extend the job to run them.

**Files:**
- Modify: `.github/workflows/ci.yml`

**Acceptance Criteria:**
- [ ] On the next push to the PR, the `build miniscram-gui` job runs `go test -race -count=1 ./...` after the build step and the run is green.
- [ ] No regression in the existing `build + test` (root) job.

**Verify:** Push to the PR branch; `gh pr checks 21` shows both jobs `SUCCESS`.

**Steps:**

- [ ] **Step 1: Edit the workflow.**

```yaml
# .github/workflows/ci.yml — under the `gui:` job, after the `Vet` step
      - name: Test
        working-directory: tools/miniscram-gui
        run: go test -race -count=1 ./...
```

- [ ] **Step 2: Push and watch.**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: run go test for tools/miniscram-gui

Task 1 introduces actionRunner unit tests; the gui CI job now runs
them in addition to build + vet."
git push
gh run watch $(gh run list --branch feature/miniscram-gui --json databaseId --limit 1 | python3 -c "import json,sys; print(json.load(sys.stdin)[0]['databaseId'])") --exit-status
```

Expected: both jobs green within ~2 minutes.

---

## Out-of-scope (per the spec)

- `miniscram --progress=json` flag for richer step reporting.
- Click-to-expand error rows in the Stats events table.
- "Recent files" list in the empty state.
- Drag-and-drop input.
- Cross-platform release wiring.
