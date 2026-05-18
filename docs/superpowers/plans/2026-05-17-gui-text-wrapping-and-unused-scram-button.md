# GUI text wrapping + unused-scram button fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix three GUI bugs from issue #54: (1) text truncation instead of wrapping in qFailed reason labels, (2) "Delete N unused .scrams" button never appears after audio-only pack failure, (3) delete button label should mention audio tracks.

**Architecture:** (1) Accumulate `unused-scram` NDJSON events in `runner.go`'s `readStderr` so they survive the `fail` event overwriting `state.LastLine`. Provide live drain via `DrainUnusedScrams()` + safety-net through `actionResult`. (2) Replace `MaxLines=1` with `WrapPolicy=WrapGraphemes` on `reasonLabel` to wrap instead of truncate. (3) Update the button label string.

**Tech Stack:** Go, Gio v0.9.0

---

## File map

| File | Change |
|------|--------|
| `tools/miniscram-gui/runner.go` | Add `pendingScrams`, parse NDJSON in `readStderr`, `DrainUnusedScrams()`, carry through `actionResult` |
| `tools/miniscram-gui/runner_test.go` | New `json_scram` fake mode + test for accumulation |
| `tools/miniscram-gui/queue_widget.go` | `reasonLabel`: `MaxLines=1` → `WrapPolicy=WrapGraphemes`; `unusedScramBar`: label text |
| `tools/miniscram-gui/queue_widget_test.go` | Update reason-label test |
| `tools/miniscram-gui/main.go` | Drain scrams in frame handler + handleActionResult |

---

### Task 1: `runner.go` — accumulate `unused-scram` events in `readStderr`

**Files:** Modify `tools/miniscram-gui/runner.go`

- [ ] **Step 1: Add `encoding/json` to imports and add `pendingScrams` to `runningState`**

```go
import (
    "bufio"
    "encoding/json"  // ADD
    "errors"
    "io"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "strings"
    "sync"
    "syscall"
    "time"
)

type runningState struct {
    Action        string
    Input         string
    Output        string
    StartedAt     time.Time
    LastLine      string
    Cancelling    bool
    pendingScrams []unusedScram  // ADD
}
```

- [ ] **Step 2: Parse NDJSON in `readStderr` and accumulate `unused-scram` events**

Replace the body of `readStderr`:

```go
func (r *actionRunner) readStderr(stderr io.ReadCloser) {
    scanner := bufio.NewScanner(stderr)
    scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line == "" {
            continue
        }
        r.mu.Lock()
        if r.state != nil {
            r.state.LastLine = line
            // Accumulate unused-scram events separately so they survive
            // being overwritten by a subsequent fail event.
            var ev progressEvent
            if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == "unused-scram" && ev.Path != "" {
                r.state.pendingScrams = append(r.state.pendingScrams, unusedScram{Path: ev.Path, Size: ev.Size})
            }
        }
        r.mu.Unlock()
        if r.invalidate != nil {
            r.invalidate()
        }
    }
}
```

- [ ] **Step 3: Add `DrainUnusedScrams()` method on `actionRunner`**

Add after `Running()`:

```go
// DrainUnusedScrams returns and clears the pending unused-scram
// events accumulated by readStderr. Safe to call from any goroutine.
func (r *actionRunner) DrainUnusedScrams() []unusedScram {
    r.mu.Lock()
    defer r.mu.Unlock()
    if r.state == nil {
        return nil
    }
    result := r.state.pendingScrams
    r.state.pendingScrams = nil
    return result
}
```

- [ ] **Step 4: Add `UnusedScrams` to `actionResult`**

```go
type actionResult struct {
    Action       string
    Input        string
    Output       string
    DurationMs   int64
    Status       string
    Error        string
    OutputSize   int64
    UnusedScrams []unusedScram  // ADD
}
```

- [ ] **Step 5: Copy pending scrams into `actionResult` in `wait()`**

In `wait()`, after `r.mu.Lock()` that captures `state`, snapshot the pending scrams:

```go
r.mu.Lock()
state := r.state
pendingScrams := state.pendingScrams  // ADD
r.cmd = nil
r.state = nil
r.mu.Unlock()
```

Then include in the result:

```go
res.UnusedScrams = pendingScrams  // ADD, before the select on r.done
```

- [ ] **Step 6: Run existing tests to confirm no regression**

Run: `cd tools/miniscram-gui && go test -run TestActionRunner -v ./...`
Expected: all existing runner tests pass (Happy, Fail, FailNDJSONReason, JSONHappy, JSONCancel, Cancel, SingleFlight, InvalidateOnLine)

- [ ] **Step 7: Commit**

```bash
git add tools/miniscram-gui/runner.go
git commit -m "fix(gui): accumulate unused-scram events in readStderr (#54)"
```

---

### Task 2: `runner_test.go` — test unused-scram accumulation

**Files:** Modify `tools/miniscram-gui/runner_test.go`

- [ ] **Step 1: Add `json_scram` fake mode to `TestMain`**

Add a new case in the `switch os.Getenv("FAKE_MODE")` block:

```go
case "json_scram":
    // Emit an unused-scram event then a fail event, mirroring what
    // pack.go writes when it hits the audio-only short-circuit.
    fmt.Fprintln(os.Stderr, `{"type":"unused-scram","path":"/disc/x.scram","size":765077352}`)
    fmt.Fprintln(os.Stderr, `{"type":"fail","label":"checking disc type","error":"cue contains only AUDIO tracks; nothing for miniscram to scramble-pack"}`)
    os.Exit(1)
```

- [ ] **Step 2: Write `TestActionRunner_AccumulatesUnusedScram`**

Add after `TestActionRunner_FailNDJSONReason`:

```go
// TestActionRunner_AccumulatesUnusedScram confirms that unused-scram
// NDJSON events survive the subsequent fail event overwriting LastLine.
// The runner must accumulate them in pendingScrams and carry them
// through to actionResult.UnusedScrams.
func TestActionRunner_AccumulatesUnusedScram(t *testing.T) {
    r, done := newTestRunner(t, "json_scram")

    if err := r.Start("pack", "/in/disc.cue", "/out/disc.miniscram"); err != nil {
        t.Fatalf("Start: %v", err)
    }

    res := waitFor(t, done, 3*time.Second)
    if res.Status != "fail" {
        t.Errorf("status = %q, want fail", res.Status)
    }
    if len(res.UnusedScrams) != 1 {
        t.Fatalf("len(UnusedScrams) = %d, want 1", len(res.UnusedScrams))
    }
    if res.UnusedScrams[0].Path != "/disc/x.scram" {
        t.Errorf("UnusedScrams[0].Path = %q, want /disc/x.scram", res.UnusedScrams[0].Path)
    }
    if res.UnusedScrams[0].Size != 765077352 {
        t.Errorf("UnusedScrams[0].Size = %d, want 765077352", res.UnusedScrams[0].Size)
    }
}
```

- [ ] **Step 3: Write `TestActionRunner_DrainUnusedScrams`**

```go
// TestActionRunner_DrainUnusedScrams confirms that DrainUnusedScrams()
// returns accumulated scrams and that subsequent calls return nil.
func TestActionRunner_DrainUnusedScrams(t *testing.T) {
    r, done := newTestRunner(t, "json_scram")

    if err := r.Start("pack", "/in/disc.cue", "/out/disc.miniscram"); err != nil {
        t.Fatalf("Start: %v", err)
    }

    // Drain while running — poll until we have entries or timeout.
    var drained []unusedScram
    deadline := time.Now().Add(3 * time.Second)
    for time.Now().Before(deadline) {
        drained = r.DrainUnusedScrams()
        if len(drained) > 0 {
            break
        }
        time.Sleep(10 * time.Millisecond)
    }
    if len(drained) != 1 {
        t.Fatalf("DrainUnusedScrams returned %d entries, want 1", len(drained))
    }
    if drained[0].Path != "/disc/x.scram" {
        t.Errorf("drained[0].Path = %q, want /disc/x.scram", drained[0].Path)
    }

    // Second drain should be empty.
    if got := r.DrainUnusedScrams(); len(got) != 0 {
        t.Errorf("second DrainUnusedScrams returned %d entries, want 0", len(got))
    }

    <-done
}
```

- [ ] **Step 4: Run both new tests**

Run: `cd tools/miniscram-gui && go test -run 'TestActionRunner_AccumulatesUnusedScram|TestActionRunner_DrainUnusedScrams' -v ./...`
Expected: both PASS

- [ ] **Step 5: Run full test suite**

Run: `cd tools/miniscram-gui && go test ./...`
Expected: all pass

- [ ] **Step 6: Commit**

```bash
git add tools/miniscram-gui/runner_test.go
git commit -m "test(gui): unused-scram accumulation in runner (#54)"
```

---

### Task 3: `queue_widget.go` — replace truncation with wrapping in `reasonLabel`

**Files:** Modify `tools/miniscram-gui/queue_widget.go`

- [ ] **Step 1: Replace `MaxLines=1` with `WrapPolicy=WrapGraphemes`**

```go
func reasonLabel(th *material.Theme, state queueState, label string) material.LabelStyle {
    lb := material.Label(th, unit.Sp(11), label)
    if state == qFailed {
        lb.MaxLines = 1       // REMOVE
        lb.WrapPolicy = text.WrapGraphemes  // ADD
    }
    return lb
}
```

Final:

```go
func reasonLabel(th *material.Theme, state queueState, label string) material.LabelStyle {
    lb := material.Label(th, unit.Sp(11), label)
    if state == qFailed {
        lb.WrapPolicy = text.WrapGraphemes
    }
    return lb
}
```

- [ ] **Step 2: Run tests to confirm breakage**

Run: `cd tools/miniscram-gui && go test -run TestReasonLabel -v ./...`
Expected: `TestReasonLabel_FailedSingleLine` FAILS (it asserts `MaxLines == 1`)

- [ ] **Step 3: Commit the breaking change (proves test catches it)**

```bash
git add tools/miniscram-gui/queue_widget.go
git commit -m "fix(gui): replace MaxLines=1 with WrapPolicy=WrapGraphemes in reasonLabel (#54)"
```

---

### Task 4: `queue_widget_test.go` — update reason-label test

**Files:** Modify `tools/miniscram-gui/queue_widget_test.go`

- [ ] **Step 1: Replace the existing test**

Replace `TestReasonLabel_FailedSingleLine`:

```go
// TestReasonLabel_FailedWrapsAtGraphemes confirms qFailed reasons use
// WrapGraphemes instead of MaxLines=1, so text wraps at character
// boundaries rather than being truncated with ellipsis.
func TestReasonLabel_FailedWrapsAtGraphemes(t *testing.T) {
    th := material.NewTheme()
    long := "cue contains only AUDIO tracks; nothing for miniscram to scramble-pack"
    lb := reasonLabel(th, qFailed, long)
    if lb.MaxLines != 0 {
        t.Errorf("reasonLabel(qFailed).MaxLines = %d, want 0 (unbounded)", lb.MaxLines)
    }
    if lb.WrapPolicy != text.WrapGraphemes {
        t.Errorf("reasonLabel(qFailed).WrapPolicy = %v, want text.WrapGraphemes", lb.WrapPolicy)
    }
}
```

- [ ] **Step 2: Run the updated test**

Run: `cd tools/miniscram-gui && go test -run TestReasonLabel -v ./...`
Expected: all pass (FailedWrapsAtGraphemes PASS, OtherStatesUncapped PASS)

- [ ] **Step 3: Run full test suite**

Run: `cd tools/miniscram-gui && go test ./...`
Expected: all pass

- [ ] **Step 4: Commit**

```bash
git add tools/miniscram-gui/queue_widget_test.go
git commit -m "test(gui): update reasonLabel test for WrapGraphemes (#54)"
```

---

### Task 5: `queue_widget.go` — update delete button label

**Files:** Modify `tools/miniscram-gui/queue_widget.go`

- [ ] **Step 1: Update label text in `unusedScramBar`**

Change lines 166-169:

```go
// Before:
label := fmt.Sprintf("Delete %d unused .scram (%s)", len(snap.UnusedScrams), humanBytes(total))
if len(snap.UnusedScrams) != 1 {
    label = fmt.Sprintf("Delete %d unused .scrams (%s)", len(snap.UnusedScrams), humanBytes(total))
}

// After:
label := fmt.Sprintf("Delete %d unused .scram (audio, %s)", len(snap.UnusedScrams), humanBytes(total))
if len(snap.UnusedScrams) != 1 {
    label = fmt.Sprintf("Delete %d unused .scrams (audio, %s)", len(snap.UnusedScrams), humanBytes(total))
}
```

- [ ] **Step 2: Build to verify compilation**

Run: `cd tools/miniscram-gui && go build ./...`
Expected: clean build

- [ ] **Step 3: Commit**

```bash
git add tools/miniscram-gui/queue_widget.go
git commit -m "fix(gui): add '(audio)' context to unused-scram delete button label (#54)"
```

---

### Task 6: `main.go` — drain scrams in frame handler + handleActionResult

**Files:** Modify `tools/miniscram-gui/main.go`

- [ ] **Step 1: Drain accumulated scrams in the frame handler**

After the existing `if rs := mdl.runner.Snapshot(); rs != nil { ... }` block (around line 1226), add:

```go
// Drain any unused-scram events accumulated by the runner's readStderr.
// These are parsed from NDJSON in readStderr and stored separately from
// state.LastLine so they survive being overwritten by a subsequent event.
for _, u := range mdl.runner.DrainUnusedScrams() {
    mdl.queue.appendUnusedScram(u)
}
```

- [ ] **Step 2: Drain scrams in `handleActionResult` as safety-net**

After the existing body of `handleActionResult`, before the closing `}` (around line 637), add:

```go
// Safety-net: drain any unused-scram events that the live frame-handler
// drain missed due to timing (e.g. process exited before a frame event).
for _, u := range res.UnusedScrams {
    m.queue.appendUnusedScram(u)
}
```

Placement should be at the end of `handleActionResult`, after the status switch block:

```go
func (m *model) handleActionResult(res actionResult) {
    ev := buildEventRec(m, res.Action, res.Input, res.Output, res)
    eventInsert(m.db, ev)
    m.refreshStats()

    if res.Status == "success" {
        // ... existing success handling ...
    } else {
        m.toast = nil
    }

    // ADD: safety-net drain for unused-scram events
    for _, u := range res.UnusedScrams {
        m.queue.appendUnusedScram(u)
    }
}
```

- [ ] **Step 3: Build and run tests**

Run: `cd tools/miniscram-gui && go build ./... && go test ./...`
Expected: clean build, all tests pass

- [ ] **Step 4: Run full project test suite**

Run: `cd /home/hugh/miniscram && go test ./...`
Expected: all tests pass

- [ ] **Step 5: Commit**

```bash
git add tools/miniscram-gui/main.go
git commit -m "fix(gui): drain unused-scram events in frame handler and handleActionResult (#54)"
```
