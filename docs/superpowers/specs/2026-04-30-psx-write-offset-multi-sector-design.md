# PSX support: handle write offsets larger than one sector

Date: 2026-04-30

## Motivation

A redumper-corpus sweep of `miniscram pack` over /roms (119 CD
dumps) identified one panic class affecting all 8 PSX dumps:

```
panic: runtime error: slice bounds out of range [2588:2352]
main.BuildEpsilonHat (builder.go:193)
```

`SLUS-01251` (Final Fantasy IX disc 1) has detected
`WriteOffsetBytes = -2588`. `builder.go:138-140` sets
`skipFirst = -WriteOffsetBytes = 2588`. The per-sector loop at
`builder.go:191-195` does:

```go
secBytes := sec[:]
if skipFirst > 0 {
    secBytes = secBytes[skipFirst:]
    skipFirst = 0
}
```

A single sector is `SectorSize = 2352` bytes. With `skipFirst > 2352`
the slice expression panics.

The sync-detection code at `pack.go:308` already accepts offsets up
to `±writeOffsetLimit = 2 * SectorSize = 4704` bytes. So the
intended capability is "handle offsets up to two whole sectors,"
but the BuildEpsilonHat skipFirst logic was written assuming
`|offset| < SectorSize`. PSX dumps frequently fall outside that
narrower range because of how PSX masters were pressed — the user
framed this fix as "proper PSX support" because the offset
characteristic is what blocks the platform end-to-end.

Affected discs in the user's corpus:

| Disc | Offset (bytes) |
| --- | --- |
| Final Fantasy VIII (4 discs, SLUS-00892/00908/00909/00910) | varies, all > 2352 |
| Final Fantasy IX (4 discs, SLUS-01251/01295/01296/01297) | -2588 |

All other supported aspects of PSX dumps (MODE2/2352 cuesheets, the
mode-1 zero-sector generator, the classifier-gated scrambler) are
already in place.

## Design

### Fix

In `BuildEpsilonHat`'s per-sector loop, drain any whole sectors of
`skipFirst` before applying the partial-sector slice. Replace the
existing block at `builder.go:191-195`:

```go
secBytes := sec[:]
if skipFirst > 0 {
    secBytes = secBytes[skipFirst:]
    skipFirst = 0
}
```

with:

```go
secBytes := sec[:]
if skipFirst >= len(sec) {
    skipFirst -= len(sec)
    continue
}
if skipFirst > 0 {
    secBytes = secBytes[skipFirst:]
    skipFirst = 0
}
```

### Why `continue` is safe

The post-skip body of the loop performs three things:

1. `out.Write(secBytes)` and `written += len(secBytes)`. With
   `secBytes` empty (whole-sector skip), this contributes nothing,
   so `written` should not advance — `continue` skips it. Correct.
2. `advanceScram(hatStart)` and `io.ReadFull(scram, scramBuf[:N])`.
   The caller's scram reader is in lockstep with the output so far;
   if we wrote nothing this iteration, we should read nothing from
   scram. `continue` skips both calls. Correct.
3. The mismatch-run state machine. Runs are local to the output
   bytestream; with no output bytes, no run state can change.
   Correct.

The `bin` reader is consumed *before* the skipFirst check
(`io.ReadFull(bin, binBuf)` at line 171), so the bin position stays
correct across whole-sector skips. The skipped sectors at the start
of the disc are leadin/pregap (zeros) for any realistic offset
within ±4704 bytes — the bin range starts well after the leadin —
but the code is correct even for hypothetical bin-overlap cases.

### Maximum iteration count

`writeOffsetLimit = 2 * SectorSize`, so `skipFirst <= 4704`. The
`continue` branch runs at most twice per pack invocation.

## Tests

### Example tests in `builder_test.go`

Extend the existing offset-test table. Each row uses the
`SynthOpts` / `packSyntheticContainer` helpers (per
`project_helpers.md` memory) to build a small synthetic scram and
asserts byte-exact round-trip plus zero panic.

- **`mode1-neg-offset-one-sector`**: offset `-SectorSize`
  (`-2352`). The boundary where `skipFirst == len(sec)` triggers
  the new `continue` branch on the very first iteration.
- **`mode1-neg-offset-multi-sector`**: offset `-2588` (the FF IX
  SLUS-01251 value). Skips one whole sector, then 236 bytes.
- **`mode1-neg-offset-max`**: offset `-4704` (`-2 * SectorSize`).
  Skips two whole sectors. Edge of `writeOffsetLimit`.
- **`mode1-pos-offset-multi-sector`**: offset `+2588`. Locks down
  that the positive path (which uses `out.Write(zeros)` of size
  `WriteOffsetBytes`) also handles offsets > sector size. Already
  works today; this is a regression guard.

### Property test

`TestBuildEpsilonHatNoPanicAcrossOffsetRange`: for each offset in
`{-4704, -3000, -2353, -2352, -2351, -1000, -48, 0, 48, 1000, 2351, 2352, 2353, 3000, 4704}`,
build a small synthetic scram (~100 sectors) and assert:

- `BuildEpsilonHat` returns without panic
- output size equals `ScramSize`

This catches future regressions in either direction (someone
tightening the boundary back to `< SectorSize` or someone widening
`writeOffsetLimit` without updating the loop).

### End-to-end smoke verification

After the unit tests pass, the implementer runs:

```bash
go build -o /tmp/miniscram .
rm -rf /tmp/repro && mkdir -p /tmp/repro
for f in /roms/final-fantasy-viii/SLUS-00892/*; do ln -s "$f" /tmp/repro/; done
/tmp/miniscram pack /tmp/repro/SLUS-00892.cue -o /tmp/repro/out.miniscram --keep-source 2>&1 | tail -10
echo "EXIT=${PIPESTATUS[0]}"
rm -rf /tmp/repro
```

Expected: `verifying scram hashes ... OK all three match`, exit 0.
Records the wall time and delta size in the PR description.

This is a one-shot validation, not a committed fixture. The dump is
~700 MB and lives in the user's RO SMB mount — not bundled with
the repo.

## Out of scope

- Extending `writeOffsetLimit` beyond ±2 sectors. Existing limit is
  intentional and aligned with sync-detection.
- Adding a `test-discs/<psx>/` fixture row to
  `e2e_redump_test.go`. Out-of-band; can be done in a follow-up if
  the user wants regression coverage with a real PSX dump.
- Subchannel data, libcrypt, or other PSX-specific protections
  noted in README's "not preserved" list. Same scope as before:
  miniscram preserves main-channel data only.

## Risk

- **Regression in the boundary case `skipFirst == 2352`.** The new
  `>= len(sec)` check fires there; the old code would have done
  `secBytes[2352:]` which is a valid empty slice. Both paths
  produce zero output for that sector, but the new path does it via
  `continue` rather than a no-op write. The example test
  `mode1-neg-offset-one-sector` exists specifically to guard this
  boundary.
- **Mismatch-run continuity.** A whole-sector skip at the start of
  the loop happens before any output, so no in-progress mismatch
  run can exist when `continue` fires. Verified by inspection of
  the loop structure: `run` is only appended-to inside the
  scram-comparison block which is guarded by `len(secBytes) > 0`
  (effectively, since the for-i loop has zero iterations when
  `secBytes` is empty).
