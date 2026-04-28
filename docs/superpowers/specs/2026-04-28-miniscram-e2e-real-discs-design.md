# miniscram e2e — real-disc fixture suite

A table-driven `redump_data`-tagged end-to-end test that exercises
miniscram against real Redumper dumps. Replaces the pair of hand-written
`TestE2EDeusEx` / `TestEDCAndECCAgainstDeusEx` tests in
`e2e_redump_test.go` with a single set of dataset-driven tests that
skip cleanly when their data isn't present.

This corresponds to TASKS.md item **B1** (real protected-disc test
against the SafeDisc/LibCrypt-class case) and partial **B3** (audio-
track exercise — see "Out of scope" for what's deferred).

## Goals

- Exercise miniscram's pack-then-unpack-then-byte-compare round trip
  against at least one *real* protected disc (Freelancer, SafeDisc
  2.70.030, 588 known intentional error sectors). v0.2 only had this
  property tested via synthetic fixtures with hand-flipped bytes.
- Keep the existing clean-disc baseline (Deus Ex) so any regression
  on the optimal-case "delta is a few hundred bytes" property gets
  caught.
- Lay infrastructure that future fixtures (notably HL1 multi-track
  with audio, deferred to a later cycle — see below) can be added to
  by appending one struct literal.

## Non-goals

- Multi-FILE `.cue` support. HL1's Redumper output stores per-track
  `.bin` files (`HALFLIFE (Track 01).bin` … `(Track 28).bin`); the
  current `cue.go` deliberately ignores `FILE` lines (cue.go:56) and
  `Pack` takes a single `BinPath`. HL1 cannot be exercised end-to-end
  until that's fixed. Documented in Out of scope; flagged as the next
  prerequisite for completing B3.
- SafeDisc-specific descrambling spot-checks ("look for hidden string"
  Rune-style). Byte-equal round trip already proves losslessness; an
  EDC-corruption preservation check would just reassert that, through
  a longer path.
- Cross-platform anything. The test paths are absolute Linux paths
  rooted under `/data/roms/redumper/...` (user's local layout).
  C3 covers cross-platform CI separately.

## Dataset locations and metadata

| Dataset | Dir | Stem | Tracks | Mode | Errors | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| Deus Ex | `/data/roms/redumper/deus-ex` | `DeusEx_v1002f` | 1 | MODE1/2352 | 0 | clean baseline |
| Freelancer | `/data/roms/redumper/freelancer` | `FL_v1` | 1 | MODE1/2352 | 588 | SafeDisc 2.70.030 |
| Half-Life | `/data/roms/redumper/half-life` | `HALFLIFE` | 28 | MODE1/2352 + 27 AUDIO | 0 | **deferred** — see Out of scope |

The user's actual filesystem may symlink or remount these under
`/home/hugh/miniscram/<dir>/`; the test uses the same convention as
the existing `e2e_redump_test.go` (an absolute constant per dataset).
If a dataset's `.scram` doesn't exist at the configured path, the
sub-test calls `t.Skipf` and proceeds. CI on a machine without the
data simply runs zero of these tests.

## Architecture

A single test file `e2e_redump_test.go` (rewritten in place) with the
`//go:build redump_data` tag and three top-level test functions:

1. **`TestE2ERoundTripRealDiscs`** — table-driven. For each dataset:
   skip if absent; pack into a temp dir on the dataset's filesystem;
   re-read the manifest; assert per-dataset bounds (error count, max
   delta size, max container size); unpack; byte-compare recovered
   `.scram` against original.
2. **`TestE2EEDCAndECCRealDiscs`** — sanity check that miniscram's
   `ComputeEDC`/`ComputeECC` agree with the actual stored EDC/ECC bytes
   in real Redumper bins. Samples four Mode-1 LBAs known to be
   protection-free for each dataset. Skips per-dataset on absent data.
3. **`filesEqual`** — kept as-is, package-level helper used by (1).

The dataset definition struct lives in the same file:

```go
type realDiscFixture struct {
    Name              string  // sub-test name, e.g. "deus-ex"
    Dir               string  // absolute path to the dataset directory
    Stem              string  // filename stem (no extension)
    ExpectedErrors    int32   // assert manifest.ErrorSectorCount == this
    MaxDeltaBytes     int64   // assert manifest.DeltaSize < this
    MaxContainerBytes int64   // assert os.Stat(container).Size() < this
    EDCSampleLBAs     []int64 // LBAs to sample in TestE2EEDCAndECCRealDiscs (must be Mode 1, no protection)
}

var realDiscFixtures = []realDiscFixture{
    {
        Name:              "deus-ex",
        Dir:               "/data/roms/redumper/deus-ex",
        Stem:              "DeusEx_v1002f",
        ExpectedErrors:    0,
        MaxDeltaBytes:     1024,
        MaxContainerBytes: 2048,
        EDCSampleLBAs:     []int64{0, 100, 1000, 100000},
    },
    {
        Name:              "freelancer",
        Dir:               "/data/roms/redumper/freelancer",
        Stem:              "FL_v1",
        ExpectedErrors:    588,
        MaxDeltaBytes:     5 * 1024 * 1024, // generous; adjust after first run
        MaxContainerBytes: 5 * 1024 * 1024,
        EDCSampleLBAs:     []int64{0, 100, 1000, 100000}, // SafeDisc errors cluster near end-of-disc
    },
}
```

The HL1 row is **not** in the initial slice — it cannot run until
multi-FILE `.cue` support exists. When that lands, HL1 is added by
appending one struct literal — no infrastructure change.

## Round-trip test details

For each fixture the sub-test does:

1. `t.Skipf` if any of `<Dir>/<Stem>.bin`, `<Dir>/<Stem>.cue`, or
   `<Dir>/<Stem>.scram` is missing. Both
   `TestE2ERoundTripRealDiscs/<name>` and
   `TestE2EEDCAndECCRealDiscs/<name>` skip independently — neither
   blocks the other, neither blocks the other fixtures. Skipping the
   whole trio (vs. just `.scram`) catches partial datasets at a
   single explicit point rather than letting `Pack` fail with a
   confusing message later.
2. `os.MkdirTemp(<Dir>, "miniscram-e2e-*")` to keep the ~scram-sized
   recovered file on the same filesystem as the dataset. The existing
   test established this convention to avoid filling `/tmp`.
3. `Pack(PackOptions{BinPath, CuePath, ScramPath, OutputPath, Verify:true}, NewReporter(io.Discard, true))` (matches the existing test's reporter pattern).
   `Verify:true` runs the post-pack round-trip check that v0.2 added,
   so even before the test's own unpack we've proved byte-equality
   internally. That's intentional belt-and-braces.
4. `ReadContainer(containerPath)` to extract the manifest.
5. Three assertions:
   - `m.ErrorSectorCount == fixture.ExpectedErrors`
   - `m.DeltaSize < fixture.MaxDeltaBytes`
   - `os.Stat(containerPath).Size() < fixture.MaxContainerBytes`
6. `Unpack(...)` to a separate path.
7. `filesEqual(recovered, original)` — must be true.
8. `t.Cleanup(func() { os.RemoveAll(tmp) })` removes the test's temp
   directory after the sub-test completes.

The bound thresholds for Freelancer (5 MB delta, 5 MB container) are
deliberately generous; we don't know the exact post-pack delta until
the first run. The plan's "verify" step will record the actual values
and we can tighten the bounds in a follow-up commit if desired. A
loose bound that catches "miniscram suddenly emits a 100 MB delta"
regressions is enough.

## EDC/ECC sanity test details

For each fixture's `EDCSampleLBAs`, read the sector from `<Dir>/<Stem>.bin`
and verify:

- Stored bytes [2064:2068] equal `ComputeEDC(sec[:2064])`.
- After zeroing bytes [2076:SectorSize] and calling `ComputeECC`, the
  result equals the originally-stored bytes [2076:SectorSize].

This is identical to the v0.2-era `TestEDCAndECCAgainstDeusEx`. It is
a sanity check on the *bin format*, not on miniscram's pack/unpack
flow. Sample LBAs must be in Mode-1 data tracks and free of protection
errors — for Deus Ex any LBA is fine; for Freelancer the four sampled
LBAs (0, 100, 1000, 100000) are well below the SafeDisc protection
zone which clusters near end-of-disc. The choice is best-guess; if
a future first run of the test reveals one of these LBAs has been
deliberately corrupted (which would be unusual SafeDisc behavior),
swap it for a known-clean value and note the variance — the failure
itself is informative.

If any of these assertions fail, the failure is in the dataset (or
in EDC/ECC themselves), not in miniscram's e2e behavior. Keeping it
separate from the round-trip test makes diagnosis cleaner.

## Errors / behavior

- Test absent dataset → `t.Skipf`, exit OK from the test runner's
  perspective.
- `Pack` returns `LayoutMismatchError` → test fails with the error's
  built-in detailed message. (For Freelancer this would mean miniscram's
  protection-aware path is broken; the goal of B1.)
- Round-trip byte-mismatch → `t.Fatal("recovered .scram differs from
  original")` — same wording as the v0.2 test.
- Bound-overrun (delta or container size) → `t.Errorf` (not Fatal),
  so the test continues to do the unpack and byte-compare. A regression
  that bloats the delta but still round-trips correctly should surface
  both the bloat *and* the still-passing round-trip in one run.

## Wiring

Replaces the existing `e2e_redump_test.go` entirely. Build tag stays
`redump_data`. No new files; no changes outside this single test file.
Helpers `filesEqual` and the in-test repetition of EDC/ECC math stay
in this file (they are not used elsewhere).

`go test -tags redump_data ./...` runs the suite. Without the tag (the
default), the file is excluded from the build entirely.

## Acceptance criteria

Mapping to TASKS.md B1:

- [ ] At least one real Redumper dump of a copy-protected disc is
      tested end-to-end (Freelancer, SafeDisc 2.70.030).
- [ ] An e2e test packs and unpacks the protected disc; round-trip
      is byte-equal.
- [ ] `manifest.error_sector_count` is non-zero and matches the disc's
      known intentional-error count (588).
- [ ] (Spot-check requirement waived per design discussion: byte-equal
      round-trip already implies preservation of every byte including
      deliberately-corrupted EDC.)

Mapping to TASKS.md B3 (partial):

- [ ] Synthetic multi-track + audio fixture in `builder_test.go` —
      **deferred until multi-FILE .cue support exists** (real-disc HL1
      cannot run end-to-end without it; synthetic-only multi-track was
      already the v0.2-era `TestPackCleanDisc` family and is not
      meaningfully extended by this cycle).

Mapping to clean-disc regression coverage (carried forward from v0.2):

- [ ] Deus Ex round-trip is byte-equal.
- [ ] Deus Ex delta < 1 KB and container < 2 KB (proves the
      smarter-builder property hasn't regressed).

## Testing the test

The `redump_data` tag gates the entire file, so day-to-day `go test
./...` runs zero of these. The user's manual smoke test:

```
go test -tags redump_data -run TestE2ERoundTripRealDiscs -v ./...
```

When all three datasets are present, expect three sub-tests to run.
When (as currently) only Freelancer is present, expect two skips and
one running test.

## File/LOC summary

| File | Action | Approx LOC |
| --- | --- | --- |
| `e2e_redump_test.go` | rewrite | ~200 (was 139) |

Net: ~+60 LOC. The growth is the dataset-config struct + the
table-driven loop + the new Freelancer assertions.

## Out of scope (deferred to later items)

- **Multi-FILE `.cue` support.** Required for HL1 (and any other
  multi-track Redumper dump that uses one `.bin` per track). Should
  become a new TASKS.md item — natural location is alongside B3 since
  it's the prerequisite for finishing B3. Estimated ~150–200 LOC across
  `cue.go` (parse `FILE` lines), `pack.go` / `unpack.go` (multi-source
  bin reader), and tests. Once it lands, HL1's row is appended to
  `realDiscFixtures` and B3 closes.
- **HL1 multi-track + audio fixture.** Blocked on the above. Expected
  shape: stem `HALFLIFE`, dir `/data/roms/redumper/half-life`,
  expected errors 0, generous delta bound (initial guess < 10 KB once
  audio sectors are confirmed to round-trip with empty delta), audio-
  track presence assertion (`len(tracks where Mode=="AUDIO") >= 1`).
  Spec for this should be appended at HL1 row time.
- **Tighter Freelancer delta/container bounds.** Initial bounds are
  generous (5 MB). After the first successful run we know the actual
  delta size; a follow-up commit can tighten to e.g. 2× actual.
- **LibCrypt PSX as a second protected-disc fixture** (TASKS.md B1
  open question). Different protection scheme; would test variable-
  offset and SecuROM-class behavior we don't currently exercise.
  Defer.
- **SafeDisc-specific spot-checks.** Out per design discussion —
  byte-equality is sufficient.
- **Per-platform CI.** TASKS.md C3.

## Variance from initial spec (post-implementation)

The initial spec described asserting `m.ErrorSectorCount == 588` directly
for Freelancer. First-run revealed a metric definition mismatch:
miniscram's `ErrorSectorCount` counts every sector requiring a delta
override (data-track errors + lead-in noise + boundary sectors ≈ 2812
for Freelancer), while redump.org's "errors count" counts only data-
track ECC/EDC errors (588). Both numbers are correct; they count
different things.

**Resolution (commit `735da03`):** Test asserts the data-track ECC/EDC
count via a new test-side `countDataTrackErrors` helper that walks the
bin and recomputes EDC per sector. That number (588 for Freelancer) is
a SafeDisc-class signature, stable across dumps. The manifest's
`ErrorSectorCount` retains a soft sentinel check (`> 0` for protected,
`== 0` for clean). Freelancer delta/container bounds loosened from 5
MiB to 15 MiB based on the observed ~7 MB.

**Also folded into this cycle (not in the initial spec):** A real bug
in `pack.go`'s `detectWriteOffset` was surfaced by the first Freelancer
pack attempt and fixed in commit `57e05b3`. Lead-in regions on real
protected discs contain non-zero data that produces coincidental
sync-pattern matches with non-BCD MSF headers; the original detector
took the first match and aborted with "implausible LBA". The fix
iterates candidates and accepts the first one with valid BCD plus a
sample-aligned, plausibly-bounded write offset.
