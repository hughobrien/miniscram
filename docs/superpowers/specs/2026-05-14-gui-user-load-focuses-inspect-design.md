# GUI: user-initiated file loads pivot to Inspect

Date: 2026-05-14

Closes: hughobrien/miniscram#43

## Motivation

Issue #43 reports that the sidebar in the GUI appears non-interactive
on the Stats tab: rows respond to clicks on Inspect but "nothing
happens" on Stats. The sidebar is in fact clickable on Stats — the
click is fully wired and `mdl.load(it.CuePath)` runs — but the right
pane stays on the Stats view, which renders only the global
aggregate from `mdl.stats` and ignores `m.path`. The load is
invisible.

Auditing the GUI for the same shape (user does a thing, `load()`
fires, view doesn't move) turns up three sites that share the bug:

1. `main.go:1287` — sidebar row click.
2. `main.go:1190` — Open button → file picker → `load()`.
3. `queue.go:172` — `addPaths` loads the dropped/picked single
   `.miniscram` into Inspect. Reachable from the window's
   drag-drop handler (`main.go:1128`) and from the AddFiles queue
   button's picker (`main.go:1255`).

These are the only sites in the codebase where `load()` runs in
direct response to an explicit user action ("I want to look at
this file"). Worker-driven sites — queue auto-follow, single-file
post-pack reload, startup `-load` flag — also call `load()` but
deliberately must NOT pivot the view, since the user may have
gone to Stats on purpose and the worker should not yank them off.

## Design

Introduce a thin helper in `tools/miniscram-gui/main.go`:

```go
func (m *model) loadAndFocus(p string) {
    m.view = "file"
    m.load(p)
}
```

The view assignment is unconditional. From Inspect it is a no-op;
from Stats it pivots. There is no third state.

Three call sites switch from `load` to `loadAndFocus`:

- `main.go:1288` — sidebar row click handler.
- `main.go:1190` — Open button, inside the file-pick goroutine,
  after `pickFile()` returns a non-empty path. If the user cancels
  the picker (`p == ""`), `loadAndFocus` is never called and the
  view stays where it was. The existing synchronous
  `autoFollow = false` write at `main.go:1182-1184` is unchanged —
  that one fires on the click itself, since clicking Open is on
  its own a "user took control" signal regardless of whether they
  follow through with a file.
- `queue.go:172` — the post-`addPaths` `.miniscram` load.

All other callers of `load()` remain on `load()`:

- `queue.go:363`, `queue.go:402` — queue worker auto-follow and
  post-pack output load.
- `main.go:605` — single-file post-pack auto-reload in
  `handleActionResult`.
- `main.go:932` — startup `-load` flag.

### Concurrency

`m.view` is currently written only from the UI goroutine (the tab
buttons at `main.go:1171-1177`). Of the three new write sites:

- `main.go:1288` (sidebar row click) — always on the UI goroutine.
- `main.go:1190` (Open button) — inside the picker goroutine spawned
  by `go func() { p, err := pickFile(); ... }()`.
- `queue.go:172` (post-`addPaths` load) — runs on whichever goroutine
  called `addPaths`. Callers: drag-drop (`main.go:1128`, UI goroutine);
  AddFiles button (`main.go:1255`, inside a `go func() { paths, err :=
  pickFiles(); ... }()` picker goroutine); AddDir
  (`main.go:1267`, never reaches the `.miniscram` branch because
  directories produce only `.cue` paths via `walkForCues`).

So `m.view` gains two new write goroutines: the Open picker and the
AddFiles picker. Both already call `load()` and freely mutate `m.path`,
`m.kind`, `m.meta` from there. Adding `m.view` to that set introduces
no new race class — the existing convention is that `load()` and the
UI handler-loop never run concurrently for the same model.

### Why a helper, not inlining

The view-switch could be inlined at each of the three sites. A
helper is preferred because:

1. The set of "user-explicit load" sites is fixed by the model's
   handler topology. Naming the operation makes the intent legible
   at the call site (`mdl.loadAndFocus(p)` reads as "user wants
   to see this") and makes the worker/explicit split self-documenting.
2. If a future site is added, picking the right helper is a smaller
   ask of the author than remembering to also set `m.view`.

## Tests

The fix has one programmatic invariant and one wiring invariant.
The first goes into Go tests; the second is checked by manual
repro because the wiring lives in the Gio `Window.NextEvent` loop,
which has no headless harness in this repo and isn't worth
introducing one for a two-line fix.

### Programmatic — `tools/miniscram-gui/load_focus_test.go` (new)

Each test constructs a `*model` directly (the same pattern used by
the existing `queue_test.go` and `result_handler_test.go`), uses a
trivial .cue fixture written to `t.TempDir()` so `load("…cue")`
succeeds without execing the miniscram CLI, and asserts the view
invariant.

- **`TestLoadAndFocus_FromStats_PivotsToInspect`** — set
  `m.view = "stats"`, call `m.loadAndFocus(cuePath)`, assert
  `m.view == "file"`.
- **`TestLoadAndFocus_FromInspect_StaysOnInspect`** — set
  `m.view = "file"`, call `m.loadAndFocus(cuePath)`, assert
  `m.view == "file"` (idempotence).
- **`TestLoad_FromStats_DoesNotPivot`** — set `m.view = "stats"`,
  call `m.load(cuePath)`, assert `m.view == "stats"`. Locks in
  the worker-path invariant: bare `load` must never yank the
  view. Regression guard for anyone tempted to "just fold the
  switch into load."
- **`TestLoad_FromInspect_StaysOnInspect`** — set `m.view = "file"`,
  call `m.load(cuePath)`, assert `m.view == "file"`. Symmetric
  guard; together with the above, pins down that `load` is
  view-neutral.

### Manual repro checklist (PR description, not committed)

Build the GUI with `CC=/usr/bin/clang CGO_ENABLED=1 go build` and
run with a populated queue. For each row, switch to the named
starting tab, perform the action, observe the result.

| # | Action | Starting tab | Expected after |
|---|---|---|---|
| 1 | Click a sidebar row | Stats  | Inspect, loaded file |
| 2 | Click a sidebar row | Inspect | Inspect, loaded file |
| 3 | Open button, pick a .cue or .miniscram | Stats | Inspect, loaded file |
| 4 | Open button, cancel the picker | Stats | Stats (unchanged) |
| 5 | Drag-drop a single .miniscram onto the window | Stats | Inspect, loaded file |
| 6 | AddFiles, pick a single .miniscram | Stats | Inspect, loaded file |
| 7 | Drag-drop one or more .cue files | Stats | Stats; queue panel shows new items |
| 8 | AddDir of a folder of .cue | Stats | Stats; queue panel shows new items |
| 9 | Queue worker auto-advances to next item | Stats | Stats; queue panel + events refresh |
| 10 | Pack completes (single-file flow) | Stats | Stats; events table refreshes |
| 11 | Verify / Unpack / Pack button click | Stats | Stats; runner strip shows progress globally |

Rows 1–6 are the bug-class cases. Rows 7–11 are the
worker-driven / non-loading cases that must NOT pivot.

## Out of scope

- Adding a Gio headless test harness for the event-loop wiring.
  Out of proportion to the fix size; the four model-level tests
  pin the invariant and the manual checklist covers the wiring.
- Restructuring the Stats view to use the selected sidebar item
  (option C from the brainstorm — "filter Stats by selection").
  Different feature; reconsider if a user asks for per-file
  stats.
- Removing the sidebar from Stats entirely (option B). The
  sidebar's queue-status display is still useful on Stats, and
  the pivot makes the interactive part work as a user would
  expect.

## Risk

- **`loadAndFocus` write to `m.view` from the Open-button
  goroutine.** New write site for `m.view` outside the main UI
  handler loop. Same goroutine already mutates `m.path`,
  `m.kind`, `m.meta`, `m.cueText` etc. via `load()`, so this is
  not a new race class — but the existing convention is implicit.
  If future work runs the UI handler concurrently with `load()`,
  the `m.view` field would join the others as a flagged race
  site. Mitigation: none beyond keeping the existing convention
  documented in `load()`'s preamble.
- **Behavior change on `view == "file"` when row-click happens
  from Inspect.** Currently `m.view` is never re-assigned in this
  case; after the fix it is reassigned to its current value. No
  observable effect; flagged for completeness.
