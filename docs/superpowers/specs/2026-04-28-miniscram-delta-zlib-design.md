# Compressed delta payload

Status: design approved 2026-04-28.

## Goal

Shrink `.miniscram` files by zlib-compressing the delta payload during
write. The stdlib `compress/zlib` reaches a 16:1 ratio on a measured
SafeDisc-style fixture; this spec carries that win into the on-disk
container with a minimal change.

## Measurement that motivated this

Half-Life packed (28 tracks, multi-FILE cue):

| Region | Bytes | Share |
|---|---:|---:|
| binary header | 41 | 0.0% |
| manifest (JSON) | 8,140 | 0.1% |
| delta payload (plain) | 5,483,541 | 99.9% |
| **container total** | **5,491,722** | |

Compressing the delta payload at `compress/zlib` `BestCompression`:

| Codec | Compressed | Ratio | Encode time |
|---|---:|---:|---:|
| zlib L9 | 336,042 | 6.13% | 44 ms |
| flate L9 (raw) | 336,036 | 6.13% | 46 ms |
| gzip L9 | 336,054 | 6.13% | 44 ms |
| flate huffman-only | 5,273,504 | 96.17% | 12 ms |
| lzw MSB/LSB w=8 | 5,212,027 | 95.05% | 65 ms |

Almost all the win comes from LZ77 back-references (huffman-only is a
no-op), consistent with SafeDisc-style protection that produces the
same corruption pattern across many sectors. Among framed deflate
variants the differences are noise (≤18 bytes); zlib wins on
compactness with a built-in adler32 integrity check.

## Wire format

Version byte stays `0x01`. The format is silently redefined: the bytes
after the manifest body are now a `compress/zlib` stream at
`BestCompression`. Decompressing the stream yields the existing v1
delta layout (`u32 count` + record sequence). Magic, version,
scrambler hash, manifest length and body are unchanged.

The version byte is not bumped because exactly one v1 file exists in
the world (`/tmp/HALFLIFE.miniscram` on the author's machine, easily
regenerated). A reader fed the pre-change plaintext v1 bytes will fail
when `zlib.NewReader` rejects the magic; the error is surfaced with a
descriptive prefix so the diagnostic is clear.

## Code change

Three call sites change:

**`manifest.go:WriteContainer`** — after writing the manifest body,
wrap `f` in `zlib.NewWriterLevel(f, zlib.BestCompression)`,
`io.Copy(zw, deltaSrc)`, then `zw.Close()` to flush before
`f.Sync` / `f.Close` / atomic rename.

**`manifest.go:ReadContainer`** — after reading the manifest, wrap
the remaining file reader in `zlib.NewReader`, `io.ReadAll` from it,
defer-close. Wrap any error as
`fmt.Errorf("decompressing delta payload: %w", err)`.

**`README.md`** — the Delta payload section gets a one-sentence note
that the bytes after the manifest are a `compress/zlib`
(BestCompression) stream of the same layout.

Everything downstream of `ReadContainer` keeps consuming the
plaintext `[]byte` it returns. `DeltaEncoder`, `ApplyDelta`,
`IterateDeltaRecords`, `Inspect`, `Verify`, manifest schema, hashes,
CLI surface, exit codes — all unchanged.

The pack-side temp delta file (written by `DeltaEncoder` and copied
into the container) stays plaintext. Compression happens only at the
container boundary, so debugging dumps of the temp file remain
hexdumpable.

## Tests

A new unit test in `manifest_test.go` round-trips a synthetic delta
through `WriteContainer` + `ReadContainer` and asserts the on-disk
post-manifest bytes start with the zlib magic `0x78` (any level
indicator) and that the bytes returned by `ReadContainer` equal the
bytes written.

A new unit test in `manifest_test.go` constructs a `.miniscram` with a
plaintext delta payload (the pre-change wire layout) and asserts
`ReadContainer` returns an error whose message contains
`decompressing delta payload`.

The existing e2e tests (`e2e_test.go`, `e2e_redump_test.go`) already
cover round-trip byte-equality of the recovered `.scram`. They become
the implicit verification that the compressed format works end to end;
no changes needed.

## Out of scope

- Streaming pack/unpack without buffering the delta in memory.
  `ReadContainer` already buffers via `io.ReadAll`; this spec is no
  regression and is acceptable for delta sizes seen in practice (KB to
  a few MB).
- Configurable compression level. `BestCompression` always; the ~50 ms
  cost is negligible against scrambling and hashing.
- Alternative codecs. LZW and huffman-only are useless on this data;
  flate-raw and gzip add either no framing or unnecessary metadata.
- A "compressed: yes/no" manifest flag. The format is mandatory-on.
- Bumping the version byte. Justified above.

## Risks

- Memory footprint of `ReadContainer` is unchanged; no regression.
- Decompression CPU on read is dominated by IO and hashing for the
  sizes involved. Not a regression.
- The single existing v1 file on disk becomes unreadable. Acceptable;
  re-pack from `HALFLIFE.cue` + `HALFLIFE.scram`.
