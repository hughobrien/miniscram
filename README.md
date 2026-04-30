# miniscram

[![License: GPL-3.0](https://img.shields.io/badge/license-GPL--3.0-blue.svg)](./LICENSE)
[![Release](https://img.shields.io/github/v/release/hughobrien/miniscram?display_name=tag&sort=semver)](https://github.com/hughobrien/miniscram/releases)

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

### Pre-built binary

Download a release binary from
[Releases](https://github.com/hughobrien/miniscram/releases). Linux,
macOS, and Windows on amd64 are published; checksums are in
`SHA256SUMS`.

### `go install`

    go install github.com/hughobrien/miniscram@latest

### Nix flake

Run without installing:

    nix run github:hughobrien/miniscram -- pack disc.cue

Install into a profile:

    nix profile install github:hughobrien/miniscram

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

- **Copy protection:** none (`Error Count: 0` per the
  [redump entry](http://redump.org/disc/25966/)).
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
  Driver per the [redump entry](http://redump.org/disc/42536/).
  Thousands of sectors are deliberately corrupted as part of the
  protection scheme.
- **Why this disc:** demonstrates that miniscram captures intentional
  ECC errors as delta overrides — the protection's exact bytes flow
  through the container so `unpack` reproduces the protected disc
  verbatim.

```
$ ls -lh FL_v1.scram
-rwxr--r-- 1 hugh hugh 798M FL_v1.scram

$ miniscram pack FL_v1.cue
[02:38:39] running scramble-table self-test ... OK ok
[02:38:39] resolving cue FL_v1.cue ... OK 1 track(s), 729914976 bytes total
[02:38:39] detecting write offset ... OK -48 bytes
[02:38:39] checking constant offset ... OK ok
[02:38:39] hashing tracks ... OK 1 track(s) hashed
[02:38:42] hashing scram ... OK c98323550138
[02:38:46] building scram prediction + delta ... OK 2812 override(s), delta 7084781 bytes
[02:38:50] writing container ... OK FL_v1.miniscram
[02:38:50] reading manifest ... OK ok
[02:38:50] running scramble-table self-test ... OK ok
[02:38:50] reading container FL_v1.miniscram ... OK delta 7084781 bytes
[02:38:50] verifying bin hashes ... OK all tracks match
[02:38:53] building scram prediction ... OK ok
[02:38:55] applying delta ... OK 7084781 byte(s) of delta applied
[02:38:55] verifying scram hashes ... OK all three match
[02:38:59] removed source FL_v1.scram

$ ls -lh FL_v1.miniscram
-rw-rw-r-- 1 hugh hugh 1.5M FL_v1.miniscram
```

**2812 override records, 7 MB raw delta.** SafeDisc's corrupted
sectors and non-zero lead-in bytes can't be recomputed from the bin,
so they ride through the delta. zlib brings the 7 MB payload down to
~1.5 MB. 798 MB → 1.5 MB is still ~530× — a heavy protection costs
more than a clean disc but is still a substantial saving.

### Max Payne 2: The Fall of Max Payne — SecuROM (main-channel clean)

- **Copy protection:** SecuROM/LibCrypt per the
  [redump entry](http://redump.org/disc/10508/).
  Unlike SafeDisc, SecuROM/LibCrypt protection lives in the
  *subchannel*, not the main data sectors.
- **Why this disc:** demonstrates that miniscram *works fine* with
  SecuROM-protected games — "works fine" meaning *doesn't break
  them*. miniscram doesn't preserve the SecuROM subchannel itself
  ([out of scope](#out-of-scope)), but the main-channel `.scram`
  round-trips byte-equal exactly like any unprotected disc. For
  end-to-end preservation keep redumper's `_logs.zip` (which
  contains the subchannel) next to the `.miniscram`.

```
$ ls -lh MP2_Play.scram
-rwxr--r-- 1 hugh hugh 811M MP2_Play.scram

$ miniscram pack MP2_Play.cue
[02:47:09] running scramble-table self-test ... OK ok
[02:47:09] resolving cue MP2_Play.cue ... OK 1 track(s), 743253168 bytes total
[02:47:09] detecting write offset ... OK -48 bytes
[02:47:09] checking constant offset ... OK ok
[02:47:09] hashing tracks ... OK 1 track(s) hashed
[02:47:12] hashing scram ... OK 1424e03e4afd
[02:47:16] building scram prediction + delta ... OK 2390 override(s), delta 5864494 bytes
[02:47:20] writing container ... OK MP2_Play.miniscram
[02:47:20] reading manifest ... OK ok
[02:47:20] running scramble-table self-test ... OK ok
[02:47:20] reading container MP2_Play.miniscram ... OK delta 5864494 bytes
[02:47:20] verifying bin hashes ... OK all tracks match
[02:47:23] building scram prediction ... OK ok
[02:47:25] applying delta ... OK 5864494 byte(s) of delta applied
[02:47:25] verifying scram hashes ... OK all three match
[02:47:29] removed source MP2_Play.scram

$ ls -lh MP2_Play.miniscram
-rw-rw-r-- 1 hugh hugh 367K MP2_Play.miniscram
```

811 MB → 367 KB (~2200×). Smaller delta than Freelancer because
SecuROM doesn't corrupt main-channel sectors the way SafeDisc does;
the protection bytes that matter sit in `MP2_Play_logs.zip`, not in
the `.scram`.

### Deus Ex v1002f — clean Mode 1 baseline

- **Copy protection:** none per the
  [redump entry](http://redump.org/disc/14933/) (write offset −48).
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
record count (`u32 = 0`); everything else in the container is the
5-byte file header plus the four critical chunks (MFST, TRKS,
HASH, DLTA), with the irreducible cost dominated by the per-track
hash records. 856 MB → 673 bytes — about 1.3 million×, the
irreducible cost being the manifest itself.

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

## Container format (v2)

### File structure

A `.miniscram` file is laid out as:

    file header     5 bytes (magic + version)
    chunks          stream of length-prefixed, CRC-protected chunks

The four critical chunks (`MFST`, `TRKS`, `HASH`, `DLTA`) must each
appear exactly once. `MFST` is always first; the others may appear
in any order. PNG-style critical/ancillary case convention applies:
chunks whose 4-byte tag begins with an uppercase ASCII letter are
critical and must be understood; lowercase tags are ancillary and
may be safely skipped by readers that don't recognise them. v2
defines no ancillary chunks — the convention is reserved for
forward-compat additions.

### File header (5 bytes)

| Byte range | Field | Type | Notes |
|---|---|---|---|
| `[0, 4)` | `magic`   | 4 bytes | ASCII `"MSCM"` |
| `[4, 5)` | `version` | 1 byte  | `0x02` for v2  |

A reader rejects the container if the magic is wrong or the version
isn't `0x02`. There is no migration code: a binary built against v2
reads only v2. Users with older containers build the matching
historical commit.

### Chunk framing

Each chunk:

| Field | Type | Notes |
|---|---|---|
| `tag`     | 4 bytes        | FOURCC, e.g. `"MFST"` |
| `length`  | u32 BE         | Payload byte count    |
| `payload` | `length` bytes | Per-chunk format      |
| `crc32`   | u32 BE         | CRC-32/IEEE over `(tag \|\| payload)` |

Reader behaviour:

- Walks chunks until clean EOF after the last `crc32` trailer.
- Rejects any non-`DLTA` chunk whose `length` exceeds 16 MiB
  (matches MAME CHD's metadata cap; defends against `malloc(garbage)`
  if a corrupt length slips past the CRC against a hostile payload).
- Rejects any chunk whose CRC32 doesn't match.
- After the walk, verifies all four critical chunks were seen
  exactly once and `MFST` appeared first.

### `MFST` — manifest scalars

| Field | Type | Notes |
|---|---|---|
| `tool_version_len`     | u16 BE | Length of `tool_version` in bytes |
| `tool_version`         | bytes  | UTF-8, e.g. `"miniscram 1.0.0"` (no NUL terminator) |
| `created_unix`         | i64 BE | UTC seconds since the Unix epoch |
| `write_offset_bytes`   | i32 BE | Sync offset between bin and scram, signed |
| `leadin_lba`           | i32 BE | LBA where lead-in starts on disc, signed |
| `scram_size`           | i64 BE | Expected size of the reconstructed `.scram` |

### `TRKS` — track table

| Field | Type | Notes |
|---|---|---|
| `count`              | u16 BE  | Number of tracks                            |
| per track:           |         |                                              |
| &nbsp;`number`       | u8      | CD track number (1..99)                     |
| &nbsp;`mode_len`     | u8      | Length of `mode` in bytes                   |
| &nbsp;`mode`         | bytes   | ASCII, e.g. `"MODE1/2352"`, `"AUDIO"`       |
| &nbsp;`first_lba`    | i32 BE  | Absolute LBA where this track starts        |
| &nbsp;`size`         | i64 BE  | Byte length of this track's `.bin` file     |
| &nbsp;`filename_len` | u16 BE  | Length of `filename` in bytes               |
| &nbsp;`filename`     | bytes   | UTF-8 basename of the track's `.bin` (no path) |

### `HASH` — file hashes

Tagged sub-records — decouples hash storage from track structure so
new digest algorithms or hash targets are one entry, not a struct
change. A v2 container records `MD5 ` (note trailing space; algo
tags are exactly 4 bytes), `SHA1`, and `S256` for the scram and for
each track.

| Field | Type | Notes |
|---|---|---|
| `count`            | u16 BE        | Number of hash records                       |
| per record:        |               |                                              |
| &nbsp;`target`     | u8            | `0` = scram, `1..N` = 1-based track index    |
| &nbsp;`algo`       | 4 bytes ASCII | `"MD5 "`, `"SHA1"`, or `"S256"`              |
| &nbsp;`digest_len` | u8            | `16` for MD5, `20` for SHA1, `32` for SHA256 |
| &nbsp;`digest`     | bytes         | Raw binary digest                            |

A reader rejects: unknown `algo`, `digest_len` not matching the
algorithm's expected length, `target` greater than the number of
tracks, or trailing bytes after the declared count of records.

### `DLTA` — delta payload

`DLTA`'s payload is a `compress/zlib` `BestCompression` stream
verbatim. The chunk's `length` prefix delimits the delta exactly,
so the reader does not rely on a read-to-EOF heuristic.

Decompressed, the delta is a big-endian record sequence:

| Field | Type | Notes |
|---|---|---|
| `count`     | u32             | Number of override records              |
| `record[i]` | variable        | See below                               |

Each `record[i]`:

| Field | Type | Notes |
|---|---|---|
| `file_offset` | u64             | Byte offset within the recovered `.scram` |
| `length`      | u32             | Payload length, `1 ≤ length ≤ scram.size` |
| `payload`     | `length` bytes  | Bytes to write at `file_offset`           |

To reconstruct the `.scram`, a reader:
1. Reads bin files in cue order, scrambling all non-AUDIO tracks via ECMA-130 §15.
2. Synthesises leadin (zeros), pregap (Mode 1 zero sectors), and leadout (Mode 0 zero sectors) regions per ECMA-130 §14.
3. Concatenates everything into a buffer matching `MFST.scram_size`.
4. Applies each delta record by overwriting `length` bytes starting at `file_offset`.

The result must hash to the `HASH` chunk's `target=0` records.

## Acknowledgments

- **Jacob Hauenstein** — the original method paper,
  [*Compact Preservation of Scrambled CD-ROM Data*](https://doi.org/10.5121/ijcsit.2022.14401)
  (IJCSIT, August 2022), which inspired this work.
- **[redumper](https://github.com/superg/redumper)** — the upstream
  CD-ROM dumper miniscram is built around. The scrambler in
  `ecma130.go` is a near-verbatim Go port of redumper's
  implementation; per-file attribution is in source.
- **Redump.org community** — for the dumping standards and disc
  verification submissions that the demonstration fixtures (Deus Ex,
  Half-Life, Freelancer, Max Payne 2) come from.

## License

Copyright (C) 2026 Hugh O'Brien. Licensed under GPL-3.0 — see
[LICENSE](./LICENSE) for the full text and [NOTICE](./NOTICE) for
copyright + third-party attribution.

## Design history

Architecture, design rationale, and per-feature decisions live in
`docs/superpowers/specs/`. This README is the authoritative reference
for the wire format and external behaviour.
