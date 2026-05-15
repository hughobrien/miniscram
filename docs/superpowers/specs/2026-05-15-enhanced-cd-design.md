# Enhanced CDs: multi-session pack support, failing-test scaffold, GUI fail reason

Date: 2026-05-15

Tracks issue #44 ("GUI Issue: Packing failed" — a user-reported
failure to pack a 4-track Girls Aloud disc with three audio tracks
followed by a data track).

## Motivation

A user tried to pack an Enhanced CD (CD-Extra: audio tracks in
session 1, a single data track in session 2) and saw only a bare
`fail` tag in the GUI queue. The actual error message was captured
by the runner but never rendered.

Diagnosis: every Enhanced CD will fail today, regardless of audio
content. The root cause is that miniscram has no concept of
sessions. Redumper's cuesheet marks the session boundary with a
single `REM SESSION 02` line and nothing else; the inter-session
lead-out (≥6750 sectors) + lead-in (≥4500 sectors) + pregap (150
sectors) ≈ 11400 sectors of gap content is implicit in the
disc/scram layout but absent from the cue. `ParseCue` ignores all
`REM` lines, so `ResolveCue` places the data track's `FirstLBA` at
the cumulative sum of audio bin sizes — about 11400 sectors short
of where the data track actually sits in the scram. Every data
sector then mismatches the scram, blowing past the 5% layout
threshold, and `BuildEpsilonHat` aborts with a `LayoutMismatchError`.

This is not specifically an "audio-before-data" bug — it is a
multi-session bug. Plain mixed-mode discs that keep everything in
a single session (e.g. Half-Life) work today; the bin's track order
is irrelevant to single-session packing.

Three things are in scope:

1. Make the failure legible in the GUI.
2. Reproduce the failure in a synthetic test, so future work has a
   regression target on machines without a real Enhanced CD dump.
3. Teach miniscram to pack two-session discs whose session 2 holds
   a data track.

## Design

### 1. GUI: render the captured fail reason

`tools/miniscram-gui/queue_widget.go::queueRowSuffix` already
renders `it.Reason` for `qSkipped` rows. For `qFailed` rows it
hardcodes the literal `"fail"`. The Reason field is populated by
the runner (`runner.go::wait`, line 211) from
`state.LastLine`, which is the most recent stderr line from
`miniscram --progress=json`. That stream emits NDJSON `fail` events
whose `error` field carries the typed error from `pack.go`. The
data is already on the queue item; only the paint step is missing.

Change `qFailed`'s branch to render `it.Reason` when non-empty,
falling back to the literal `"fail"`. Keep the `bad` colour. No
truncation logic — typical error strings are short ("layout
mismatch: N/M sectors differ", "no plausible scrambled sync field
found"). If a real disc surfaces something pathologically long, a
follow-up can add ellipsis-with-tooltip; not in scope here.

### 2. Synthetic Enhanced CD fixture and failing test

Add a sibling to `synthDisc` in `fixtures_test.go`. Call it
`synthEnhancedCD(t, opts SynthEnhancedCDOpts)`. The existing
`synthDisc` carries enough audio/data ordering assumptions that
extending it with a multi-session flag would obscure both paths;
a sibling is cleaner.

Returned `SynthDisc` exposes Bin (concatenated), AudioBins (one
per audio track), Cue, Scram, LeadinLBA, plus a new
`ExpectedDataFirstLBA int32` so the post-fix assertion can pin it
without re-deriving the gap arithmetic in the test.

#### Cue layout

```
FILE "x (Track 1).bin" BINARY
  TRACK 01 AUDIO
    INDEX 01 00:00:00
FILE "x (Track 2).bin" BINARY
  TRACK 02 AUDIO
    INDEX 01 00:00:00
... (more audio tracks as opts request)
REM SESSION 02
FILE "x (Track N).bin" BINARY
  TRACK NN MODE1/2352
    INDEX 01 00:00:00
```

The `REM SESSION 02` line is what `ParseCue` must learn to detect
(see §3).

#### Scram layout (in LBA order)

| Region | Sectors | Content |
|---|---|---|
| Disc lead-in | 45000 (= -45150..-150) | All zeros |
| Track 1 pregap | 150 (= -150..0) | Silent audio (PCM zeros) — track 1 is audio, see §3.c |
| Audio track 1..N-1 | sum of audio sizes | PCM as-is from the audio bins |
| Session 1 lead-out | 6750 | `generateLeadoutSector(lba)` (Mode 0 scrambled zero, existing helper) |
| Session 2 lead-in | 4500 | All zeros |
| Session 2 pregap | 150 | `generateMode1ZeroSector(lba)` (Mode 1 scrambled zero, existing helper) |
| Data track | data size / 2352 | Output of `Scramble()` applied to the descrambled bin sectors |
| Trailing lead-out | small (configurable) | `generateLeadoutSector(lba)` |

Audio PCM is deterministic non-zero noise (the existing
`audioBins[a][j] = byte(j*3 + a*17)` pattern is fine — the only
constraint is no accidental `Sync` byte sequence, which that
pattern won't produce).

#### Test (`pack_test.go`)

Initial assertion against current main:

```go
err := Pack(opts, nil)
var lme *LayoutMismatchError
if !errors.As(err, &lme) {
    t.Fatalf("expected LayoutMismatchError, got %v", err)
}
if lme.MismatchRatio <= layoutMismatchAbortRatio {
    t.Fatalf("ratio %.3f should exceed abort threshold", lme.MismatchRatio)
}
```

This is *red* on main. It documents the bug and locks in a
regression target. Once §3 lands, flip the test to assert
`Pack` succeeds, the round-trip verifies, and the delta size
stays within a documented bound (the session lead-in's 4500 zero
sectors will already match the disc-leadin convention; only the
session lead-out and pregap should contribute overrides — a few
KiB at most).

### 3. Multi-session pack support

Three layers, smallest blast radius first.

#### 3.a. `cue.go` — surface session boundaries

Add to `Track`:

```go
Session int `json:"session,omitempty"`
```

Default 1. Serializes via `omitempty` so single-session manifests
stay byte-identical.

`ParseCue`'s line loop currently has:

```go
if line == "" || strings.HasPrefix(line, "REM ") || line == "REM" {
    continue
}
```

Replace with a small REM dispatcher: on `REM SESSION NN` (case-
insensitive token match, decimal NN), set a `currentSession` int
and stamp it on every subsequent `Track`. All other `REM` lines
keep the `continue` behaviour.

Validation: session numbers must be monotonically increasing (1,
2, …). A `REM SESSION 01` after we've already seen `REM SESSION
02` is malformed. Reject with a clear error.

#### 3.b. `pack.go` — detect gap, adjust FirstLBAs

After the existing `detectWriteOffset` and `checkConstantOffset`
steps, walk the resolved tracks. For each track `t` with
`t.Session > prev.Session`, scan the scram for the first scrambled
sync at or after the *naive* cumulative byte position
`(naiveFirstLBA - leadinLBA) * SectorSize + writeOffsetBytes`.
Reuse `validateSyncCandidate` unchanged. The actual LBA decoded
from that sync, minus the naive cumulative LBA, is `gapSectors`
for this boundary.

Add `gapSectors` to `t.FirstLBA` and to every later track's
`FirstLBA`. The session-2-first-track is now correctly positioned;
subsequent session-2 tracks inherit the same shift.

Bound check: `gapSectors` must satisfy
`11400 ≤ gapSectors ≤ 30000`. The lower bound is redumper's
sum-of-minima (6750 + 4500 + 150). The upper bound is a sanity
guard against scram corruption or detection errors; 30000 sectors
(~6:40 of disc time) sits comfortably above the minima while
still flagging a runaway detection.
Outside the bounds: return a typed error
(`ErrSessionGapOutOfRange`) with the detected value, so the GUI
can surface it.

Limitation (out of scope, documented): if the next session's first
track is `AUDIO`, there is no scrambled sync to lock onto, and
detection is ambiguous (audio false positives are not reliable
gap markers). Pack returns a typed error
(`ErrSessionFirstTrackNotData`) up front, before any detection
attempt. The synthetic test covers only the
`AUDIO… → REM SESSION 02 → DATA` shape.

#### 3.c. `builder.go` — emit gap content; fix audio-leading pregap

Two changes.

**Audio-leading pregap (independent micro-fix).** The current
pregap branch (line 168) emits `generateMode1ZeroSector(lba)`
unconditionally. For audio-leading discs the disc-actual pregap
is silent audio. Guard:

```go
case lba < p.BinFirstLBA:
    if p.Tracks[0].Mode == "AUDIO" {
        // silent audio pregap: sec stays all zeros
    } else {
        sec = generateMode1ZeroSector(lba)
    }
```

(`p.Tracks` is non-empty by construction — `tracks[0].FirstLBA`
seeds `BinFirstLBA` upstream — so no length guard.)

Avoids ~150 sectors of overrides per audio-leading disc. Trivial
and orthogonal to multi-session; ships in the same change because
the synthetic test exercises both.

**Inter-session gap.** Grow `BuildParams`:

```go
type SessionGap struct {
    StartLBA         int32 // first LBA of the gap (= session 1's last bin LBA + 1)
    LeadoutSectors   int32 // session 1 lead-out (Mode 0 scrambled zero)
    LeadinSectors    int32 // session 2 lead-in (zeros)
    PregapSectors    int32 // session 2 pregap (Mode 1 scrambled zero for data)
}

type BuildParams struct {
    // ... existing fields ...
    SessionGaps []SessionGap
}
```

For the standard CD-Extra layout the three sub-region sizes sum
to the detected `gapSectors`. We split using redumper's minima as
canonical values (150 pregap, 4500 leadin, remainder leadout), so
non-minimum discs put the slack into the leadout — that's
consistent with how real drives extend session 1's lead-out
rather than the lead-in or pregap.

In `BuildEpsilonHat`'s main loop, add a single classification
helper `regionAt(lba, gaps)` that returns one of
`{leadin, pregap, bin, gapLeadout, gapLeadin, gapPregap, leadout}`,
and branch on the result. The classifier scans `SessionGaps`
linearly (≤2 entries in practice). When `regionAt` returns a
gap sub-region, emit:

- `gapLeadout` → `generateLeadoutSector(lba)`
- `gapLeadin`  → zeros (no helper needed; default-zeroed `sec`)
- `gapPregap`  → `generateMode1ZeroSector(lba)`

The existing leadout/pregap helpers are reused — no new sector
generators. The bin reader is *not* advanced for gap LBAs (gaps
are not in the bin file).

#### Manifest changes

- `Track.Session int` (new, `omitempty`).
- `Track.FirstLBA` already carries the detected absolute LBA; the
  gap is implicit in the LBA jump between the last session-1
  track and the first session-2 track, so no separate gap field
  in the manifest.
- Unpack reconstructs the same `SessionGaps` from
  `tracks[i].FirstLBA - (tracks[i-1].FirstLBA + tracks[i-1].Size/SectorSize)`
  where `tracks[i].Session > tracks[i-1].Session`. Single helper
  `derivedSessionGaps(tracks)` shared between pack and unpack so
  the reconstruction is one place.

### Test coverage additions

- `cue_test.go`: `REM SESSION 02` stamps the subsequent track;
  non-monotone session numbers reject.
- `fixtures_test.go`: `synthEnhancedCD` helper.
- `pack_test.go`: red-then-green test described in §2; plus
  positive coverage that the detected `gapSectors` is within
  bounds, and that an audio-first session 2 returns
  `ErrSessionFirstTrackNotData`.
- `builder_test.go`: `regionAt` classifier table-driven test.

Property test candidate (memory[[feedback_property_tests]]):
`derivedSessionGaps` round-trips against `SessionGaps` →
`tracks[].FirstLBA` → `derivedSessionGaps` for random valid
inputs. Inclusion in v1 vs follow-up is the author's call.

## Scope boundary

In scope:
- GUI fail-row reason rendering.
- `synthEnhancedCD` fixture and initially-red test.
- `REM SESSION` parsing.
- Per-boundary gap detection from scram, with bounds check.
- Audio-leading pregap fix (silent audio rather than Mode 1 zero).
- `Track.Session` (manifest field, omitempty).

Out of scope (deferred):
- Discs with 3+ sessions. The data structures generalize but the
  test only covers two sessions.
- Audio-leading session 2.
- CD-i / Mode 2 mixed in session 2 — `validModes` already accepts
  `MODE2/2352`, so the path should work, but no synthetic test
  is added.
- Confirming round-trip against the real Girls Aloud fixture —
  the fixture isn't on this machine. Synthetic test is the proxy.

## Risks and open questions

- Detection relies on `validateSyncCandidate` finding a real
  scrambled sync past the audio region. Audio false positives
  are mathematically negligible (Sync = `00 FF FF FF FF FF FF FF
  FF FF FF 00`; silence is all zero; music is random-ish) but
  not strictly impossible. The 30000-sector upper bound is the
  backstop.
- The 30000-sector upper bound is a guess based on redumper's
  minima plus ~3 minutes of slack. Real CD-Extra discs in the
  wild may exceed it. Mitigation: the typed error includes the
  detected value, so a user hitting the cap can file a follow-up
  with the exact size and we can adjust.
- The "split the gap into leadout / leadin / pregap using
  redumper's minima with slack going to leadout" rule is a
  modelling choice that minimizes overrides for the *expected*
  redumper output. If a particular disc puts slack elsewhere
  (e.g. an unusually long lead-in), the difference becomes
  overrides — correct but larger delta. Acceptable.

## Suggested PR breakdown

Recommendation, not part of the design contract.

1. GUI fail-row reason (`queue_widget.go`) — independent.
2. `synthEnhancedCD` fixture + red regression test asserting
   `LayoutMismatchError`.
3. Multi-session pack support — turns the red test green.

Each PR is mergeable on its own; CI proves the diagnosis (PR 2 is
red on main, fixed by PR 3). Bundling into a single PR is fine
if review bandwidth prefers it.
