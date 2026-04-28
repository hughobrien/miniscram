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
- Report progress clearly on stderr so a watching archivist can see what
  the tool is doing to their data at every step.
- Reclaim disk by default: once verification proves the `.scram` can be
  reconstructed exactly, remove it (opt out via `--keep-source` or
  `--no-verify`).
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

External runtime dependency: `xdelta3` on `PATH`.

### File discovery

Archivists typically work inside a per-disc directory where every
sidecar file shares a common stem (`DeusEx_v1002f.bin`,
`DeusEx_v1002f.cue`, `DeusEx_v1002f.scram`, …). Both `pack` and
`unpack` accept three input shapes accordingly:

1. **No positional arguments** — discover all inputs from the current
   directory.
2. **One positional argument** — interpreted as either a directory or
   a stem (with an optional path prefix). If the argument is an
   existing directory, discover inputs from there. Otherwise it is a
   stem (any known extension is stripped: `.bin`, `.cue`, `.scram`,
   `.miniscram`). The expected files are `<stem>.bin`, `<stem>.cue`,
   `<stem>.scram` for `pack`, or `<stem>.bin` and `<stem>.miniscram`
   for `unpack`.
3. **Full set of explicit paths** — three for `pack`
   (`<bin> <cue> <scram>`), two for `unpack` (`<bin> <in.miniscram>`).

Directory-discovery rules (cases 1 and 2 with a directory):

- Exactly one of each required extension → use those.
- Zero matches for any required extension → error: "no `.bin` file in
  `<dir>`; pass it explicitly".
- More than one match for any extension → error: "found 2 `.bin`
  files in `<dir>`: a.bin, b.bin; please specify explicitly". The tool
  does **not** try to pair files by stem when the directory contains
  multiple sets — the user disambiguates by passing the stem
  explicitly (case 2).

Stem-discovery rules (case 2 with a stem):

- Each `<stem>.<ext>` must exist. Missing files → error naming the
  exact path that was expected.

Default output paths (used when `-o` is omitted):

- `pack`: `<bin-stem>.miniscram` next to `<bin>`.
- `unpack`: `<miniscram-stem>.scram` next to `<in.miniscram>`.

If the resolved output path already exists, `pack` and `unpack` refuse
to overwrite it; pass `-f` / `--force` to overwrite. (Applies to
explicit `-o` paths too.)

Discovered paths are echoed in the status output so there is never
ambiguity about which files the tool is operating on.

Examples:

```
cd ~/dumps/DeusEx_v1002f && miniscram pack          # case 1: cwd
miniscram pack ~/dumps/DeusEx_v1002f                # case 2: directory
miniscram pack ~/dumps/DeusEx_v1002f/DeusEx_v1002f  # case 2: stem with path
miniscram pack DeusEx_v1002f                        # case 2: stem in cwd
miniscram pack DeusEx.bin DeusEx.cue DeusEx.scram   # case 3: explicit
```

### `miniscram --help`

```
miniscram — compactly preserve scrambled CD-ROM dumps alongside .bin images.

USAGE:
    miniscram <command> [arguments] [options]
    miniscram <command> --help
    miniscram --version

COMMANDS:
    pack       pack a .scram into a compact .miniscram container
    unpack     reproduce a .scram from .bin + .miniscram
    help       show this help, or 'miniscram help <command>'

ABOUT:
    miniscram stores the bytes of a .scram (Redumper's scrambled
    intermediate CD-ROM dump) as a small binary delta against the
    unscrambled .bin final dump. With this tool and the .bin, you
    can reproduce the original .scram byte-for-byte. Implements the
    method from Hauenstein, "Compact Preservation of Scrambled CD-ROM
    Data" (IJCSIT, August 2022), specialised for Redumper output.

REQUIRES:
    xdelta3 binary on PATH (e.g. apt install xdelta3)

EXIT CODES:
    0    success
    1    usage / input error (missing files, bad arguments)
    2    layout mismatch — >5% of bin sectors differ from .scram
         (typically: wrong .scram for this .bin, or unsupported disc)
    3    xdelta3 failed (encode or decode)
    4    verification failed (output deleted; source preserved)
    5    I/O error
    6    wrong .bin for this .miniscram (sha256 mismatch)
```

### `miniscram pack --help`

```
USAGE:
    miniscram pack [<bin> <cue> <scram>] [-o <out.miniscram>] [options]

ARGUMENTS (optional — discovered from the current directory if omitted):
    <bin>      path to the unscrambled CD image (Redumper *.bin)
    <cue>      path to the cue sheet (Redumper *.cue)
    <scram>    path to the scrambled intermediate dump (Redumper *.scram)

    With no arguments, looks in the current directory for exactly one
    *.bin, one *.cue, and one *.scram. If any extension matches more
    than one file, you must pass paths explicitly.

OPTIONS:
    -o, --output <path>    where to write the .miniscram container.
                           default: <bin-basename>.miniscram next to
                           <bin>.
    -f, --force            overwrite the output file if it already
                           exists. without this flag the tool refuses
                           to overwrite.
    --keep-source          do not remove <scram> after a successful
                           verified pack. By default the source .scram
                           is removed once verification proves it can
                           be reconstructed exactly from <bin> + <out>.
    --no-verify            skip the inline round-trip verification.
                           implies --keep-source — we will never
                           auto-delete a source whose recovery has not
                           been proven.
    --allow-cross-fs       permit auto-deletion of <scram> when <out>
                           is on a different filesystem. Default refuses
                           cross-filesystem deletes: a power-cut between
                           writing one disk and deleting another can
                           lose both copies.
    -q, --quiet            suppress progress output; errors still go
                           to stderr.
    -h, --help             show this help.

PIPELINE:
    0. discover <bin>, <cue>, <scram> from the current directory if
       not given on the command line.
    1. parse <cue> for track layout (MODE1/2352, MODE2/2352, AUDIO).
    2. auto-detect the disc's write offset from the first scrambled
       sync field in <scram>; verify the offset by descrambling the
       sync's BCD MSF header.
    3. sample syncs at start, middle, and end of the data region;
       confirm constant offset (variable-offset discs are not
       supported and will abort cleanly here).
    4. build a reconstructed scrambled image (ε̂) from <bin>,
       comparing against <scram> sector-by-sector to find error
       sectors. Aborts if >5% of sectors mismatch.
    5. run 'xdelta3 -e -9 -B <scramSize>' to produce a binary delta
       from ε̂ to <scram>.
    6. write the .miniscram container (header + manifest + delta).
    7. verify by unpacking the freshly-written container and
       comparing sha256(reconstructed) against sha256(original).
    8. (default) remove <scram>. suppressed by --keep-source or
       --no-verify, or if <out> is on a different filesystem and
       --allow-cross-fs was not passed.

EXAMPLES:
    # cd into a Redumper dump folder and let the tool figure it out
    cd ~/dumps/DeusEx
    miniscram pack

    # explicit paths with explicit output
    miniscram pack DeusEx.bin DeusEx.cue DeusEx.scram -o DeusEx.miniscram

    # keep both files (e.g., before deciding to commit a batch)
    miniscram pack --keep-source

    # write the container to a different volume and still auto-delete
    miniscram pack -o /mnt/archive/DeusEx.miniscram --allow-cross-fs
```

### `miniscram unpack --help`

```
USAGE:
    miniscram unpack [<bin> <in.miniscram>] [-o <out.scram>] [options]

ARGUMENTS (optional — discovered from the current directory if omitted):
    <bin>             path to the unscrambled CD image (Redumper *.bin)
                       — must be the same .bin used when packing
    <in.miniscram>    path to the .miniscram container produced by
                       'miniscram pack'

    With no arguments, looks in the current directory for exactly one
    *.bin and one *.miniscram.

OPTIONS:
    -o, --output <path>    where to write the reconstructed .scram.
                           default: <miniscram-basename>.scram next to
                           <in.miniscram>.
    -f, --force            overwrite the output file if it already
                           exists.
    --no-verify       skip sha256 verification of the reconstructed
                      .scram against the value recorded at pack time.
                      not recommended; the verification is what proves
                      you recovered the bytes correctly.
    -q, --quiet       suppress progress output; errors still go to
                      stderr.
    -h, --help        show this help.

PIPELINE:
    0. discover <bin> and <in.miniscram> from the current directory if
       not given on the command line.
    1. read the .miniscram header, manifest, and delta.
    2. hash <bin> and verify it matches manifest.bin_sha256; abort
       with exit code 6 if it does not — this would otherwise produce
       garbage output.
    3. rebuild ε̂ from <bin> using the layout parameters embedded in
       the manifest (no .cue required at unpack time).
    4. run 'xdelta3 -d' to apply the delta and produce <out.scram>.
    5. verify sha256(<out.scram>) == manifest.scram_sha256. on
       mismatch, delete <out.scram> and exit non-zero.

EXAMPLES:
    # discover from current directory, default output path
    cd ~/dumps/DeusEx
    miniscram unpack

    # explicit paths
    miniscram unpack DeusEx.bin DeusEx.miniscram -o DeusEx.scram
```

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
  reporter.go     # human-readable progress reporter (stderr)
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
11. `fsync` the `.miniscram` file. Rename to its final path. Remove temp
    files.
12. **Remove source** (default; suppressed by `--keep-source` or
    `--no-verify`). Confirm the output is on the same filesystem as the
    source `.scram` (same `st_dev`); if not, log a warning and skip
    deletion unless `--allow-cross-fs` was passed. Then `os.Remove(scram)`
    and log the freed bytes. Failure to delete is non-fatal — the
    `.miniscram` is already valid.

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

## Status reporting

Audience is digital preservationists who care intensely about what the
tool is doing to their data. Default output is human-readable progress
on stderr (so stdout stays clean for `miniscram unpack ... -o -` style
piping later if we want it). `--quiet` suppresses everything but errors.

### Reporter interface

```go
type Reporter interface {
    Step(label string) StepHandle           // begins a step, returns handle
    Info(format string, args ...any)        // one-off line
    Warn(format string, args ...any)
}

type StepHandle interface {
    Progress(current, total uint64)         // throttled to ≤1 update / 200ms
    Done(format string, args ...any)        // closes step with a result line
    Fail(err error)                         // closes step with ✗ and error
}
```

Implementations:

- `TermReporter` for interactive stderr (timestamps, dot-leader fill,
  ANSI progress bar via `\r`, ✓/✗ glyphs).
- `PlainReporter` for non-TTY stderr (no progress bars, no ANSI; one
  line per `Done`/`Fail`/`Info`).
- `QuietReporter` for `--quiet` (drops everything; `Fail` still bubbles
  the error up the call stack as usual).

Selection: TTY-detect on stderr, else `PlainReporter`.

### What gets reported

Every operation is wrapped in a `Step`. Concrete steps (in pack order):

| step                                     | progress?           |
| ---------------------------------------- | ------------------- |
| parse cue                                | no                  |
| stat scram                               | no                  |
| auto-detect write offset                 | no                  |
| constant-offset sync check               | no                  |
| hash bin (sha256)                        | yes (bytes hashed)  |
| hash scram (sha256)                      | yes (bytes hashed)  |
| build ε̂ + lockstep pre-check            | yes (sectors built) |
| run xdelta3 -e                           | yes (bytes consumed) |
| write container                          | no                  |
| verify (rebuild ε̂ + xdelta3 -d + sha256) | yes (sectors built) |
| remove source                            | no                  |

Final summary line on success, examples:

```
✓ pack complete in 47.3s — Δ 14.6 KiB (0.0016% of original .scram)
                            removed DeusEx_v1002f.scram (856 MiB)
```

```
✓ unpack complete in 38.1s — wrote DeusEx_v1002f.scram (856 MiB)
                              sha256 verified ✓
```

### Example `pack` output (interactive)

```
[17:31:04] parsing cue.................................... 1 track Mode 1 ✓
[17:31:04] stat scram..................................... 856 MiB
[17:31:04] auto-detecting write offset.................... -48 bytes (BCD MSF 00:00:00 ✓)
[17:31:04] sampling syncs for constant offset............. 3/3 match ✓
[17:31:04] hashing bin                            ███████░ 754 MiB sha256:0c1b78bf
[17:31:06] hashing scram                          ███████░ 856 MiB sha256:abcd1234
[17:31:08] building ε̂ + lockstep pre-check        ███████░ 381,602 sectors, 0 mismatches ✓
[17:31:42] running xdelta3 -e -9                          done Δ=14.2 KiB
[17:32:14] writing container                              14.6 KiB → DeusEx_v1002f.miniscram
[17:32:14] verifying round-trip                           sha256 matches ✓
[17:32:18] removing source DeusEx_v1002f.scram (856 MiB)  freed
✓ pack complete in 1m14s — Δ 14.6 KiB (0.0016% of original .scram)
```

The intent: at any moment a watching archivist can see exactly what the
tool is doing to their dump. The "removing source" line is loud and
obvious; failures at any step fail-fast with a `✗` and a non-zero exit.

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
