# miniscram v1.0 — simplify

This refactor cycle ships as miniscram 1.0.0 — the first stable
release. Internal refactor and format simplification: reduce
duplication, tighten test coverage around end-to-end flows, and slim
the container format. No user-visible behavior changes from v0.2
(same CLI surface, same exit codes, same error messages).

Container version byte is **0x01** — v1 is the start of versioned
history. Any other version byte is rejected as unsupported. No files
in the wild; pre-v1 dev formats are not a maintained migration
target.

---

## Goals

1. **DRY.** Eliminate duplicated logic across pack/unpack/verify/builder/delta.
2. **Cohesion.** Files and types name what they do; no two helpers compute
   the same value with different signatures.
3. **Laser-focused tests.** End-to-end coverage of the user-visible
   surface; unit tests only for invariants that aren't observable
   end-to-end. Add a CLI-level e2e harness that drives `run()` directly.
4. **Slim format.** Drop manifest fields that duplicate values
   reachable from tracks[] or the delta payload. Allow delta override
   runs of arbitrary length. Move the scrambler-table hash to the
   binary header.

## Non-goals

- Changes to the CLI surface (subcommands, flags, defaults, exit
  codes). Stays exactly as v0.2 had it.
- Changes to error sentinels or their public-facing names.
- Changes to the scrambling algorithm, ECC/EDC layout, or any other
  ECMA-130 specifics.
- Performance work. Speed isn't the bottleneck; clarity is.

---

## Container format v1

### Binary header (fixed 41 bytes)

```
[ 0,  4)  magic              "MSCM"           (4 bytes ASCII)
[ 4,  5)  version            0x01             (1 byte)
[ 5, 37)  scrambler_table_sha256              (32 raw bytes, not hex)
[37, 41)  manifest_length    u32 big-endian   (manifest body byte count)
[41,  N)  manifest body      JSON
[ N,   )  delta payload      (length implicit: file size - N)
```

`ReadContainer` validates in this order: magic, version, scrambler-table
hash (against `expectedScrambleTableSHA256`), manifest length plausibility
(`> 0 && ≤ 16 MiB`), manifest JSON parse. Mismatched scrambler hash
yields a sentinel error pointing at scrambler-table drift rather than
generic "bad container".

Rationale: lifting the scrambler hash out of JSON makes it
parseable without touching the JSON layer and keeps the JSON focused
on per-disc data.

### Manifest body (JSON)

```json
{
  "tool_version": "miniscram 1.0.0 (go1.22)",
  "created_utc": "2026-04-28T14:30:21Z",
  "write_offset_bytes": -52,
  "leadin_lba": -45150,
  "scram": {
    "size": 739729728,
    "hashes": {"md5": "...", "sha1": "...", "sha256": "..."}
  },
  "tracks": [
    {
      "number": 1,
      "mode": "MODE1/2352",
      "first_lba": 0,
      "filename": "DeusEx_v1002f.bin",
      "size": 739729728,
      "hashes": {"md5": "...", "sha1": "...", "sha256": "..."}
    }
  ]
}
```

**Dropped from v0.2:**

| Field | Reason |
|---|---|
| `format_version` | Redundant with binary version byte. |
| `bin_size` | `sum(track.size)`. |
| `bin_md5` / `bin_sha1` / `bin_sha256` | Roll-up of per-track hashes is computed at unpack time anyway; storing it duplicates the per-track values without adding signal. |
| `bin_first_lba` | `tracks[0].first_lba`. |
| `bin_sector_count` | `sum(track.size) / SectorSize`. |
| `error_sectors` (capped 10000 array) | Same information lives in the delta payload; `inspect` already enumerates it via `IterateDeltaRecords`. |
| `error_sector_count` | `count` from delta payload header. |
| `delta_size` | `len(deltaBytes)` from `ReadContainer`. |
| `scrambler_table_sha256` | Moved to binary header. |

**Reshaped:** per-entity hashes nest into a `hashes: {md5, sha1, sha256}`
sub-object instead of three flat fields. This eliminates the field-name
prefix repetition (`bin_md5`, `scram_md5`, etc.) and makes the manifest
roughly 60% shorter overall.

**Per-track `size` and `first_lba` semantics unchanged from v0.2:**
`first_lba` is the absolute LBA where the track's FILE begins on disc;
`size` is the byte count of the track's `.bin` file. ResolveCue
populates both at pack time; manifest persists them so unpack doesn't
need a cuesheet.

### Delta payload

```
u32  override_count   big-endian
for i in 0..override_count:
  u64  file_offset    big-endian
  u32  length         big-endian   (1 ≤ length ≤ scram_size)
  bytes payload       (length bytes)
```

Wire shape unchanged from v0.2 except: **per-record `length ≤ SectorSize`
cap is lifted.** Encoder emits one record per contiguous mismatch run
regardless of size; sanity ceiling of `length ≤ scram_size` remains for
framing-corruption detection.

Rationale: the cap was an artifact of the original sector-by-sector
encoder. The new unified builder encodes natural runs.

---

## Component layout

12 source files (was 15). Sizes are estimates.

| File | LOC | Responsibility |
|---|---|---|
| `main.go` | ~150 | CLI dispatch, flag-parsing helper `parseSubcommand`, single `errToExit` map. |
| `pack.go` | ~250 | `Pack()` only. Round-trip verification calls `Verify()` instead of duplicating Unpack. |
| `unpack.go` | ~250 | `Unpack()` and `Verify()` (Verify is a thin wrapper around Unpack into a tempfile + hash compare). |
| `inspect.go` | ~150 | `formatHumanInspect` + `formatJSONInspect` + `runInspect`. Smaller because manifest is smaller. |
| `builder.go` | ~250 | One unified `BuildEpsilonHat` with optional mismatch callback. Layout-mismatch ratio check as a separate post-walk function. |
| `delta.go` | ~150 | `EncodeDelta`, `ApplyDelta`, `IterateDeltaRecords` — one of each. |
| `cue.go` | ~265 | Unchanged. |
| `manifest.go` | ~150 | Slimmer Manifest type, `WriteContainer`, `ReadContainer`. |
| `ecma130.go` | ~240 | **NEW**: merge of scrambler.go + ecc.go + edc.go (all implement ECMA-130). |
| `layout.go` | ~80 | Constants, BCD MSF helpers, single `scramFileOffset`. |
| `reporter.go` | ~125 | Existing reporter + new `runStep(r, label, fn)` helper. |
| `help.go` | ~140 | Help text constants. |

### Key API consolidations

**`BuildEpsilonHat` (one function, replaces two):**
```go
type EpsilonHatParams struct {
    LeadinLBA, BinFirstLBA, BinSectorCount int32
    WriteOffsetBytes int
    ScramSize int64
    Tracks []Track
}

// onMismatch is invoked once per contiguous mismatch run when scram is
// non-nil. It receives the file offset of the first mismatched byte
// and the run of *scram* bytes (so the caller can write delta records).
// If onMismatch is nil, mismatches are still counted for the layout
// ratio check but the byte runs are discarded.
//
// Returns the list of LBAs that contained at least one mismatch
// (capped at errorSectorsListCap) and the total mismatched-sector count.
func BuildEpsilonHat(
    out io.Writer,
    p EpsilonHatParams,
    bin io.Reader,
    scram io.Reader,
    onMismatch func(off int64, scramRun []byte),
) (errLBAs []int32, mismatchedSectors int, err error)
```

The layout-mismatch ratio check (`> 5%`) is a separate post-walk
function callers run if they want it: `CheckLayoutMismatch(errLBAs,
mismatchedSectors, totalDiscSectors) error`. Pack runs it; unpack
doesn't (it has no scram to compare against).

Pack's delta encoding becomes:
```go
enc := NewDeltaEncoder(deltaOut)
errLBAs, mismatched, err := BuildEpsilonHat(hat, p, bin, scram, enc.OnMismatch)
if err != nil { ... }
if err := enc.Close(); err != nil { ... }
if err := CheckLayoutMismatch(errLBAs, mismatched, totalDisc); err != nil { ... }
```

Unpack passes `nil` for both scram and onMismatch.

**`hashTrackFiles` (one helper, replaces two near-identical loops):**
```go
// Streams every file once, computing per-file hashes and a roll-up
// hash across all files. Used by Pack (to populate the manifest) and
// by Unpack (to verify track files match the manifest).
func hashTrackFiles(files []ResolvedFile) (perFile []FileHashes, rollup FileHashes, err error)
```

**`validateSyncCandidate` (one helper, replaces two closures):**
```go
// Reads the 3 bytes of MSF header at syncOff+SyncLen, descrambles via
// the table, and returns the implied write offset if all three checks
// pass: BCD-valid header, decoded LBA in [leadinLBA, 500_000], offset
// sample-aligned and within ±2 sectors. ok=false on any failure.
func validateSyncCandidate(f io.ReaderAt, syncOff int64, leadinLBA int32, scramSize int64) (writeOffset int, ok bool)
```

Used by both `detectWriteOffset` (returns first valid candidate) and
`checkConstantOffset` (samples N candidates and asserts equality).

**`runStep` (Reporter helper):**
```go
// runStep wraps the Step/Done/Fail pattern. fn returns the optional
// done message and any error. Equivalent to:
//   st := r.Step(label)
//   msg, err := fn()
//   if err != nil { st.Fail(err); return err }
//   st.Done(msg)
//   return nil
func runStep(r Reporter, label string, fn func() (doneMsg string, err error)) error
```

Replaces ~25 hand-rolled instances across pack/unpack/verify.

**`parseSubcommand` (CLI helper):**
```go
// parseSubcommand registers the common flags (-h/--help, -q/--quiet)
// and parses args. Returns the FlagSet (caller registers subcommand-
// specific flags before calling), the parsed positional args, and a
// help-or-usage exit code if parsing failed or help was requested.
func parseSubcommand(name string, helpText string, stderr io.Writer, args []string, configure func(*flag.FlagSet)) (positional []string, quiet bool, exit int, ok bool)
```

Where `ok=false` means the caller should return `exit` immediately.
Reduces the four runX functions to ~30 LOC each from ~50–65.

**`errToExit` (single function, replaces three):**
```go
func errToExit(err error) int {
    var lme *LayoutMismatchError
    switch {
    case errors.As(err, &lme):
        return exitLayout
    case errors.Is(err, errBinHashMismatch):
        return exitWrongBin
    case errors.Is(err, errVerifyMismatch),
         errors.Is(err, errOutputHashMismatch):
        return exitVerifyFail
    case err != nil:
        return exitIO
    default:
        return exitOK
    }
}
```

Pack/Unpack/Verify all call this. The set of sentinels each subcommand
can produce is a runtime concern; the exit-code map is universal.

### Eliminated helpers

- `verifyRoundTrip` (`pack.go`) — Pack calls `Verify` instead.
- `BuildEpsilonHatAndDelta` (`builder.go`) — replaced by `BuildEpsilonHat` + delta-encoder callback.
- `filepathDir` (`main.go`) — `filepath.Dir` from stdlib.
- `deltaJSONSize` (`unpack.go`) — caller uses `len(deltaBytes)`.
- `ScramOffset` (`layout.go`) — `scramFileOffset` is the canonical helper.
- `packErrorToExit` / `unpackErrorToExit` / `verifyErrorToExit` — single `errToExit`.
- `printPackHelp` / `printUnpackHelp` / etc — one `printHelp(w, text)`.

---

## Test architecture

10 test files post-sweep (was 14 if we count `e2e_redump_test.go`).

| File | Purpose |
|---|---|
| `fixtures_test.go` | **NEW** Shared helpers: `synthDisc(t, opts)`, `writeFixture(t, dir, disc)`, `sampleManifest()`, `mustHashFile(t, path)`, `buildDelta(t, offsets)`. |
| `e2e_test.go` | **NEW** Table-driven full-lifecycle: pack → inspect → verify → unpack → byte-equal. Fixture matrix: clean / negative-offset / positive-offset / multi-track audio / mode2 / with-errors. Replaces most of pack_test/unpack_test/verify_test. |
| `cli_test.go` | **NEW** Drives `run([]string{...}, stdout, stderr)` directly. Asserts on exit codes, stderr routing, file effects. Covers the runX wrappers that have zero direct tests today. |
| `cue_test.go` | Cue parser edge cases only: malformed lines, multi-FILE, mode validation, INDEX rules. Trimmed. |
| `layout_test.go` | BCD MSF round-trip, `TotalLBAs`, `scramFileOffset`. |
| `delta_test.go` | Encode/decode/iterate round-trip on hand-crafted byte streams; framing-corruption detection. Trimmed. |
| `ecma130_test.go` | **REPLACES** scrambler_test + edc_test + ecc_test. Scrambler self-test, ECC/EDC against known vectors. |
| `manifest_test.go` | Container header round-trip, scrambler-hash mismatch rejection, version-mismatch rejection, manifest JSON round-trip. Slightly extended. |
| `inspect_test.go` | Format-output snapshots only (no fixture construction — fixtures come from fixtures_test.go). Trimmed from 480 to ~150. |
| `e2e_redump_test.go` | Real-disc fixtures (Freelancer, HL1). Unchanged. |

### Coverage philosophy

**E2e tests own the contract.** Most behavioral coverage lives in
`e2e_test.go`. A typical test row in the matrix:

```go
{
    name:           "negative-offset",
    opts:           synthOpts{mainSectors: 100, writeOffset: -48, leadout: 10},
    expectErrors:   0,
    expectFlags:    expectByteEqualRoundTrip,
}
```

A single `runE2E(t, row)` helper packs the synthesized disc, inspects
the container, verifies it, unpacks it, and asserts byte-equality
against the original `.scram`. The same helper handles the full
lifecycle for every row.

**Unit tests own invariants not observable end-to-end:**
- `BCDMSFToLBA` round-trip across the valid range.
- `ComputeEDC` / `ComputeECC` against ECMA-130 reference vectors.
- `Scramble` self-test (already in production code; test exercises it).
- Cue parser rejection of malformed input (each rejection is one test).
- Delta framing-error detection (truncated header, truncated payload, implausible length).

**CLI tests own the entry-point surface:**
- Each subcommand: success path → exit 0.
- Each subcommand: missing positional → exit 1, usage on stderr.
- Each subcommand: unknown flag → exit 1.
- Bin-hash mismatch on unpack/verify → exit 5.
- Output-hash mismatch on verify → exit 3.
- Container-version mismatch → exit 4 (I/O), useful migration message.
- `--help` for each subcommand → exit 0, help text on stderr.

**No tests of internal-helper-function shapes.** Tests like
`TestHashFile_EmptyFile` are absorbed by e2e flows where the helper is
exercised in context.

### Removed test patterns

- Per-test fixture construction (each test building its own synthetic
  disc with copy-pasted helpers).
- `mustHashFile` returning sha256-only — replaced with shared helper
  that returns all three hashes so tests assert all three.
- Tests that exercise only the manifest construction by calling Pack
  and reading back — superseded by the e2e matrix.
- `var _ = bytes.Equal` (`pack_test.go:115`) — leftover.

---

## Migration phases

Each phase leaves the tree compiling and `go test ./...` green. Recommended
commit boundaries.

1. **Format v1** — new binary header, slim manifest, `scrambler_table_sha256`
   in header. Set version byte to 0x01 (fresh start at v1 boundary).
   Update `WriteContainer`/`ReadContainer`. Update inspect formatters.
   Update existing tests minimally to keep them compiling. Largest
   single change; gates everything downstream.
2. **Builder unification** — collapse `BuildEpsilonHat` and
   `BuildEpsilonHatAndDelta` into one with `onMismatch` callback. Move
   ratio-check to a separate `CheckLayoutMismatch` helper.
3. **Delta encoder unification** — single `DeltaEncoder` type used by
   both standalone callers and the builder's mismatch callback. Lift
   the per-sector cap.
4. **Hash-loop helper** — `hashTrackFiles(files)` shared by Pack and
   Unpack.
5. **Sync-validation helper** — `validateSyncCandidate` shared by
   `detectWriteOffset` and `checkConstantOffset`.
6. **Pack uses Verify for round-trip** — delete `verifyRoundTrip` from
   pack.go.
7. **CLI consolidation** — `parseSubcommand` helper, single `errToExit`.
   Delete `filepathDir`, `deltaJSONSize`. Single `printHelp`.
8. **File mergers** — `ecma130.go` (ecc+edc+scrambler), fold verify.go
   into unpack.go.
9. **Reporter `runStep` helper** — collapse Step/Done/Fail boilerplate
   site by site.
10. **Test sweep** — fixtures_test.go, e2e_test.go, cli_test.go; delete
    redundant tests.
11. **Cleanup pass** — stale comments, dead code, version-string bump
    to `miniscram 1.0.0`.
12. **README.md** — write a top-level README documenting:
    - what miniscram is (1-paragraph elevator pitch),
    - the CLI subcommands (pack/unpack/verify/inspect) with example
      invocations,
    - exit codes,
    - the v1 container format (binary header + manifest schema +
      delta payload), with byte offsets and field semantics, written
      well enough that an outside reader could implement a compatible
      reader without reading the source. Format section is the
      authoritative reference; the spec doc this file lives in is
      design rationale, the README is the user-facing format
      documentation.
    - pointer to `docs/superpowers/specs/` for the full design history.

Each phase is independently revertible. If a phase breaks something, the
previous tree is the last green commit.

---

## Error handling (unchanged)

Sentinels: `errBinHashMismatch`, `errOutputHashMismatch`,
`errVerifyMismatch`. `LayoutMismatchError` type for layout mismatches.

Exit codes: 0 (success), 1 (usage), 2 (layout mismatch), 3 (verification
failed), 4 (I/O), 5 (wrong bin).

Error wrapping: `fmt.Errorf("%w: %v", sentinel, detail)` pattern used
where sentinels need to attach context. `compareHashes` returns a plain
error describing the diff; callers wrap with the appropriate sentinel.

The refactor doesn't touch any of these. Tests assert sentinel
identity (`errors.Is`) and exit-code mapping; both stay valid.

---

## Risks and mitigations

**Risk: format change breaks the existing test corpus.** Phase 1 updates
all hand-constructed Manifest literals in tests in lockstep. Tests stay
green at every commit boundary because the format change is one atomic
phase.

**Risk: `BuildEpsilonHat` unification subtly changes the lockstep
behavior** (e.g., the order of mismatch detection vs. write).
Mitigation: keep the e2e Freelancer + HL1 round-trip tests running
through every phase; both fixtures exercise non-trivial mismatch
patterns.

**Risk: e2e test sweep deletes a test that was the only thing
covering some edge case.** Mitigation: phase 10 is last; by then the
e2e matrix is in place. Each test deleted is justified by an existing
e2e row covering the same path. If no e2e row covers it, add one
before deleting.

**Risk: scrambler-hash-in-header complicates future scrambler-table
changes.** Mitigation: the table hasn't changed in 30 years and isn't
expected to. If it ever does, the binary version byte already exists
to flag a different generation.

---

## Acceptance

- [ ] `go test ./...` passes after every phase.
- [ ] CLI surface byte-identical to v0.2: same flag names, same exit
      codes, same error message formats (modulo internal wording fixes
      where they are demonstrably wrong).
- [ ] Container version byte is 0x01. Any other version byte is
      rejected with an "unsupported container version" error.
- [ ] Manifest is the slim shape from this spec; no v0.2 fields linger.
- [ ] Source LOC ≤ 2300 (was ~3000).
- [ ] Test LOC ≤ 1700 (was ~3000), with strictly higher real coverage:
      every CLI subcommand has direct exit-code coverage; every fixture
      shape (clean / write-offset / multi-track / mode2 / errors) has
      e2e coverage.
- [ ] No duplicated helpers (`grep -c "^func " *.go` shows no near-clone
      pairs across files).
- [ ] `e2e_redump_test.go` Freelancer + HL1 fixtures still round-trip
      byte-equal.
