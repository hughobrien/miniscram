# GUI: `load(.cue)` redirects to the packed `.miniscram` when scram is gone

Date: 2026-05-14

## Motivation

After a batch pack with the default `delete-scram=true` flow, each
cue's sibling `.scram` is consumed and a `.miniscram` is written.
The queue rows still track the cue's path. Clicking a row reloads
the `.cue`, `cueView` checks for the sibling `.scram`, doesn't find
it, and renders:

> Missing .scram next to cue — pack can't run

This is technically accurate — pack really cannot run again from a
state with no `.scram`. But the user just packed it: the result is
sitting next to the cue as a `.miniscram`. The warning is noisy
and the useful view (track listing, redump matches, container
stats) is one click away that the user has to manually take by
clicking the `.miniscram` somewhere else.

The fix: in `load(p)` when `p` is a `.cue`, if there is no usable
sibling `.scram` but there is a sibling `.miniscram`, redirect to
`load(miniscramPath)`. The cue view is only the right answer when
`.scram` is actually there (re-pack is possible) or when neither
is there (orphaned cue — the warning is still correct).

## Design

In `tools/miniscram-gui/main.go::load`, inside the `case ".cue":`
branch, before any existing cue-loading work, insert the redirect:

```go
case ".cue":
    base := strings.TrimSuffix(p, filepath.Ext(p))
    scramPath := base + ".scram"
    miniscramPath := base + ".miniscram"
    scramOK := false
    if st, err := os.Stat(scramPath); err == nil && st.Size() > 0 {
        scramOK = true
    }
    if !scramOK {
        if _, err := os.Stat(miniscramPath); err == nil {
            m.load(miniscramPath)
            return
        }
    }
    // existing .cue load below — unchanged
```

The `st.Size() > 0` test mirrors `cueView`'s check at
`main.go:1503` — an empty `.scram` is treated as missing. The two
spots must agree: anywhere `cueView` would render the "missing"
warning, `load` redirects first so the warning never appears.

### Behavior matrix

| sibling `.scram` | sibling `.miniscram` | result |
|---|---|---|
| present, non-empty | either | cue view (existing — Ready to pack / repack) |
| absent or empty | present | miniscram view (the fix) |
| absent or empty | absent | cue view with current "missing scram" warning |

### Why recursion vs inlining the miniscram load

`load(miniscramPath)` already covers ~30 lines of CLI-exec,
JSON-parse, on-disk-size capture, and redump-lookup spawning.
Calling `m.load(miniscramPath); return` reuses that path verbatim.
The outer `load()` reset block at the top runs twice — a few
assignments to nil/empty — but the cost is negligible and the
control flow stays trivially readable.

### Composition with other recent changes

- **`loadAndFocus` (issue #43).** Reachable via the row-click and
  Open-button paths. `loadAndFocus(p)` sets `m.view = "file"` and
  calls `load(p)`. The new redirect inside `load(p)` is invisible
  to `loadAndFocus` — `m.view` is already on the correct side.
- **`bin_hash_cache`.** Only the cue path runs `hashCueBins`. When
  the redirect fires we skip the cue path entirely, so the cache
  is not consulted. That's correct: the `.miniscram` view reads
  hashes directly from inspect-JSON (which include them), not via
  bin streaming.

### Worker / non-user-explicit callers

- `queue.go:363` (worker auto-follow when advancing to next ready
  item) calls `load(cuePath)` for an item that was classified as
  `qReady`. `qReady` already requires a usable `.scram`, so the
  redirect does not trigger. If the `.scram` were externally
  deleted between queue-add and worker-pick, the pack would fail
  regardless; redirecting to a stray `.miniscram` is unusual but
  not wrong. Acceptable edge.
- `queue.go:402` (worker post-pack reload) calls `load(out)` where
  `out` is the `.miniscram`. Routes through the `.miniscram` branch
  directly — no interaction with the new code.
- `main.go:605` (single-file post-pack reload in
  `handleActionResult`) — same: loads the `.miniscram` output
  directly.
- `main.go:932` (startup `-load` flag) — if the user starts the
  GUI with `-load some.cue` and the cue is already packed
  (.scram gone, .miniscram present), the redirect fires and
  lands them on the inspect view. That seems right: the user
  asked for "this disc," the inspect view is more useful.

## Tests

A new test file `tools/miniscram-gui/load_cue_redirect_test.go`
(or appended to an existing test file — the implementer's call)
with four behavior-pinning tests. All four use the existing
`newTestModel(t)` helper and write fixture files into `t.TempDir()`.

- **`TestLoad_CueWithMissingScram_RedirectsToMiniscram`** — write
  `disc.cue` (empty or trivial) and `disc.miniscram` (any bytes),
  no `.scram`. Call `m.load(cuePath)`. Assert `m.path ==
  miniscramPath`. The recursive `load(.miniscram)` will fail the
  inspect-CLI exec in the test env (`m.cliBinary == ""`); that's
  expected and not under test. The observable redirect is `m.path`
  mutating to the miniscram path.
- **`TestLoad_CueWithEmptyScram_RedirectsToMiniscram`** — write
  zero-byte `.scram`, present `.miniscram`, `.cue`. Same assertion.
  Pins the "size > 0" rule against accidental relaxation.
- **`TestLoad_CueWithScramAndMiniscram_StaysOnCue`** — write all
  three (non-empty `.scram`, present `.miniscram`, `.cue`). Call
  `m.load(cuePath)`. Assert `m.kind == "cue"` and `m.path ==
  cuePath`. Pins the "both present → cue" rule (keep-source flow).
- **`TestLoad_CueWithNoScramAndNoMiniscram_StaysOnCue`** — only
  the `.cue`. Assert `m.kind == "cue"` and `m.path == cuePath`.
  Pins the orphaned-cue case — current "missing scram" warning
  must still render.

### Cleanup of `hashCueBins` background goroutine in tests

`load(.cue)` ends with `go m.hashCueBins(...)`. The "stays on cue"
tests trigger this. With empty/trivial `.cue` content,
`parseCueLines` returns zero tracks, the job loop in `hashCueBins`
finds no eligible files, the wait-group is empty, and the
goroutine returns near-instantly. No special teardown needed.
The redirect tests skip `hashCueBins` entirely because the
recursive `load(.miniscram)` takes the `.miniscram` branch.

## Out of scope

- Adding a "back to cue" affordance in the miniscram view. The
  miniscram view already shows the per-track listing and inspect
  metadata; the cue text is not useful to surface as a separate
  panel.
- Removing the "missing scram" warning entirely. It's still the
  right message for orphaned cues with no packed sibling.
- Detecting whether a `.miniscram` was actually produced from the
  sibling `.cue` (vs a stray same-basename file). Cross-validation
  would require parsing both containers; not worth it for an edge
  case that's the user's own naming fault.
- Aligning load() behavior with `classify`. `classify` (`queue.go`)
  uses the same logic to mark items as `qSkipped/"already packed"`.
  We do not yet plumb that classification into the click handler;
  load()'s own check is the single source of truth for the
  redirect.

## Risk

- **Stray `.miniscram` with matching basename.** Redirects to an
  unrelated container. Symptom: the inspect view shows tracks
  that don't match the cue. The user can drop the actual cue
  elsewhere or rename the stray file. Not a correctness hazard
  for any normal workflow.
- **Recursion depth.** Exactly one level — `load(.cue) → load(
  .miniscram)`. The `.miniscram` branch does not call back into
  load(). Safe.
