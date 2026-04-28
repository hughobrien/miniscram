# miniscram — design

A small Go tool that compactly preserves a Redumper `.scram` (scrambled CD-ROM
intermediate dump) alongside its corresponding `.bin` (final unscrambled
dump) by storing only the binary delta needed to reconstruct the `.scram`
exactly. Implements the method described in Hauenstein, *Compact
Preservation of Scrambled CD-ROM Data* (IJCSIT, August 2022), specialised
for Redumper's output format.

## Goals

- `pack`: given `.bin` + `.cue` + `.scram`, produce a single `.miniscram`
  container that, together with the `.bin`, can losslessly reconstruct
  the `.scram`.
- `unpack`: given `.bin` + `.miniscram`, reproduce the original `.scram`
  byte-for-byte.
- Verify round-trip integrity by default in both directions; refuse to emit
  unverified output.
- Stay small. One Go module, flat layout, no embedded VCDIFF — shell out
  to `xdelta3`.

## Non-goals

- Other dumper formats (DiscImageCreator and friends).
- Preservation of Redumper sidecar files (`.subcode`, `.state`, `.skeleton`,
  `.toc`, `.fulltoc`, `.hash`, `.log`). Out of scope.
- Variable-offset discs (Redumper's `force_descramble` path). Detect and
  abort with a clear error.
- DVD/BD scrambling. CD-ROM only.

## Background

Redumper reads a CD-ROM in scrambled (audio) mode and emits two main
outputs: `.scram` (raw scrambled main-channel data with sub-sector write
offset preserved) and `.bin` (unscrambled, error-corrected, sector-aligned
disc image). For preservation, both are valuable: `.scram` retains
intentional EDC/ECC error sectors used as copy protection (e.g., the
hidden text in *Rune* described by Hauenstein), while `.bin` is the
community-standard format for matching dumps.

Storing both doubles the disk footprint. The Hauenstein method observes
that for non-error sectors, scrambling is exactly reversible, so most of
`.scram` can be reconstructed from `.bin`. The leftover differences
(error sectors, plus format-specific extras like Redumper's leadin/leadout
regions) are typically small and compress well via a binary diff.

### Redumper `.scram` layout

The `.scram` file is a flat byte array indexed by absolute disc LBA, with
`LBA_START = -45150` (ECMA-130 leadin start). Each LBA occupies 2352 bytes
at file offset:

```
fileOffset(lba) = (lba - LBA_START) * 2352 + writeOffsetBytes
```

`writeOffsetBytes` is the disc's write offset in samples × 4 bytes/sample.
For the Deus Ex test data, write offset = −12 samples = −48 bytes; this
information is **not** present in the `.cue` file (it lives in
Redumper-specific metadata or Redump submission templates) and is
auto-detected by miniscram from `.scram` itself.

Concretely for the Deus Ex test data:

| LBA range          | scram region                | bytes        | content              |
| ------------------ | --------------------------- | ------------ | -------------------- |
| −45150 … −151      | bytes 0 … 105,839,951       | 105,839,952  | all zeros (leadin)   |
| −150 … −1          | next 150 sectors            | 352,800      | scrambled-zero pregap|
| 0 … 336,353        | next 336,354 sectors        | 791,104,608  | scrambled main data  |
| 336,354 … 336,451  | last 97 full + 1 truncated  | 230,424      | scrambled leadout    |

The first scrambled sync field lands at byte offset `45000 × 2352 − 48 =
105,839,952`, matching LBA −150 with a write offset of −48 bytes. The
last sector is truncated by 72 bytes because the trailing data does not
align cleanly after the offset shift.

### Scrambling

Per ECMA-130 §16, bytes 12..2351 of every data sector are XORed against
a fixed 2340-byte stream produced by an LFSR with seed `0x0001`,
polynomial `x¹⁵ + x + 1`, eight bits per output byte. The 12-byte sync
field (`00 FF FF FF FF FF FF FF FF FF FF 00`) is **not** scrambled.
Audio sectors are not scrambled at all. The XOR is self-inverse: applying
it twice returns the original bytes.

## CLI

```
miniscram pack   <bin> <cue> <scram>  -o <out.miniscram>   [--no-verify]
miniscram unpack <bin>                -o <out.scram>   <in.miniscram>   [--no-verify]
```

`pack` produces a self-contained `.miniscram` file. `unpack` reproduces
the original `.scram` from `.bin` + `.miniscram` without needing the
original `.cue` (parsed track layout is embedded in the manifest).

External runtime dependency: `xdelta3` on `PATH`.

## Module layout

Single Go module, flat package:

```
miniscram/
  go.mod
  main.go         # subcommand dispatch
  scrambler.go    # ECMA-130 LFSR table + Scramble()
  cue.go          # minimal cue parser (MODE1/2352, MODE2/2352, AUDIO)
  layout.go       # LBA <-> .scram byte-offset arithmetic
  builder.go      # BuildEpsilonHat()
  pack.go         # pack pipeline
  unpack.go       # unpack pipeline
  manifest.go     # JSON manifest + container framing
  xdelta3.go      # subprocess wrapper
  *_test.go
```

## Pack pipeline

Inputs: `bin`, `cue`, `scram`. Output: `out.miniscram`.

1. Parse `cue` into `[]Track{firstLBA, mode}`. Audio tracks marked so they
   bypass scrambling.
2. `stat(scram)` for total size.
3. **Auto-detect write offset.** Scan `.scram` past the leadin region for
   the first occurrence of the unscrambled sync field. Compute
   `writeOffsetBytes = firstSyncOffset − ((−150 − LBA_LEADIN_START) ×
   SECTOR_SIZE)`. Validate:
   - `writeOffsetBytes` is a multiple of 4 (sample-aligned).
   - Magnitude is plausible (within ±10 sectors).
   - Descrambling the candidate first-sync's 4-byte BCD MSF header yields
     `00:00:00` (LBA −150 in MSF). If not, abort — input is anomalous.
4. **Constant-offset check.** Sample syncs at start, middle, and end of the
   data region. Confirm `(syncOffset − leadinSize) % SECTOR_SIZE` is
   identical for all. If not, this is a variable-offset disc; abort.
5. **Build ε̂ in lockstep with `.scram`.** See section below. Returns the
   list of error sectors as a side effect.
6. Hash `bin` and `scram` (sha256, streamed).
7. Run `xdelta3 -e -9 -B <scramSize> -s ε̂ scram` to a temp file.
8. Build the manifest JSON (see schema below).
9. Write the container: magic + version + manifest length + manifest +
   Δ bytes.
10. **Verify.** Run the unpack pipeline on the freshly-written container
    into a second temp file; sha256 the result; assert it equals
    `manifest.scram_sha256`. On mismatch, delete the output and exit
    non-zero.
11. Remove temp files; rename the verified output to its final path.

## Unpack pipeline

Inputs: `bin`, `in.miniscram`. Output: `out.scram`.

1. Read the container header → manifest + Δ bytes (Δ to a temp file
   because xdelta3's `-d` requires a seekable source).
2. Hash `bin` and verify against `manifest.bin_sha256`. Abort on mismatch.
3. Build ε̂ to a temp file using the manifest's parameters (track layout,
   write offset, scram size, leadin LBA).
4. Run `xdelta3 -d -s ε̂ Δ` to the output file.
5. **Verify.** sha256 the output and compare against
   `manifest.scram_sha256`. On mismatch, delete the output and exit
   non-zero.

## ε̂ construction

Single-pass builder. Produces ε̂ to a temp file and returns
`(errorSectors []int32, err error)`.

### Constants

```go
const (
    LBA_LEADIN_START = -45150
    LBA_PREGAP_START = -150
    SECTOR_SIZE      = 2352
    SYNC_LEN         = 12
)
var SYNC = []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
                  0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}
```

### Scramble table

Generated once at startup by the LFSR (seed `0x0001`, polynomial
`x¹⁵ + x + 1`, ECMA-130 §16). Bytes 0..11 of the table are zero so XOR
leaves the sync field untouched; bytes 12..2351 hold the LFSR output.
Self-test at startup: `sha256(table) == hardcodedConstant`.

### Per-LBA byte offset

```go
func scramOffset(lba int32, writeOffsetBytes int) int64 {
    return int64(lba-LBA_LEADIN_START)*SECTOR_SIZE + int64(writeOffsetBytes)
}
```

### Build pass

```
totalLBAs = ceil((scramSize - writeOffsetBytes) / SECTOR_SIZE)
endLBA    = LBA_LEADIN_START + totalLBAs

if writeOffsetBytes > 0:
    write writeOffsetBytes zero bytes to ε̂
if writeOffsetBytes < 0:
    skip first |writeOffsetBytes| bytes of the first sector emission

for lba := LBA_LEADIN_START; lba < endLBA; lba++:
    sector := buildSectorForLBA(lba)
    writeToEpsilonHat(sector)

    // Lockstep correctness pre-check across the entire .scram.
    // Skip any LBA whose sector would extend past scramSize (truncated final).
    secOffset := scramOffset(lba, writeOffsetBytes)
    if lba >= bin_first_lba && lba < bin_first_lba + bin_sector_count
       && secOffset >= 0 && secOffset + SECTOR_SIZE <= scramSize:
        scramSec := readSectorFromScram(secOffset)
        if !bytes.Equal(sector, scramSec):
            errorSectors = append(errorSectors, lba)

truncate ε̂ to exactly scramSize bytes

if float64(len(errorSectors)) / float64(bin_sector_count) > 0.05:
    return error "layout mismatch — too many sectors differ; \
                  first 10 mismatched LBAs: ..."
```

The standard ceiling-division idiom `(scramSize - writeOffsetBytes +
SECTOR_SIZE - 1) / SECTOR_SIZE` implements the `ceil` above. The +/−
write-offset cases both round up so the trailing sector covers the file
end; the post-loop truncate trims any overhang back to `scramSize`.

### `buildSectorForLBA(lba)`

```
case lba in [LBA_LEADIN_START, LBA_PREGAP_START):
    return zero[2352]                       // matches Redumper's leadin convention
case lba in [LBA_PREGAP_START, bin_first_lba):
    return scrambleTable                    // scrambled-zero
case lba in [bin_first_lba, bin_first_lba + bin_sector_count):
    binSec := readSectorFromBin(lba - bin_first_lba)
    if track(lba).mode == AUDIO:
        return binSec                       // audio sectors are not scrambled
    else:
        return scramble(binSec)             // XOR bytes 12..2351 with table.
                                            // Same operation for MODE1/2352
                                            // and MODE2/2352 — scrambling is
                                            // mode-agnostic per ECMA-130.
case lba in [bin_first_lba + bin_sector_count, endLBA):
    return scrambleTable                    // scrambled-zero leadout
```

### Memory & I/O

One 2352-byte sector buffer per stream plus the 2352-byte scramble
table. Total RAM well under 100 KB. All file I/O is sequential; no random
access required during ε̂ construction.

## Output container & manifest

### Container framing

Single self-contained file:

| offset | size      | field                                |
| ------ | --------- | ------------------------------------ |
| 0      | 4         | magic = `"MSCM"`                     |
| 4      | 1         | format version = `0x01`              |
| 5      | 4         | manifest length N (big-endian uint32)|
| 9      | N         | manifest JSON (UTF-8)                |
| 9+N    | remainder | raw xdelta3 Δ bytes (VCDIFF)         |

Δ runs to EOF — no length prefix — so pack can stream xdelta3 stdout
directly into the container after writing the header.

### Manifest schema (JSON)

```json
{
  "format_version": 1,
  "tool_version": "miniscram 0.1.0",
  "created_utc": "2026-04-27T17:30:00Z",

  "scram_size": 897527784,
  "scram_sha256": "…",
  "bin_size": 791104608,
  "bin_sha256": "…",

  "write_offset_bytes": -48,
  "leadin_lba": -45150,

  "tracks": [
    {"number": 1, "first_lba": 0, "mode": "MODE1/2352"}
  ],
  "bin_first_lba": 0,
  "bin_sector_count": 336354,

  "error_sectors": [],
  "error_sector_count": 0,

  "delta_size": 12345,
  "scrambler_table_sha256": "…"
}
```

### Field rationale

- `scram_size`, `scram_sha256` — round-trip integrity check at unpack.
- `bin_*` — verified at the start of unpack so we fail fast if the wrong
  `.bin` is supplied.
- `write_offset_bytes`, `leadin_lba`, `tracks`, `bin_first_lba`,
  `bin_sector_count` — everything unpack needs to rebuild ε̂ identically
  without re-parsing `.cue`.
- `error_sectors` / `error_sector_count` — captured during the lockstep
  pre-check; meaningful provenance and matches Redumper's "Error Count"
  field.
- `delta_size` — quick stat without reading the file body.
- `scrambler_table_sha256` — pinned reference to the ECMA-130 LFSR table.

`error_sectors` is capped at 10,000 entries; beyond that the array is
omitted and only `error_sector_count` is kept.

## Round-trip invariants

Cryptographic / algebraic (always hold; the safety net):

1. `xdelta3 -d -s ε̂ Δ = ε`, byte-for-byte, regardless of how good ε̂ is.
2. `scramble(scramble(x)) == x`. Self-inverse XOR.
3. `sha256(scrambleTable) == hardcodedConstant`. Asserted at startup.
4. `sha256(scram_unpacked) == manifest.scram_sha256`.
5. `sha256(bin_passed_to_unpack) == manifest.bin_sha256`.

Layout (used as pack-time pre-checks; if any fails we abort cleanly):

6. **Lockstep pre-check** across every `.bin`-covered LBA. If >5% of
   sectors mismatch, our layout is wrong; abort.
7. Constant write offset across syncs sampled at start/middle/end of the
   data region.
8. Auto-detected first sync's BCD MSF header decodes to `00:00:00`.

Output quality (advisory):

9. If `len(Δ) > 0.10 × len(scram)`: warn. If `> 0.50 × len(scram)`: error.

The reconstruction is exact regardless of layout-detection bugs;
worst-case consequence is a fat Δ. The verify step at the end of pack
catches any deeper failure.

## Failure-mode summary

| What goes wrong                                          | Effect on output bytes                | Effect on Δ size                        |
| -------------------------------------------------------- | ------------------------------------- | --------------------------------------- |
| Write offset off by a few samples                        | None                                  | KB to MB                                |
| Write offset off by entire sectors                       | None                                  | Slightly larger                         |
| `.cue` parsed wrong / audio treated as data              | None                                  | Grows per wrong sector (~2.3 KB each)   |
| Catastrophically wrong layout                            | None                                  | Up to ~`gzip(scram)` size               |
| `xdelta3` produces a bad Δ                               | Caught by inline verify; pack aborts  | n/a                                     |
| `bin` modified between pack and unpack                   | Caught by `bin_sha256` check          | n/a                                     |

`xdelta3 -B <scramSize>` is passed at pack so even pathological
misalignment can find matches across the whole source window.

## Testing

### Unit tests

- Scramble table: LFSR sha256 equals hardcoded constant; first 12 bytes
  zero; spot-check against ECMA-130 §16.
- Scrambler self-inverse: `Scramble(Scramble(x)) == x` for 1000 random
  sectors.
- MSF/LBA helpers: `BCDMSFToLBA(0x00, 0x00, 0x00) == −150`,
  `BCDMSFToLBA(0x00, 0x02, 0x00) == 0`, plus values past the wrap.
- Cue parser: accepts `MODE1/2352`, `MODE2/2352`, and `AUDIO` track types
  and rejects everything else with a clear error. Golden tests on
  synthetic strings (single Mode 1 track, multi-track data+audio,
  multi-index, malformed inputs).
- Layout arithmetic: `scramOffset(−150, −48) == 105839952`, plus
  `+offset` and zero-offset cases.
- Manifest round-trip and container framing (including corrupted
  magic/version/length).

### Synthetic end-to-end

Generate a tiny fake disc in memory: 150 pregap sectors + 100 Mode 1
data sectors with valid sync/header + 10 leadout sectors. Build matching
fake `.bin` and `.scram` (with chosen write offset −48). Pack + unpack
and assert byte-identical. Second variant introduces one corrupted
"error" sector; assert `error_sector_count == 1` and round-trip is still
exact.

### Real end-to-end against Deus Ex (build-tagged)

Behind `//go:build redump_data` so CI can skip it without the dataset.
Pack `DeusEx_v1002f.{bin,cue,scram}`, unpack, assert byte-identical
recovery. Assert `len(Δ) < 0.01 × len(scram)` and
`manifest.error_sector_count == 0` (submission info reports Error
Count: 0).

### Runtime self-tests

Cheap; run at the start of every `pack` / `unpack` invocation:

- Scramble-table sha256 check.
- Sanity-check `LBA_LEADIN_START × SECTOR_SIZE` arithmetic doesn't
  overflow on the host.

### Out of scope for tests

- xdelta3 internals — trusted dependency; round-trip verify catches our
  misuse.
- Variable-offset discs — abort with clear error; no test fixtures.
- Mode 2 / mixed-mode real-world fixtures. Mode 2 is supported by the
  implementation (scrambling is mode-agnostic) and exercised by synthetic
  tests, but no Redumper Mode 2 dump is in the test corpus yet.
