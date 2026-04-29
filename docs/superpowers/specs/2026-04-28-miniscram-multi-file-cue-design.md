# miniscram multi-FILE cue support — multi-track + per-track hashes + CLI simplification

Add multi-FILE `.cue` support so miniscram can pack and unpack Redumper
dumps that store one `.bin` per track (the standard multi-track layout —
HL1, most pre-2000 game CDs with audio tracks). Bundle in a redump.org-
template-parity manifest schema (per-track hashes + whole-disc roll-up)
and a CLI simplification (drop cwd-discovery; every subcommand takes a
single explicit positional).

This corresponds to TASKS.md item **B1.5** (the implicit prerequisite
flagged in the B1 cycle, not yet a TASKS entry) and unblocks **B3**
(multi-track + audio fixture).

## Goals

- Pack and unpack Redumper-style multi-FILE `.cue` files (one `FILE`
  per `TRACK`, per-track `.bin` files in the cue's directory). HL1
  (Track 01 Mode 1 + Tracks 02-28 AUDIO) is the immediate test case.
- Match Redumper submission templates: per-track md5/sha1/sha256 in
  the manifest, computed in a single I/O pass per track file. Adds
  to C1's whole-file hashes rather than replacing them — top-level
  roll-up persists as bytewise concat hash for cross-checking.
- Make miniscram's manifest self-describing for unpack: per-track
  filename + size in the manifest, so unpack/verify discover bin
  files in the container's directory without needing the cue.
- Simplify the CLI: every subcommand takes one explicit positional
  (the cue, or the .miniscram). Drop cwd-discovery and the
  multi-positional shapes. Cleaner UX, less code, cleaner tests.

## Non-goals

- **Reading v3 containers.** v3 → v4 bump uses the same migration-
  error pattern. Per user, no extant containers need preserving.
- **Pack producing a multi-FILE manifest from a single-FILE cue.**
  Single-FILE cues still work (the `FILE` line names one file; tracks
  array has one entry; behavior is back-compatible from the user's
  perspective).
- **Reading cues with relative-path traversal** (`FILE "../foo.bin"`).
  Redumper never produces these; reject as malformed.
- **Cue files with non-`BINARY` FILE types** (WAVE, MP3, MOTOROLA).
  Redumper always uses BINARY; reject anything else.
- **Per-FILE pregap synthesis.** miniscram preserves whatever bytes
  the per-track .bin files contain. Pregap silence in audio tracks
  is recorded as part of the track's file (Redumper convention).
  No special handling needed.
- **Multi-track-per-FILE cues.** Cues where a single FILE contains
  multiple TRACKs (with INDEX 01 markers identifying internal
  boundaries) are rejected as unsupported. Redumper always produces
  one TRACK per FILE for multi-track discs; the multi-track-per-FILE
  shape is a non-Redumper convention we don't need to support.
- **Renaming track files at unpack.** Unpack reads from the recorded
  filename and writes a single .scram. The original per-track .bin
  files are not reconstructed — that's the user's archive bundle's
  job (Redumper output stays alongside).

## Manifest schema (format_version 4)

```go
type Track struct {
    Number   int    `json:"number"`
    Mode     string `json:"mode"`        // "MODE1/2352", "MODE2/2352", "AUDIO"
    FirstLBA int32  `json:"first_lba"`   // absolute LBA where the track's FILE begins (not where INDEX 01 begins)
    Size     int64  `json:"size"`        // bytes in this track's .bin file
    Filename string `json:"filename"`    // basename of source .bin (no path)
    MD5      string `json:"md5"`         // lowercase hex, computed at pack
    SHA1     string `json:"sha1"`
    SHA256   string `json:"sha256"`
}
```

Top-level Manifest fields unchanged in shape; `BinMD5/SHA1/SHA256`
become the whole-disc roll-up (hash of `concat(track1.bin, track2.bin,
…)` in cue order). For single-FILE discs the per-track entry's hash
equals the top-level roll-up — redundant but consistent.

`format_version` JSON field becomes `4`. `containerVersion` byte
becomes `0x04`. v0.3 rejected with:

```
unsupported container version 0x03 (this build expects 0x04);
v0.3 .miniscram files cannot be read directly by this build —
re-pack from the original .bin
```

## Cue parsing

`ParseCue(r io.Reader)` stays a pure parser (no filesystem I/O). The
parser:

- Captures each `FILE` line and attaches its filename to the `TRACK`
  entries that follow until the next `FILE`.
- **Rejects** cues where a single `FILE` contains more than one
  `TRACK`. This is the multi-track-per-FILE shape miniscram doesn't
  support; Redumper output never produces it.
- **Rejects** non-`BINARY` FILE types and filenames containing path
  separators or `..`.
- **Discards** INDEX 01's MSF value. The within-file INDEX 01 offset
  is not needed: with one TRACK per FILE, the track's file is its
  data unit, and pregap (e.g., 150 sectors of audio silence at the
  start of an audio track's file) is handled implicitly by the file
  ownership — `trackModeAt(pregap_LBA)` returns the file's track's
  mode (e.g., AUDIO), which is what the predictor needs.

```go
func ParseCue(r io.Reader) ([]Track, error)
```

Returns Tracks with `Number`, `Mode`, `Filename` populated. `FirstLBA`
is left at zero; `ResolveCue` populates it from cumulative file sizes.
`Size`, `MD5`, `SHA1`, `SHA256` are populated downstream (Resolve and
Pack respectively).

## Cue resolution and bin reader

A new helper bridges parsing and filesystem:

```go
type CueResolved struct {
    Tracks []Track        // with FirstLBA, Size, Filename populated
    Files  []ResolvedFile // ordered list of bin files to stream in cue order
}

type ResolvedFile struct {
    Path string // absolute path (cue dir + basename)
    Size int64  // bytes
}

// ResolveCue parses cuePath, resolves each FILE entry's absolute path
// relative to cuePath's directory, stats each file for size, and
// computes each Track's absolute FirstLBA as the cumulative sum of
// prior files' sector counts (Track.FirstLBA = sum of bytes-of-prior-
// files / SectorSize). Returns ready-to-use Tracks + ordered file
// list for streaming.
func ResolveCue(cuePath string) (CueResolved, error)
```

For Redumper one-TRACK-per-FILE output, `Track.FirstLBA` is the LBA
where the track's file begins on the disc — which is also where the
predictor must start treating that track's mode as authoritative.
Pregap regions inside a track's file (e.g., 150 sectors of silence
at the start of an audio track's file) are part of the track's mode
range, exactly what the file-ownership semantics produce.

For single-FILE cues the result has one entry in `Files` and the
existing single-bin behavior is preserved. For multi-FILE cues,
`Files` contains all per-track .bin files in cue order.

The bin stream is constructed via `io.MultiReader` over `os.Open`
results — one open per file, sequential read. Total size =
sum of file sizes; matches the .scram size for round-trip.

```go
// OpenBinStream opens all bin files in cue order and returns a
// streaming reader plus a Close func that closes every underlying
// file. Caller must call Close.
func OpenBinStream(files []ResolvedFile) (io.Reader, func() error, error)
```

(Note: `io.MultiReader` doesn't compose Close. The returned closer
walks the underlying files and closes each.)

## Pack flow

`pack <cue>` (one positional only):

1. `ResolveCue(cuePath)` → tracks + ordered file list.
2. **Single hashing pass** over all track files (same shape as
   unpack's step 4): for each file, open and stream via a MultiWriter
   that fans out to per-track md5/sha1/sha256 + disc-level
   md5/sha1/sha256 simultaneously. At each file's EOF: snapshot
   per-track hashes into the Track entry, reset per-track hashers
   for the next file (or instantiate fresh ones), close the file,
   continue. Disc-level hashers keep accumulating across all files.
   After the last file: snapshot the roll-up into top-level
   `BinMD5/SHA1/SHA256`. One I/O pass per track file.
3. Hash the .scram via the existing `hashFile(scramPath)`.
4. `OpenBinStream` for the actual ε̂+delta build (second I/O pass on
   the bin files; the first was hashing).
5. `BuildEpsilonHatAndDelta` consumes the multi-bin reader exactly
   as it consumes a single-file reader today — no internal change
   required.
6. Write container with `FormatVersion: 4`, populated Track entries,
   top-level whole-disc roll-up, scram hashes.

Two I/O passes over the bin files is acceptable: pass 1 is hashing,
pass 2 is the predictor. Disk caches keep the second pass fast.

## Unpack flow

`unpack <miniscram>` (one positional only):

1. `ReadContainer(containerPath)` → manifest.
2. Reject if `manifest.FormatVersion != 4` or container version byte
   != 0x04 (existing rejection path; updated message for v3→v4).
3. For each track in `manifest.Tracks`: resolve `<container-dir>/
   <track.Filename>`. Stat the file; verify size matches `track.Size`
   before opening (cheap check; protects against truncated files).
4. **Single hashing pass** over all track files: for each file, open
   and stream through per-track hashers AND the disc-level roll-up
   hashers in parallel via a shared MultiWriter. At each file's EOF,
   snapshot per-track hashes and compare to `track.MD5/SHA1/SHA256`
   (early-fail with `errBinHashMismatch`); the disc-level hashers
   keep accumulating across files. After the last file, snapshot the
   roll-up and compare to top-level `BinMD5/SHA1/SHA256`.
5. **Rebuild pass:** `OpenBinStream` over the same files; feed
   `BuildEpsilonHat`; apply delta.
6. Hash recovered scram via `hashFile`; compare against
   `manifest.ScramMD5/SHA1/SHA256` (`errOutputHashMismatch` on
   mismatch, exit 3).

Net: two I/O passes over the bin files (one for hashing, one for
rebuild). The per-track and roll-up hash computations share a single
pass via fan-out to multiple `hash.Hash` instances.

## Verify flow

`verify <miniscram>` (one positional only):

Identical to unpack except the recovered scram lands in a tempfile
(per the existing Verify pattern), is hashed, then deleted. Per-track
+ roll-up bin hashes verified in the single hashing pass before the
ε̂ rebuild starts; a wrong-file or corrupt-track-file fast-exits 5
before the expensive rebuild.

A new helper `hashReader(r io.Reader) (FileHashes, error)` is
extracted from `hashFile` so the same logic can be applied to the
multi-bin stream and to single readers without duplicating code.
`hashFile(path)` becomes a thin wrapper that opens, defers close,
and calls `hashReader`.

## CLI surface

```
miniscram pack    <cue>        [-o <out>] [-f] [--keep-source] [--no-verify] [--allow-cross-fs] [-q] [-h]
miniscram unpack  <miniscram>  [-o <out>] [-f] [--no-verify] [-q] [-h]
miniscram verify  <miniscram>                      [-q] [-h]
miniscram inspect <miniscram>  [--full] [--json]   [-h]
```

All flags survive their existing semantics. The single positional is
mandatory; zero-arg or multi-arg input is a usage error.

`discover.go` is deleted entirely. Helpers (`DiscoverPack`,
`DiscoverUnpack`, `DiscoverPackFromArg`, `DiscoverUnpackFromArg`,
`uniqueByExt`, `stripKnownExt`) are removed; nothing references them.

`DefaultPackOutput(cuePath)` and `DefaultUnpackOutput(containerPath)`
move into `pack.go` / `unpack.go` (or wherever else simpler) — they're
trivial and don't justify a separate file.

`DefaultPackOutput`'s shape changes: was `<bin-stem>.miniscram` next
to bin; becomes `<cue-stem>.miniscram` next to cue. (Equivalent for
single-FILE Redumper output where `<stem>.bin` and `<stem>.cue` share
a stem.)

## Files touched

| File | Action |
| --- | --- |
| `cue.go` | Extend `Track` struct (5 new fields). Extend `ParseCue` to capture FILE/track association and reject non-BINARY FILEs. Add `CueFile`, `CueResolved`, `ResolvedFile`, `ResolveCue`, `OpenBinStream`. |
| `manifest.go` | Bump `containerVersion` to 0x04. Update v0.3→v0.4 migration message. (Track struct lives in cue.go but is referenced from Manifest; the JSON tags on Track are what serialize.) |
| `pack.go` | Replace `BinPath` arg with cue-driven flow. Drop `binSize`/`binSHA` single-file logic. Hash each track file in one I/O pass; populate per-track entries; compute disc-level roll-up. Update `verifyRoundTrip`. Update `PackOptions` shape. |
| `unpack.go` | Replace `BinPath` with track-resolution from manifest. Verify per-track + roll-up. Stream bin via `OpenBinStream`. |
| `verify.go` | Same shape changes as unpack — VerifyOptions drops BinPath; ContainerPath is the only input. |
| `main.go` | Rewrite `runPack`, `runUnpack`, `runVerify` to take single positional. Drop multi-arg/zero-arg shapes. Update flag descriptions. |
| `help.go` | Rewrite all four subcommand help texts for the new CLI surface. Top-level COMMANDS list unchanged. |
| `discover.go` | Delete entirely. |
| `inspect.go` | Update `formatHumanInspect` to show per-track hashes + sizes + filenames. Update `formatJSONInspect` if needed (Track fields now serialize naturally via JSON tags). |
| All `*_test.go` | Discovery-related tests deleted. New tests for multi-FILE cue parsing, multi-bin reader, multi-track pack/unpack/verify with synthetic fixtures, v0.3 rejection. |

## Testing

Synthetic fixtures (no Redumper data needed):

- `TestParseCue_MultiFile` — cue with 3 FILE entries, 3 TRACK
  entries, returns the right tracks + files associations.
- `TestParseCue_RejectsNonBinaryFile` — `FILE "x.wav" WAVE` errors.
- `TestParseCue_RejectsRelativeTraversal` — `FILE "../bad.bin"`
  errors.
- `TestResolveCue_ComputesAbsoluteLBAs` — multi-FILE cue, verify
  Track[i].FirstLBA matches sum of prior file sectors + INDEX 01
  offset.
- `TestOpenBinStream_ReadsConcatenated` — write 3 small files,
  open as stream, verify bytes are concatenated correctly; verify
  Close closes every underlying file.
- `TestPackMultiFileSynthDisc` — synthesize a 3-track disc (data +
  audio + data) by writing 3 .bin files + a multi-FILE .cue, pack,
  inspect manifest: 3 track entries with per-track hashes; round-trip
  byte-equal.
- `TestUnpackMultiFileSynthDisc` — pack a 3-file disc, unpack to a
  fresh dir (with the per-track files placed there by the test),
  verify byte-equal recovered scram.
- `TestUnpackRejectsTrackFileSizeMismatch` — pack, mutate one
  track's `.bin` size on disk before unpack, expect
  `errBinHashMismatch` (size doesn't match `Track.Size`).
- `TestUnpackRejectsPerTrackHashMismatch` × 3 — per-track md5/sha1/
  sha256 each tampered separately; each fails fast with
  `errBinHashMismatch` before the ε̂ rebuild.
- `TestReadContainerRejectsV3` — hand-built v0.3 container produces
  the v0.3→v0.4 migration error.
- `TestCLISinglePositional` — pack/unpack/verify all reject
  zero-positional, two-positional, three-positional input as
  exit 1.

E2E (real disc, build-tagged):

- HL1 row added to `realDiscFixtures` in `e2e_redump_test.go`:
  `{Name: "half-life", Dir: "test-discs/half-life",
  Stem: "HALFLIFE", ExpectedDataTrackErrors: 0,
  MaxDeltaBytes: 10240, MaxContainerBytes: 20480, EDCSampleLBAs:
  [...]}`. Round-trip exercises the multi-FILE bin path. Audio
  tracks should produce a near-empty delta (audio sectors are
  byte-identical between bin and scram, so the predictor emits
  them verbatim).

Existing tests adapted:
- `TestPackCleanDisc`, `TestUnpackRoundTripSynthDisc`, etc. all
  rewrite their setup to use `pack <cue>` shape and a single-FILE
  cue. The synthetic disc helper (`writeSynthDiscFiles`) writes
  a single-FILE .cue.
- `TestCLIPackDiscovers`, `TestCLIVerifyDiscovery`, and any test
  whose name contains "Discovery" or that tests the cwd/stem
  resolution paths: deleted.
- `packForVerify` in `verify_test.go` updates to the new pack
  signature (cue, scram, output — no separate bin).

## Errors / behavior

| Condition | Sentinel | Exit |
| --- | --- | --- |
| Cue references a missing track .bin | new `errMissingTrackFile` | 4 (I/O) |
| Track .bin size doesn't match manifest Track.Size | `errBinHashMismatch` | 5 |
| Per-track hash mismatch | `errBinHashMismatch` | 5 |
| Disc-level roll-up mismatch | `errBinHashMismatch` | 5 |
| Recovered scram hash mismatch | `errOutputHashMismatch` | 3 |
| Cue parse error (bad FILE type, traversal, malformed) | wrapped, surfaces verbatim | 1 (usage) |
| v0.3 container | new migration error | 4 |
| Wrong number of positionals | usage error | 1 |

## File/LOC summary

| File | Approx ΔLOC |
| --- | --- |
| `cue.go` | +200 (multi-FILE parser, ResolveCue, OpenBinStream) |
| `manifest.go` | +5 (version bump + message) |
| `pack.go` | -40 / +80 (drop single-bin shortcut; per-track + roll-up hashing) |
| `unpack.go` | -30 / +60 (drop single-bin; per-track verification) |
| `verify.go` | -10 / +5 (mostly mechanical; trickle-down from PackOptions/UnpackOptions changes) |
| `main.go` | -100 / +40 (drop multi-positional handling) |
| `help.go` | -20 / +20 (rewrite help texts) |
| `discover.go` | deleted (-127) |
| `inspect.go` | +30 (display per-track hashes) |
| Test files | net + ~300 (new multi-FILE coverage; delete discovery tests) |

Net: roughly +400 LOC in production code, +300 in tests, -127 in
discovery.go. Total cycle effort: ~1.5 days.

## Out of scope (deferred to later items)

- **HL1 audio-specific assertions** beyond byte-equal round-trip
  and the existing data-track-error count helper (which only
  applies to Mode 1 tracks). Audio sectors don't have EDC/ECC; the
  e2e suite naturally skips them.
- **Reconstructing per-track `.bin` files at unpack.** Unpack still
  produces one `.scram` (matching the recovered Redumper-style
  scrambled image). If anyone ever needs per-track recovery, that's
  a separate "splitting" feature.
- **Cue rewriting / generation.** miniscram never generates cues;
  it consumes the user's existing one.
- **Disc-multi-bin Mode 2 / XA path** (TASKS.md B4). Synthetic
  multi-track Mode 2 fixture is a separate cycle.
- **Tighter HL1 fixture bounds** post-first-run (see B1 cycle
  precedent — initial bounds generous, tighten after measurement).
