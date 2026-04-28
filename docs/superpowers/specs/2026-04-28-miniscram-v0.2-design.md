# miniscram v0.2 — pure-Go, structured delta

This spec replaces the xdelta3 dependency with a pure-Go implementation
of CD-ROM Mode 1 EDC + ECC and a small structured delta format. The
result is a single self-contained Go binary with no runtime
dependencies, while typically producing a *smaller* delta than v0.1
(sub-KiB versus xdelta3's ~12 KiB on a clean Deus Ex disc).

The v0.1 design at `docs/superpowers/specs/2026-04-27-miniscram-design.md`
remains the reference for everything not changed here: CLI shape, file
discovery, source removal, container framing, manifest schema (with
`format_version` bumping from 1 to 2), reporter, etc.

## Goals

- Drop the runtime dependency on the `xdelta3` binary.
- Drop `os/exec` from the codebase (no subprocess calls anywhere).
- Produce a *smaller* delta on clean discs by teaching the builder to
  emit proper Mode 1 zero pregap and leadout sectors, so ε̂ matches a
  clean `.scram` byte-for-byte across the entire disc.
- Stay simple. The structured delta format is byte-keyed, no
  compression. EDC and ECC are derived from ECMA-130 directly.

## Non-goals

- Backward-compatibility with v1 containers. v2 readers reject v1
  containers cleanly via the existing version-byte check.
- General-purpose binary diff. The structured delta is tailored to
  miniscram's "ε̂ vs scram" pattern.
- Mode 2 EDC/ECC. Mode 2 sectors don't carry the same EDC/ECC fields
  in the same positions; v0.2 only generates Mode 1 zero sectors for
  pregap and leadout (the universal convention).
- Bringing the `xdelta3` path back as a fallback. It's deleted, not
  conditionally compiled.

## Empirical motivation

Measurement of a real-world Deus Ex Redumper dump shows the diff
between v0.1's ε̂ (which emits raw `scrambleTable` for pregap and
leadout) and the actual `.scram` is concentrated in the LBA-specific
header bytes plus their downstream EDC/ECC perturbation:

| Region | Sectors | Diff vs `scrambleTable` |
| --- | --- | --- |
| Leadin | 45 000 | 0 bytes (both all-zero) |
| Pregap | 150 | ~8 700 bytes |
| Bin coverage (clean) | 336 354 | 0 bytes |
| Leadout | 97 + 1 partial | ~1 300 bytes total |

xdelta3 compresses these regions because they're highly repetitive
scrambled-zero patterns differing only in headers. If we generate the
same scrambled-Mode-1-zero content the disc was mastered with, ε̂
matches `.scram` exactly across all four regions and the delta payload
becomes effectively empty.

## Architecture

Three changes, applied in order:

1. **Add EDC and ECC** as pure-Go pure-functional helpers, derived
   from ECMA-130 §14.3 (EDC) and §14.5–14.6 + Annex A (ECC).
2. **Make the builder emit proper Mode 1 zero pregap and leadout
   sectors** with LBA-derived BCD MSF headers, computed EDC, computed
   ECC, then scrambled.
3. **Replace `xdelta3.go`** with a tiny structured-delta encoder /
   decoder that records byte-range overrides where ε̂ still differs
   from `.scram` (rare error sectors, anomalous leadout regions, etc.).

## File changes

```
+ edc.go            edc_test.go
+ ecc.go            ecc_test.go
+ delta.go          delta_test.go
~ builder.go        builder_test.go    (smarter pregap/leadout, combined build+delta)
~ pack.go           pack_test.go       (drop xdelta3 calls)
~ unpack.go         unpack_test.go     (drop xdelta3 calls)
~ manifest.go                           (format_version → 2; containerVersion byte → 0x02)
~ layout.go         layout_test.go     (add LBAToBCDMSF + round-trip test)
~ help.go                              (drop "REQUIRES: xdelta3" line)
~ main.go                              (drop exitXDelta; renumber exit codes)
~ main_test.go                         (drop ensureXDelta3 calls + "os/exec")
~ e2e_redump_test.go                   (drop ensureXDelta3 + "os/exec")
- xdelta3.go        xdelta3_test.go    (deleted entirely)
```

## EDC and ECC

Both algorithms are derived from ECMA-130. Reference test vectors come
from real Deus Ex sectors (which carry correct EDC/ECC by
construction), not from any other software's port.

### `edc.go`

Per ECMA-130 §14.3: 32-bit CRC over bytes 0..2063 of the unscrambled
Mode 1 sector, polynomial
`(x^16 + x^15 + x^2 + 1) · (x^16 + x^2 + x + 1)`, LSB-first bit
ordering within each byte, x^0 parity bit at bit 7 of byte 2067.

Reflected (LSB-first table form) polynomial: `0xD8018001`. Verified
empirically — sector 100 of `DeusEx_v1002f.bin` has the EDC value
`0xa482591e` at offset 2064–2067, exactly the value our spec-derived
CRC computes over its first 2064 bytes.

```go
const edcPoly = 0xD8018001
var edcTable = buildEDCTable() // 256 entries, computed at init()

// ComputeEDC returns the 4-byte EDC for a Mode 1 sector.
// Input:  bytes 0..2063 of the unscrambled sector (sync + header + user data).
// Output: bytes intended for offset 2064..2067, little-endian.
func ComputeEDC(secPrefix []byte) [4]byte
```

### `ecc.go`

Per ECMA-130 §14.5–14.6 + Annex A. Reed-Solomon Product Code over
GF(2^8) with primitive polynomial `x^8 + x^4 + x^3 + x^2 + 1` (`0x11D`)
and primitive element `α = 2`.

Bytes 12..2351 are viewed as 1170 16-bit words ordered as
`S(n) = MSB[B(2n+13)] | LSB[B(2n+12)]`. RSPC is applied independently
to the MSB and LSB streams.

- **P-vectors**: 43 columns × 26 rows. Each P-codeword is a (26, 24)
  Reed-Solomon code; the parity check matrix is
  `H_P = [[1 1 ... 1 1]; [α^25 α^24 ... α 1]]`. The 172 P-parity
  bytes go to offsets 2076..2247.
- **Q-vectors**: 26 diagonals × 45 entries with index
  `(44·M_Q + 43·N_Q) mod 1118`. Each Q-codeword is a (45, 43)
  Reed-Solomon code with the same matrix shape but `α^44`. The 104
  Q-parity bytes go to offsets 2248..2351.

```go
var gfExp [256]byte // gfExp[i] = α^i mod (x^8+x^4+x^3+x^2+1)
var gfLog [256]byte // gfLog[α^i] = i

// ComputeECC fills bytes 2076..2351 of sec with the P+Q parity
// computed over bytes 12..2075. sec must be a full 2352-byte buffer
// with everything except the ECC region populated.
func ComputeECC(sec *[SectorSize]byte)
```

### Tests

- **`TestEDCSpotChecks`**: hard-coded reference 4-byte EDC for a known
  LBA-0 Mode 1 zero sector, computed once from the spec in Python and
  pinned.
- **`TestECCSpotChecks`**: hard-coded reference 276-byte ECC for the
  same LBA-0 Mode 1 zero sector.
- **`TestEDCKnownNonZeroData`**: hand-crafted sector with deterministic
  user data; pinned EDC value.
- **`TestGFTableInvariants`**: `gfExp[gfLog[i]] == i` for `i` in
  `1..255`; sha256 of `gfExp || gfLog` matches a hard-coded constant.
- **`TestEDCAgainstDeusEx`** / **`TestECCAgainstDeusEx`** (build-tagged
  `redump_data`): pull sectors at LBA 0, 100, 1000, and the last bin
  LBA from `DeusEx_v1002f.bin`; recompute EDC + ECC; assert match.

## Builder enhancement

### New helper in `layout.go`

```go
// LBAToBCDMSF converts an LBA into the 3-byte BCD MSF triple stored
// in the header field of a Mode 1 sector. LBA -150 yields 00:00:00.
// Caller must ensure lba is in [-150, 99*60*75 - 150).
func LBAToBCDMSF(lba int32) [3]byte
```

Round-trip property: `BCDMSFToLBA(LBAToBCDMSF(L)) == L` for `L` in the
documented range.

### New helper in `builder.go`

```go
// generateMode1ZeroSector returns the scrambled bytes of a Mode 1
// sector with all-zero user data and a BCD MSF header for the given
// LBA. This is the standard pregap/leadout content for CD-ROMs
// mastered with Mode 1 zero sectors in those regions.
func generateMode1ZeroSector(lba int32) [SectorSize]byte {
    var sec [SectorSize]byte
    copy(sec[:SyncLen], Sync[:])
    msf := LBAToBCDMSF(lba)
    sec[12], sec[13], sec[14] = msf[0], msf[1], msf[2]
    sec[15] = 0x01
    edc := ComputeEDC(sec[:2064])
    sec[2064], sec[2065], sec[2066], sec[2067] = edc[0], edc[1], edc[2], edc[3]
    ComputeECC(&sec)
    Scramble(&sec)
    return sec
}
```

### Updated `buildSectorForLBA`

```
case lba < LBAPregapStart:                          // leadin
    return zero[2352]                                  // (unchanged)
case lba < p.BinFirstLBA:                           // pregap
    return generateMode1ZeroSector(lba)                // NEW
case lba in [BinFirstLBA, BinFirstLBA+BinSectorCount):
    binSec, scramble if data                            // (unchanged)
case lba >= BinFirstLBA+BinSectorCount:             // leadout
    return generateMode1ZeroSector(lba)                // NEW
```

### Lockstep extends across the whole disc

With the smarter builder, ε̂ now matches a clean `.scram` byte-for-byte
in pregap, bin, and leadout. The lockstep comparison runs over every
sector position from `LeadinLBA` to `endLBA - 1` (not just
bin-covered LBAs as in v0.1):

- Leadin: ε̂ = 0, expect scram = 0. Mismatches here would be very
  unusual.
- Pregap: ε̂ = scrambled Mode 1 zero. Mismatches indicate non-standard
  pregap content (rare).
- Bin: mismatches are error sectors (intentional EDC/ECC corruption
  used as copy protection).
- Leadout: ε̂ = scrambled Mode 1 zero. Mismatches indicate the drive
  read past-disc garbage instead of Mode 1 zero — fairly common; the
  delta captures it faithfully.

The 5 % abort threshold still applies, computed as
`len(overrides) / total_disc_sectors`.

## Structured delta format

### Wire format (binary, big-endian)

```
delta = u32: override_count
        followed by override_count records of:
            u64: file_offset
            u32: length         // 1 ≤ length ≤ SectorSize
            length bytes:       // payload to write at file_offset
```

For Deus Ex with the smarter builder, `override_count = 0` is expected
on a clean disc. Total delta payload = 4 bytes.

### `EncodeDelta`

```go
// EncodeDelta walks epsilonHat (a built ε̂ stream) and scram (the
// original) in lockstep and writes a structured delta to out.
// Returns the number of override records written.
func EncodeDelta(out io.Writer, epsilonHat, scram io.Reader, scramSize int64) (int, error)
```

Implementation: read both streams in 1 MiB chunks; maintain a
"current run of mismatched bytes" `(start_offset, byte_buffer)`. When
the bytes match again, flush the run as a single override; clear and
continue. Two regions of differing bytes are *adjacent* if they
share at least one matching byte between them — the encoder doesn't
greedily merge across long matching gaps. In practice, two error
sectors that are bit-for-bit different across all 4704 bytes between
them collapse into one 4704-byte override; two error sectors with
even one matching byte between them stay as two records.

### `ApplyDelta`

```go
// ApplyDelta reads override records from delta and writes them into
// out at the recorded file offsets. out must have been pre-populated
// with ε̂ truncated to scramSize before calling.
func ApplyDelta(out io.WriterAt, delta io.Reader) error
```

The unpack pipeline writes ε̂ to the output file, then opens it
read-write and lets `ApplyDelta` overlay the overrides via `WriteAt`.
No separate temp file needed.

### Why no compression

Override payloads are scrambled CD bytes (high-entropy, near-random).
The 12-byte per-override header is dominated by the 2352-byte payload.
Compression would buy under 5 % even in pathological cases. Skip it
to keep the format trivially auditable.

## Combined build + delta encoding

`EncodeDelta` and `ApplyDelta` are the canonical entry points to the
structured-delta format and are tested in isolation. The pack
pipeline, however, would have to walk an 800+ MiB ε̂ twice if it built
ε̂ and then ran `EncodeDelta` over it — once to write ε̂, once to read
it back for the diff.

To avoid that, ε̂ generation and delta encoding fuse into one
function used only by the pack pipeline:

```go
// BuildEpsilonHatAndDelta walks bin and scram in lockstep, writing the
// reconstructed scrambled image to epsilonHat and the structured
// delta to deltaOut. scram may be nil (in which case deltaOut must
// also be nil — used by Unpack to rebuild ε̂ without producing a delta).
//
// Returns the number of override records written and the LBAs they
// cover (capped at errorSectorsListCap = 10 000 entries). Aborts with
// LayoutMismatchError if more than 5 % of total disc sectors mismatch.
func BuildEpsilonHatAndDelta(
    epsilonHat io.Writer,
    deltaOut   io.Writer,   // nil iff scram is nil
    p          BuildParams,
    bin        io.Reader,
    scram      io.Reader,   // nil for unpack (no comparison)
) (overrideCount int, errLBAs []int32, err error)
```

`BuildEpsilonHat` (the existing API) becomes a thin wrapper that
calls `BuildEpsilonHatAndDelta(epsilonHat, nil, p, bin, nil)`.

`BuildEpsilonHatAndDelta` reuses the same per-byte run-tracking logic
that `EncodeDelta` uses, factored into an unexported `deltaWriter`
helper in `delta.go`. The wire format is therefore guaranteed
identical between the two paths.

## Reporter step changes

Pack steps shrink from "building ε̂ + lockstep pre-check" + "running
xdelta3 -e" + "writing container" to:

```
[17:32:01] building ε̂ + delta ............................. OK 0 override(s)
[17:32:01] writing container ............................... OK <path>
```

Unpack steps shrink from "building ε̂" + "running xdelta3 -d" +
"verifying output sha256" to:

```
[17:32:01] building ε̂ ...................................... OK
[17:32:01] applying delta .................................. OK 0 override(s)
[17:32:01] verifying output sha256 ......................... OK matches
```

## Pack pipeline (changes)

Steps 1–6 unchanged. Steps 7–11 collapse:

| # | v0.1 | v0.2 |
| --- | --- | --- |
| 7 | Build ε̂ + lockstep pre-check | Build ε̂ **and** encode delta in one pass |
| 8 | `xdelta3 -e -B 256MB ε̂ scram → delta` | (gone — delta written in step 7) |
| 9 | Write container | (unchanged) |
| 10 | Verify by Unpack with `xdelta3 -d` | Verify by Unpack with `ApplyDelta` |
| 11 | Remove source | (unchanged) |

## Unpack pipeline (changes)

| # | v0.1 | v0.2 |
| --- | --- | --- |
| 1–3 | (unchanged) | (unchanged) |
| 4 | `xdelta3 -d -s ε̂ delta → output` | Write ε̂ directly to output; `ApplyDelta(output, delta)` overlays overrides via `WriteAt` |
| 5 | Verify output sha256 | (unchanged) |

## Container & manifest

Container framing identical: magic `MSCM` + version byte + length + JSON manifest + payload. Only changes:

- `containerVersion` byte: `0x01` → `0x02`.
- Manifest field `format_version`: `1` → `2`.
- Payload byte sequence is now a structured delta instead of a VCDIFF
  blob.

A v0.1 binary reading a v0.2 container fails cleanly with the
existing "unsupported container version" error. We don't ship a
backward-compatibility shim.

## CLI surface

- `--help` loses the `REQUIRES: xdelta3 binary on PATH` line. miniscram
  has no external runtime dependencies.
- Exit codes renumber. v0.1 had `1=usage 2=layout 3=xdelta3
  4=verify 5=I/O 6=wrong bin`. v0.2 reclaims slot 3:
  - `0` success
  - `1` usage / input error
  - `2` layout mismatch
  - `3` verification failed (was 4)
  - `4` I/O error (was 5)
  - `5` wrong .bin for this .miniscram (was 6)

  The v0.1 `exitXDelta = 3` slot doesn't exist in v0.2; xdelta3 errors
  no longer occur.
- All other flags (`--keep-source`, `--no-verify`, `--allow-cross-fs`,
  `-f`, `-q`, file discovery shapes) are unchanged.

## What gets deleted

- `xdelta3.go` and `xdelta3_test.go` (the wrapper and its tests).
- `XDelta3Encode`, `XDelta3Decode`, `runXDelta3`,
  `xdelta3SourceWindowCap` (constants and functions).
- The `ensureXDelta3(t)` test helper and the skip-when-binary-missing
  pattern from `pack_test.go`, `unpack_test.go`, `main_test.go`,
  `e2e_redump_test.go`.
- The `os/exec` import from every file. miniscram has no subprocess
  invocations after this change.
- The "running xdelta3 -e" / "running xdelta3 -d" reporter steps
  (replaced with "building ε̂ + delta" / "applying delta").
- The "xdelta3 not found on PATH" error path.

## Known limitations and untested scenarios

These are intentional gaps where v0.2 will work in practice but isn't
formally validated. Calling them out so future maintainers and users
know what's covered and what isn't.

### Cross-platform

Development happens on Linux. The codebase is stdlib-only, uses no
syscalls, and follows POSIX path conventions through `path/filepath`.
It should build and run on macOS, Windows, and the BSDs without
modification, but **CI does not currently exercise non-Linux
targets**. If you hit a portability issue (Windows path quirks, etc.),
file an issue.

### Mode 2 sectors

ECMA-130 defines two CD-ROM sector modes:

- **Mode 1**: 2048 bytes user data, EDC at offset 2064, ECC at offset
  2076. v0.2 implements full EDC/ECC support for Mode 1.
- **Mode 2**: 2336 bytes user data, no EDC/ECC at the standard
  offsets. (Mode 2 has sub-forms with their own protection schemes
  via the CD-ROM XA extensions.)

v0.2 only generates Mode 1 zero sectors for pregap and leadout. This
is the universal mastering convention regardless of the disc's
*data-track* mode — pregap and leadout exist outside the user data
area and are conventionally Mode 1 zero on every commercial disc the
author has tested.

Mode 2 *user data* tracks in `.bin` are still scrambled the same way
(scrambling is mode-agnostic per ECMA-130) and round-trip correctly,
but the synthetic test fixture only exercises Mode 1.

### Discs whose pregap or leadout aren't Mode 1 zero

Some discs (mostly older, hand-mastered, or homebrew) may use
non-standard pregap or leadout content. v0.2 still round-trips these
correctly: the lockstep comparison records the differing bytes as
delta overrides, just like error sectors. The delta gets larger but
the recovery is exact.

The `manifest.error_sector_count` field doesn't distinguish "real
error sectors in bin coverage" from "non-Mode-1-zero pregap/leadout".
If you need to inspect the override list, the LBAs are recorded in
`manifest.error_sectors` (capped at 10 000 entries).

### Variable-offset discs

Unchanged from v0.1. The `checkConstantOffset` step rejects discs
where the write offset varies across the data region (Redumper's
`force_descramble` case). This is documented as out-of-scope.

### Audio offsets

Not relevant: miniscram doesn't touch audio offsets. Audio sectors
in `.bin` are passed through unscrambled and round-trip exactly via
the byte-keyed delta mechanism.

## Migration path from v0.1

1. v0.1 containers (`format_version: 1`) cannot be unpacked by v0.2.
   The v0.2 unpacker rejects them with the existing "unsupported
   container version 0x01 (this build expects 0x02)" message.
2. To migrate, run `miniscram unpack` with v0.1 to recover the
   `.scram`, then `miniscram pack` with v0.2.
3. Don't auto-delete the source on the v0.1 side until you've
   verified the v0.2 round-trip works.

A future v0.3 *could* add a v0.1 reader for backward compatibility,
but at this stage no real-world v0.1 containers exist outside
development environments.
