# Native combined (multi-track-per-FILE) cuesheet support

Date: 2026-06-06
Status: approved (design)
Semver impact: MINOR (no container-format change; older readers reject cleanly)

## Problem

`ParseCue` rejects cuesheets where a single `FILE` contains more than
one `TRACK`:

```
FILE "Vampire_play.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    INDEX 00 68:21:24
    INDEX 01 68:23:24
```

This is the layout produced by MAME's `chdman createcd` / `extractcd`
round-trip: all tracks are merged into one combined `.bin`, and the cue
references that single file with absolute-MSF indexes. Redumper's native
output is one-TRACK-per-FILE, which miniscram already handles.

A real disc exhibiting this (`Vampire_play`, mixed MODE1 data + AUDIO)
fails at the first step:

```
resolving cue Vampire_play.cue ... FAIL FILE "Vampire_play.bin"
  contains more than one TRACK; multi-track-per-FILE cues are unsupported
```

### What is *not* broken

miniscram's scrambler, prediction, delta, and mixed-mode (data+audio)
handling are all fine. Given the equivalent one-TRACK-per-FILE cue and
the same `.scram`, pack→verify round-trips byte-exact (117 disagreeing
sectors → 2082 override records; all three scram hashes match). The only
gap is the parser/resolver's one-track-per-file assumption.

### Foundation validated on the real disc

The combined bin is the byte-exact concatenation of the per-track bins.
With the track boundary at the lowest INDEX MSF of track 2
(`68:21:24` = frame 307599 = byte 307599 × 2352 = 723472848, exactly the
size of `Vampire_play (Track 1).bin`):

| range (combined bin)        | SHA-1            | redumper-logged per-track SHA-1 |
|-----------------------------|------------------|---------------------------------|
| `[0 : 723472848]`           | `7e6817…db8c5c`  | `7e6817…db8c5c` (Track 1) ✓      |
| `[723472848 : EOF]`         | `f28721…a64927`  | `f28721…a64927` (Track 2) ✓      |

So hashing INDEX-derived byte ranges over the combined bin reproduces
redumper's per-track hashes exactly.

## Goal

Native ingest: `pack`/`unpack`/`verify` accept a combined cue + its
single combined `.bin` directly, with no preprocessing and no separate
per-track files required. Support is **general** — arbitrary track
counts and any mix of `MODE1/2352`, `MODE2/2352`, `AUDIO` within one
combined FILE. (All miniscram-accepted modes are 2352-byte sectors, so
offset math stays uniform.)

## Design

### Core idea

Generalize a `Track` from "a whole file" to "a byte range within a
file." The `Track` struct already carries `Filename`, `FirstLBA`,
`Size`, and `Hashes`; the only missing concept is a within-file offset.

"Combined" is **not** a mode flag. It is simply *any FILE that contains
more than one TRACK*. A single cue may mix single-track and multi-track
FILEs; the resolver treats each FILE group uniformly.

### 1. Data model (cue.go)

Add one field to `Track`:

```go
FileOffset int64 // byte offset of this track's data within Filename
```

A track is the range `[FileOffset, FileOffset+Size)` inside `Filename`.
Split cues set `FileOffset = 0` and `Size = whole file` — byte-identical
to today's behavior.

### 2. Parser — `ParseCue` (cue.go)

- Remove the "more than one TRACK per FILE" rejection.
- For each track, capture the **lowest INDEX's MSF** (INDEX 00 if
  present, else INDEX 01) as a file-relative frame offset. `parseMSF`
  already exists; its result is currently discarded and is now retained
  on the track (a transient parse-time field; see §3).
- Keep all existing validation: BINARY-only FILE type, reject
  path-bearing filenames (`/`, `\`, `..`), `validModes` whitelist.

### 3. Resolver — `ResolveCue` (cue.go)

Group consecutive tracks by FILE (a FILE boundary starts a new group).
Maintain `cumulativeFileLBA` across FILE groups, as today.

For each FILE group:
- **Single track** (split convention): `FileOffset = 0`, `Size = whole
  file size`, `FirstLBA = cumulativeFileLBA`. Identical to current code.
- **Multiple tracks** (combined): for each track in order,
  - `FileOffset = lowestINDEX_frames × SectorSize` (file-relative),
  - `Size = nextTrack.FileOffset − thisTrack.FileOffset`; the last
    track runs to the file's EOF,
  - `FirstLBA = cumulativeFileLBA + lowestINDEX_frames`. (For a single
    combined FILE this reduces to `lowestINDEX_frames`, which equals the
    split-cue cumulative-sectors value.)
- Advance `cumulativeFileLBA += fileSize / SectorSize`.

**Validation — reject the cue on any of:**
- first track in a FILE group does not start at offset 0,
- INDEX-derived offsets are not strictly monotonic,
- any offset or the file size is not a multiple of `SectorSize`,
- any offset is out of the file's bounds,
- the track spans do not tile the file exactly (gap or overlap).

`Files []ResolvedFile` still lists each real FILE exactly once, so
`OpenBinStream` (io.MultiReader over whole files) is unchanged — a
combined cue streams its single combined bin.

### 4. Hashing — `hashTrackFiles` → `hashTracks` (pack.go)

Hash **per track range** with `io.SectionReader(f, FileOffset, Size)`
rather than per whole file. This removes the `perTrack[i] ↔ files[i]`
1:1 assumption at pack.go:147–154; the call becomes per-track. Split
cues hash byte-identical content to today (validated in §Foundation).

### 5. Prediction / delta (pack.go, builder.go) — unchanged

`buildHatAndDelta` and the scrambler consume the concatenated byte
stream plus per-track `(FirstLBA, Mode)`. A combined bin satisfies this
natively, so no changes here. The `layoutMismatchAbortRatio` gate and
write-offset detection are unaffected.

### 6. Unpack / verify (unpack.go, verify.go)

Unpack rebuilds its file list from the container's track records. It
must now:
- **Dedup tracks that share a `Filename`** so the combined bin is opened
  once (today it appends one `ResolvedFile` per track).
- **Re-derive `FileOffset`** for each track by grouping tracks by
  `Filename` and accumulating per-track `Size` within the group.
- Verify per-track hashes over the re-derived `[FileOffset, FileOffset+
  Size)` ranges.

The scram reproduction path is otherwise unchanged.

### 7. Container format & semver — no chunk change

A combined container is just N track records that share one `Filename`.
`FileOffset` is **re-derived at unpack** (group-by-filename + cumulative
`Size`), so nothing new is serialized in MFST/TRKS/HASH/DLTA. The chunk
codec is untouched.

An older miniscram reading a combined container would open the shared
bin once per track and fail per-track / scram hash verification
**loudly** — a clean rejection, not silent corruption. Per the project
semver policy this is a **MINOR** change (new capability; older readers
reject cleanly).

**Defaulted decision:** re-derive `FileOffset` at unpack rather than
persisting it in TRKS, to keep zero format change. Persisting it would
also be additive but would touch the chunk codec; deferred unless a
future need arises.

## Testing

### Property test (centerpiece)

For a synthetic disc, `pack(split-cue)` and `pack(combined-cue)` must
produce containers that are **identical modulo `Filename` and
`FileOffset`**: same per-track `FirstLBA`, `Mode`, `Size`, and `Hashes`;
same delta bytes; same scram hashes. This is a clean agreement invariant
(an oracle property in the sense of CLAUDE.md "property tests are
first-class") and directly encodes the design's core claim.

### Resolver unit tests

- gap between track spans → reject,
- overlapping spans → reject,
- non-sector-aligned INDEX offset → reject,
- first track offset ≠ 0 → reject,
- INDEX 00 vs INDEX 01 boundary selection,
- all-AUDIO combined cue,
- 3+ tracks, mixed modes.

### E2E (`-tags redump_data`)

Add a fixture row that packs the `Vampire_play` combined cue and asserts
byte-exact scram round-trip, plus container equality (modulo filename/
offset) with the split-cue pack of the same disc.

## Out of scope (YAGNI)

- Cross-layout reproduction (pack combined → unpack to split files, or
  the reverse). Pack-combined → unpack-combined is symmetric; that is
  all we commit to.
- Non-2352 sector sizes.
- Splitting or regenerating bins on disk (`chdman`/redumper already do
  this; miniscram ingests what is present).

## Affected files

- `cue.go` — `Track.FileOffset`, `ParseCue` (accept multi-track FILE,
  capture lowest INDEX MSF), `ResolveCue` (per-FILE-group offset/size
  derivation + validation).
- `pack.go` — `hashTrackFiles` → per-track range hashing.
- `unpack.go` / `verify.go` — dedup shared bin, re-derive offsets,
  range-based verification.
- Tests — resolver units, split/combined agreement property test, E2E
  fixture row.
