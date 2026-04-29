# miniscram

Compactly preserve scrambled CD-ROM dumps. miniscram stores a
[Redumper](https://github.com/superg/redumper) `.scram` file as a small
structured delta against the unscrambled `.bin`, so you keep the
original byte-for-byte but only pay for the parts that can't be
recomputed from the cuesheet and bins.

Inspired by [Hauenstein, *"Compact Preservation of Scrambled CD-ROM
Data"*](https://doi.org/10.5121/ijcsit.2022.14401) (IJCSIT, August
2022) — same core idea (delta against an unscrambled-bin prediction),
but with miniscram's own container format and override-record delta
encoding rather than the paper's xdelta3-over-DiscImageCreator
approach. Specialised for Redumper output.

## Install

    go install ./...    # produces ./miniscram

## CLI

### `pack`

Pack a `.scram` into a `.miniscram` container.

    miniscram pack disc.cue [-o out.miniscram] [-f] [--no-verify] [--keep-source]

Reads `disc.scram` (derived from the cue stem) and the `.bin` files
referenced by `disc.cue`. Writes `disc.miniscram` and removes
`disc.scram` after a verified round-trip.

### `unpack`

Reproduce the `.scram` from `.bin` + `.miniscram`.

    miniscram unpack disc.miniscram [-o out.scram] [-f] [--no-verify]

### `verify`

Non-destructive integrity check. Rebuilds the recovered `.scram` in a
temp file, hashes it, compares against the manifest, deletes the temp.

    miniscram verify disc.miniscram

### `inspect`

Pretty-print a container.

    miniscram inspect disc.miniscram [--full] [--json]

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | usage / input error |
| 2 | layout mismatch |
| 3 | verification failed |
| 4 | I/O error |
| 5 | wrong .bin for this .miniscram |

## What miniscram targets

**Redumper-output CD-ROM dumps.** Tested against real fixtures of
varying complexity:

- **Deus Ex** — clean Mode 1, single track.
- **Freelancer** — SafeDisc 2.70.030, 588 deliberately corrupted
  sectors per the redump.org submission. Round-trips byte-equal.
- **Half-Life** — multi-FILE cue, 1 Mode 1 + 27 audio tracks.

A safety net aborts pack if more than 5% of disc sectors disagree with
the bin-driven prediction — that catches wrong-bin / wrong-cue /
wrong-scram pairings and malformed inputs.

### Things that should work, untested

- **Mode 2/2352 data tracks** (CD-i, VCD, PSX-XA Form 2). The
  scrambler treats Mode 1 and Mode 2 identically.
- **Audio-only discs.** The disc round-trips, but ~150 pregap
  sectors get baked into the delta as overrides (~350 KiB extra
  noise) because pregap is synthesised as Mode 1 zero sectors.

### Refuses or under-performs

- **Variable write offset** — refused; miniscram can't reconstruct a
  varying offset.
- **Layout mismatch > 5%** — refused.
- **Cuesheets with multi-track-per-FILE** — rejected by the parser.
  Redumper produces one TRACK per FILE; convert from DiscImageCreator
  or IsoBuster output beforehand.
- **Modes other than `MODE1/2352`, `MODE2/2352`, `AUDIO`** — rejected.
- **Discs with non-zero pregap or leadout** (CD+, Enhanced CDs) —
  pregap/leadout is synthesised as zero sectors; real content becomes
  delta overrides. Functional but inflates the delta.
- **Non-zero lead-in.** Lead-in (LBAs -45150 to -150) is filled with
  zeros. SafeDisc / SecuROM dumps have non-zero lead-in data; those
  bytes flow through the delta. This is why protected-disc deltas are
  measured in MiB rather than KiB.

### Out of scope

- **DVD / Blu-ray.** Different sector format, not addressable by
  miniscram's ECMA-130 pipeline.
- **Multi-session CDs with non-trivial second sessions.** The cuesheet
  parser doesn't model session boundaries.
- **Subchannel data.** Main channel only. PSX libcrypt-class
  protection lives in subchannel and is invisible to miniscram.
  Redumper preserves it in the `*_logs.zip` it produces alongside the
  `.scram`; keep that bundle next to the `.miniscram`.

## Container format (v1)

### File structure

A `.miniscram` file is laid out as:

    binary header   41 bytes (fixed)
    manifest body   variable (JSON)
    delta payload   variable (binary)

### Binary header (41 bytes)

| Byte range | Field | Type | Notes |
|---|---|---|---|
| `[0, 4)`   | `magic` | 4 bytes | ASCII `"MSCM"` |
| `[4, 5)`   | `version` | 1 byte | `0x01` for v1 |
| `[5, 37)`  | `scrambler_table_sha256` | 32 bytes | Raw SHA-256 of the ECMA-130 scramble table |
| `[37, 41)` | `manifest_length` | u32 BE | Byte count of the manifest body |

A reader rejects the container if the magic is wrong, the version
isn't `0x01`, or `scrambler_table_sha256` doesn't match the reader's
own ECMA-130 implementation.

### Manifest (JSON)

UTF-8 encoded JSON object. All fields are required.

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

Field semantics:

- `write_offset_bytes`: signed bytes; positive = scram leads, negative = scram lags. Always a multiple of 4 (audio sample alignment).
- `leadin_lba`: integer LBA where the scram file begins on disc. Real Redumper output uses `-45150`; truncated test fixtures may use higher values.
- `scram.size`: byte length of the original `.scram`.
- `scram.hashes`: lowercase hex MD5, SHA-1, SHA-256 of the original `.scram`.
- `tracks[*].number`: 1-indexed track number per the cuesheet.
- `tracks[*].mode`: one of `"MODE1/2352"`, `"MODE2/2352"`, `"AUDIO"`.
- `tracks[*].first_lba`: absolute LBA where the track's `.bin` begins on disc (cumulative file size in sectors).
- `tracks[*].filename`: basename of the track's `.bin`. No paths.
- `tracks[*].size`: byte length of the track's `.bin`. Always a multiple of `2352`.
- `tracks[*].hashes`: lowercase hex MD5, SHA-1, SHA-256 of the track's `.bin`.

### Delta payload (binary)

Begins immediately after the manifest body, as a `compress/zlib`
`BestCompression` stream. Decompressed, the layout is the big-endian
record sequence below.

| Field | Type | Notes |
|---|---|---|
| `count` | u32 | Number of override records |
| `record[i]` | variable | See below |

Each `record[i]`:

| Field | Type | Notes |
|---|---|---|
| `file_offset` | u64 | Byte offset within the recovered `.scram` |
| `length` | u32 | Payload length, `1 ≤ length ≤ scram.size` |
| `payload` | `length` bytes | Bytes to write at `file_offset` |

To reconstruct the `.scram`, a reader:
1. Reads bin files in cue order, scrambling all non-AUDIO tracks via ECMA-130 §15.
2. Synthesises leadin (zeros), pregap (Mode 1 zero sectors), and leadout (Mode 0 zero sectors) regions per ECMA-130 §14.
3. Concatenates everything into a buffer matching `scram.size`.
4. Applies each delta record by overwriting `length` bytes starting at `file_offset`.

The result must hash to `scram.hashes`.

## License

GPL-3.0 — see [LICENSE](./LICENSE).

Some routines are adapted from
[redumper](https://github.com/superg/redumper) (also GPL-3.0). The
scrambler in `ecma130.go` in particular is a near-verbatim port of
redumper's canonical implementation. Attribution is noted at each lift
point in source.

## Design history

Architecture, design rationale, and per-feature decisions live in
`docs/superpowers/specs/`. This README is the authoritative reference
for the wire format and external behaviour.
