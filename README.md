# miniscram

Compactly preserve scrambled CD-ROM dumps. miniscram stores a
[Redumper](https://github.com/superg/redumper) `.scram` file as a small
structured delta against the unscrambled `.bin`, so you keep the
original byte-for-byte but only store the parts that can't be
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

**Redumper-output CD-ROM dumps.** A safety net aborts pack if more
than 5% of disc sectors disagree with the bin-driven prediction —
that catches wrong-bin / wrong-cue / wrong-scram pairings and
malformed inputs.

## Demonstrations

Three real-disc fixtures exercise different parts of the pipeline.
Each is picked for what it stresses, not because of the game.

### Half-Life GOTY — mixed-mode hybrid CD

- **Copy protection:** none (`Error Count: 0` in the
  [redump submission](http://redump.org/disc/25966/)).
- **Why this disc:** 1 Mode 1 data track + 27 Red Book audio tracks.
  The audio dominates the disc surface and exercises the audio-bypass
  path of the scrambler (audio sectors are not descrambled — only the
  data track is).

```
$ ls -lh HALFLIFE.scram
-rwxr--r-- 1 hugh hugh 766M HALFLIFE.scram

$ miniscram pack HALFLIFE.cue
[02:27:02] running scramble-table self-test ... OK ok
[02:27:02] resolving cue HALFLIFE.cue ... OK 28 track(s), 695747472 bytes total
[02:27:02] detecting write offset ... OK -48 bytes
[02:27:02] checking constant offset ... OK ok
[02:27:02] hashing tracks ... OK 28 track(s) hashed
[02:27:05] hashing scram ... OK 78f21058c2c7
[02:27:08] building scram prediction + delta ... OK 2150 override(s), delta 5483541 bytes
[02:27:12] writing container ... OK HALFLIFE.miniscram
[02:27:12] reading manifest ... OK ok
[02:27:12] running scramble-table self-test ... OK ok
[02:27:12] reading container HALFLIFE.miniscram ... OK delta 5483541 bytes
[02:27:12] verifying bin hashes ... OK all tracks match
[02:27:15] building scram prediction ... OK ok
[02:27:17] applying delta ... OK 5483541 byte(s) of delta applied
[02:27:17] verifying scram hashes ... OK all three match
[02:27:20] removed source HALFLIFE.scram

$ ls -lh HALFLIFE.miniscram
-rw-rw-r-- 1 hugh hugh 337K HALFLIFE.miniscram
```

The 766 MB `.scram` is consumed and replaced by a 337 KB sidecar —
about 2300× smaller — in 18 seconds on a laptop. The round-trip
verification runs during pack so the `.scram` is only removed once
unpack has been proven byte-equal against the original.

### Freelancer — SafeDisc 2.70.030

- **Copy protection:** SafeDisc 2.70.030 + Macrovision Security
  Driver. 588 deliberately corrupted sectors per the protection
  scheme.
- **Why this disc:** demonstrates that miniscram captures intentional
  ECC errors as delta overrides. The protection's bytes flow through
  the container so `unpack` reproduces the protected disc verbatim.

*(Transcript pending.)*

### Deus Ex v1002f — clean Mode 1 baseline

- **Copy protection:** none ("None found [OMIT FROM SUBMISSION]" per
  [redump verification](http://forum.redump.org/post/128271/),
  write offset −48).
- **Why this disc:** the simplest case — a single Mode 1 data track,
  zero ECC/EDC errors. Establishes the lower bound: with no
  protection, no audio, and no errors, the bin fully predicts the
  scram and the delta is empty.

```
$ ls -lh DeusEx_v1002f.scram
-rwxr--r-- 1 hugh hugh 856M DeusEx_v1002f.scram

$ miniscram pack DeusEx_v1002f.cue
[02:30:13] running scramble-table self-test ... OK ok
[02:30:13] resolving cue DeusEx_v1002f.cue ... OK 1 track(s), 791104608 bytes total
[02:30:13] detecting write offset ... OK -48 bytes
[02:30:13] checking constant offset ... OK ok
[02:30:13] hashing tracks ... OK 1 track(s) hashed
[02:30:17] hashing scram ... OK 318c8497c2ca
[02:30:21] building scram prediction + delta ... OK 0 override(s), delta 4 bytes
[02:30:24] writing container ... OK DeusEx_v1002f.miniscram
[02:30:24] reading manifest ... OK ok
[02:30:24] running scramble-table self-test ... OK ok
[02:30:24] reading container DeusEx_v1002f.miniscram ... OK delta 4 bytes
[02:30:24] verifying bin hashes ... OK all tracks match
[02:30:28] building scram prediction ... OK ok
[02:30:30] applying delta ... OK 4 byte(s) of delta applied
[02:30:30] verifying scram hashes ... OK all three match
[02:30:34] removed source DeusEx_v1002f.scram

$ ls -lh DeusEx_v1002f.miniscram
-rw-rw-r-- 1 hugh hugh 673 DeusEx_v1002f.miniscram
```

**0 override records.** The 4-byte uncompressed delta is just the
record count (`u32 = 0`); everything else in the 673-byte container
is the binary header (41 bytes) plus the JSON manifest with track
hashes. 856 MB → 673 bytes — about 1.3 million×, the irreducible
cost being the manifest itself.

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
