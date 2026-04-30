# Changelog

All notable changes to miniscram are recorded here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.0] - 2026-04-30

### Added

- `--keep-source` flag on `miniscram pack` (preserve `.scram` after
  a verified round-trip; useful when iterating against a fixture).
- Classifier-gated prediction: bin sectors that redumper passed
  through unchanged (zeroed, invalid sync, valid sync + bad mode/MSF)
  are no longer re-scrambled by the predictor — saves a 2352-byte
  override per such sector. Pinned via 46 imported redumper test
  fixtures and a 1000-iteration property test against a Go-port
  oracle of `Scrambler::descramble`.
- Property tests for v2 codecs (`MFST` / `TRKS` / `HASH` round-trip),
  `Scramble` involution, BCD-MSF round-trip, and full
  `WriteContainer` / `ReadContainer` round-trip.
- 19-sub-test corruption-rejection battery covering every named v2
  read-time error path (bad magic, wrong version, mid-chunk
  truncation, CRC mismatch, length-cap exceeded, unknown critical /
  ancillary chunks, missing required, duplicate critical, MFST not
  first, hash-after-DLTA accepted).

### Changed

- **Container format v1 → v2 (wire break).** PNG/CHD-style chunks:
  5-byte file header (magic + version) followed by `MFST` / `TRKS` /
  `HASH` / `DLTA` chunks, each length-prefixed with a CRC-32/IEEE
  trailer over `(tag || payload)`. 16 MiB length sanity cap on
  non-`DLTA` chunks. PNG critical/ancillary case-bit reserved for
  forward-compat without a version bump. No migration code — a v2
  binary rejects v1 containers with an error pointing at the source
  repo to build a matching commit.
- Pack reporter now prints
  `N disagreeing sector(s) → R override record(s), P pass-through(s), delta D bytes`.
  Previously the label `N override(s)` conflicted with `inspect`'s
  `override_records: R` (a different, larger number — byte-run
  granularity, not sector granularity).
- Reporter no longer prefixes each line with `[hh:mm:ss]`. Adds no
  value for an interactive CLI; clutters terminal output and docs.
- README demo blocks condensed for the three "size headline" fixtures
  (Half-Life, Max Payne 2, Deus Ex) — only `ls -lh` is shown for those.
  Freelancer is the comprehensive end-to-end walkthrough:
  `sha256sum` → pack → `ls` → inspect → verify → unpack → `ls` →
  `sha256sum`, proving byte-equality with an external tool.

### Removed

- 32-byte in-header scrambler-table SHA-256. The build-startup pin
  in `ecma130.go` is the actual drift guard; the in-header copy was
  always redundant given the version-byte gate.
- ISO-8601 `created_utc` string in the manifest — replaced by
  `created_unix` (int64 BE seconds since the Unix epoch). Display
  formatting moved to the `inspect` print site.
- `(go1.x.y)` runtime suffix on `tool_version`. Forensics noise that
  doesn't affect output bytes.

## [1.0.0] - 2026-04-29

Initial public release.

### Features

- `miniscram pack` — converts a Redumper `.scram` into a compact
  `.miniscram` container by storing the binary delta against an
  ECMA-130-reconstructed prediction of the scrambled image. Removes
  the source `.scram` after verified round-trip by default.
- `miniscram unpack` — reproduces the `.scram` from `.bin` +
  `.miniscram`, byte-for-byte.
- `miniscram verify` — non-destructive integrity check that rebuilds
  the recovered `.scram` to a temp file and compares hashes against
  the manifest.
- `miniscram inspect` — read-only pretty-print of a `.miniscram`
  container, with `--full` and `--json` modes.

### Container format (v1)

- 41-byte binary header (`MSCM` magic + version + scrambler-table
  SHA-256 + manifest length).
- UTF-8 JSON manifest (tool version, write offset, lead-in LBA,
  per-track and full-disc MD5/SHA-1/SHA-256 hashes).
- Big-endian override-record delta payload, zlib-compressed
  (`BestCompression`).

### Demonstrated against

Real Redumper dumps:
[Deus Ex (PC)](http://redump.org/disc/14933/),
[Half-Life GOTY (PC)](http://redump.org/disc/25966/),
[Freelancer (PC)](http://redump.org/disc/42536/),
[Max Payne 2: The Fall of Max Payne (PC, Play disc)](http://redump.org/disc/10508/).

### Acknowledgments

- Method inspired by Hauenstein,
  [*Compact Preservation of Scrambled CD-ROM Data*](https://doi.org/10.5121/ijcsit.2022.14401)
  (IJCSIT, August 2022).
- Scrambler implementation adapted from
  [redumper](https://github.com/superg/redumper) (GPL-3.0).
- Test fixtures from the
  [redump.org](https://redump.org) preservation community.

[1.1.0]: https://github.com/hughobrien/miniscram/releases/tag/v1.1.0
[1.0.0]: https://github.com/hughobrien/miniscram/releases/tag/v1.0.0
