# Unused .scram cleanup after audio-only reject

Date: 2026-05-15
Depends on: [PR #51 / issue #50](https://github.com/hughobrien/miniscram/pull/51)

## Problem

Issue #50's fix rejects audio-only cuesheets with `ErrAudioOnlyDisc`
before any scram I/O. That stops the wasted minutes of scanning, but
the user is still left with a large, useless `.scram` file (765 MB
for the audio CD in the issue) — redumper writes one for every disc
regardless of whether scrambled data exists. Miniscram has no use for
it, the audio data lives in the `.bin` files alongside, and on the
user's filesystem that's effectively dead bytes.

We want to:

- Tell the user clearly that the `.scram` is unused, with its path
  and size.
- Offer a one-step removal in both the CLI (`--remove-unused-scram`
  opt-in flag) and the GUI (accumulator + bottom-of-queue button).
- Preserve the destructive-action asymmetry: deletion is opt-in here
  (no container to recover from), unlike the success path where
  deletion is the default (the container preserves the data).

## Goals

- Pack emits a structured `unused-scram` event before the
  `ErrAudioOnlyDisc` fail event, carrying the path and byte size.
- CLI grows a `--remove-unused-scram` flag that, when set, removes
  the `.scram` after Pack returns `ErrAudioOnlyDisc`.
- GUI accumulates `unused-scram` paths across the queue run and
  presents a single bottom-of-queue button to delete them all at
  once.

## Non-goals

- No prompt-on-stdin in the CLI (`--progress=json` consumers would
  break, and none of the rest of the CLI is interactive).
- No per-row delete buttons or per-failure toasts in the GUI — the
  user expressly wants a single batch action, not a question per
  disc.
- No persistence of the GUI accumulator across process restarts.
- No retry logic for failed deletions — the user gets a summary and
  can address remaining files manually.

## Design

### 1. Reporter: new `unused-scram` event

Add a method to the `Reporter` interface in `reporter.go`:

```go
// UnusedScram reports a source .scram file whose contents are
// useless to miniscram (today: only emitted on audio-only cues, ahead
// of the matching fail event). Carries the path and byte size so
// downstream consumers can offer cleanup.
UnusedScram(path string, size int64)
```

Three implementations:

- **`textReporter.UnusedScram`** — emits one line:
  `note: unused .scram at <path> (<size> bytes) — pass --remove-unused-scram to delete`.
  Raw byte count matches the existing reporter style (no MiB
  helpers in the root package).
- **`jsonReporter.UnusedScram`** — emits:
  `{"type":"unused-scram","path":"<abs>","size":<int>}`.
- **`quietReporter.UnusedScram`** — no-op.

`progressEvent` in `tools/miniscram-gui/queue.go` gains optional
`Path string` and `Size int64` fields (`omitempty` json tags) so the
GUI can decode the new event. The other event types continue to leave
those fields zero/empty.

`prettyProgressLine` does NOT need a new case — `unused-scram` is a
metadata event, not a progress event, and it should not appear in the
live progress strip. The GUI handles it through its NDJSON capture
loop (Section 3), not through `prettyProgressLine`.

### 2. Pack: emission point

In `pack.go::Pack`, the audio-only reject block (added by PR #51)
currently reads:

```go
if !anyDataTrack(tracks) {
    st = r.Step("checking disc type")
    st.Fail(ErrAudioOnlyDisc)
    return ErrAudioOnlyDisc
}
```

Extend it to stat the scram file and emit the new event before
failing. The scram path is already in `opts.ScramPath`; the size from
`os.Stat` (which would otherwise run a few lines later for a non-
audio-only disc):

```go
if !anyDataTrack(tracks) {
    if info, err := os.Stat(opts.ScramPath); err == nil {
        r.UnusedScram(opts.ScramPath, info.Size())
    }
    st = r.Step("checking disc type")
    st.Fail(ErrAudioOnlyDisc)
    return ErrAudioOnlyDisc
}
```

Stat failure is swallowed deliberately: if the scram file is missing
the reject still fires, just without a cleanup offer. This is safer
than failing the pack with a different error.

### 3. CLI flag: `--remove-unused-scram`

In `main.go::runPack`:

- Declare a new flag alongside `keep-source`:
  ```go
  fs.BoolVar(&removeUnused, "remove-unused-scram", false,
      "remove source .scram when miniscram has nothing to pack (audio-only cues)")
  ```
- After `Pack` returns:
  ```go
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
  ```

The check sits BEFORE the existing `if err != nil { return ... }`,
because the audio-only error is the trigger for the removal. The
process still exits non-zero (audio-only is a real failure for pack),
the removal just happens on the way out.

The help text for `pack` documents both flags so the asymmetry is
explicit:

```
  --keep-source              keep .scram after a successful pack
                             (default: delete on success)
  --remove-unused-scram      remove .scram when there's nothing to
                             pack (audio-only cues; default: keep)
```

### 4. GUI: accumulator + bottom-of-queue button

#### Model

`queueModel` in `tools/miniscram-gui/queue.go` gains:

```go
type unusedScram struct {
    Path string
    Size int64
}

unusedScrams []unusedScram   // accumulated; cleared on click or dismiss
```

Lock discipline: `unusedScrams` is read/written under the existing
`q.mu` so the model stays consistent under concurrent queue worker
writes and UI reads.

#### Capture

The existing NDJSON capture loop in `main.go` (around line 1210, the
`progressEvent` parsing for per-row progress) gains a branch:

```go
if ev.Type == "unused-scram" && ev.Path != "" {
    mdl.queue.appendUnusedScram(unusedScram{Path: ev.Path, Size: ev.Size})
}
```

`appendUnusedScram` deduplicates by `Path` — re-running a cue that
already produced an entry doesn't double-count. Pseudocode:

```go
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
```

The GUI passes NO new flag to the pack subprocess (it does not pass
`--remove-unused-scram`); the GUI takes responsibility for deletion
itself when the user clicks the button.

#### Render

A new widget `unusedScramBar` lives at the bottom of the queue panel,
inserted between the queue list and the existing "Stop queue" strip
(not replacing it). It renders only when `len(unusedScrams) > 0`.
Layout:

```
[ Delete N unused .scrams (X.X MiB) ]   [×]
```

- Primary button: full-width-ish, tinted with the existing `bad`
  colour (or `surface2` with `bad` text) to flag the destructive
  intent. Label uses `humanBytes` (the GUI-local helper) to format
  the total size.
- `×` dismisses without deleting (clears the slice).

The bar appears as soon as the first `unused-scram` event arrives
(during the run, not just at the end); count and size update as more
arrive.

#### Action

On primary-button click:

1. Snapshot the slice under `q.mu`.
2. Release the lock; iterate and `os.Remove` each path.
3. Collect failures; show a brief toast `"deleted N of M; X failed"`
   if any (otherwise no toast; the bar simply disappears).
4. Clear `q.unusedScrams` under `q.mu`.

Failed deletions are reported via the toast described above. Per-
failure detail (which path failed and why) is dropped on the floor in
V1; the user can re-attempt manually via `rm` if needed.

#### Lifecycle

Slice persists across queue runs in the same process. Cleared only
on click or `×`. Not persisted to disk; restarting the GUI loses the
list.

## Testing

### Unit

1. **`reporter_test.go`** — table-driven cases for the new method:
   - JSON reporter: feed `(path="/p", size=765077352)`, assert
     the emitted line is exactly `{"type":"unused-scram","path":"/p","size":765077352}`.
   - Text reporter: feed same, assert the line contains both
     `/p` and `765077352`.
   - Quiet reporter: feed same, assert no output.

2. **`pack_test.go`** — two new tests reusing the audio-only
   fixture pattern from `TestPackRejectsAudioOnlyCue`:
   - `TestPackEmitsUnusedScramEvent`: pass a capturing reporter
     (collect all events into a slice), call Pack with an audio-only
     cue + real `.scram` file. Assert exactly one `UnusedScram(path,
     size)` call with the expected size matching the file.
   - `TestPackUnusedScramStatFailureIsSwallowed`: same fixture but
     with a non-existent ScramPath. Assert the pack still returns
     `ErrAudioOnlyDisc` and the reporter saw zero `UnusedScram`
     calls.

3. **`cli_test.go` or `pack_test.go`** — two CLI-flag tests:
   - `TestCLIPack_RemoveUnusedScramFlagDeletesFile`: invoke `runPack`
     with `--remove-unused-scram` on an audio-only fixture; assert
     the `.scram` file is gone after the call AND exit code matches
     `ErrAudioOnlyDisc`'s expected exit.
   - `TestCLIPack_NoFlagKeepsFile`: same fixture without the flag;
     assert the `.scram` still exists.

4. **`tools/miniscram-gui/queue_test.go`** — pure-state tests:
   - `TestQueue_CapturesUnusedScramEvent`: feed an NDJSON line
     `{"type":"unused-scram","path":"/p","size":N}` through the
     parsing branch (extract the branch into a small helper if
     it's not already callable in isolation); assert the slice
     contains one entry.
   - `TestQueue_DedupesUnusedScramPaths`: feed the same event
     twice; assert one entry.
   - `TestQueue_DeleteUnusedScramsClears`: pre-populate the slice
     with two real temp files (`t.TempDir`); invoke the deletion
     handler; assert both removed and slice empty.
   - `TestQueue_DeleteUnusedScramsPartialFailure`: pre-populate
     with one real file plus one already-deleted path; invoke
     handler; assert real file is removed, the failure is recorded
     for surfacing, slice is cleared regardless.

### No e2e / property impact

The audio-only path is non-algorithmic; existing fixtures don't need
changes. The `-tags redump_data` suite has no audio-only fixture so
nothing to update there either.

## Migration notes

- No container-format change; the `unused-scram` event lives only in
  the `--progress=json` stream.
- No flag rename. `--keep-source` keeps its existing semantics.
- The GUI's accumulator is process-local; existing GUI users see the
  new bar appear only after they pack an audio-only cue with the new
  binary.

## Open questions

None — the four design sections were each approved during
brainstorming.
