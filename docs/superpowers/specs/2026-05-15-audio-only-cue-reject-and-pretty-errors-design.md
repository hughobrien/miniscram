# Audio-only cue rejection + pretty GUI error reasons

Date: 2026-05-15
Issue: [#50](https://github.com/hughobrien/miniscram/issues/50)

## Problem

GitHub issue #50 reports a pack failure on an audio-only CD with the
text:

```
AudioCD.cue{"type":"fail","label":"detecting write offset","error":
"no plausible scrambled sync field found in 765077352 bytes of scram"}
```

The screenshot shows the raw NDJSON blob rendered in the GUI sidebar's
failed-row column, wrapping over six lines in red.

Two distinct defects feed this:

1. **Correctness.** Miniscram's container format only stores deltas
   against the descrambled prediction. Audio tracks are unscrambled
   PCM and have no sync field; `detectWriteOffset` will *always* fail
   on a cuesheet whose tracks are all `AUDIO`. The user wastes minutes
   of I/O on a doomed pack. Hugh's own comment on the issue confirms
   this: "scrambling only applies to data tracks, so if you have an
   audio only CD there's nothing for this tool to work with."
2. **Display.** The GUI runs the subprocess with `--progress=json` and
   stores the most recent stderr line in `runningState.LastLine`. On
   failure, `runner.go` copies that raw NDJSON line directly into
   `actionResult.Error`, which then feeds both `toastState.FailMsg` and
   `queueItem.Reason`. The pretty-line helper (`prettyProgressLine`) is
   applied to the running strip but not to the post-failure surfaces.
   Any pack failure today renders as raw JSON, not just this one.

## Goals

- Reject audio-only cuesheets at the top of `Pack` with a clear
  sentinel error, before any I/O on the scram file.
- Make GUI failure surfaces (toast + queue row) display the
  human-readable error text, never the raw NDJSON event.
- Cap the sidebar reason column to a single line with ellipsis so
  long-but-plain error messages don't tower over the rest of the
  queue.

## Non-goals

- Detecting audio-only discs in the GUI's pre-pack inspect view (e.g.
  greying out the Pack button or labelling the cue as unsupported on
  load). The Pack-time reject is the single source of truth; if Hugh
  wants pre-pack UX later it can be a follow-up.
- Reworking the whole `prettyProgressLine` rendering layer. The fix is
  to apply the existing helper at the failure site.
- Any e2e (`-tags redump_data`) coverage — the audio-only path is a
  pure-cue check and never touches a real scram file.

## Design

### 1. `pack.go` — reject audio-only cuesheets

Add a new sentinel near the existing exported errors at the top of
`pack.go`:

```go
// ErrAudioOnlyDisc means the cuesheet has zero data tracks (every
// TRACK is AUDIO). Miniscram only packs scrambled data tracks; on an
// audio-only disc there is nothing to do, and detectWriteOffset would
// otherwise spin through the entire scram file before failing with
// "no plausible scrambled sync field found".
var ErrAudioOnlyDisc = errors.New(
    "cue contains only AUDIO tracks; nothing for miniscram to scramble-pack")
```

In `Pack`, immediately after the existing "resolving cue" step closes
(`st.Done("%d track(s), %d bytes total", …)` around `pack.go:87`) and
before the scram `os.Stat`, insert:

```go
if !anyDataTrack(tracks) {
    st = r.Step("checking disc type")
    st.Fail(ErrAudioOnlyDisc)
    return ErrAudioOnlyDisc
}
```

Add helper near the existing track utilities (probably `cue.go` next
to `Track.IsData`):

```go
func anyDataTrack(tracks []Track) bool {
    for _, t := range tracks {
        if t.IsData() {
            return true
        }
    }
    return false
}
```

`Track.IsData()` already returns `t.Mode != "AUDIO"`, so this reuses
the established predicate. Emitting a dedicated step (`"checking disc
type"`) keeps the `--progress=json` stream coherent — the failure
event has a labelled step rather than appearing untethered.

CLI behaviour: `miniscram pack AudioCD.cue` now exits non-zero with the
sentinel message on stderr. GUI behaviour: the NDJSON `fail` event
carries `error: "cue contains only AUDIO tracks; ..."`, which after
section 2 renders cleanly in the toast and sidebar.

### 2. `runner.go` — pretty failure reason

In `actionRunner.wait` (`tools/miniscram-gui/runner.go`), the
fail-classification block currently reads:

```go
case err != nil:
    res.Status = "fail"
    res.Error = state.LastLine
    if res.Error == "" {
        res.Error = err.Error()
    }
```

Change to:

```go
case err != nil:
    res.Status = "fail"
    res.Error = prettyProgressLine(state.LastLine)
    if res.Error == "" {
        res.Error = err.Error()
    }
```

`prettyProgressLine` already lives in `queue.go` and is already
exported within the package. It handles:

- `{"type":"fail","error":"X"}` → `"X"` (the common pack-failure
  case)
- `{"type":"fail","label":"X"}` (no error field) → `"X failed"`
- non-JSON → returned unchanged

Because `prettyProgressLine("")` returns `""`, the empty-string
fallback to `err.Error()` keeps working.

Both downstream consumers — `toastState.FailMsg` set in
`startActionOrSurfaceFailure` and `queueItem.Reason` set in
`recordResult` — read `res.Error` and automatically inherit the clean
text. No changes are needed in `widgets.go`, `queue_widget.go`, or
`main.go`.

### 3. `queue_widget.go` — single-line reason

In `queueRowSuffix`, the `qFailed` branch currently builds:

```go
lb := material.Label(th, unit.Sp(11), label)
lb.Color = col
return lb.Layout(gtx)
```

After Section 2, even the cleaned-up text can still be long (e.g. *no
plausible scrambled sync field found in 765077352 bytes of scram* ≈
60 chars), and the narrow sidebar will wrap multi-line. Force
single-line + ellipsis only for the `qFailed` state:

```go
lb := material.Label(th, unit.Sp(11), label)
lb.Color = col
if it.State == qFailed {
    lb.MaxLines = 1 // Truncator defaults to "…"
}
return lb.Layout(gtx)
```

(`material.LabelStyle.MaxLines` and `Truncator` exist in
`gioui.org@v0.9.0/widget/material/label.go`; default `Truncator` is
already `"…"` so we only need to set `MaxLines`.)

The full text remains accessible via:

- The bottom toast (which is wider and isn't capped).
- The events history table (which already stores `ev.Error` and
  renders it on its own row).

Other terminal states (`qDone`, `qSkipped`, `qCancelled`) keep current
behaviour to avoid touching cells that are already short and tidy.

## Testing

### Unit

- `pack_test.go` (or wherever `Pack` failure-mode tests live; will
  confirm during planning): add a case that constructs a synthetic
  resolved cue with all-AUDIO tracks and calls `Pack`. Assert
  `errors.Is(err, ErrAudioOnlyDisc)`. The scram file is never opened
  so the test needs only a placeholder path (or `os.DevNull`).

- `runner_test.go:130` (existing fail-path test): change the fake
  subprocess's last stderr line to a real NDJSON event
  (`{"type":"fail","label":"detecting write offset","error":"X"}`)
  and assert `res.Error == "X"`. Add a sibling case where the last
  line is non-JSON (e.g. a plain runtime panic string) to confirm the
  passthrough still works.

- `queue_widget_test.go` (new or extend existing if present): build a
  `qFailed` `queueItem` with a 200-character `Reason`, render
  `queueRowSuffix`, and either (a) assert the resulting label style
  has `MaxLines == 1` via a small testing seam, or (b) snapshot a
  golden image and assert row height is bounded. Pick (a) if the
  package has no existing image-golden harness; otherwise mirror the
  existing style.

### Property test opportunity

None obvious — these are display/policy changes, not algorithmic. The
[[feedback_property_tests]] memory still applies elsewhere (e.g. the
delta codec) but doesn't fit this work.

### No e2e impact

The `-tags redump_data` suite runs against data-bearing discs; none of
the fixtures are audio-only. No fixture changes needed.

## Migration notes

- The error is a fresh sentinel, not a wire-format change, so no
  container compatibility concerns.
- `--progress=json` output gains one new `step` label
  (`"checking disc type"`); the `packPhases` table in
  `tools/miniscram-gui/queue.go` doesn't strictly need an entry (the
  step is short-lived and only emits when the disc is rejected
  anyway), but adding one near `resolving cue` (e.g. 0.03) keeps the
  progress bar monotone if anyone watches it on a failed pack.

## Open questions

None — sections 1–4 above are settled per the brainstorming session.
