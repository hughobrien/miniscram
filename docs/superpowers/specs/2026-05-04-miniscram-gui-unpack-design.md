# miniscram-gui: unpack flow + running-state strip

Date: 2026-05-04
Status: design
Issue: [#18](https://github.com/hughobrien/miniscram/issues/18)
Affects: `tools/miniscram-gui/` (own go.mod)

## Motivation

The Gio prototype (PR #21) ships Pack, Verify, and Inspect but Unpack
is a stub: clicking it shows a "not wired" dialog. Closing that gap
needs three things at once:

1. The unpack itself — a save dialog for the output path, a
   subprocess spawn, and an event row recording the result.
2. A running-state UI element. Pack is ~5 s on Half-Life and ~6 s on
   Freelancer; unpack will be the same order of magnitude (it
   re-builds the scram from bin + delta). Today's synchronous handlers
   freeze the window for that whole duration. With a third action
   joining, that's no longer acceptable.
3. A success affordance. After writing 800 MiB of `.scram`, the user
   wants to see it landed.

This spec covers all three because they're tightly coupled — the
running-state widget is shared by Pack/Verify/Unpack, and the
success toast is shared too. Unpack on its own without the strip
would just compound the freeze problem.

## Non-goals

- A `--progress=json` flag in the miniscram CLI. The strip parses
  the existing stderr step lines (`hashing scram …`, `building scram
  prediction …`, `applying delta …`, `verifying scram hashes …`) for
  this round. If line-tail proves jittery in practice,
  `--progress=json` is a clean follow-up spec.
- Cross-platform release wiring. The unpack code paths are written
  to work on Linux/macOS/Windows but only tested on Linux this round.
- Drag-and-drop input. Separate sub-feature.
- Changing miniscram CLI behavior. The GUI is a strict CLI consumer.

## Architecture

A new `actionRunner` runs `miniscram pack|unpack|verify` as a
subprocess on a goroutine and streams stderr line-by-line to the
model via a mutex-protected `runningState` struct. While the runner
is active, the window renders a `runningStrip` widget at the
window-frame level (not inside `body()`), so it stays visible across
`Inspect` ↔ `Stats` view switches.

When the subprocess exits the runner fires an `onDone(actionResult)`
callback that writes an event row to the events table, refreshes the
stats aggregate, and — on success — populates `model.toast` for ~6 s.

Cancellation sends `SIGTERM` via `cmd.Process.Signal`. The miniscram
CLI already cleans its own tempfiles on signal-death (per the
existing pack/verify behavior in `pack.go` and `verify.go`), so the
GUI does no cleanup of its own. A cancelled action records as a
third event status, `cancelled`, alongside `success` and `fail`.

The runner is single-flight: `Start` refuses if `state != nil`.
Disabled action buttons are the cosmetic enforcement; the refusal is
the safety net.

## Components

### New file `tools/miniscram-gui/runner.go`

Three pieces, all package-internal:

```go
type actionRunner struct {
    mu        sync.Mutex
    cmd       *exec.Cmd
    state     *runningState
    onDone    func(actionResult)
    invalidate func()
}

type runningState struct {
    Action     string    // "pack" | "unpack" | "verify"
    Input      string
    Output     string    // optional; for pack/unpack the destination
    StartedAt  time.Time
    LastLine   string    // most recent non-empty stderr line
    Cancelling bool      // set between SIGTERM and process exit
}

type actionResult struct {
    Action     string
    Input, Output string
    DurationMs int64
    Status     string    // "success" | "fail" | "cancelled"
    Error      string    // tail of stderr on fail
    OutputSize int64     // for the toast
}
```

Methods:

- `Start(action, input, output string, args ...string)` — spins up
  `exec.Command("miniscram", action, input, args...)`, attaches a
  stderr reader goroutine, calls `cmd.Start()`, then a wait
  goroutine. Single-flight refusal returns silently (button-disable
  prevents this in normal use).
- `Cancel()` — sets `state.Cancelling = true`, sends SIGTERM. No-op
  if not running.
- `Snapshot() *runningState` — UI takes a copy under the mutex per
  frame.

The stderr reader uses `bufio.Scanner`; on each non-empty line, it
trims, sets `state.LastLine`, and calls `invalidate()`. Default
buffer size (64 KiB) is fine for miniscram's short status lines.

### `runningStrip` widget (in `runner.go`)

Layout: small spinner (animated dot, `time.Tick`-driven), action
verb + input basename, `state.LastLine` in muted mono, elapsed time
on the right, `Cancel` button. When `state.Cancelling`, the Cancel
button disables and the strip shows "Cancelling…".

On a failed/cancelled action the strip stays visible (red accent on
the spinner area, normal text otherwise), shows the stderr tail or
"Cancelled by user", with a `Dismiss` button instead of `Cancel`.
This dismisses by setting `model.runner = nil` and clearing any
pending result-display state.

### `toast` widget (in `runner.go`)

Bottom-attached strip. Layout: action verb + output basename + size
(`humanBytes(OutputSize)`) + duration + `Reveal in folder` button +
`✕` dismiss. Self-dismisses on a `time.AfterFunc(6 * time.Second,
w.Invalidate)` schedule; the layout function checks elapsed and
returns zero dimensions when expired.

### `pickSave` in `tools/miniscram-gui/main.go`

Companion to existing `pickFile`. Native save dialog with overwrite
confirmation handled by the OS:

| OS      | Command                                                          |
|---------|------------------------------------------------------------------|
| Linux   | `zenity --file-selection --save --confirm-overwrite --filename` |
| macOS   | `osascript -e 'POSIX path of (choose file name default name … default location …)'` |
| Windows | `powershell` with `System.Windows.Forms.SaveFileDialog`          |

Default filename: `<basename>.scram` (where basename is the loaded
miniscram's stem). Default directory: directory of the loaded
miniscram.

### `revealInFolder(path string)`

`xdg-open <dirname>` / `open <dirname>` / `explorer <dirname>` per
platform. Best-effort, swallows errors silently (same pattern as
existing `openURL`).

### `main.go` wiring changes

- `model` gains `runner *actionRunner` and `toast *toastState` fields.
- Pack/Unpack/Verify click handlers replaced:
  - **Pack**: `runner.Start("pack", mdl.path, outMiniscramPath, args...)` where args is `[]string{}` if checkbox checked, else `[]string{"--keep-source"}`.
  - **Unpack**: first `pickSave()`; if path returned, refuse if it
    equals `mdl.path`; else `runner.Start("unpack", mdl.path, savePath, "-o", savePath)`.
  - **Verify**: `runner.Start("verify", mdl.path, "")`.
- All three buttons disable when `runner.state != nil`.
- `runningStrip` rendered between the top-bar divider and the body
  Flexed region. Renders zero height when `runner.state == nil`.
- `toast` rendered just above the footer divider. Zero height when
  `mdl.toast == nil`.
- `onDone` callback (set when the runner is created), runs on the
  wait goroutine (not the UI goroutine — `eventInsert` is fine to
  call there, modernc.org/sqlite is goroutine-safe): writes an event
  row, calls `mdl.refreshStats()` unconditionally so the next
  Stats-tab open shows fresh numbers, and on success sets
  `mdl.toast = &toastState{...; ExpiresAt: now + 6s}`.

### `db.go`

No schema change. The `events.action` column already accepts an
arbitrary text value; `unpack` rows just become a new value alongside
`pack` and `verify`. The chip palette in `cellAction` already styles
`unpack` (light blue), unchanged.

### Event status enum

Today the `events.status` column holds `success` or `fail`. This
spec adds `cancelled`. Touch points:

- `actionRunner.wait` writes `cancelled` when `state.Cancelling`.
- `eventRow` in the stats view's chip palette gains a `cancelled`
  branch (mid-grey + dimmed text).
- `eventsAggregate` SQL filters `status = 'success'` for "best
  ratio" / "bytes saved" / "override total" — unchanged, since
  cancelled rows aren't success.
- `seed.go` doesn't need a cancelled fixture (one row would be a
  curiosity, not a representative seed).

## Data flow

User clicks Unpack…:

1. `pickSave()` runs in a goroutine; if user cancels the dialog,
   no-op.
2. Sanity check: if returned path == `mdl.path` (the source
   miniscram), set a transient strip-style error and return.
3. `mdl.runner.Start("unpack", mdl.path, savePath, "-o", savePath)`.
4. Stderr reader goroutine streams lines into `state.LastLine`,
   calls `invalidate()` on each.
5. UI redraws the strip each frame, showing the current step.
6. On `cmd.Wait()` return:
   - On `state.Cancelling`: `actionResult.Status = "cancelled"`.
   - On non-zero exit: `Status = "fail"`, `Error = state.LastLine`
     (miniscram errors land on the final stderr line, so the
     line-tail captures them naturally).
   - On success: `Status = "success"`, `OutputSize = os.Stat(output).Size()`
     when `output != ""` (verify has no output to size).
7. `onDone` writes the event, refreshes stats, sets `mdl.toast` (on
   success) or leaves the strip up in red (on fail/cancelled).

Pack and Verify follow the same flow with different command-line
shapes.

## Error handling

| Scenario                                  | Behavior                                                                 |
|-------------------------------------------|--------------------------------------------------------------------------|
| Save dialog cancelled                     | No-op.                                                                   |
| Save path == source `.miniscram`          | Refuse before spawning, surface as a strip error.                        |
| Save dialog returns non-`.scram` extension | Allow it. The user explicitly chose that name.                          |
| `cmd.Start()` fails (binary missing)      | `Status="fail"`, `Error=err.Error()`, strip turns red.                  |
| Subprocess exits non-zero                 | `Status="fail"`, `Error` is the tail of stderr.                          |
| User clicks Cancel mid-flight             | SIGTERM, wait, record `cancelled`. CLI cleans its own tempfiles.        |
| User clicks Cancel after process exited   | No-op (runner state is nil by then).                                     |
| Window closed while action runs           | `app.DestroyEvent` handler calls `runner.Cancel()` then `cmd.Wait` with a 5 s budget; orphan-prevention. |
| Toast Reveal-in-folder fails              | Silent. Same pattern as `openURL`.                                       |
| New action started while toast is up      | Toast cleared immediately.                                                |

## Concurrency

- `runningState` is read by the UI goroutine and written by the
  stderr-reader goroutine and the wait goroutine. Single mutex on
  `actionRunner`; UI takes a `Snapshot` (deep copy) per frame.
- `Start` and `Cancel` take the mutex; both serialize on it.
- `invalidate()` is a function captured at runner construction time;
  calling it from any goroutine is safe (Gio's `Window.Invalidate` is
  documented goroutine-safe).
- The `time.AfterFunc` for toast expiration calls `invalidate()`; the
  layout function does the actual zero-height render based on
  `time.Now() > toast.ExpiresAt`.

## Testing

### Automated

`tools/miniscram-gui/runner_test.go` — unit tests for `actionRunner`
using a fake binary built into the test helper (a small Go program
written via `t.TempDir()` + `go build`):

- **happy path**: fake prints 3 stderr lines on a 100 ms cadence,
  exits 0. Asserts `state.LastLine` updates, `actionResult.Status ==
  "success"`, no goroutine leak (verified via `runtime.NumGoroutine`
  before/after).
- **non-zero exit**: fake prints stderr, exits 1. Asserts
  `Status == "fail"`, `Error` equals the last stderr line.
- **SIGTERM mid-flight**: fake sleeps 5 s, test calls `Cancel()`
  after 100 ms. Asserts `Status == "cancelled"`, wait returns within
  1 s.
- **single-flight**: two consecutive `Start()` calls; second is a
  no-op while first runs.

The widgets (`runningStrip`, `toast`) and `pickSave` are not
unit-tested in this round. Gio render testing is high-effort,
low-yield, and the smoke tests below cover regression.

### Manual smoke

Against `test-discs/` fixtures:

- **Unpack DeusEx**: click Unpack, accept default save path. Strip
  shows step lines, completes in ~5 s, toast appears with size ≈
  856 MiB, "Reveal in folder" opens the test-discs/deus-ex
  directory. Stats gains an `unpack success` row.
- **Cancel mid-Pack**: click Pack on a freshly-cleaned half-life cue
  (delete the `.miniscram`, `.scram` still present). Click Cancel
  after ~2 s. Strip shows "Cancelling…" then "Cancelled". No
  `.miniscram` file left on disk. Stats gains an `pack cancelled`
  row.
- **Pack failure**: pack a cue with the sibling `.scram` removed.
  Strip turns red, shows the miniscram error (e.g., "scram not
  found"). Stats gains a `pack fail` row.
- **Window-close during action**: start a pack, close the GUI
  window. The miniscram subprocess is no longer present in `ps`
  within ~5 s.

### CI

Existing `gui` job (added in PR #21) builds with `go build .` and
`go vet ./...`. Adding `go test ./...` to it picks up the new
runner_test. No new apt packages needed for the runner test (uses
the same Go toolchain).

## Out of scope (follow-up specs)

- `miniscram --progress=json`: the line-tail approach lands first;
  if it's noisy, the CLI gains a structured progress emitter and the
  strip switches to consume it.
- "Recent files" in the empty state: pulls from the events DB.
  Independent of this work.
- Click-to-expand in the stats events table to reveal the error
  tail. Independent.
- Drag-and-drop input. Independent.
- Cross-platform release wiring. Builds first, ships later.
