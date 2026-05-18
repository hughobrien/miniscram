# GUI: fix text wrapping and unused-scram button visibility

## Problem

Issue #54 reports three related GUI problems after the audio-only disc reject
flow (#50/#51):

1. **Text truncation instead of wrapping.** The `reasonLabel` helper forces
   `MaxLines = 1` on `qFailed` rows. Error messages such as
   `"cue contains only AUDIO tracks; nothing for miniscram to scramble-pack"`
   are ellipsized to a single line instead of wrapping.

2. **"Delete N unused .scrams" button never appears.** When `Pack` hits the
   audio-only short-circuit it emits two NDJSON stderr lines in quick
   succession:
   - `{"type":"unused-scram","path":"...","size":...}`
   - `{"type":"fail","label":"checking disc type","error":"..."}`

   `runner.go`'s `readStderr` overwrites `state.LastLine` with each new line.
   By the time the frame handler runs `json.Unmarshal(rs.LastLine)`, it sees
   the `fail` event — not `unused-scram` — so `appendUnusedScram` is never
   called and `unusedScramBar` stays hidden.

3. **Delete button label omits context.** The label
   `"Delete N unused .scram(s) (X bytes)"` doesn't explain that these are
   audio-track scrams that miniscram can't use.

## Design

### 1. `runner.go` — accumulate `unused-scram` events in `readStderr`

**Current behaviour:** `readStderr` unconditionally updates
`state.LastLine`, which functions as a one-line tail. Any NDJSON line that
isn't the last one is silently lost.

**New behaviour:** After updating `LastLine`, `readStderr` attempts to parse
the line as NDJSON. If `type == "unused-scram"` and `path != ""`, the event
is appended to a new `pendingScrams []unusedScram` field on `runningState`.

Two drain paths ensure the event reaches the queue model:

- **Live drain** — a new `(*actionRunner).DrainUnusedScrams() []unusedScram`
  method atomically swaps out `pendingScrams` and returns the accumulated
  entries. Called from the frame-handler loop alongside the existing
  `rs.Snapshot()` block.

- **Safety-net drain** — `actionResult` gains an `UnusedScrams` field. In
  `wait()`, before `r.state = nil`, the pending scrams are copied into the
  result. `handleActionResult` appends them. This catches the case where the
  process exits before the frame handler ran a drain cycle.

**Locking:** `pendingScrams` sits under the same `r.mu` that protects
`state`. Both `DrainUnusedScrams` and `readStderr`'s accumulation path lock
the mutex.

### 2. `queue_widget.go/queue_widget_test.go` — replace truncation with wrapping

**Current:** `reasonLabel` sets `MaxLines = 1` for `qFailed`.

**Change:** Remove `MaxLines = 1`; set `WrapPolicy = text.WrapGraphemes`
instead. This wraps at grapheme-cluster boundaries, so long error messages
break cleanly without towering (word-boundary wrapping on continuous strings
like filenames or "no plausible scrambled sync field found in … bytes of scram"
produces no breaks at all, causing overflow).

**Test update:** `TestReasonLabel_FailedSingleLine` becomes
`TestReasonLabel_FailedWrapsAtGraphemes` — asserts `MaxLines == 0`
(no cap) and `WrapPolicy == text.WrapGraphemes`.

### 3. `queue_widget.go` — update delete button label

**Current:** `"Delete N unused .scram(s) (X bytes)"`

**New:** `"Delete N unused .scram(s) (audio, X bytes)"`

The `(audio)` qualifier tells the user these are from audio-only tracks and
safe to delete. The total character count stays close — 4 extra characters —
so the label still fits the 280 dp panel.

### 4. `main.go` — drain scrams in frame handler + handleActionResult

**Frame handler** (~line 1214 today): after the existing `Snapshot()` +
`json.Unmarshal` block, call `mdl.runner.DrainUnusedScrams()` and append
each entry to `mdl.queue`.

**handleActionResult** (~line 1243): after the switch on `res.Status`,
iterate `res.UnusedScrams` and call `mdl.queue.appendUnusedScram`.

## Files touched

| File | Change |
|------|--------|
| `tools/miniscram-gui/runner.go` | Add `pendingScrams`, parse NDJSON in `readStderr`, expose `DrainUnusedScrams()`, carry through `actionResult` |
| `tools/miniscram-gui/runner_test.go` | New test: `TestReadStderrAccumulatesUnusedScram` |
| `tools/miniscram-gui/queue_widget.go` | `reasonLabel`: `MaxLines=1` → `WrapPolicy=WrapGraphemes`; `unusedScramBar`: label text |
| `tools/miniscram-gui/queue_widget_test.go` | Update reason-label test |
| `tools/miniscram-gui/main.go` | Drain scrams in frame handler + handleActionResult |

## Not in scope

- The `unusedScramBar` already supports singular/plural label branching.
  No change needed.
- The dismiss (×) button remains unchanged — it clears the accumulator
  without deleting.
- No changes to the CLI or reporter packages — the NDJSON wire format is
  correct; the bug was only in the GUI runner's one-line tail.
