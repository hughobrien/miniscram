# miniscram-gui: queue panel + drag-and-drop + convert directory

Date: 2026-05-04
Status: design
Issue: [#19](https://github.com/hughobrien/miniscram/issues/19)
Affects: `tools/miniscram-gui/` (own go.mod)

## Motivation

The GUI today is single-file: open one `.miniscram` or one `.cue`, act
on it, look at the result. Real corpora are dozens of discs in
sibling directories. Today the workflow for a 30-disc redumper folder
is: click Open file, navigate to the first cue, click Pack, wait, ✕
the toast, click Open file, navigate to the second cue, click Pack —
thirty times. The headline feature this spec adds is a left-hand
queue panel that turns that into a drag-and-drop and walk-away.

The queue is also the natural home for two long-deferred items:
drag-and-drop input (carved out of the unpack spec as a follow-up)
and "convert this whole directory" (issue #19's subtitle). Both
collapse into one mechanism — `addPaths(paths []string)` — fed by
drop events, a multi-select picker, and a directory picker.

## Non-goals

- Cross-platform drag-and-drop. The handler is written portably but
  smoke-tested on Linux/X11+Wayland only this round. macOS and
  Windows drop support is a follow-up if anyone reports it.
- Queue persistence across GUI restart. Mid-queue close discards
  pending items; completed pack events are already in the events DB.
- Queue-wide Verify or Unpack actions. The queue is pack-only,
  matching issue #19's "Convert" framing. Single-file Verify/Unpack
  remain on the right-pane buttons.
- Parallel queue execution. One subprocess at a time, sequencing on
  the existing single-flight `actionRunner`. Disk contention from
  parallel packs is real, the speed-up is unclear, and the existing
  running-strip + toast already give the right "one thing happening"
  story.
- A draggable panel-width splitter. Fixed ~280 dp.
- Re-pack of `done` or `already packed` rows. The user can manually
  delete the produced `.miniscram` and re-drop the cue.
- Reordering pending rows.
- Changes to the miniscram CLI. The GUI is a strict consumer of
  `--progress=json` (which already lands per #26).

## Architecture

A new `queue` subsystem owns a list of `queueItem`s and one worker
goroutine that drains them sequentially. The worker reuses the
existing `actionRunner` for each item, so the running-strip,
SIGTERM-based cancel, stderr line-tail, and event-row writes all
keep working unchanged — each queue item is "just another pack" from
the runner's point of view.

The window-level Flex (top-bar / divider / running-strip / body /
toast / divider / footer) gains an `Hflex` in the body slot:

```
top-bar
divider
running-strip            (only while a queue item runs)
hflex {
  queue-panel (fixed ~280 dp wide)
  vertical-divider
  flexed { existing inspect/stats list }
}
toast
divider
footer
```

The right pane keeps its existing `material.List` scroll, so long
inspect content scrolls independently of the queue panel.

**Single-flight at the subprocess level is preserved.** The worker
is the only `runner.Start` caller during a queue run; the
right-pane Pack/Verify/Unpack buttons disable while
`queue.workerRunning`. The runner is unchanged.

### Auto-follow rule

The right pane auto-follows the currently-running queue item by
default. The `queue.autoFollow` flag yields to manual interaction
and resumes when the user re-engages:

- Worker advances to a new running item: if `autoFollow`, call
  `mdl.load(item.CuePath)`. Else leave the right pane alone.
- User clicks Open file…: `autoFollow = false`.
- User clicks a queue row: load that cue into the right pane. If the
  row is the currently-running item, also set `autoFollow = true`.
  Else `autoFollow = false`.

## Components

### New file `tools/miniscram-gui/queue.go`

```go
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
    ID         int64        // monotonic, scoped to this session
    CuePath    string       // absolute, filepath.Clean'd
    Basename   string       // for display
    Reason     string       // for skipped/failed: short human reason
    State      queueState
    Label      string       // current --progress=json step label
    Fraction   float64      // 0..1, derived from Label via packPhases
    StartedAt  time.Time
    DurationMs int64
}

type queueModel struct {
    mu            sync.Mutex
    items         []queueItem
    nextID        int64
    deleteScram   bool   // queue-level toggle, default true
    stopped       bool   // user clicked Stop queue
    autoFollow    bool   // initially true; toggled per the rule above
    workerRunning bool
}
```

The `model` struct in `main.go` gains one field: `queue *queueModel`.
Initialised in `main()` alongside `runner`.

### `addPaths(paths []string)` — the single funnel

```go
func (q *queueModel) addPaths(mdl *model, paths []string) {
    var cues []string
    for _, p := range paths {
        abs, err := filepath.Abs(p)
        if err != nil { continue }
        abs = filepath.Clean(abs)
        st, err := os.Stat(abs)
        if err != nil { continue }
        switch {
        case st.IsDir():
            cues = append(cues, walkForCues(abs)...)
        case strings.EqualFold(filepath.Ext(abs), ".cue"):
            cues = append(cues, abs)
        case strings.EqualFold(filepath.Ext(abs), ".miniscram"):
            // single-file flow: load into inspect.
            // Per the auto-follow rule, this disengages autoFollow if
            // a queue is running (manual right-pane change wins).
            mdl.load(abs)
            q.autoFollow = false
        }
    }
    // dedup against existing items (any state) by absolute path
    for _, cue := range cues {
        if q.hasPath(cue) { continue }
        q.items = append(q.items, classify(cue, q.nextID))
        q.nextID++
    }
    // kick the worker if there's work
    if !q.workerRunning && q.hasReady() {
        go q.drain(mdl)
    }
}

func walkForCues(root string) []string {
    var out []string
    _ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
        if err != nil {
            if d != nil && d.IsDir() { return filepath.SkipDir }
            return nil
        }
        if d.IsDir() {
            // skip dot-prefixed dirs, e.g. ".git"
            if strings.HasPrefix(d.Name(), ".") && path != root {
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
```

### `drain` — the worker

```go
func (q *queueModel) drain(mdl *model) {
    q.mu.Lock()
    if q.workerRunning { q.mu.Unlock(); return }
    q.workerRunning = true
    q.mu.Unlock()
    defer func() {
        q.mu.Lock()
        q.workerRunning = false
        q.mu.Unlock()
        mdl.invalidate()
    }()

    for {
        idx := q.nextReadyIndex()
        if idx == -1 {
            // stopped: flush remaining qReady to qCancelled, no spawn
            q.cancelRemainingReady()
            return
        }
        q.markRunning(idx)
        item := q.items[idx]
        if q.autoFollow { mdl.load(item.CuePath) }
        mdl.invalidate()

        out := strings.TrimSuffix(item.CuePath, filepath.Ext(item.CuePath)) + ".miniscram"
        args := []string{"pack", "--progress=json", item.CuePath}
        if !q.deleteScram { args = append(args, "--keep-source") }

        if err := mdl.runner.Start("pack", item.CuePath, out, args...); err != nil {
            q.markFailed(idx, err.Error())
            continue
        }
        res := <-mdl.runner.done
        q.recordResult(idx, res)
        eventInsert(mdl.db, queueEventRec(mdl, item, res))
        mdl.refreshStats()
        mdl.invalidate()
    }
}
```

`nextReadyIndex` returns -1 when `q.stopped` is set. `cancelRemainingReady`
flips every remaining `qReady` to `qCancelled` in one mutex-held pass.

### `done` channel ownership

Today the FrameEvent drain in `main.go:738–742` consumes
`mdl.runner.done` and calls `handleActionResult`. With a worker also
reading the channel, the two would race. Resolution: the FrameEvent
drain checks `mdl.queue.workerRunning` and *skips* the receive when
true. Single-file Pack/Verify/Unpack buttons are disabled during a
queue run, so no orphan results land in the channel from
non-queue activity.

### `queueEventRec` — shared with single-file path

`handleActionResult` in `main.go:363` already does the work of
turning an `actionResult` into an `eventRec` (inspect-JSON the new
miniscram, fill redump title, etc.). Extract that into
`buildEventRec(mdl, action, input, output, res actionResult) eventRec`
in a new helper used by both `handleActionResult` and the worker's
`queueEventRec`. Existing tests in `result_handler_test.go` pin the
current behavior; the helper makes both call sites share that
pinned logic.

**Toast is suppressed for queue items.** The single-file path sets
`mdl.toast` on success; the worker path does not. A 30-disc batch
popping thirty toasts would be noise — the queue panel is the
source of feedback. The toast resumes its normal behavior for
single-file actions taken between queue runs.

### Progress fraction from `--progress=json`

`reporter.go:132` defines the wire shape:

```go
type progressEvent struct {
    Type  string `json:"type"`     // "step" | "done" | "info" | "warn" | "fail"
    Label string `json:"label,omitempty"`
    Msg   string `json:"msg,omitempty"`
    Error string `json:"error,omitempty"`
}
```

The runner's stderr reader puts each NDJSON line into
`runningState.LastLine` as today. The queue layout *also* parses
each new `LastLine` as JSON; if it's a `step` event with a known
`Label`, the row's `Fraction` advances:

Real labels emitted by `pack.go` (lines 55–153, in order, some
conditional). One label — "resolving cue {path}" — has a variable
suffix, so the lookup matches by prefix:

```go
// in declared order; phase boundaries set the row's target fraction.
// Lookup is prefix-match: the runtime label can carry a variable suffix.
var packPhases = []struct {
    Prefix   string
    Fraction float64
}{
    {"resolving cue",                       0.02},
    {"detecting write offset",              0.05},
    {"checking constant offset",            0.08}, // conditional
    {"hashing tracks",                      0.15},
    {"hashing scram",                       0.30},
    {"building scram prediction + delta",   0.65},
    {"writing container",                   0.95},
}
```

Conditional phases (e.g. "checking constant offset" only fires on
some packs) just don't advance the fraction past their slot — the
next firing phase pulls it forward. On a `done` event for the final
phase ("writing container"), clamp to 1.0. On `fail`, freeze
fraction + paint the row red. Unknown label = leave fraction
unchanged (the running-strip already animates, so the row doesn't
need an idle pulse).

Initial fractions are informed guesses; refining them with real
timings against `test-discs/` is a plan-writing task.

A test `TestPackPhasesCoverage` runs miniscram pack against a
synthetic `packSyntheticContainer` fixture with `--progress=json` and
asserts every emitted `step` label is in `packPhases`. Catches
silent label-rename drift in `pack.go`.

### Drag-and-drop wiring

Gio's `gioui.org/io/transfer` package delivers drop events. The body
area registers a `transfer.TargetOp{Tag: queueDropTag, Type: "text/uri-list"}`
each frame. The FrameEvent loop drains
`transfer.DataEvent`s from the queue, reads each `data io.ReadCloser`,
splits on newlines, parses each line as a `file://` URI, decodes
percent-encoding via `net/url.QueryUnescape`, and calls
`q.addPaths(mdl, paths)`.

Linux X11 and Wayland both deliver `text/uri-list` natively; Gio
normalises the surface API. macOS and Windows use different MIME
types (`public.file-url`, `CF_HDROP`); this round registers
`text/uri-list` only and treats macOS/Windows drop as best-effort.

### Native pickers

Two new helpers in `main.go`, mirroring the existing `pickFile` /
`pickSave`:

- `pickFiles() ([]string, error)` — multi-select.
- `pickDir() (string, error)` — directory.

| Action | Linux (zenity) | Linux (kdialog) | macOS | Windows |
|---|---|---|---|---|
| Multi-select files | `--file-selection --multiple --separator='\n' --file-filter='cuesheets \| *.cue'` | `--getopenfilename '' '*.cue'` (single-select fallback — kdialog has no multi mode) | `osascript`: `choose file with multiple selections allowed of type {"cue"}` | PowerShell: `OpenFileDialog.Multiselect = $true` |
| Pick directory | `--file-selection --directory` | `--getexistingdirectory` | `osascript`: `choose folder` | PowerShell: `FolderBrowserDialog` |

Both feed into `q.addPaths(mdl, paths)`. Picker launch failure (no
zenity/kdialog/osascript) shows a transient error in the queue
header for ~5 s, same pattern as today's `pickFile`.

### Queue panel layout

Top-down inside the panel (fixed 280 dp wide):

1. Header: "Queue · N ready · M skipped" (text2, `unit.Sp(13)`).
2. Buttons row: `[+ Add files…] [+ Add directory…]`, surface2 background.
3. `☑ delete .scram after pack` checkbox.
4. Thin divider.
5. Items list (scrollable, `material.List`).
6. Thin divider.
7. `[Stop queue]` button (visible only when `workerRunning`).

Empty state: items area replaced by a centred dashed-border drop
target with copy "Drop cues or a folder here".

### Queue row anatomy (40 dp tall, full panel width)

| State | Visuals |
|---|---|
| `qReady` | small pending dot · basename · `[×]` remove button |
| `qRunning` | green fill at width = `panel_w * Fraction` painted *behind* the row · basename · `[⏹]` cancel-current |
| `qDone` | green ✓ · basename · duration ("5.4s") |
| `qFailed` | red ✗ · basename · "fail" tag · row tinted red |
| `qSkipped` | grey ⊘ · basename · reason at right (greyed) |
| `qCancelled` | grey ⊝ · basename |

Row click handler: load the cue into the right pane (existing
`mdl.load`). For the running row, also set `autoFollow = true`. For
any other row, `autoFollow = false`. The `×` (ready) and `⏹` (running)
buttons stop event propagation so they don't trigger the row click.

## Data flow

User drops a directory onto the window:

1. `transfer.DataEvent` fires; FrameEvent reads the body, parses
   URIs, calls `q.addPaths(mdl, paths)`.
2. `addPaths` resolves abs paths, walks dirs for `*.cue`, dedups, and
   classifies each cue (`qReady` / `qSkipped` with reason).
3. If `!q.workerRunning && q.hasReady()`, `go q.drain(mdl)`.
4. `drain` finds the next `qReady`, marks it `qRunning`, calls
   `mdl.load(item.CuePath)` if `autoFollow`, then
   `mdl.runner.Start("pack", input, output, "pack", "--progress=json", input, ...)`.
5. The runner's stderr reader streams NDJSON lines into
   `runningState.LastLine`; each `invalidate()` triggers a frame.
6. The queue layout, on each frame, parses `LastLine` as JSON; if
   it's a `step` event, looks up `packPhases[Label]` and updates
   the running row's `Fraction`.
7. On `cmd.Wait()` return, runner sends an `actionResult` on
   `runner.done`. The worker (which owns the channel during the
   queue run) receives, calls `q.recordResult(idx, res)` to flip the
   row to `qDone` / `qFailed` / `qCancelled`, writes an event row
   via `eventInsert`, and refreshes stats.
8. Worker advances. Loop until no `qReady` remains or `q.stopped`.
9. On worker exit, the FrameEvent drain resumes ownership of
   `runner.done` for any subsequent single-file action.

User clicks Stop queue mid-run:

1. `q.stopped = true`, `mdl.runner.Cancel()`.
2. Running row goes through the runner's cancellation path,
   `actionResult.Status = "cancelled"`, recorded as `qCancelled`.
3. Worker loops, `nextReadyIndex` returns -1, `cancelRemainingReady`
   flips every remaining `qReady` to `qCancelled` in one pass,
   worker exits.
4. Stats refresh shows one `pack cancelled` event row (the in-flight
   item only — the never-spawned items do not produce events).

## Error handling

| Scenario | Behavior |
|---|---|
| Drop non-cue, non-dir, non-miniscram file | Silently skipped in `addPaths`. |
| Drop a `.miniscram` (any queue state) | Loads into inspect (existing single-file flow). If a queue is running, `autoFollow` disengages — symmetric with clicking a non-running queue row. |
| Directory walk hits permission error on a subdir | `WalkDir` callback returns `filepath.SkipDir` for that subdir. |
| Directory walk finds zero cues | Header reads "Queue · 0 ready · 0 skipped"; rows area shows the empty drop-target. |
| Same cue dropped twice | Dedup by `filepath.Abs+Clean`; second drop is a no-op. |
| Picker fails to launch | Transient error in queue header (~5 s). |
| `runner.Start` returns `errAlreadyRunning` from worker | Mark item `qFailed reason="runner busy"` and continue. (Should not happen — worker is sole `Start` caller during queue run.) |
| miniscram subprocess unexpected exit | Runner classifies as `fail`, captures stderr tail; queue marks `qFailed` with that tail as `Reason`. |
| Window closed mid-queue | `app.DestroyEvent`: set `q.stopped = true`, `runner.Cancel()`, wait up to 5 s for in-flight exit. Pending items are lost (no persistence). |
| Failed item leaves a partial `.miniscram` on disk | The miniscram CLI cleans its own tempfiles on signal-death. For non-signal failures, we trust the CLI; if a stray output remains, that's a CLI bug. |
| User clicks `×` on a ready row while queue is running | Row removed; if it was the next-up, worker just picks the new next-up. |

## Concurrency

- `queueModel.mu` guards `items`, `stopped`, `workerRunning`, `autoFollow`.
- The UI goroutine takes a snapshot under the mutex per frame
  (similar to `actionRunner.Snapshot()`).
- The worker goroutine reads `mdl.runner.done` directly. The
  FrameEvent drain checks `q.workerRunning` and skips the receive
  when true. Buttons that call `runner.Start` (single-file Pack /
  Verify / Unpack) are disabled while `q.workerRunning`.
- `mdl.load` writes to model fields read by the UI goroutine; today
  it's already called from a goroutine (the `pickFile` handler at
  `main.go:752`). The auto-follow path follows the same pattern.
- `invalidate()` is goroutine-safe (Gio's `Window.Invalidate` is
  documented goroutine-safe).

## Testing

### Automated (`tools/miniscram-gui/queue_test.go`)

- `TestClassify` — cue with no scram → `qSkipped reason="no sibling .scram"`; cue with sibling miniscram → `qSkipped reason="already packed"`; cue with scram only → `qReady`.
- `TestWalkForCues` — synthetic temp tree with cues at multiple depths, dotfiles, dot-prefixed dirs. Returns absolute paths in deterministic (sorted) order.
- `TestAddPathsDedup` — same cue dropped twice, and `./a.cue` vs `./sub/../a.cue`, both collapse to one item.
- `TestDrainHappyPath` — 3 ready + 1 skipped, fake binary that exits 0 with `--progress=json` events for each pack phase. Asserts items end as `[done, done, done, skipped]`, fractions reach 1.0, three event rows written to DB.
- `TestStopQueue` — 5 ready items, stop after item 2 starts. Asserts `[done, cancelled, cancelled, cancelled, cancelled]`, exactly one `pack cancelled` event row written (the in-flight item only).
- `TestCancelCurrent` — 3 ready items, cancel current after item 1 starts. Asserts `[cancelled, done, done]`.
- `TestPackPhasesCoverage` — runs real miniscram pack against a `packSyntheticContainer` fixture with `--progress=json`, asserts every emitted `step.Label` matches a prefix in `packPhases`. Catches silent label-rename drift in `pack.go`.

### Manual smoke (Linux, against `test-discs/`)

- Drop `test-discs/` directory: queue populates with all cues, runs sequentially, each row's green fill advances through phases, panel header counts decrement.
- Drop a single cue while another is packing: appends to queue, doesn't disrupt right pane.
- Click a `done` row: right pane loads the freshly-built `.miniscram`.
- Click another row mid-pack: right pane switches, auto-follow disengages. Click the running row: auto-follow re-engages, right pane refreshes.
- Stop queue mid-pack: in-flight row shows "Cancelling…", everything below flips to `cancelled` after the in-flight subprocess exits. No stray `.miniscram` on disk for the cancelled item.
- Pick a directory via "Add directory…": same as drop.
- Pick multiple files via "Add files…": all queued, ready/skipped classification correct.
- Drop a `.miniscram`: loads into inspect (any queue state). Drop a `.miniscram` while a queue is running: loads into inspect *and* the in-flight item stops being auto-followed in the right pane.

### CI

Existing `gui` job (`go build .`, `go vet ./...`) gains `go test ./...`
which picks up `queue_test.go`. No new apt packages needed —
zenity/kdialog only execute at runtime, not in tests; `TestPackPhasesCoverage`
shells out to the locally-built `miniscram` binary, which CI already
produces.

## Out of scope (follow-up specs)

- Cross-platform drag (macOS/Windows). Linux-only this round.
- Queue persistence across GUI restart.
- Queue-wide Verify or Unpack actions.
- "Re-pack" affordance for `done` / `already packed` rows.
- Parallel queue execution (N workers).
- Draggable splitter for the panel width.
- Reorder pending rows (drag rows up/down).
- Inline expand of failed-row stderr tail (currently visible only in
  the Stats events table).
