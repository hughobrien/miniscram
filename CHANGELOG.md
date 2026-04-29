# Changelog

All notable changes to miniscram are recorded here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[1.0.0]: https://github.com/hughobrien/miniscram/releases/tag/v1.0.0
