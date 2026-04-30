# miniscram

[![License: GPL-3.0](https://img.shields.io/badge/license-GPL--3.0-blue.svg)](./LICENSE)
[![Release](https://img.shields.io/github/v/release/hughobrien/miniscram?display_name=tag&sort=semver)](https://github.com/hughobrien/miniscram/releases)

Shrink [Redumper](https://github.com/superg/redumper) `.scram` files
from ~800 MB to between a few hundred bytes and a couple of MB —
without losing a byte. miniscram stores only the delta between the
original `.scram` and a scramble predicted from the unscrambled
`.bin`. Round-trip byte-equality is verified at pack time; the
source `.scram` is only deleted after `unpack` reproduces it exactly.

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

Four real-disc fixtures exercise different parts of the pipeline.
Each is picked for what it stresses, not because of the game.

### Freelancer — SafeDisc 2.70.030

- **Copy protection:** SafeDisc 2.70.030 + Macrovision Security
  Driver per the [redump entry](http://redump.org/disc/42536/).
  588 sectors are deliberately corrupted as part of the protection
  scheme.
- **Why this disc:** demonstrates that miniscram captures intentional
  ECC errors as delta overrides — the protection's exact bytes flow
  through the container so `unpack` reproduces the protected disc
  verbatim.

Full end-to-end demo: `sha256sum` the original, pack (which consumes
the `.scram`), inspect the container, unpack to restore, then
`sha256sum` again to prove byte-equality.

```
$ ls FL_*
FL_v1 (Track 0).bin
FL_v1.bin
FL_v1.cue
FL_v1.scram
FL_v1_logs.zip

$ sha256sum FL_v1.scram
c9832355013839c6a539124c1794bf3567410a64002bfabc58a64058e81a9761  FL_v1.scram

$ miniscram pack FL_v1.cue
running scramble-table self-test ... OK ok
resolving cue FL_v1.cue ... OK 1 track(s), 729914976 bytes total
detecting write offset ... OK -48 bytes
checking constant offset ... OK ok
hashing tracks ... OK 1 track(s) hashed
hashing scram ... OK c98323550138
building scram prediction + delta ... OK 2812 disagreeing sector(s) → 45927 override record(s), 0 pass-through(s), delta 7084781 bytes
writing container ... OK FL_v1.miniscram
reading manifest ... OK ok
running scramble-table self-test ... OK ok
reading container FL_v1.miniscram ... OK delta 7084781 bytes
verifying bin hashes ... OK all tracks match
building scram prediction ... OK ok
applying delta ... OK 7084781 byte(s) of delta applied
verifying scram hashes ... OK all three match
removed source FL_v1.scram

$ ls FL_*
FL_v1 (Track 0).bin
FL_v1.bin
FL_v1.cue
FL_v1.miniscram
FL_v1_logs.zip

$ miniscram inspect FL_v1.miniscram
container:  MSCM v2
manifest:
  tool_version:           miniscram 1.1.0
  created_utc:            2026-04-30T05:50:34Z
  write_offset_bytes:     -48
  leadin_lba:             -45150
  scram.size:             836338152
  scram.hashes.md5:       0a8b730494451efe0a034d398d17c7cf
  scram.hashes.sha1:      6ffe07dff23723aafe1914d0d482ff653fdd0399
  scram.hashes.sha256:    c9832355013839c6a539124c1794bf3567410a64002bfabc58a64058e81a9761
tracks:
  track 1: MODE1/2352  first_lba=0  size=729914976  filename=FL_v1.bin
    md5:    3afa320a456fd9c254576188dd3610d8
    sha1:   7ee7f17ed6dcd3655262514b83526aa6886d83d2
    sha256: 36d874732bb13918ce3ed91a42bb1efae58b943138089105d23c1f7908bd521c
delta:
  override_records:       45927

$ miniscram unpack FL_v1.miniscram
running scramble-table self-test ... OK ok
reading container FL_v1.miniscram ... OK delta 7084781 bytes
verifying bin hashes ... OK all tracks match
building scram prediction ... OK ok
applying delta ... OK 7084781 byte(s) of delta applied
verifying output hashes ... OK all three match

$ ls FL_*
FL_v1 (Track 0).bin
FL_v1.bin
FL_v1.cue
FL_v1.miniscram
FL_v1.scram
FL_v1_logs.zip

$ sha256sum FL_v1.scram
c9832355013839c6a539124c1794bf3567410a64002bfabc58a64058e81a9761  FL_v1.scram
```

798 MB → 1.5 MB (~530×). 2812 disagreeing sectors → 45927 override
records, 7 MB uncompressed delta; zlib brings that down to ~1.5 MB
on disk. SafeDisc's corrupted sectors and non-zero lead-in bytes
can't be recomputed from the `.bin`, so they ride through the
delta — heavy protection costs more than a clean disc but still a
substantial saving. The trailing `sha256sum` matches the original;
miniscram's own pack-time round-trip and unpack-time hashing already
cover this, the external check just makes byte-equality visible.

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
$ ls -lh MP2_Play.scram MP2_Play.miniscram
-rw-rw-r-- 1 hugh hugh  366K MP2_Play.miniscram
-rwxr--r-- 1 hugh hugh  811M MP2_Play.scram
```

811 MB → 366 KB (~2270×). Smaller delta than Freelancer because
SecuROM doesn't corrupt main-channel sectors the way SafeDisc does;
the protection bytes that matter sit in `MP2_Play_logs.zip`, not in
the `.scram`.

### Half-Life GOTY — mixed-mode hybrid CD

- **Copy protection:** none (`Error Count: 0` per the
  [redump entry](http://redump.org/disc/25966/)).
- **Why this disc:** 1 Mode 1 data track + 27 Red Book audio tracks.
  The audio dominates the disc surface and exercises the audio-bypass
  path of the scrambler (audio sectors are not descrambled — only the
  data track is).

```
$ ls -lh HALFLIFE.scram HALFLIFE.miniscram
-rw-rw-r-- 1 hugh hugh  332K HALFLIFE.miniscram
-rwxr--r-- 1 hugh hugh  766M HALFLIFE.scram
```

766 MB → 332 KB (~2400×). Lead-in noise and per-track boundary
sectors account for most of the delta; audio sectors themselves
bypass the scrambler and don't contribute overrides.

### Deus Ex v1002f — clean Mode 1 baseline

- **Copy protection:** none per the
  [redump entry](http://redump.org/disc/14933/) (write offset −48).
- **Why this disc:** the simplest case — a single Mode 1 data track,
  zero ECC/EDC errors. Establishes the lower bound: with no
  protection, no audio, and no errors, the bin fully predicts the
  scram and the delta is empty.

```
$ ls -lh DeusEx_v1002f.scram DeusEx_v1002f.miniscram
-rw-rw-r-- 1 hugh hugh  329 DeusEx_v1002f.miniscram
-rwxr--r-- 1 hugh hugh 856M DeusEx_v1002f.scram
```

**0 override records.** The 4-byte uncompressed delta is just the
record count (`u32 = 0`); everything else in the container is the
5-byte file header plus the four critical chunks (MFST, TRKS,
HASH, DLTA), with the irreducible cost dominated by the per-track
hash records. 856 MB → 329 bytes — about 2.7 million×, the
irreducible cost being the manifest itself.

### Things that should work, untested on real-disc fixtures

- **Mode 2/2352 data tracks** (CD-i, VCD, PSX-XA Form 2). The
  scrambler treats Mode 1 and Mode 2 identically; covered by
  synthetic round-trip tests but no real-disc dataset yet.
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
| `tool_version`         | bytes  | UTF-8, e.g. `"miniscram 1.1.0"` (no NUL terminator) |
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
