# miniscram inspect — read-only container pretty-printer

A new `miniscram inspect <container>` subcommand. Read-only; no
output files, no temp files. Pretty-prints the container framing and
manifest, with an opt-in `--full` listing of override records and a
`--json` mode for machine consumption.

The v0.2 design at `docs/superpowers/specs/2026-04-28-miniscram-v0.2-design.md`
defines the container format and manifest schema; this spec assumes
that baseline and only describes the new subcommand.

This corresponds to TASKS.md item **A1**, scoped to "narrow A1": no
framing validation beyond what `ReadContainer` already performs.
Validation features (byte-offset error reporting, structural fsck) are
left to a future C2 / `inspect --check` cycle.

## Goals

- Give an archivist a one-command view of any `.miniscram` container's
  contents without unpacking or writing to disk.
- Provide a stable, scriptable JSON representation for tooling that
  wants to feed miniscram metadata into spreadsheets, dashboards, or
  batch dispatchers.
- Reuse `ReadContainer` and the existing wire formats — no new parsers,
  no duplication.

## Non-goals

- Container validation. `inspect` reports what it can parse and bails
  on anything `ReadContainer` rejects. It does **not** attempt graceful
  partial output on corrupt containers, and it does **not** verify
  override-record framing beyond what's needed to enumerate them. A
  later C2 (or `inspect --check`) handles structural fsck.
- Re-hashing or content verification. That's A2 (`verify`).
- Discovery from the working directory. Pack/unpack discover by stem
  because the workspace is a "one disc per directory" convention;
  `inspect` is targeted at a specific file and takes one mandatory
  positional argument.

## CLI surface

```
miniscram inspect [--full] [--json] [-h|--help] <container>
```

| Flag | Effect |
| --- | --- |
| `--full` | Append a per-record listing of every override (no cap). Without it, only the override count is shown. |
| `--json` | Emit the manifest verbatim plus a `delta_records` array on stdout. Always includes all records; `--full` is implied for JSON. |
| `-h` / `--help` | Print usage and exit 0. |

Exactly one positional argument: the container path. Zero or more than
one is a usage error (exit 1).

Stdout is the inspection output; stderr carries errors and warnings.

## Output: human format (default)

```
container:  MSCM v2
manifest:
  tool_version:           0.2.0
  created_utc:            2026-04-28T14:30:21Z
  bin_size:               739729728
  bin_sha256:             a1b2…full hex…ef
  scram_size:             739729728
  scram_sha256:           c3d4…full hex…21
  write_offset_bytes:     -52
  leadin_lba:             -150
  bin_first_lba:          0
  bin_sector_count:       314546
  delta_size:             312
  error_sector_count:     0
  scrambler_table_sha256: 8a9b…full hex…77
tracks:
  track 1: MODE1/2352  first_lba=0
  track 2: AUDIO       first_lba=12345
delta:
  override_records:       4
```

Conventions:

- **Field names match the manifest's JSON keys** (snake_case). One
  field per line. Greppable: `miniscram inspect foo.miniscram | grep
  bin_sha256` returns a single line with the full hash.
- **Hashes are shown in full.** Truncating breaks `grep` against
  redump.org records and other reference databases. Whoever wants
  short can pipe through `cut`.
- **`container:` line** shows the magic ("MSCM") and version byte (`v2`)
  derived from the raw container header. The manifest's redundant
  `format_version` field is omitted from the human view to keep one
  source of truth.
- **`tracks:` section** lists each track as `track N: MODE  first_lba=X`.
  Padded to align modes when the longest mode in the list determines
  column width — purely aesthetic, doesn't affect grep.
- **`delta:` section** is always present, with at minimum the
  `override_records` count parsed from the delta payload header (not
  from any manifest field — the wire format is authoritative).

## Output: human format with `--full`

When `--full` is set and at least one override exists, append:

```
overrides:
  byte_offset=12345    length=8     lba=5
  byte_offset=23456    length=4     lba=10
  ...
```

One line per override record. The wire format guarantees that no
record crosses a sector boundary, so each maps to exactly one LBA:

```
lba = byte_offset / 2352 + bin_first_lba
```

Columns are space-padded for readability but each line is parseable
with `awk` or similar.

If `--full` is set and there are zero overrides, the `overrides:`
section is omitted entirely (the count of 0 in the `delta:` section
suffices).

**Note:** TASKS.md A1 originally specified an additional rule: for
containers under 1 MiB, the override list would auto-preview up to 100
lines without `--full`. That rule is dropped here. A single explicit
switch (`--full`) is simpler than a size-conditional auto-preview, and
nothing else in the CLI behaves size-conditionally. Users who want a
preview can run `miniscram inspect --full foo.miniscram | head`.

## Output: `--json` format

```json
{
  "format_version": 2,
  "tool_version": "0.2.0",
  "created_utc": "2026-04-28T14:30:21Z",
  "scram_size": 739729728,
  "scram_sha256": "...",
  "bin_size": 739729728,
  "bin_sha256": "...",
  "write_offset_bytes": -52,
  "leadin_lba": -150,
  "tracks": [
    {"number": 1, "mode": "MODE1/2352", "first_lba": 0}
  ],
  "bin_first_lba": 0,
  "bin_sector_count": 314546,
  "error_sector_count": 0,
  "delta_size": 312,
  "scrambler_table_sha256": "...",
  "delta_records": [
    {"byte_offset": 12345, "length": 8, "lba": 5},
    {"byte_offset": 23456, "length": 4, "lba": 10}
  ]
}
```

- The first set of fields is the manifest emitted by `Manifest.Marshal()`,
  unchanged. We add a single top-level `delta_records` field.
- `--json` always emits all records (no cap, regardless of `--full`).
- Container magic/version byte are not included; they're implicit in
  the file being parseable. JSON output describes the *manifest*, not
  the container framing.
- `error_sectors` (the optional list, capped at 10000 in
  `Manifest.Marshal`) appears or not exactly as it does in the
  serialized manifest. `inspect` doesn't materialize it independently.

## New helper

Introduce a single helper in `delta.go`:

```go
// IterateDeltaRecords walks the override records in delta, calling fn
// for each record's byte offset and length. fn is not given the
// payload bytes. Returns the count from the wire-format header and any
// framing error encountered.
func IterateDeltaRecords(delta []byte, fn func(off uint64, length uint32) error) (uint32, error)
```

Reuses the existing wire format documented at the top of `delta.go`.
The whole delta payload is already in memory after `ReadContainer`
returns, so this is a straightforward in-memory walk. Used by `inspect`
in v0.2; future `verify` (A2) and structural fsck (C2) can reuse it.

## Errors

`inspect` calls `ReadContainer`, which already produces helpful errors
for:

- Bad magic ("not a miniscram container").
- Unsupported version byte (existing v1 → v2 migration message).
- Implausible manifest length.
- Manifest JSON parse failure.

Those errors propagate to stderr and `inspect` exits 4 (I/O). No new
exit codes; no graceful partial-output mode (that's the C2 boundary).

If `IterateDeltaRecords` hits a framing error (truncated record header,
zero-length payload, length > 2352), the human and JSON views still
emit everything before the failure point, and the error is appended on
stderr — exit 4. This case shouldn't occur on a container produced by
miniscram itself; the worst-case display is enough information for
debugging without pretending to be a fsck.

## Wiring

`main.go`'s `run()` currently threads only `stderr` through. Add
`stdout` so `runInspect` can write its primary output to the right
stream. `runPack` and `runUnpack` continue using stderr for their
progress reporter; nothing about their output changes.

`runInspect(args, stdout, stderr)` follows the same shape as
`runPack`/`runUnpack`:

1. Parse flags via `flag.NewFlagSet`.
2. Resolve positional arg (exactly one; zero or two-plus is exit 1).
3. Call `ReadContainer`; on error, print to stderr and exit 4.
4. If `--json`: marshal manifest, splice `delta_records`, write to
   stdout, exit 0.
5. Otherwise: write the human format to stdout, including the
   `--full` block if applicable, exit 0.

`help.go` gains an `inspect` block for `miniscram help inspect` and a
top-level mention.

## Testing

All tests are hermetic — no real `.miniscram` files on disk. Existing
test helpers in `pack_test.go` already build in-memory containers via
`Pack` against synthesized inputs; reuse them.

| Test | Asserts |
| --- | --- |
| `TestInspectHumanFormat` | Default output contains key fields with their full values. Checks specific lines, not whole-buffer equality, so unrelated additions don't break unrelated tests. |
| `TestInspectJSON` | `--json` output unmarshals; `delta_records` length matches the count in the wire header; manifest fields match the original. |
| `TestInspectFullListsOverrides` | With `--full` and a delta containing N overrides, the `overrides:` section has exactly N lines. Without `--full`, no `overrides:` section appears. |
| `TestInspectFullNoOverrides` | With `--full` and zero overrides, no `overrides:` section appears (count line still does). |
| `TestInspectRejectsV1` | A container hand-built with version byte 0x01 produces the v0.2 migration error on stderr and exits 4. |
| `TestInspectBadMagic` | A file with wrong magic produces the existing error and exits 4. |
| `TestInspectUsageErrors` | Zero positional → exit 1. Two positionals → exit 1. `--help` → exit 0 with help text. |
| `TestIterateDeltaRecords` | Unit test in `delta_test.go`: synthetic delta with known records walks correctly; truncated payload produces a framing error. |

Golden values for the human format are kept simple — assert presence
and content of specific lines rather than diffing whole buffers.

## File/LOC summary

| File | Action | Approx LOC |
| --- | --- | --- |
| `inspect.go` | new | ~140 |
| `inspect_test.go` | new | ~180 |
| `delta.go` | add `IterateDeltaRecords` | +20 |
| `delta_test.go` | add walk test | +40 |
| `main.go` | thread stdout, add dispatch | +15 |
| `help.go` | top-level + inspect help | +25 |

Total: ~420 LOC including tests, within the "half-day, ~150 LOC + tests"
budget that TASKS.md A1 sized.

## Out of scope (deferred to later items)

- **A2 / `verify`:** content re-hashing of the recovered scram. Will
  reuse the manifest formatting helpers introduced here.
- **A4 / `--continue`:** batch mode validation predicate. Will reuse
  `ReadContainer` + `IterateDeltaRecords` to determine whether a target
  container is valid enough to skip.
- **C2 / fsck:** byte-offset structural validation, partial-output
  recovery. May fold into a future `inspect --check` flag rather than
  a separate subcommand.
- **C1 / md5+sha1 manifest fields:** when added, `inspect` displays
  them automatically because `Manifest.Marshal()` is the source of
  truth for the field set.
