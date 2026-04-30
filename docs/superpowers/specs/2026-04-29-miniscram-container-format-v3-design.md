# miniscram container format v3 — chunk-based binary

Replace the v1/v2 container layout (fixed header + length-prefixed
JSON manifest + zlib delta) with a PNG/CHD-style chunk format. The
JSON manifest goes away entirely; what remains of the manifest is
encoded as fixed binary fields inside named chunks.

Container version byte is **0x03**. v1 and v2 are not readable by
this build. Any other version byte produces a hard error pointing
the user at the source repo to build a matching binary.

This redesign incorporates the v1→v2 audit (drop the in-header
scrambler-table SHA) as a stepping stone — that field is gone in v3
for the same reasons documented in the v2 commit and PR body.

---

## Goals

1. **Idiomatic file format.** Magic + version + a stream of typed,
   length-prefixed, CRC-protected chunks. Pattern-matches PNG, RIFF,
   IFF, FLAC, MAME CHD. Inspectable with `xxd | head` — FOURCC tags
   read as ASCII, lengths are obvious, layout is self-documenting.
2. **Zero new dependencies.** Hand-rolled chunk reader/writer using
   only `encoding/binary` and `hash/crc32` from the Go std library.
3. **Forward-compat lever, unused for now.** PNG critical/ancillary
   convention (uppercase first letter = required, lowercase = readers
   may skip) is reserved. v3 ships with no ancillary chunks; the
   convention is just available for future incremental additions
   without bumping version.
4. **Per-chunk integrity.** CRC32 over `(type || payload)` on every
   chunk catches mid-stream corruption that the file-level scram/bin
   hashes miss (e.g., a flipped bit in MFST that still parses).

## Non-goals

- Backward compatibility. v3 readers do not handle v1 or v2. Old
  containers must be repacked with the matching binary.
- Format extensibility *features* (ancillary chunks, optional fields,
  unknown-tag handling beyond the version-byte gate). The convention
  is reserved; v3 itself adds no ancillary chunks.
- Changes to the delta wire format (still zlib over the same
  override-record encoding).
- Changes to scrambling, ECC/EDC, or any ECMA-130 specifics.

---

## On-disk layout

```
File header (8 bytes, fixed):
  +----+----+----+----+----+----+----+----+
  | 'M'| 'S'| 'C'| 'M'| ver|  reserved   |
  +----+----+----+----+----+----+----+----+
   0    1    2    3    4    5    6    7

  bytes 0..3   magic         "MSCM"
  byte  4      version       0x03
  bytes 5..7   reserved      must be 0x00

Chunk (repeated until EOF):
  +----+----+----+----+----+----+----+----+----+...
  |  type (4 ASCII)   | length (uint32 BE)| payload  |  CRC32 (BE)  |
  +----+----+----+----+----+----+----+----+----+...

  bytes 0..3   type          4 ASCII bytes (FOURCC)
  bytes 4..7   length        payload length, big-endian uint32
  bytes 8..    payload       `length` bytes
  next 4       CRC32         CRC-32/IEEE over (type || payload), BE
```

CRC32 polynomial is `0xEDB88320` (the IEEE / `crc32.IEEETable` variant
in `hash/crc32`). PNG uses the same polynomial. Computed over the
4-byte type + the payload, not over the length field.

**End of file:** the reader walks chunks until it hits clean EOF
(io.EOF after a complete chunk). Truncation mid-chunk produces an
error. There is no explicit end-of-file marker chunk — `DLTA`'s
length prefix already delimits the delta payload, and EOF after the
final chunk is unambiguous.

**Endianness:** all multi-byte integers are big-endian, matching PNG
and CHD convention.

---

## Chunk vocabulary (v3)

All v3 chunks are critical (uppercase first letter). All four must
appear exactly once. `MFST` must be the first chunk; the others may
appear in any order.

### `MFST` — manifest scalars

```
tool_version_len       uint16 BE       length of UTF-8 tool_version string
tool_version           bytes           UTF-8, no NUL terminator
created_unix           int64  BE       UTC seconds since epoch
write_offset_bytes     int32  BE       sync offset between bin and scram
leadin_lba             int32  BE       LBA where lead-in starts on disc
scram_size             int64  BE       expected size of reconstructed scram
```

### `TRKS` — track table

```
count                  uint16 BE       number of tracks
per track:
  number               uint8           CD track number (1..99)
  mode_len             uint8           length of mode string
  mode                 bytes           ASCII, e.g. "MODE1/2352", "AUDIO"
  first_lba            int32  BE       LBA where this track starts
  size                 int64  BE       size of this track's .bin file
  filename_len         uint16 BE       length of UTF-8 filename
  filename             bytes           UTF-8 .bin filename, no path
```

### `HASH` — file hashes

Tagged sub-records — decouples hash storage from track structure so
new digest algorithms or new hash targets are one entry, not a
struct change.

```
count                  uint16 BE       number of hash records
per record:
  target               uint8           0 = scram, 1..N = track index (1-based)
  algo                 [4]byte         ASCII tag, e.g. "MD5 ", "SHA1", "S256"
  digest_len           uint8           digest length in bytes (16/20/32)
  digest               bytes           raw binary digest
```

A v3 container records `MD5 `, `SHA1`, `S256` for the scram and for
each track — same coverage as v2.

### `DLTA` — zlib-compressed delta payload

Payload is the zlib stream verbatim. Length tells the reader exactly
where the delta ends; no read-to-EOF heuristic.

---

## Read behavior

```
1. Read 8-byte file header.
2. Reject bad magic.
3. Reject any version != 0x03 with the error
     "container version 0x%02x; this build only reads v3.
      rebuild miniscram from a matching commit:
      https://github.com/hughobrien/miniscram"
4. Walk chunks until EOF:
   a. Read 8-byte chunk header (type + length).
   b. Read `length` bytes of payload.
   c. Read 4-byte CRC; verify CRC32(type || payload).
   d. Dispatch by type. Unknown uppercase type → reject as
      "unsupported critical chunk %q".
      Unknown lowercase type → skip silently (reserved for future).
5. After EOF, verify all four critical chunks were seen exactly once.
   Missing or duplicate → reject with which one.
6. Verify MFST was the first chunk encountered.
```

## Write behavior

```
1. Write 8-byte file header (magic + version + 3 zero bytes).
2. Emit chunks in the order: MFST, TRKS, HASH, DLTA.
3. Each chunk: write type, write length (BE uint32), write payload,
   write CRC32(type || payload) (BE uint32).
4. fsync, atomic rename — same write-then-rename pattern as v1/v2.
```

---

## Audit: what's stored, what's not

The v3 manifest stores only fields with a real consumer. Everything
that was in v1/v2 was checked against actual usage:

| Field | Consumer | Verdict |
|---|---|---|
| `MFST.tool_version` | inspect output only | provenance — keep, conventional, ~20 bytes |
| `MFST.created_unix` | inspect output only | provenance — keep, 8 bytes |
| `MFST.write_offset_bytes` | builder reconstruction | critical |
| `MFST.leadin_lba` | builder reconstruction | critical |
| `MFST.scram_size` | builder reconstruction | critical |
| `TRKS.number` | error messages | keep, 1 byte, useful |
| `TRKS.mode` | builder mode dispatch | critical |
| `TRKS.first_lba` | builder | critical |
| `TRKS.size` | size-validate + multi-bin boundaries | critical |
| `TRKS.filename` | unpack file lookup | critical |
| `HASH` per file | output verification | critical (skipped on `--no-verify`) |
| `DLTA` | reconstruction | critical |

Dropped vs v2:
- The 32-byte in-header scrambler SHA (covered by the v2 audit).
- The `(go1.x.y)` runtime suffix on `tool_version` — forensics noise,
  doesn't affect output bytes. v3 records just `miniscram x.y.z`.
- The ISO-8601 `created_utc` string. Replaced by `created_unix` int64;
  display formatting moves to inspect.
- The variable-length JSON manifest framing.

Considered and kept:
- `tool_version` and `created_unix` — strictly only used by inspect,
  but ~30 bytes total and "when packed by what" is core archive
  metadata. Inspect would lose two displayed fields without them.
- `TRKS.number` — derivable from array index in practice (CD tracks
  are 1-based and contiguous), but 1 byte per track and clarifies
  error messages.

---

## Error handling

Each rejection produces a clear error:

- Bad magic → `"not a miniscram container (bad magic %q)"`
- Wrong version → `"container version 0x%02x; this build only reads v3.
  rebuild miniscram from a matching commit:
  https://github.com/hughobrien/miniscram"`
- Truncated header / chunk header / chunk payload → wrap `io.ErrUnexpectedEOF`
- CRC mismatch → `"chunk %q crc mismatch"`
- Unknown critical chunk → `"unsupported critical chunk %q"`
- Missing required chunk → `"missing required chunk %q"`
- Duplicate critical chunk → `"duplicate chunk %q"`
- MFST not first → `"MFST must be the first chunk"`
- DLTA zlib failure → existing wrapping (`"decompressing delta payload"`)

---

## Implementation shape

- New file `chunks.go` — `writeChunk(w, tag, payload)`,
  `readChunk(r) (tag, payload, err)`, CRC table init, fourcc helpers.
  ~150 LoC.
- `manifest.go` — replace `WriteContainer`/`ReadContainer` bodies.
  `Manifest` struct stays roughly the same shape but `CreatedUTC string`
  becomes `CreatedUnix int64` (formatting moves to display sites).
  Drop the JSON marshaling path entirely.
- `inspect.go` — adapt the human-format output. Chunk-walker can
  display chunks more naturally than the current layout. JSON-mode
  output (`--json`) keeps its current shape — that's a separate
  contract from the on-disk format.
- `pack.go` — drop the `runtime.Version()` suffix on `toolVersion`;
  switch to int64 created_unix.
- All test files — update fixtures and assertions for v3 layout and
  the new field types.

## Testing

- Existing `TestContainerRoundtrip` adapted to the new layout.
- New `TestContainerRejectsCorruption` covering each rejection path:
  bad magic, wrong version (v1, v2, v9 each), truncated mid-chunk,
  CRC mismatch (one bit flipped in payload), unknown critical chunk,
  unknown lowercase chunk (must accept), missing required chunk,
  duplicate critical chunk, MFST not first.
- E2E synthetic round-trip (`e2e_test.go`) byte-exact-bin assertion
  remains the primary correctness gate. Format change is invisible
  to that test except for container size.
- `e2e_redump_test.go` per-fixture container-size bounds will need
  fresh values after running the new packer once. The byte-exact bin
  assertion is unchanged.
- Inspect golden tests adapted for the new on-disk version label.

## Out of scope (separate work tracks)

- The plextor-specific `LBALeadinStart = -45150` constant is not
  guaranteed across drives — being handled separately.
- CLI surface unchanged. Same subcommands, flags, exit codes.

---

## Versioning policy going forward

v3 is the new baseline. Any breaking change (renaming an existing
chunk, changing a struct layout inside `MFST`/`TRKS`/`HASH`, changing
the on-disk header) bumps the version byte. Adding a *new* lowercase
chunk does not bump version — readers skip it. Adding a new uppercase
chunk does bump, since old readers must reject it.

There is no migration code path. A binary built against v3 reads only
v3. Users who need to read older containers build the matching
historical commit.
