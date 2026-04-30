# unscramble fixtures

These 46 sector fixtures are copied verbatim from
[redumper](https://github.com/superg/redumper) at
`tests/unscramble/`. They enumerate every pass/fail case
exercised by redumper's `Scrambler::descramble()` decision in
`cd/cd_scrambler.ixx`.

Each filename encodes `<n>_<description>.<lba>.<verdict>`:

- `<lba>` is the expected LBA (or `null` for "no LBA hint",
  matching redumper's `lba == nullptr` path).
- `<verdict>` is `pass` (redumper descrambles → bin holds
  descrambled bytes) or `fail` (redumper passes through → bin
  holds the raw scrambled bytes).

miniscram's `classifyBinSector` (`ecma130.go`) mirrors the same
decision against the *bin* form, so these fixtures double as
miniscram's authoritative classifier-correctness corpus.

License: GPL-3.0, inherited from redumper (license-compatible with
miniscram). Project-level acknowledgement is in `NOTICE`.
