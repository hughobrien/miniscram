# miniscram hash parity — md5/sha1/sha256 across the manifest

Bring miniscram's manifest into parity with redump.org submission templates by
recording md5 and sha1 alongside the existing sha256 for both the unscrambled
`.bin` and the original `.scram`. Computed in a single I/O pass per file via
a `hash.Hash` slice. Strict verification on unpack and verify: any of the
three mismatching is a hard failure.

This corresponds to TASKS.md item **C1**.

## Goals

- Record `bin_md5` + `bin_sha1` and `scram_md5` + `scram_sha1` in the manifest
  alongside their existing sha256 siblings, so a `miniscram inspect --json`
  output is directly usable as input to redump.org submission tooling.
- Catch any single-hash regression: if md5, sha1, or sha256 disagrees between
  the recorded manifest value and a fresh recompute, fail the operation
  (no exceptions for "weaker hash mismatched but stronger one matched"). User
  decision: collisions are not an expected failure mode at this scale, so any
  divergence is a real signal.
- Update `inspect` to display all three hashes per file and `verify` /
  `unpack` to check all three.

## Non-goals

- **CRC-32.** Redumper records crc32 too; not in TASKS.md C1's scope. Add
  later as a separate item if redump.org tooling actually needs it.
- **Reading v2 containers.** The version byte bumps from 0x02 to 0x03 and v2
  containers are rejected with the same migration-error pattern v1 used.
  No extant v2 containers exist outside this machine; user-confirmed it's
  fine to require a re-pack. Keeps `ReadContainer` / inspect / verify /
  unpack uniformly v3-only.
- **`--format-version=2` flag on pack.** Over-engineering.
- **Cryptographic signing of the manifest.** TASKS.md theme D — out of scope.
- **Changing the sha256-only verifyRoundTrip behavior in pack.go.** Pack
  already computes sha256 inline and round-trips; the same single-pass helper
  will yield md5/sha1/sha256 for free (one I/O pass), and pack's verify path
  will check all three for parity with unpack and verify.

## Manifest format

`format_version` bumps from `2` to `3`. Container version byte bumps from
`0x02` to `0x03`. The two move in lockstep — there is no "format_version 3
in a v0x02 container" or vice versa. The version-byte mismatch is the
authoritative rejection point; `format_version` in JSON is redundant
documentation, kept for human-readable inspect output.

New manifest fields (in struct declaration order, matching JSON serialization):

```go
ScramSize     int64  `json:"scram_size"`
ScramMD5      string `json:"scram_md5"`     // NEW
ScramSHA1     string `json:"scram_sha1"`    // NEW
ScramSHA256   string `json:"scram_sha256"`
BinSize       int64  `json:"bin_size"`
BinMD5        string `json:"bin_md5"`       // NEW
BinSHA1       string `json:"bin_sha1"`      // NEW
BinSHA256     string `json:"bin_sha256"`
```

Field ordering: each file's hashes group together, weakest-to-strongest.
Matches Redumper submission template ordering (md5, sha1, sha256).

All hashes are stored as lowercase hex strings (matching the existing
`sha256File` output format).

## Single-pass hash helper

Replace `sha256File(path string) (string, error)` in `pack.go` with:

```go
// FileHashes holds the three hashes miniscram records per file.
type FileHashes struct {
    MD5    string
    SHA1   string
    SHA256 string
}

// hashFile streams path through MD5, SHA-1, and SHA-256 in a single I/O
// pass and returns all three as lowercase hex.
func hashFile(path string) (FileHashes, error) {
    f, err := os.Open(path)
    if err != nil {
        return FileHashes{}, err
    }
    defer f.Close()
    m, s1, s256 := md5.New(), sha1.New(), sha256.New()
    w := io.MultiWriter(m, s1, s256)
    if _, err := io.Copy(w, f); err != nil {
        return FileHashes{}, err
    }
    return FileHashes{
        MD5:    hex.EncodeToString(m.Sum(nil)),
        SHA1:   hex.EncodeToString(s1.Sum(nil)),
        SHA256: hex.EncodeToString(s256.Sum(nil)),
    }, nil
}
```

`sha256File` is removed. All four call sites switch to `hashFile`:

- `pack.go:109` (bin hash) — store all three on the manifest.
- `pack.go:117` (scram hash) — store all three.
- `pack.go:470` (verifyRoundTrip) — compare all three; any mismatch is
  `errOutputHashMismatch`.
- `unpack.go:58` (bin verification) — compare all three; any mismatch is
  `errBinHashMismatch`.
- `unpack.go:158` (output verification) — compare all three; any mismatch is
  `errOutputHashMismatch`.
- `verify.go:63` (recovered scram verification) — compare all three; any
  mismatch is `errOutputHashMismatch`.

## Error handling and exit codes

Sentinels rename (mechanical, single-package rename):

- `errBinSHA256Mismatch` → `errBinHashMismatch`
- `errOutputSHA256Mismatch` → `errOutputHashMismatch`

Exit codes are unchanged: `errBinHashMismatch` → 5 (wrong .bin),
`errOutputHashMismatch` → 3 (verification failed). Mapping stays in
`pack.go`'s `packErrorToExit`, `unpack.go`'s `unpackErrorToExit`, and
`verify.go`'s `verifyErrorToExit`.

Mismatch messages identify which hash(es) failed:

```
bin hash mismatch: md5 got abc...123, manifest expects def...456;
sha1 got 789...abc, manifest expects fed...000;
sha256 matches
```

A single helper `compareHashes(got, want FileHashes) error` lives in `pack.go`
next to `hashFile`:

- Returns `nil` on all-match.
- Otherwise returns a plain (un-sentinel-wrapped) error whose message lists
  each hash's status (`md5 mismatch: got X, manifest expects Y; sha1 matches;
  sha256 mismatch: got A, manifest expects B`).
- Callers wrap with `fmt.Errorf("%w: %v", <their sentinel>, err)` to attach
  the bin/output/verify sentinel — preserves the existing
  `errors.Is(err, errBinHashMismatch)` style in the exit-code switches.

`errVerifyMismatch` (pack-internal, for the round-trip self-check) is not
renamed; it continues to wrap whatever `compareHashes` returns when Pack's
post-pack verify catches a divergence. Only the two SHA256-named sentinels
get renamed.

## inspect output

Human format gains four lines, in declaration order:

```
manifest:
  ...
  scram_size:             739729728
  scram_md5:              c3d4...full hex...e8
  scram_sha1:             d5e6...full hex...90
  scram_sha256:           abcd...full hex...ef
  bin_size:               739729728
  bin_md5:                7891...full hex...01
  bin_sha1:               9abc...full hex...23
  bin_sha256:             4567...full hex...89
  ...
```

Field-name alignment is unchanged (existing column-padding rule —
`scram_sha256:` was the longest; new field names are shorter so they slot in
without realignment).

JSON format: no special handling. `formatJSONInspect` already passes through
manifest fields verbatim, so the new fields appear naturally in the
serialized output between `scram_size` and `bin_size` per their struct order.

## Container version handling

`containerVersion` constant bumps from `0x02` to `0x03`. The
`ReadContainer` mismatch error gets updated message text:

```go
return nil, nil, fmt.Errorf("unsupported container version 0x%02x (this build expects 0x%02x); "+
    "v0.2 .miniscram files cannot be read directly by this build — re-pack from the original .bin",
    header[4], containerVersion)
```

`Manifest.FormatVersion` constant bumps from `2` to `3` (set in `pack.go`'s
manifest construction).

## Files touched

| File | Change |
| --- | --- |
| `manifest.go` | Add 4 fields to `Manifest`; bump `containerVersion`; update v0.2 → vN-mismatch message text. |
| `pack.go` | Replace `sha256File` with `hashFile` + `FileHashes` + `compareHashes`; populate new manifest fields; update `verifyRoundTrip` to compare all three; bump `FormatVersion` to 3. |
| `unpack.go` | Use `hashFile`; rename sentinels; compare all three at both check sites. |
| `verify.go` | Use `hashFile`; compare all three; reuse renamed sentinels. |
| `inspect.go` | Add the four `bin_md5`/`bin_sha1`/`scram_md5`/`scram_sha1` lines to the human formatter (alphabetical-within-group). JSON formatter unchanged. |
| `main.go` | Update `errors.Is` calls in `packErrorToExit`, `unpackErrorToExit`, `verifyErrorToExit` to use renamed sentinels. |

No new files. No new exit codes. No new CLI flags.

## Testing

| Test | Asserts |
| --- | --- |
| `TestHashFile_AllThreeMatchKnownVectors` | `hashFile` of a small known-content file returns expected md5/sha1/sha256 hex (use a precomputed reference, e.g. the empty string's known hashes). |
| `TestHashFile_OpenError` | Bad path returns the `os.Open` error verbatim. |
| `TestPackPopulatesAllSixHashFields` | After `Pack`, the manifest's BinMD5, BinSHA1, BinSHA256, ScramMD5, ScramSHA1, ScramSHA256 are all non-empty and match a separate-pass recompute. |
| `TestUnpackVerifiesAllThreeBinHashes` | Tamper one byte in the manifest's `bin_md5` (without changing the others), run unpack, expect `errBinHashMismatch` and exit 5. Same for `bin_sha1`. (Existing tampering tests already cover sha256.) |
| `TestUnpackVerifiesAllThreeOutputHashes` | Tamper `scram_md5` only, run unpack, expect `errOutputHashMismatch` and exit 3. Same for `scram_sha1`. |
| `TestVerifyVerifiesAllThreeOutputHashes` | Same shape as above but via `Verify`. |
| `TestReadContainerRejectsV2` | A hand-built v0.2 container produces the new migration error message; exit 4 from inspect. (Existing v0.1 rejection test pattern; copy and adapt.) |
| `TestInspectShowsAllSixHashes` | Human inspect output of a freshly-packed container contains all six `*_md5`, `*_sha1`, `*_sha256` lines with full hex values. |
| `TestInspectJSONIncludesAllSixHashes` | JSON inspect's top-level keys include `bin_md5`, `bin_sha1`, `scram_md5`, `scram_sha1`. |

Existing tests that reference the old sentinel names (`errBinSHA256Mismatch`,
`errOutputSHA256Mismatch`) get mechanical renames. The two existing
"tamper sha256" tests stay valid — they catch sha256 mismatches; the new
md5/sha1 tamper tests cover the policy difference.

## Migration notes

This cycle bumps `format_version` from 2 to 3 and the container version byte
from 0x02 to 0x03. Per user confirmation, no extant v2 containers need
to be preserved. Anyone with a v2 container needs to re-pack from the
original `.bin`.

The previous v0.1→v0.2 migration message format is preserved verbatim
(just s/v0.1/v0.2/ and update the build-version reference), so the user
experience of hitting an unsupported container is consistent across both
bumps.

## Out of scope (deferred to later items)

- **CRC-32 manifest field.** Add when redump.org tooling explicitly needs
  it, otherwise YAGNI.
- **Multi-version read compatibility.** Single-version reader keeps the
  code simple; if a future format change adds genuinely-needed-to-preserve
  state, revisit then.
- **Hash policy options on the CLI** (e.g., `--allow-md5-mismatch`). The
  user picked strict (any-of-three exits 3) and there's no foreseen reason
  to relax that. If a real archivist workflow ever wants tolerance, expose
  it then.
- **Algorithmic changes** to hash storage (e.g., binary not hex). Hex
  matches Redumper templates; cost of binary storage is negligible.
