# miniscram

Compactly preserve scrambled CD-ROM dumps. Stores the bytes of a
Redumper `.scram` file as a small structured delta against the
unscrambled `.bin`. With miniscram and your `.bin`, you can reproduce
the original `.scram` byte-for-byte. Implements the method from
Hauenstein, "Compact Preservation of Scrambled CD-ROM Data"
(IJCSIT, August 2022), specialised for Redumper output.

## Install

```sh
go install ./...    # produces ./miniscram
```

## CLI

### pack

Pack a `.scram` into a `.miniscram` container.

    miniscram pack disc.cue [-o out.miniscram] [-f] [--no-verify] [--keep-source]

Reads `disc.scram` (derived from the cue stem) and the `.bin` files
referenced by `disc.cue`. Writes `disc.miniscram` (or `-o`) and
removes `disc.scram` after a verified round-trip.

### unpack

Reproduce the `.scram` from `.bin` + `.miniscram`.

    miniscram unpack disc.miniscram [-o out.scram] [-f] [--no-verify]

### verify

Non-destructive integrity check.

    miniscram verify disc.miniscram

Rebuilds the recovered `.scram` in a temp file, hashes it, compares
against the manifest's recorded hashes, deletes the temp file.

### inspect

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

## Scope and durability

miniscram targets **Redumper-output CD-ROM dumps**. The lockstep
layout-mismatch check (Pack aborts when more than 5% of disc sectors
disagree with the bin-driven prediction) is the primary safety net —
it protects against wrong-bin / wrong-cue / wrong-scram pairings and
catches malformed inputs early.

### Spec-defined constants

The constants governing scrambling, EDC, ECC, sync detection, and MSF
addressing are all fixed by ECMA-130 / Red Book. The codebase pins the
three generated tables by SHA-256 and panics at process start if any
table-builder drifts:

- `expectedScrambleTableSHA256` — `ecma130.go:20`, checked in `init()` at line 61.
- `expectedEDCTableSHA256` — `ecma130.go:82`, checked in `init()` at line 84.
- `expectedGFTablesSHA256` — `ecma130.go:145`, checked in `init()` at line 147.

### Tested end-to-end

Real-disc fixtures in `e2e_redump_test.go` (run with `-tags redump_data`):

- **Deus Ex v1002f** (clean single-track Mode 1, 0 ECC/EDC errors).
- **Freelancer FL_v1** (SafeDisc 2.70.030; 588 deliberately corrupted
  sectors per the redump.org submission). Round-trip byte-equal +
  exact assertion on the data-track ECC/EDC error count.
- **Half-Life HALFLIFE** (multi-FILE cue, 1 Mode 1 + 27 audio tracks,
  0 ECC/EDC errors).

Synthetic fixtures in `e2e_test.go` table:

- Clean Mode 1, single track.
- Negative write offset (-48 bytes) and positive write offset
  (+48 bytes). Note: the engine accepts up to ±2 sectors (±4704 bytes)
  per `validateSyncCandidate`'s `writeOffsetLimit`, but the test
  matrix only exercises ±48.
- Mode 2/2352 single track.
- Three injected error sectors.
- Data + 1 audio track.

### Should work, untested

- **Mode 2/2352 with bin-covered sectors that have non-trivial EDC/ECC
  layout** (CD-i, VCD, PSX-XA Form 2). The scrambler path treats Mode 1
  and Mode 2 identically — both go through `Scramble` at
  `builder.go:175` (only AUDIO is skipped). miniscram does not compute
  EDC/ECC for bin-covered sectors; it just scrambles the bytes you
  hand it. Form 1 vs Form 2 layout differences in user data should
  pass through unchanged. *Untested against real CD-i / VCD / PSX-XA
  dumps.*

- **Audio-only discs (no data tracks).** Conjectural. The bin-walk is
  audio-friendly (AUDIO tracks skip the scrambler), but the pregap is
  still synthesised as Mode 1 zero sectors at `builder.go:45-62`. On
  an audio-only disc, the actual pregap is unscrambled silence, so the
  prediction will mismatch the real bytes — those ~150 pregap sectors
  become delta overrides. The disc round-trips, but the delta carries
  ~350 KiB of avoidable noise.

### Refuses or under-performs

- **Variable write offset.** `checkConstantOffset` (`pack.go:406`)
  samples sync positions across the scram and aborts with `"variable
  write offset detected"` if they don't agree. Refusal is correct —
  miniscram can't reconstruct a varying offset.
- **Layout mismatch > 5%.** `layoutMismatchAbortRatio = 0.05`
  (`builder.go:39`), measured against total disc sectors (leadin +
  pregap + bin + leadout). Pack aborts with `LayoutMismatchError`.
  Freelancer's 588 corrupted sectors sit well under the threshold —
  the round-trip in `e2e_redump_test.go` proves Pack accepts it. A
  heavily protected small disc could plausibly approach 5%; there is
  no user knob today.
- **Cuesheets with multi-track-per-FILE.** Rejected by `ParseCue`
  (`cue.go:106`). Redumper produces one TRACK per FILE; cuesheets
  from DiscImageCreator or IsoBuster may not. Convert beforehand.
- **Modes other than `MODE1/2352`, `MODE2/2352`, `AUDIO`.** Rejected
  by the `validModes` whitelist in `cue.go:29`. No `MODE2/2336`, no
  PSX-style packed Mode 2.
- **Discs with non-zero pregap data** (e.g. CD+ / Enhanced CDs).
  Pregap is synthesised as Mode 1 zero sectors at
  `builder.go:45-62` (with computed EDC + ECC). Any disc whose
  pregap contains real data will produce delta overrides for those
  sectors. Functional, but inflates the delta.
- **Discs with non-Mode-0 leadout.** Same shape — leadout is
  synthesised as Mode 0 zero sectors at `builder.go:64-83`. Different
  leadout content becomes delta overrides.
- **Non-zero lead-in.** Lead-in (LBAs -45150 to -150) is filled with
  zeros at `builder.go` LBA-walk default. SafeDisc and SecuROM tend to
  have non-zero lead-in data; those bytes flow through the delta. This
  is why a SafeDisc dump's delta is several MiB rather than KiB.

### Out of scope (architectural)

- **DVD / Blu-ray.** Different sector format (2048-byte data blocks,
  no scrambling, different ECC). Not addressable by miniscram's
  ECMA-130 pipeline.
- **Multi-session CDs with non-trivial second sessions.** The cuesheet
  parser doesn't model session boundaries.
- **Subchannel data.** Main-channel only — `grep -i "subchannel"` over
  the source returns zero hits. PSX libcrypt-class protection lives in
  subchannel and is invisible to miniscram. Redumper preserves it in
  the `*_logs.zip` it produces alongside the `.scram`; keep that
  bundle next to the `.miniscram`.

## Design history

Architecture, design rationale, and per-feature decisions live in
`docs/superpowers/specs/`. This README is the authoritative reference
for the wire format only.
