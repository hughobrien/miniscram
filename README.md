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

Big-endian. Begins immediately after the manifest body.

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

## Design history

Architecture, design rationale, and per-feature decisions live in
`docs/superpowers/specs/`. This README is the authoritative reference
for the wire format only.
