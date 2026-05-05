# miniscram-gui queue panel + drag-and-drop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a left-hand queue panel that turns drop-a-folder into walk-away batch packing, with auto-follow inspect, JSON-progress fills, and native multi-select / directory pickers.

**Architecture:** New `queueModel` owns a list of `queueItem`s; one worker goroutine drains it sequentially using the existing `actionRunner` (no runner changes). Body of the window becomes an Hflex with a fixed-width queue panel on the left and the existing inspect/stats list on the right. Auto-follow loads the running cue into the right pane; user clicks yield/resume the follow. All `runner.Start` calls pass `--progress=json` so the strip and queue rows consume structured events.

**Tech Stack:** Go, Gio v0.9.0 (`gioui.org/io/transfer` for drag-drop), modernc.org/sqlite, `miniscram --progress=json` (already shipped in #26).

**Spec:** [`docs/superpowers/specs/2026-05-04-miniscram-gui-queue-design.md`](../specs/2026-05-04-miniscram-gui-queue-design.md)

**File map:**

| File | Role |
|---|---|
| `tools/miniscram-gui/queue.go` (new) | `queueModel`, `queueItem`, `classify`, `walkForCues`, `addPaths`, `drain` worker, `prettyProgressLine`, `packPhases` lookup |
| `tools/miniscram-gui/queue_widget.go` (new) | Queue panel layout (header, buttons, items list, stop button) and queue row layouts (one per `queueState`) |
| `tools/miniscram-gui/queue_test.go` (new) | Unit tests for classify/walk/addPaths/drain/prettyProgressLine; integration test for `packPhases` coverage |
| `tools/miniscram-gui/runner_test.go` | Add new `FAKE_MODE` cases for NDJSON-emitting fakes |
| `tools/miniscram-gui/result_handler_test.go` | Adjust to use the new `buildEventRec` helper |
| `tools/miniscram-gui/main.go` | Extract `buildEventRec`; add `pickFiles` / `pickDir`; new buttons + state; Hflex body; drag-drop event drain; auto-follow rule; pass `--progress=json` to existing single-file callers |
| `tools/miniscram-gui/widgets.go` | `runningStripWidget` calls `prettyProgressLine` |

---

### Task 1: Extract `buildEventRec` helper from `handleActionResult`

**Goal:** Pull the actionResult → eventRec translation into a standalone helper so the queue worker can reuse it without writing the toast or refreshing stats.

**Files:**
- Modify: `tools/miniscram-gui/main.go` (`handleActionResult` at lines ~363–442)
- Modify: `tools/miniscram-gui/result_handler_test.go` (existing tests still pass; add one for `buildEventRec` directly)

**Acceptance Criteria:**
- [ ] `buildEventRec(mdl *model, action, input, output string, res actionResult) eventRec` exists and is unit-tested.
- [ ] `handleActionResult` calls `buildEventRec` then handles the toast and stats refresh; existing `result_handler_test.go` tests pass unchanged.
- [ ] The helper does not call `eventInsert`, `refreshStats`, or set `m.toast` — it only builds the row.

**Verify:** `cd tools/miniscram-gui && go test -run TestHandleActionResult ./...` → all 6 existing tests pass; one new `TestBuildEventRec_*` test passes.

**Steps:**

- [ ] **Step 1: Read `handleActionResult` to find the row-building block.**

In `main.go` at ~lines 363–419, the function builds `ev := eventRec{...}` and fills it via the `fillTitle` closure plus the per-action `switch`. Lines 420–442 do `eventInsert`, `refreshStats`, and toast logic — those stay in `handleActionResult`.

- [ ] **Step 2: Write `TestBuildEventRec_PackSuccess` test before extracting.**

In `result_handler_test.go`:

```go
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
```

- [ ] **Step 3: Run the new test, verify it fails.**

Run: `cd tools/miniscram-gui && go test -run TestBuildEventRec_PackSuccess ./...`
Expected: FAIL — `undefined: buildEventRec`.

- [ ] **Step 4: Extract `buildEventRec` in `main.go`.**

Replace the body of `handleActionResult` so that it delegates row-building:

```go
// buildEventRec turns an actionResult into a populated eventRec.
// Pure (no DB writes, no toast); shared by handleActionResult and the queue worker.
func buildEventRec(m *model, action, input, output string, res actionResult) eventRec {
    ev := eventRec{
        TS:         time.Now(),
        Action:     action,
        InputPath:  input,
        OutputPath: output,
        DurationMs: res.DurationMs,
        Status:     res.Status,
        Error:      res.Error,
    }
    fillTitle := func(meta *inspectJSON) {
        if meta == nil || len(meta.Tracks) == 0 {
            return
        }
        if e, ok := redumpGet(m.db, meta.Tracks[0].Hashes["sha1"]); ok && e.State == "found" {
            ev.Title = e.Title
        }
    }
    if res.Status != "success" {
        return ev
    }
    switch action {
    case "pack":
        if output != "" {
            if st, err := os.Stat(output); err == nil {
                ev.MiniscramSize = st.Size()
            }
            if raw, err := exec.Command("miniscram", "inspect", output, "--json").Output(); err == nil {
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
        if output != "" {
            if st, err := os.Stat(output); err == nil {
                ev.ScramSize = st.Size()
            }
        }
        if m.meta != nil {
            ev.MiniscramSize = m.miniscramOnDisk
            ev.OverrideRecords = len(m.meta.DeltaRecords)
            ev.WriteOffset = m.meta.WriteOffsetBytes
            fillTitle(m.meta)
        }
    case "verify":
        if m.meta != nil {
            ev.ScramSize = m.meta.Scram.Size
            ev.MiniscramSize = m.miniscramOnDisk
            ev.OverrideRecords = len(m.meta.DeltaRecords)
            ev.WriteOffset = m.meta.WriteOffsetBytes
            fillTitle(m.meta)
        }
    }
    return ev
}

func (m *model) handleActionResult(res actionResult) {
    ev := buildEventRec(m, res.Action, res.Input, res.Output, res)
    eventInsert(m.db, ev)
    m.refreshStats()

    if res.Status == "success" {
        var outputSize int64
        switch res.Action {
        case "pack":
            outputSize = ev.MiniscramSize
        case "unpack":
            outputSize = ev.ScramSize
        }
        m.toast = &toastState{
            Action:     res.Action,
            Output:     res.Output,
            OutputSize: outputSize,
            DurationMs: res.DurationMs,
            ExpiresAt:  time.Now().Add(6 * time.Second),
        }
    } else {
        m.toast = nil
    }
}
```

- [ ] **Step 5: Run all `result_handler_test.go` + `buildEventRec` tests.**

Run: `cd tools/miniscram-gui && go test -run 'TestHandleActionResult|TestBuildEventRec' ./...`
Expected: 7 PASS (6 existing + 1 new).

- [ ] **Step 6: Commit.**

```bash
git add tools/miniscram-gui/main.go tools/miniscram-gui/result_handler_test.go
git commit -m "refactor(gui): extract buildEventRec helper for queue worker reuse"
```

---

### Task 2: queueModel data layer (types, classify, walk, addPaths)

**Goal:** Pure data layer for the queue — no UI, no goroutines yet. `addPaths` resolves drops/picks into queue items; `classify` and `walkForCues` set the right per-cue state.

**Files:**
- Create: `tools/miniscram-gui/queue.go`
- Create: `tools/miniscram-gui/queue_test.go`

**Acceptance Criteria:**
- [ ] `queueModel` and `queueItem` defined with the exact fields from the spec.
- [ ] `classify(cue, id)` returns `qReady`, `qSkipped reason="no sibling .scram"`, or `qSkipped reason="already packed"` based on sibling files.
- [ ] `walkForCues(root)` returns absolute paths sorted lexicographically; skips dot-prefixed dirs except the root itself.
- [ ] `addPaths(mdl, paths)` resolves dirs to cues, dedups by `filepath.Clean(filepath.Abs(...))`, and ignores non-cue/non-miniscram/non-dir entries silently.
- [ ] `addPaths` does NOT start the worker yet (worker comes in Task 4 — leave a `// TODO(task 4): kick worker` placeholder marked clearly).

**Verify:** `cd tools/miniscram-gui && go test -run 'TestClassify|TestWalkForCues|TestAddPaths' ./...` → all pass.

**Steps:**

- [ ] **Step 1: Write the tests first.**

Create `tools/miniscram-gui/queue_test.go`:

```go
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
```

- [ ] **Step 2: Run tests, verify they fail.**

Run: `cd tools/miniscram-gui && go test -run 'TestClassify|TestWalkForCues|TestAddPaths' ./...`
Expected: FAIL — `undefined: classify, walkForCues, queueModel, qReady, qSkipped`.

- [ ] **Step 3: Create `queue.go` with the data layer.**

```go
// tools/miniscram-gui/queue.go
package main

import (
    "io/fs"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "sync"
    "time"
)

type queueState string

const (
    qReady     queueState = "ready"
    qSkipped   queueState = "skipped"
    qRunning   queueState = "running"
    qDone      queueState = "done"
    qFailed    queueState = "failed"
    qCancelled queueState = "cancelled"
)

type queueItem struct {
    ID         int64
    CuePath    string // absolute, filepath.Clean'd
    Basename   string
    Reason     string
    State      queueState
    Label      string  // current --progress=json step label
    Fraction   float64 // 0..1
    StartedAt  time.Time
    DurationMs int64
}

type queueModel struct {
    mu            sync.Mutex
    items         []queueItem
    nextID        int64
    deleteScram   bool
    stopped       bool
    autoFollow    bool
    workerRunning bool
}

func newQueueModel() *queueModel {
    return &queueModel{
        deleteScram: true, // matches the existing cue-view checkbox default
        autoFollow:  true,
    }
}

// Note: also add `queue *queueModel` to the `model` struct in main.go in
// this task. The field is referenced by tests in later tasks. Initialize it
// in `main()` after `mdl.runner = ...` with `mdl.queue = newQueueModel()`.

func exists(p string) bool {
    _, err := os.Stat(p)
    return err == nil
}

// classify inspects a cue's siblings to determine its initial state.
func classify(cue string, id int64) queueItem {
    base := strings.TrimSuffix(cue, filepath.Ext(cue))
    item := queueItem{
        ID:       id,
        CuePath:  cue,
        Basename: filepath.Base(cue),
    }
    switch {
    case !exists(base + ".scram"):
        item.State = qSkipped
        item.Reason = "no sibling .scram"
    case exists(base + ".miniscram"):
        item.State = qSkipped
        item.Reason = "already packed"
    default:
        item.State = qReady
    }
    return item
}

// walkForCues finds *.cue under root recursively. Returns absolute
// paths sorted ascending. Skips dot-prefixed dirs except the root.
func walkForCues(root string) []string {
    var out []string
    _ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
        if err != nil {
            if d != nil && d.IsDir() {
                return filepath.SkipDir
            }
            return nil
        }
        if d.IsDir() {
            if path != root && strings.HasPrefix(d.Name(), ".") {
                return filepath.SkipDir
            }
            return nil
        }
        if strings.EqualFold(filepath.Ext(d.Name()), ".cue") {
            out = append(out, path)
        }
        return nil
    })
    sort.Strings(out)
    return out
}

func (q *queueModel) hasPath(abs string) bool {
    for _, it := range q.items {
        if it.CuePath == abs {
            return true
        }
    }
    return false
}

// addPaths is the single funnel for drop / multi-pick / dir-pick.
// mdl may be nil in tests (no .miniscram inspect-load triggered).
func (q *queueModel) addPaths(mdl *model, paths []string) {
    q.mu.Lock()
    defer q.mu.Unlock()

    var cues []string
    for _, p := range paths {
        abs, err := filepath.Abs(p)
        if err != nil {
            continue
        }
        abs = filepath.Clean(abs)
        st, err := os.Stat(abs)
        if err != nil {
            continue
        }
        switch {
        case st.IsDir():
            cues = append(cues, walkForCues(abs)...)
        case strings.EqualFold(filepath.Ext(abs), ".cue"):
            cues = append(cues, abs)
        case strings.EqualFold(filepath.Ext(abs), ".miniscram"):
            // Single-file flow: load into inspect. Disengages autoFollow.
            if mdl != nil {
                mdl.load(abs)
                q.autoFollow = false
            }
        }
    }
    for _, cue := range cues {
        if q.hasPath(cue) {
            continue
        }
        q.items = append(q.items, classify(cue, q.nextID))
        q.nextID++
    }
    // TODO(Task 4): if !q.workerRunning && q.hasReady() { go q.drain(mdl) }
}
```

- [ ] **Step 4: Add the `queue *queueModel` field to `model` and initialise it.**

In `main.go` `type model struct` (~line 232), add:

```go
queue *queueModel
```

In `main()` (~line 620, just after `mdl.runner = newActionRunner(...)`), add:

```go
mdl.queue = newQueueModel()
```

This is done in Task 2 so subsequent tasks' tests can reference `m.queue` directly.

- [ ] **Step 5: Run tests, expect pass.**

Run: `cd tools/miniscram-gui && go test -run 'TestClassify|TestWalkForCues|TestAddPaths' ./...`
Expected: 5 PASS.

- [ ] **Step 6: Run `go vet` and full tests to confirm nothing else regressed.**

Run: `cd tools/miniscram-gui && go vet ./... && go test ./...`
Expected: vet clean, all tests pass.

- [ ] **Step 6: Commit.**

```bash
git add tools/miniscram-gui/queue.go tools/miniscram-gui/queue_test.go
git commit -m "feat(gui): queue data layer (classify, walkForCues, addPaths)"
```

---

### Task 3: `--progress=json` integration (parser + packPhases + apply to all callers)

**Goal:** Add NDJSON parsing utilities, the `packPhases` lookup, prettify the running-strip line, and pass `--progress=json` from every existing `runner.Start` call so the strip and queue rows have one consistent structured input.

**Files:**
- Modify: `tools/miniscram-gui/queue.go` (add `progressEvent`, `prettyProgressLine`, `packPhases`, `lookupFraction`)
- Modify: `tools/miniscram-gui/queue_test.go` (`TestPrettyProgressLine`, `TestLookupFraction`)
- Modify: `tools/miniscram-gui/widgets.go` (`runningStripWidget` calls `prettyProgressLine`)
- Modify: `tools/miniscram-gui/main.go` (existing single-file Pack/Verify/Unpack callers gain `--progress=json`)
- Modify: `tools/miniscram-gui/result_handler_test.go` (no behavior change expected; just confirm)

**Acceptance Criteria:**
- [ ] `prettyProgressLine(s)` returns `"step: <label>…"` / `"done: <label> ✓"` / `<msg>` / `<error>` for valid NDJSON; returns the input unchanged otherwise.
- [ ] `lookupFraction(label string) (float64, bool)` matches by prefix against `packPhases` and returns the fraction + true on hit.
- [ ] All three single-file callers (`Pack`, `Verify`, `Unpack`) pass `--progress=json` to `miniscram`.
- [ ] The strip widget displays a friendly "step: writing miniscram…" instead of raw JSON.

**Verify:** `cd tools/miniscram-gui && go test -run 'TestPrettyProgressLine|TestLookupFraction' ./... && go build ./...` → tests pass and the GUI builds.

**Steps:**

- [ ] **Step 1: Write tests first.**

Append to `queue_test.go`:

```go
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
```

- [ ] **Step 2: Run tests, verify they fail.**

Run: `cd tools/miniscram-gui && go test -run 'TestPrettyProgressLine|TestLookupFraction' ./...`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Add the implementation to `queue.go`.**

```go
// at top of queue.go, alongside other imports:
import (
    "encoding/json"
    // ... existing imports
)

// progressEvent is the wire shape emitted by `miniscram --progress=json`.
// Mirrors the struct in reporter.go; redefined here because the GUI is its
// own Go module and cannot import the main package.
type progressEvent struct {
    Type  string `json:"type"`
    Label string `json:"label,omitempty"`
    Msg   string `json:"msg,omitempty"`
    Error string `json:"error,omitempty"`
}

// prettyProgressLine renders a stderr line for human display. NDJSON
// events from --progress=json get a friendly form; non-JSON falls
// through unchanged so the strip keeps working in any future mode.
func prettyProgressLine(s string) string {
    var ev progressEvent
    if err := json.Unmarshal([]byte(s), &ev); err != nil {
        return s
    }
    switch ev.Type {
    case "step":
        return "step: " + ev.Label + "…"
    case "done":
        return "done: " + ev.Label + " ✓"
    case "info", "warn":
        return ev.Msg
    case "fail":
        if ev.Error != "" {
            return ev.Error
        }
        return ev.Label + " failed"
    }
    return s
}

// packPhases maps known pack-step label prefixes to a target fraction.
// Order matters only for human readability; lookup is by prefix-match.
// Labels and their order come from pack.go (see the spec for citations).
var packPhases = []struct {
    Prefix   string
    Fraction float64
}{
    {"resolving cue", 0.02},
    {"detecting write offset", 0.05},
    {"checking constant offset", 0.08}, // conditional
    {"hashing tracks", 0.15},
    {"hashing scram", 0.30},
    {"building scram prediction + delta", 0.65},
    {"writing container", 0.95},
}

func lookupFraction(label string) (float64, bool) {
    for _, p := range packPhases {
        if strings.HasPrefix(label, p.Prefix) {
            return p.Fraction, true
        }
    }
    return 0, false
}
```

- [ ] **Step 4: Run tests, expect pass.**

Run: `cd tools/miniscram-gui && go test -run 'TestPrettyProgressLine|TestLookupFraction' ./...`
Expected: 7 PASS.

- [ ] **Step 5: Make the strip use `prettyProgressLine`.**

In `widgets.go`, replace the `stepText := state.LastLine` block (~line 37) with:

```go
stepText := prettyProgressLine(state.LastLine)
if stepText == "" {
    stepText = "Starting…"
}
```

- [ ] **Step 6: Add `--progress=json` to existing single-file callers.**

In `main.go` `loop()`:

- Verify (~line 768): change
  ```go
  _ = mdl.runner.Start("verify", mdl.path, "", "verify", mdl.path)
  ```
  to
  ```go
  _ = mdl.runner.Start("verify", mdl.path, "", "verify", "--progress=json", mdl.path)
  ```

- Unpack (~line 794): change
  ```go
  _ = mdl.runner.Start("unpack", srcPath, out, "unpack", srcPath, "-o", out)
  ```
  to
  ```go
  _ = mdl.runner.Start("unpack", srcPath, out, "unpack", "--progress=json", srcPath, "-o", out)
  ```

- Pack (~line 803–810): change `args := []string{"pack", mdl.path}` to:
  ```go
  args := []string{"pack", "--progress=json", mdl.path}
  ```

- [ ] **Step 7: Verify everything builds and existing tests pass.**

Run: `cd tools/miniscram-gui && go vet ./... && go test ./...`
Expected: vet clean, all tests pass (the runner_test.go fakes still emit plain text — `prettyProgressLine` falls through unchanged for them).

- [ ] **Step 8: Commit.**

```bash
git add tools/miniscram-gui/queue.go tools/miniscram-gui/queue_test.go tools/miniscram-gui/widgets.go tools/miniscram-gui/main.go
git commit -m "feat(gui): --progress=json plumbing (prettyProgressLine, packPhases lookup)"
```

---

### Task 4: Queue worker (`drain`) with done-channel ownership

**Goal:** The worker goroutine that processes ready items sequentially via the existing `actionRunner`, with single-flight ownership of `runner.done` while running.

**Files:**
- Modify: `tools/miniscram-gui/queue.go` (`drain`, `nextReadyIndex`, `cancelRemainingReady`, `markRunning`, `markFailed`, `recordResult`, `hasReady`)
- Modify: `tools/miniscram-gui/runner_test.go` (add NDJSON fake mode + a long-running NDJSON fake)
- Modify: `tools/miniscram-gui/queue_test.go` (`TestDrainHappyPath`, `TestStopQueue`, `TestCancelCurrent`)

**Acceptance Criteria:**
- [ ] Worker advances through `qReady` items, calling `runner.Start("pack", ..., "pack", "--progress=json", cue, optional --keep-source)`.
- [ ] After `runner.done` fires, item state moves to `qDone` / `qFailed` / `qCancelled` based on `actionResult.Status`.
- [ ] An event row is written via `eventInsert(buildEventRec(...))` after each item completes.
- [ ] `q.toast` is NOT set by the worker (suppression per spec).
- [ ] On stop: in-flight item is SIGTERM'd, remaining `qReady` items flip to `qCancelled` without spawning subprocesses, only the in-flight item produces an event row.
- [ ] On cancel-current (without setting `q.stopped`): worker advances to next ready item.

**Verify:** `cd tools/miniscram-gui && go test -run 'TestDrain|TestStopQueue|TestCancelCurrent' ./...` → all pass.

**Steps:**

- [ ] **Step 1: Add NDJSON fake modes to `runner_test.go`.**

Extend the `TestMain` switch in `runner_test.go`:

```go
case "json_happy":
    // emit one step + done per known phase, then exit 0
    phases := []string{
        "resolving cue /test/in.cue",
        "detecting write offset",
        "hashing tracks",
        "hashing scram",
        "building scram prediction + delta",
        "writing container",
    }
    enc := func(t, label, msg string) {
        line := `{"type":"` + t + `","label":"` + label + `"`
        if msg != "" {
            line += `,"msg":"` + msg + `"`
        }
        line += "}"
        fmt.Fprintln(os.Stderr, line)
    }
    for _, p := range phases {
        enc("step", p, "")
        time.Sleep(10 * time.Millisecond)
        enc("done", p, "ok")
    }
    os.Exit(0)
case "json_long":
    fmt.Fprintln(os.Stderr, `{"type":"step","label":"writing container"}`)
    sig := make(chan os.Signal, 1)
    signal.Notify(sig, syscall.SIGTERM)
    select {
    case <-sig:
        os.Exit(130)
    case <-time.After(5 * time.Second):
        os.Exit(0)
    }
```

- [ ] **Step 2: Write `TestDrainHappyPath` in `queue_test.go`.**

```go
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
    q.addPaths(m, []string{dir})

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
    q.addPaths(m, []string{dir})

    // Run drain on a goroutine; stop after the first item starts.
    go q.drain(m)
    // Wait for first item to start running.
    deadline := time.Now().Add(2 * time.Second)
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
    deadline = time.Now().Add(3 * time.Second)
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
    q.addPaths(m, []string{dir})

    go q.drain(m)
    // Wait for item 0 to start.
    deadline := time.Now().Add(2 * time.Second)
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
    deadline = time.Now().Add(3 * time.Second)
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
```

The tests use `m.queue` (the field added on `model` in Task 2). `q` is a local alias for `m.queue` returned by the helper.

- [ ] **Step 3: Run tests, verify they fail.**

Run: `cd tools/miniscram-gui && go test -run 'TestDrain|TestStopQueue|TestCancelCurrent' ./...`
Expected: FAIL — `drain` undefined.

- [ ] **Step 4: Implement `drain` and helpers in `queue.go`.**

Append to `queue.go`:

```go
import (
    // add to existing imports:
    "strings"
    "time"
)

func (q *queueModel) hasReady() bool {
    for _, it := range q.items {
        if it.State == qReady {
            return true
        }
    }
    return false
}

func (q *queueModel) nextReadyIndex() int {
    if q.stopped {
        return -1
    }
    for i, it := range q.items {
        if it.State == qReady {
            return i
        }
    }
    return -1
}

func (q *queueModel) cancelRemainingReady() {
    for i, it := range q.items {
        if it.State == qReady {
            q.items[i].State = qCancelled
            _ = it
        }
    }
}

func (q *queueModel) markRunning(idx int) {
    q.items[idx].State = qRunning
    q.items[idx].StartedAt = time.Now()
}

func (q *queueModel) markFailed(idx int, reason string) {
    q.items[idx].State = qFailed
    q.items[idx].Reason = reason
}

// recordResult flips the row state + records duration based on the
// runner's actionResult classification.
func (q *queueModel) recordResult(idx int, res actionResult) {
    it := &q.items[idx]
    it.DurationMs = res.DurationMs
    switch res.Status {
    case "success":
        it.State = qDone
        it.Fraction = 1.0
    case "fail":
        it.State = qFailed
        it.Reason = res.Error
    case "cancelled":
        it.State = qCancelled
    }
}

// drain processes qReady items sequentially. The single-flight invariant
// in actionRunner means only one subprocess runs at a time. The worker
// owns mdl.runner.done while workerRunning == true.
func (q *queueModel) drain(mdl *model) {
    q.mu.Lock()
    if q.workerRunning {
        q.mu.Unlock()
        return
    }
    q.workerRunning = true
    q.mu.Unlock()
    defer func() {
        q.mu.Lock()
        q.cancelRemainingReady() // flush any qReady left after stop / loop exit
        q.workerRunning = false
        q.mu.Unlock()
        if mdl != nil && mdl.invalidate != nil {
            mdl.invalidate()
        }
    }()

    for {
        q.mu.Lock()
        idx := q.nextReadyIndex()
        if idx == -1 {
            q.mu.Unlock()
            return
        }
        q.markRunning(idx)
        item := q.items[idx]
        autoFollow := q.autoFollow
        deleteScram := q.deleteScram
        q.mu.Unlock()

        if autoFollow && mdl != nil {
            mdl.load(item.CuePath)
        }
        if mdl != nil && mdl.invalidate != nil {
            mdl.invalidate()
        }

        out := strings.TrimSuffix(item.CuePath, filepath.Ext(item.CuePath)) + ".miniscram"
        args := []string{"pack", "--progress=json", item.CuePath}
        if !deleteScram {
            args = append(args, "--keep-source")
        }

        if err := mdl.runner.Start("pack", item.CuePath, out, args...); err != nil {
            q.mu.Lock()
            q.markFailed(idx, err.Error())
            q.mu.Unlock()
            continue
        }

        res := <-mdl.runner.done

        q.mu.Lock()
        q.recordResult(idx, res)
        item = q.items[idx] // refresh for buildEventRec
        q.mu.Unlock()

        if mdl != nil && mdl.db != nil {
            ev := buildEventRec(mdl, "pack", item.CuePath, out, res)
            eventInsert(mdl.db, ev)
            mdl.refreshStats()
        }
        if mdl != nil && mdl.invalidate != nil {
            mdl.invalidate()
        }
    }
}
```

(Note: the test setup creates `m.runner` directly. The worker reads `mdl.runner.done` and the runner's existing single-flight semantics keep the channel safe.)

- [ ] **Step 5: Run tests, expect pass.**

Run: `cd tools/miniscram-gui && go test -run 'TestDrain|TestStopQueue|TestCancelCurrent' ./...`
Expected: 3 PASS.

If `TestStopQueue` is flaky on slow CI, raise the wait deadline; the timing is the loose part.

- [ ] **Step 6: Run all tests + vet + build.**

Run: `cd tools/miniscram-gui && go vet ./... && go test ./... && go build ./...`
Expected: clean.

- [ ] **Step 7: Commit.**

```bash
git add tools/miniscram-gui/queue.go tools/miniscram-gui/queue_test.go tools/miniscram-gui/runner_test.go
git commit -m "feat(gui): queue worker drain with stop/cancel-current semantics"
```

---

### Task 5: `TestPackPhasesCoverage` (live miniscram check)

**Goal:** Lock down `packPhases` against silent label-rename in `pack.go` by running real miniscram pack with `--progress=json` and asserting every emitted `step.Label` matches a prefix in `packPhases`.

**Files:**
- Modify: `tools/miniscram-gui/queue_test.go` (one new test)

**Acceptance Criteria:**
- [ ] Test builds (or finds) a `miniscram` binary, builds a synthetic pack-able fixture, runs `pack --progress=json`, and asserts every `step.Label` is covered.
- [ ] If the test cannot locate a real `miniscram` binary (e.g., not on PATH), it `t.Skip`s with a clear message rather than failing.

**Verify:** `cd tools/miniscram-gui && go test -run TestPackPhasesCoverage ./... -v` → PASS or SKIP with reason.

**Steps:**

- [ ] **Step 1: Inspect how `packSyntheticContainer` is used in the main package.**

Search for it: `grep -n 'packSyntheticContainer' /home/hugh/miniscram/*.go`. Note the signature; the main module is a different Go module so the GUI test can't call it directly. Instead the test will:

1. Locate `miniscram` on PATH (or skip).
2. Build a tiny synthetic cue + scram pair in `t.TempDir()` using a known-small fixture (the project's `cli_test.go` already does this — copy the pattern). If that's hard from a different module, fall back to skipping when no real fixture is available.

The simplest robust approach: run against `test-discs/half-life/` if present (gitignored), else skip.

- [ ] **Step 2: Add the test.**

```go
func TestPackPhasesCoverage(t *testing.T) {
    miniscramBin, err := exec.LookPath("miniscram")
    if err != nil {
        t.Skip("miniscram not on PATH; build it and run again to lock packPhases")
    }
    fixtureCue := os.Getenv("MINISCRAM_TEST_CUE")
    if fixtureCue == "" {
        // Default fixture lives outside this module; ENOENT == skip.
        candidate := filepath.Join("..", "..", "test-discs", "half-life", "half-life.cue")
        if _, err := os.Stat(candidate); err == nil {
            fixtureCue = candidate
        }
    }
    if fixtureCue == "" {
        t.Skip("no MINISCRAM_TEST_CUE env and no test-discs/half-life fixture; skipping")
    }

    // Pack to a temp output to avoid clobbering a real .miniscram next to the cue.
    tmp := t.TempDir()
    out := filepath.Join(tmp, "fixture.miniscram")
    cmd := exec.Command(miniscramBin, "pack", "--progress=json", "--keep-source", "-o", out, fixtureCue)
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil {
        t.Fatalf("miniscram pack failed: %v\nstderr:\n%s", err, stderr.String())
    }

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
```

(Add `bytes` and `os/exec` to the imports of `queue_test.go` if not already there.)

- [ ] **Step 3: Run the test.**

Run: `cd tools/miniscram-gui && go test -run TestPackPhasesCoverage ./... -v`
Expected: PASS if `miniscram` is on PATH and `test-discs/half-life/` exists; SKIP otherwise.

If labels mismatch, update `packPhases` in `queue.go` to match what `pack.go` actually emits.

- [ ] **Step 4: Commit.**

```bash
git add tools/miniscram-gui/queue_test.go
git commit -m "test(gui): TestPackPhasesCoverage locks packPhases against pack.go drift"
```

---

### Task 6: Native pickers (`pickFiles`, `pickDir`)

**Goal:** Add `pickFiles()` (multi-select cues) and `pickDir()` (directory) helpers mirroring the existing `pickFile`/`pickSave` patterns.

**Files:**
- Modify: `tools/miniscram-gui/main.go` (two new helpers near `pickFile`)

**Acceptance Criteria:**
- [ ] `pickFiles() ([]string, error)` returns a slice of absolute paths from the platform multi-select dialog, or empty slice on cancel.
- [ ] `pickDir() (string, error)` returns one absolute path or empty on cancel.
- [ ] Build is clean on Linux (`go build`) — macOS / Windows code paths are present but unverified this round.

**Verify:** `cd tools/miniscram-gui && go build ./...` → clean. (Manual verification of the dialogs is part of Task 9.)

**Steps:**

- [ ] **Step 1: Add `pickFiles` to `main.go` near the existing `pickFile`.**

```go
func pickFiles() ([]string, error) {
    switch runtime.GOOS {
    case "linux":
        if p, err := exec.LookPath("zenity"); err == nil {
            out, err := exec.Command(p, "--file-selection", "--multiple",
                "--separator=\n",
                "--title=Add cue files to queue",
                "--file-filter=cuesheets | *.cue",
                "--file-filter=all files | *").Output()
            if err != nil {
                return nil, err
            }
            return splitLines(strings.TrimSpace(string(out))), nil
        }
        if p, err := exec.LookPath("kdialog"); err == nil {
            // kdialog has no native multi mode; fall back to single-select.
            out, err := exec.Command(p, "--getopenfilename", "", "*.cue|cuesheets\n*|all files").Output()
            if err != nil {
                return nil, err
            }
            return splitLines(strings.TrimSpace(string(out))), nil
        }
        return nil, errors.New("install zenity or kdialog for the native multi picker")
    case "darwin":
        out, err := exec.Command("osascript", "-e",
            `set fs to choose file with prompt "Add cue files to queue" of type {"cue"} with multiple selections allowed`+"\n"+
                `set lst to ""`+"\n"+
                `repeat with f in fs`+"\n"+
                `set lst to lst & POSIX path of f & linefeed`+"\n"+
                `end repeat`+"\n"+
                `return lst`).Output()
        if err != nil {
            return nil, err
        }
        return splitLines(strings.TrimSpace(string(out))), nil
    case "windows":
        ps := `Add-Type -AssemblyName System.Windows.Forms;` +
            `$f = New-Object System.Windows.Forms.OpenFileDialog;` +
            `$f.Filter = "cuesheets|*.cue|All|*";` +
            `$f.Multiselect = $true;` +
            `if ($f.ShowDialog() -eq 'OK') { $f.FileNames -join "` + "`n" + `" }`
        out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
        if err != nil {
            return nil, err
        }
        return splitLines(strings.TrimSpace(string(out))), nil
    }
    return nil, fmt.Errorf("no multi picker for %s", runtime.GOOS)
}

func pickDir() (string, error) {
    switch runtime.GOOS {
    case "linux":
        if p, err := exec.LookPath("zenity"); err == nil {
            out, err := exec.Command(p, "--file-selection", "--directory",
                "--title=Add directory to queue").Output()
            if err != nil {
                return "", err
            }
            return strings.TrimSpace(string(out)), nil
        }
        if p, err := exec.LookPath("kdialog"); err == nil {
            out, err := exec.Command(p, "--getexistingdirectory").Output()
            if err != nil {
                return "", err
            }
            return strings.TrimSpace(string(out)), nil
        }
        return "", errors.New("install zenity or kdialog for the native directory picker")
    case "darwin":
        out, err := exec.Command("osascript", "-e",
            `POSIX path of (choose folder with prompt "Add directory to queue")`).Output()
        if err != nil {
            return "", err
        }
        return strings.TrimSpace(string(out)), nil
    case "windows":
        ps := `Add-Type -AssemblyName System.Windows.Forms;` +
            `$f = New-Object System.Windows.Forms.FolderBrowserDialog;` +
            `if ($f.ShowDialog() -eq 'OK') { $f.SelectedPath }`
        out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
        if err != nil {
            return "", err
        }
        return strings.TrimSpace(string(out)), nil
    }
    return "", fmt.Errorf("no directory picker for %s", runtime.GOOS)
}

func splitLines(s string) []string {
    if s == "" {
        return nil
    }
    var out []string
    for _, line := range strings.Split(s, "\n") {
        if line = strings.TrimSpace(line); line != "" {
            out = append(out, line)
        }
    }
    return out
}
```

- [ ] **Step 2: Build.**

Run: `cd tools/miniscram-gui && go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 3: Commit.**

```bash
git add tools/miniscram-gui/main.go
git commit -m "feat(gui): pickFiles + pickDir native multi/dir pickers"
```

---

### Task 7: Queue panel + row widgets

**Goal:** Render the queue panel — header, buttons, items list, stop button, drop-target empty state — and a row layout that handles all six `queueState` values including the green progress fill behind running rows.

**Files:**
- Create: `tools/miniscram-gui/queue_widget.go`

**Acceptance Criteria:**
- [ ] `queuePanel(th, q, mdl, btns) layout.Widget` renders the full panel and is callable from `loop()` in main.go.
- [ ] Empty state shows a centred "Drop cues or a folder here" with a thin dashed border.
- [ ] Each `queueState` has a distinct visual: ready (pending dot), running (green fill at width = panel_w * Fraction), done (✓ + duration), failed (✗ + reason, red tint), skipped (⊘ + reason, greyed), cancelled (⊝, greyed).
- [ ] Per-row click bubbles up via a `widget.Clickable` keyed by `queueItem.ID`.
- [ ] `× / ⏹` per-row buttons are wired and stop event propagation.
- [ ] Stop queue button only renders when `q.workerRunning == true`.

**Verify:** `cd tools/miniscram-gui && go build ./...` → clean. (Visual verification is the smoke test.)

**Steps:**

- [ ] **Step 1: Create `queue_widget.go` with the panel and row layouts.**

```go
// tools/miniscram-gui/queue_widget.go
package main

import (
    "fmt"
    "image"

    "gioui.org/font"
    "gioui.org/layout"
    "gioui.org/op"
    "gioui.org/op/clip"
    "gioui.org/op/paint"
    "gioui.org/unit"
    "gioui.org/widget"
    "gioui.org/widget/material"
)

// queuePanelButtons groups the panel's interactive widgets so loop() can
// own their state.
type queuePanelButtons struct {
    AddFiles      widget.Clickable
    AddDir        widget.Clickable
    DeleteScramCB widget.Bool
    Stop          widget.Clickable
    rowClick      map[int64]*widget.Clickable
    rowAction     map[int64]*widget.Clickable // × for ready, ⏹ for running
}

func newQueuePanelButtons() *queuePanelButtons {
    return &queuePanelButtons{
        rowClick:  map[int64]*widget.Clickable{},
        rowAction: map[int64]*widget.Clickable{},
    }
}

func (b *queuePanelButtons) RowClick(id int64) *widget.Clickable {
    if c, ok := b.rowClick[id]; ok {
        return c
    }
    c := new(widget.Clickable)
    b.rowClick[id] = c
    return c
}

func (b *queuePanelButtons) RowAction(id int64) *widget.Clickable {
    if c, ok := b.rowAction[id]; ok {
        return c
    }
    c := new(widget.Clickable)
    b.rowAction[id] = c
    return c
}

const queuePanelWidth = 280

// queuePanel renders the left-hand queue. Accepts a snapshot (slice +
// flags) so layout never holds the queue mutex.
type queueSnapshot struct {
    Items         []queueItem
    DeleteScram   bool
    WorkerRunning bool
    ReadyCount    int
    SkippedCount  int
}

func (q *queueModel) Snapshot() queueSnapshot {
    q.mu.Lock()
    defer q.mu.Unlock()
    cp := make([]queueItem, len(q.items))
    copy(cp, q.items)
    s := queueSnapshot{
        Items:         cp,
        DeleteScram:   q.deleteScram,
        WorkerRunning: q.workerRunning,
    }
    for _, it := range cp {
        switch it.State {
        case qReady:
            s.ReadyCount++
        case qSkipped:
            s.SkippedCount++
        }
    }
    return s
}

func queuePanel(th *material.Theme, snap queueSnapshot, btns *queuePanelButtons, listScroll *widget.List) layout.Widget {
    return func(gtx layout.Context) layout.Dimensions {
        gtx.Constraints.Min.X = gtx.Dp(unit.Dp(queuePanelWidth))
        gtx.Constraints.Max.X = gtx.Dp(unit.Dp(queuePanelWidth))

        return layout.Inset{Top: unit.Dp(12), Bottom: unit.Dp(12), Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx,
            func(gtx layout.Context) layout.Dimensions {
                return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
                    layout.Rigid(queueHeader(th, snap)),
                    layout.Rigid(spacer(0, 10)),
                    layout.Rigid(queueAddButtons(th, btns)),
                    layout.Rigid(spacer(0, 8)),
                    layout.Rigid(queueDeleteScramRow(th, btns)),
                    layout.Rigid(spacer(0, 8)),
                    layout.Rigid(thinDivider),
                    layout.Flexed(1, queueItemsList(th, snap, btns, listScroll)),
                    layout.Rigid(thinDivider),
                    layout.Rigid(queueStopButton(th, snap, btns)),
                )
            })
    }
}

func queueHeader(th *material.Theme, snap queueSnapshot) layout.Widget {
    return func(gtx layout.Context) layout.Dimensions {
        text := fmt.Sprintf("Queue · %d ready · %d skipped", snap.ReadyCount, snap.SkippedCount)
        lb := material.Label(th, unit.Sp(13), text)
        lb.Color = text2
        lb.Font.Weight = font.SemiBold
        return lb.Layout(gtx)
    }
}

func queueAddButtons(th *material.Theme, btns *queuePanelButtons) layout.Widget {
    return func(gtx layout.Context) layout.Dimensions {
        return layout.Flex{}.Layout(gtx,
            layout.Rigid(panelButton(th, &btns.AddFiles, "+ Add files…")),
            layout.Rigid(spacer(8, 0)),
            layout.Rigid(panelButton(th, &btns.AddDir, "+ Add dir…")),
        )
    }
}

func panelButton(th *material.Theme, c *widget.Clickable, label string) layout.Widget {
    return func(gtx layout.Context) layout.Dimensions {
        btn := material.Button(th, c, label)
        btn.Background = surface2
        btn.Color = text1
        btn.CornerRadius = unit.Dp(4)
        btn.TextSize = unit.Sp(12)
        btn.Inset = layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}
        return btn.Layout(gtx)
    }
}

func queueDeleteScramRow(th *material.Theme, btns *queuePanelButtons) layout.Widget {
    return func(gtx layout.Context) layout.Dimensions {
        cb := material.CheckBox(th, &btns.DeleteScramCB, "delete .scram after pack")
        cb.TextSize = unit.Sp(11)
        cb.Color = text2
        return cb.Layout(gtx)
    }
}

func queueStopButton(th *material.Theme, snap queueSnapshot, btns *queuePanelButtons) layout.Widget {
    return func(gtx layout.Context) layout.Dimensions {
        if !snap.WorkerRunning {
            return layout.Dimensions{}
        }
        return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
            btn := material.Button(th, &btns.Stop, "Stop queue")
            btn.Background = mustRGB("3a1a1a")
            btn.Color = bad
            btn.CornerRadius = unit.Dp(4)
            btn.TextSize = unit.Sp(12)
            btn.Inset = layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}
            return btn.Layout(gtx)
        })
    }
}

func queueItemsList(th *material.Theme, snap queueSnapshot, btns *queuePanelButtons, listScroll *widget.List) layout.FlexChild {
    return layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
        if len(snap.Items) == 0 {
            return queueDropTarget(th)(gtx)
        }
        return material.List(th, listScroll).Layout(gtx, len(snap.Items),
            func(gtx layout.Context, i int) layout.Dimensions {
                return queueRow(th, snap.Items[i], btns)(gtx)
            })
    })
}

func queueDropTarget(th *material.Theme) layout.Widget {
    return func(gtx layout.Context) layout.Dimensions {
        return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
            lb := material.Label(th, unit.Sp(12), "Drop cues or a\nfolder here")
            lb.Color = text3
            lb.Alignment = 1 // center
            return lb.Layout(gtx)
        })
    }
}

// queueRow returns a layout per row driven by the item's state.
func queueRow(th *material.Theme, it queueItem, btns *queuePanelButtons) layout.Widget {
    return func(gtx layout.Context) layout.Dimensions {
        clickArea := btns.RowClick(it.ID)
        actionBtn := btns.RowAction(it.ID)
        rowH := gtx.Dp(unit.Dp(34))

        return clickArea.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
            // Background: green fill behind running rows; light tint behind failed.
            macro := op.Record(gtx.Ops)
            content := layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx,
                func(gtx layout.Context) layout.Dimensions {
                    return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
                        layout.Rigid(queueRowGlyph(it, gtx)),
                        layout.Rigid(spacer(8, 0)),
                        layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
                            lb := material.Label(th, unit.Sp(12), it.Basename)
                            switch it.State {
                            case qSkipped, qCancelled:
                                lb.Color = text3
                            case qFailed:
                                lb.Color = bad
                            default:
                                lb.Color = text1
                            }
                            return lb.Layout(gtx)
                        }),
                        layout.Rigid(queueRowSuffix(th, it)),
                        layout.Rigid(spacer(6, 0)),
                        layout.Rigid(queueRowActionBtn(th, it, actionBtn)),
                    )
                })
            call := macro.Stop()

            // Backgrounds.
            switch it.State {
            case qRunning:
                fillW := int(float64(content.Size.X) * it.Fraction)
                if fillW > 0 {
                    paint.FillShape(gtx.Ops, accent, clip.Rect{Max: image.Pt(fillW, content.Size.Y)}.Op())
                }
            case qFailed:
                paint.FillShape(gtx.Ops, mustRGB("2a1212"), clip.Rect{Max: content.Size}.Op())
            }
            call.Add(gtx.Ops)
            content.Size.Y = max(content.Size.Y, rowH)
            return content
        })
    }
}

func queueRowGlyph(it queueItem, gtx layout.Context) layout.Widget {
    return func(gtx layout.Context) layout.Dimensions {
        var c = pending
        var glyph = "○"
        switch it.State {
        case qReady:
            glyph = "○"
            c = pending
        case qRunning:
            glyph = "●"
            c = accentFg
        case qDone:
            glyph = "✓"
            c = good
        case qFailed:
            glyph = "✗"
            c = bad
        case qSkipped:
            glyph = "⊘"
            c = text3
        case qCancelled:
            glyph = "⊝"
            c = text3
        }
        size := gtx.Dp(unit.Dp(12))
        defer clip.Rect{Max: image.Pt(size, size)}.Push(gtx.Ops).Pop()
        paint.ColorOp{Color: c}.Add(gtx.Ops)
        // simple: draw a small circle/dot using clip.Ellipse
        ell := clip.Ellipse{Max: image.Pt(size, size)}
        paint.FillShape(gtx.Ops, c, ell.Op(gtx.Ops))
        _ = glyph // glyph rendering left as future polish; the dot color carries enough state today
        return layout.Dimensions{Size: image.Pt(size, size)}
    }
}

func queueRowSuffix(th *material.Theme, it queueItem) layout.Widget {
    return func(gtx layout.Context) layout.Dimensions {
        var label string
        var col = text3
        switch it.State {
        case qDone:
            label = fmt.Sprintf("%.1fs", float64(it.DurationMs)/1000)
            col = good
        case qFailed:
            label = "fail"
            col = bad
        case qSkipped:
            label = it.Reason
        case qCancelled:
            label = "cancelled"
        default:
            return layout.Dimensions{}
        }
        lb := material.Label(th, unit.Sp(11), label)
        lb.Color = col
        return lb.Layout(gtx)
    }
}

func queueRowActionBtn(th *material.Theme, it queueItem, click *widget.Clickable) layout.Widget {
    return func(gtx layout.Context) layout.Dimensions {
        var label string
        switch it.State {
        case qReady:
            label = "×"
        case qRunning:
            label = "⏹"
        default:
            return layout.Dimensions{}
        }
        btn := material.Button(th, click, label)
        btn.Background = bg
        btn.Color = text3
        btn.CornerRadius = unit.Dp(3)
        btn.TextSize = unit.Sp(11)
        btn.Inset = layout.Inset{Top: 2, Bottom: 2, Left: 6, Right: 6}
        return btn.Layout(gtx)
    }
}

func max(a, b int) int {
    if a > b {
        return a
    }
    return b
}
```

- [ ] **Step 2: Build.**

Run: `cd tools/miniscram-gui && go build ./... && go vet ./...`
Expected: clean (the widget code is heavy on Gio APIs; expect to massage import lists and a few small adjustments). If the dot glyph renders oddly, trim it to a coloured rectangle — the spec's only commitment is "distinct visual per state."

- [ ] **Step 3: Commit.**

```bash
git add tools/miniscram-gui/queue_widget.go
git commit -m "feat(gui): queue panel + row widgets with state-driven visuals"
```

---

### Task 8: main.go integration (Hflex, auto-follow, button wiring, addPaths kick)

**Goal:** Wire the queue into `loop()` — Hflex layout, auto-follow rule, button-disable while queue runs, stop/cancel-current handlers, and Task 2's TODO (`go q.drain(mdl)`) gets uncommented.

**Files:**
- Modify: `tools/miniscram-gui/main.go`
- Modify: `tools/miniscram-gui/queue.go` (uncomment the worker kick in `addPaths`)

**Acceptance Criteria:**
- [ ] `model` has a `queue *queueModel` field, initialised in `main()`.
- [ ] Window body is an Hflex with the queue panel on the left and the existing Inspect/Stats list on the right.
- [ ] Single-file Pack/Verify/Unpack buttons disable when `q.workerRunning`.
- [ ] FrameEvent drain skips `mdl.runner.done` when `q.workerRunning`.
- [ ] Add files / Add dir buttons spawn a goroutine that calls `pickFiles` / `pickDir` and feeds results to `q.addPaths`.
- [ ] Click on a queue row triggers auto-follow per the rule (load cue; set autoFollow=true if it's the running row, false otherwise).
- [ ] `× / ⏹` buttons remove pending rows / cancel current respectively.
- [ ] Stop queue button sets `q.stopped = true` and calls `runner.Cancel()`.
- [ ] On `app.DestroyEvent`, `q.stopped = true` is set before the existing 5 s grace.

**Verify:** `cd tools/miniscram-gui && go build ./... && go vet ./...` → clean. Manual: `./miniscram-gui`, drop a directory of cues, observe the panel runs through them.

**Steps:**

- [ ] **Step 1: Confirm `queue` field on `model` is initialised.**

The `queue *queueModel` field was added to `model` in Task 2. Confirm `main()` initialises it with `mdl.queue = newQueueModel()` after `mdl.runner = newActionRunner(...)` — if Task 2 only added the struct field without the initialisation, add it now.

- [ ] **Step 2: Uncomment the worker kick in `queue.go` `addPaths`.**

Replace the `// TODO(Task 4):` line with:

```go
if !q.workerRunning && q.hasReady() {
    go q.drain(mdl)
}
```

Move this OUT of the locked section (or unlock first) — `drain` takes the mutex itself. Adjust the function to release the lock before the goroutine:

```go
func (q *queueModel) addPaths(mdl *model, paths []string) {
    q.mu.Lock()
    var cues []string
    // ... existing classification loop
    q.mu.Unlock()

    needWorker := !q.workerRunning && q.hasReady()
    if needWorker && mdl != nil {
        go q.drain(mdl)
    }
}
```

(Move the .miniscram inspect-load call AFTER the lock release, so `mdl.load` doesn't run with `q.mu` held.)

- [ ] **Step 3: Add panel buttons + Hflex in `loop()`.**

In `loop()` (~line 678), near the existing button declarations, add:

```go
qBtns := newQueuePanelButtons()
qBtns.DeleteScramCB.Value = true // mirror the queue-level default
var qListScroll widget.List
qListScroll.Axis = layout.Vertical
```

Replace the existing `layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { ... })` slot in the body Flex with:

```go
layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
    snap := mdl.queue.Snapshot()
    return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
        layout.Rigid(queuePanel(th, snap, qBtns, &qListScroll)),
        layout.Rigid(verticalDivider),
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
    )
}),
```

Add `verticalDivider` near the other `divider` helpers (top of `main.go` or in a small utility section):

```go
func verticalDivider(gtx layout.Context) layout.Dimensions {
    w := gtx.Dp(unit.Dp(1))
    h := gtx.Constraints.Min.Y
    paint.FillShape(gtx.Ops, line, clip.Rect{Max: image.Pt(w, h)}.Op())
    return layout.Dimensions{Size: image.Pt(w, h)}
}
```

- [ ] **Step 4: Wire button click handlers in the FrameEvent block.**

After the existing single-file button handlers, add:

```go
// Add files / Add dir
if qBtns.AddFiles.Clicked(gtx) {
    go func() {
        paths, err := pickFiles()
        if err != nil || len(paths) == 0 {
            return
        }
        mdl.queue.addPaths(mdl, paths)
        if mdl.invalidate != nil {
            mdl.invalidate()
        }
    }()
}
if qBtns.AddDir.Clicked(gtx) {
    go func() {
        p, err := pickDir()
        if err != nil || p == "" {
            return
        }
        mdl.queue.addPaths(mdl, []string{p})
        if mdl.invalidate != nil {
            mdl.invalidate()
        }
    }()
}
// Mirror the panel's checkbox toggle into queueModel.
if qBtns.DeleteScramCB.Update(gtx) {
    mdl.queue.mu.Lock()
    mdl.queue.deleteScram = qBtns.DeleteScramCB.Value
    mdl.queue.mu.Unlock()
}
// Stop queue
if qBtns.Stop.Clicked(gtx) {
    mdl.queue.mu.Lock()
    mdl.queue.stopped = true
    mdl.queue.mu.Unlock()
    mdl.runner.Cancel()
}
// Per-row actions: × on ready, ⏹ on running.
snapForClicks := mdl.queue.Snapshot()
for _, it := range snapForClicks.Items {
    if click := qBtns.RowClick(it.ID); click.Clicked(gtx) {
        mdl.load(it.CuePath)
        mdl.queue.mu.Lock()
        // Auto-follow re-engages only if this is the running item.
        mdl.queue.autoFollow = (it.State == qRunning)
        mdl.queue.mu.Unlock()
    }
    if act := qBtns.RowAction(it.ID); act.Clicked(gtx) {
        switch it.State {
        case qReady:
            mdl.queue.removeByID(it.ID)
        case qRunning:
            mdl.runner.Cancel()
        }
    }
}
```

Add `removeByID` to `queue.go`:

```go
func (q *queueModel) removeByID(id int64) {
    q.mu.Lock()
    defer q.mu.Unlock()
    for i, it := range q.items {
        if it.ID == id {
            q.items = append(q.items[:i], q.items[i+1:]...)
            return
        }
    }
}
```

- [ ] **Step 5: Disable single-file buttons while the queue is running.**

Wherever `verifyBtn.Clicked(gtx)`, `unpackBtn.Clicked(gtx)`, `packBtn.Clicked(gtx)` is checked, add `&& !mdl.queue.workerRunning` (under the queue lock; safest is a small helper):

```go
qWorker := func() bool {
    mdl.queue.mu.Lock()
    defer mdl.queue.mu.Unlock()
    return mdl.queue.workerRunning
}
```

Then `if verifyBtn.Clicked(gtx) && !qWorker() && mdl.kind == "miniscram" && !mdl.runner.Running() { ... }` (and same for unpack and pack).

Also disable the buttons visually in the right pane when queue is running. The simplest approach: pass `qWorker()` into `body()` and have it disable the buttons. For this round, the click-handler guard above is sufficient; visual disabling can land later.

- [ ] **Step 6: Skip the FrameEvent drain when worker owns the channel.**

Replace the existing drain at ~line 738:

```go
select {
case res := <-mdl.runner.done:
    mdl.handleActionResult(res)
default:
}
```

with:

```go
if !qWorker() {
    select {
    case res := <-mdl.runner.done:
        mdl.handleActionResult(res)
    default:
    }
}
```

- [ ] **Step 7: Update `app.DestroyEvent` to set `q.stopped`.**

Replace the existing handler at ~line 723 with:

```go
case app.DestroyEvent:
    if mdl.queue != nil {
        mdl.queue.mu.Lock()
        mdl.queue.stopped = true
        mdl.queue.mu.Unlock()
    }
    if mdl.runner != nil && mdl.runner.Running() {
        mdl.runner.Cancel()
        deadline := time.Now().Add(5 * time.Second)
        for mdl.runner.Running() && time.Now().Before(deadline) {
            time.Sleep(50 * time.Millisecond)
        }
    }
    return e.Err
```

- [ ] **Step 8: Build, vet, run unit tests.**

Run: `cd tools/miniscram-gui && go vet ./... && go test ./... && go build ./...`
Expected: clean.

- [ ] **Step 9: Commit.**

```bash
git add tools/miniscram-gui/main.go tools/miniscram-gui/queue.go
git commit -m "feat(gui): wire queue panel into main loop with auto-follow + stop"
```

---

### Task 9: Drag-and-drop wiring (text/uri-list)

**Goal:** Accept dropped files and directories on the window via Gio's `transfer.TargetFilter`. Parse `text/uri-list` payloads into paths and feed them to `q.addPaths`.

**Files:**
- Modify: `tools/miniscram-gui/main.go`

**Acceptance Criteria:**
- [ ] The window registers a `transfer.TargetFilter{Target: dropTag, Type: "text/uri-list"}`.
- [ ] On `transfer.DataEvent`, the body is read, parsed line-by-line, each `file://` URI decoded via `net/url.QueryUnescape`, and the resulting paths fed to `q.addPaths`.
- [ ] Builds clean on Linux. macOS / Windows are best-effort and not verified this round.

**Verify:** `cd tools/miniscram-gui && go build ./...` → clean. Manual smoke (Linux): drop a directory onto the GUI window, observe the queue populate.

**Steps:**

- [ ] **Step 1: Add imports + tag.**

In `main.go`:

```go
import (
    // existing imports
    "bufio"
    "io"
    "net/url"
    "gioui.org/io/transfer"
)

var dropTag = new(int) // any unique pointer works as event.Tag
```

- [ ] **Step 2: Register a target each frame and drain DataEvents.**

In `loop()` inside the FrameEvent case, near the top of the frame:

```go
// Register the body as a drop target. event.Op tags the area so
// transfer.TargetFilter can match. Place this inside the body region's
// clip; for simplicity we tag the whole window area.
event.Op(gtx.Ops, dropTag)

// Drain transfer events.
for {
    ev, ok := gtx.Event(transfer.TargetFilter{Target: dropTag, Type: "text/uri-list"})
    if !ok {
        break
    }
    if dev, ok := ev.(transfer.DataEvent); ok {
        rc := dev.Open()
        paths := readURIList(rc)
        rc.Close()
        if len(paths) > 0 {
            mdl.queue.addPaths(mdl, paths)
        }
    }
}
```

- [ ] **Step 3: Add the URI-list parser.**

```go
// readURIList parses a text/uri-list body (RFC 2483) into local paths.
// Lines starting with '#' are comments. Only file:// URIs are extracted.
func readURIList(r io.Reader) []string {
    var paths []string
    sc := bufio.NewScanner(r)
    for sc.Scan() {
        line := strings.TrimSpace(sc.Text())
        if line == "" || strings.HasPrefix(line, "#") {
            continue
        }
        u, err := url.Parse(line)
        if err != nil || u.Scheme != "file" {
            continue
        }
        p, err := url.QueryUnescape(u.Path)
        if err != nil {
            continue
        }
        paths = append(paths, p)
    }
    return paths
}
```

- [ ] **Step 4: Build + vet.**

Run: `cd tools/miniscram-gui && go vet ./... && go build ./...`
Expected: clean.

- [ ] **Step 5: Commit.**

```bash
git add tools/miniscram-gui/main.go
git commit -m "feat(gui): drag-and-drop queue input via text/uri-list"
```

---

### Task 10: Smoke verification + screenshot refresh

**Goal:** Run through the spec's manual smoke checklist on a real fixture, capture before/after behavior, and update README screenshots if appropriate.

**Files:**
- Possibly: `tools/miniscram-gui/screenshots/` (if screenshots are added)
- Possibly: `tools/miniscram-gui/README.md` (if documenting the queue)

**Acceptance Criteria:**
- [ ] Drop `test-discs/` (or similar fixture dir) onto the GUI: queue populates, sequential pack runs, header counts decrement, rows turn green.
- [ ] Drop a single cue while another is packing: appends to queue, doesn't disrupt right pane.
- [ ] Click a `done` row: right pane loads the freshly-built `.miniscram`.
- [ ] Click another row mid-pack: right pane switches, auto-follow disengages. Click the running row again: right pane refreshes, auto-follow re-engages.
- [ ] Stop queue mid-pack: in-flight item shows "Cancelling…", everything below flips to `cancelled`, no stray `.miniscram` for the cancelled item.
- [ ] Add files / Add dir buttons open the right native dialogs and feed the queue.
- [ ] Drop a `.miniscram`: loads into inspect; if a queue is running, `autoFollow` disengages.
- [ ] No goroutine leaks after a finished queue (ad-hoc check via `runtime.NumGoroutine` if convenient, or trust the unit tests).

**Verify:** Walk through each bullet manually; record any deviations as follow-up issues.

**Steps:**

- [ ] **Step 1: Build the GUI binary against the locally-built miniscram.**

```bash
cd /home/hugh/miniscram && go build -o /tmp/miniscram .
cd tools/miniscram-gui && nix-shell ../../shell.nix --run 'go build -o miniscram-gui .'
PATH=/tmp:$PATH ./miniscram-gui
```

- [ ] **Step 2: Walk the smoke list above.**

Pause the dev session and exercise each bullet. Note any observed defects in a scratch list. Common areas to watch:

- The queue panel width should not crowd the right pane.
- The progress fill should advance through phases without jumping backwards.
- The stop-queue path should leave no orphan subprocesses (`pgrep -f miniscram` after stop).
- The drag-drop should accept multiple files dragged in a single operation.

- [ ] **Step 3 (optional): Update screenshots.**

If the queue panel deserves a screenshot, capture one with a representative queue mid-flight (use `-mock-running pack` if needed for a stable shot) and add it to `tools/miniscram-gui/screenshots/`. Update the README's Screenshots section.

- [ ] **Step 4: Commit any smoke-driven fixes + screenshot updates.**

```bash
git add tools/miniscram-gui/...
git commit -m "polish(gui): smoke-driven fixes for queue panel"
```

If no fixes were needed, skip the commit and close out.

- [ ] **Step 5: Open a PR.**

Push the branch and open the PR via `gh pr create`. The PR description should reference issue #19 and link the design + plan docs.
