# miniscram verify — non-destructive integrity check

A new `miniscram verify [<bin> <miniscram>]` subcommand. Rebuilds the
recovered `.scram` to a temporary file, hashes it, compares the result
against `manifest.scram_sha256`, then deletes the temp file. Catches
container corruption without producing the multi-hundred-MB output
file the user would have to manage.

The v0.2 design at `docs/superpowers/specs/2026-04-28-miniscram-v0.2-design.md`
defines the container format and pack/unpack pipeline; this spec
assumes that baseline and only describes the new subcommand.

This corresponds to TASKS.md item **A2**.

## Goals

- Give an archivist a one-command "is this container still valid"
  check. Same operation pack.go's post-pack `verifyRoundTrip` does,
  but exposed as a CLI command for ad-hoc auditing of containers
  packed in earlier sessions.
- Match `unpack`'s discovery and reporter ergonomics so users can
  swap `verify` in for `unpack` without thinking about flags.
- Catch wrong-bin and content-corruption failures with distinct exit
  codes for scripting.

## Non-goals

- Structural framing validation (truncated headers, bad magic, bad
  manifest length). That's C2 / `inspect --check`.
- Layout heuristics on bin/manifest mismatch. By the time a
  `.miniscram` exists, layout has already been validated at pack
  time; verify cannot encounter `LayoutMismatchError`.
- JSON output. Verify's payload is two hashes and a verdict; adding
  `--json` is cheap but unmotivated. Defer until external tooling
  asks for it (the v0.1 spec's "no JSON unless asked" stance, per
  TASKS.md theme D).
- A `--full-bin-check` flag (TASKS.md A2 open question). The normal
  flow already fast-exits on wrong-bin via the bin sha256 check; a
  separate flag would be redundant.

## CLI surface

```
miniscram verify [<bin> <in.miniscram>] [-q|--quiet] [-h|--help]
```

| Position count | Resolution |
| --- | --- |
| 0 | discover from cwd: exactly one `*.bin` + one `*.miniscram` |
| 1 | discover from stem: `<arg>.bin` + `<arg>.miniscram` (extensions stripped) |
| 2 | explicit bin and container paths in that order |

Identical resolution to `unpack`, via the existing
`resolveUnpackInputs`. No `-o` flag — verify produces no output file.
No `-f` / `--force` — there is nothing to overwrite.

The reporter writes step progress to stderr, matching `unpack`. There
is no stdout output. On success the final reporter line is the
user-visible "OK" signal; on failure the reporter surfaces the
mismatched hash pair before the process exits.

## Reporter step list

1. `running scramble-table self-test`
2. `reading container <path>` — manifest + delta sizes
3. `verifying bin sha256` — match or fast-exit 5
4. `building ε̂` — to a temp file under `filepath.Dir(containerPath)`
5. `applying delta` — N bytes of delta applied
6. `verifying scram sha256` — match or exit 3

Steps 1–5 are produced by `Unpack(..., Verify: false)` exactly as
they appear in real unpack runs. Step 6 is verify's only addition,
mirroring the "verifying output sha256" step that Unpack itself
emits when `Verify: true`.

`-q` / `--quiet` suppresses progress, matching unpack.

## Architecture

`runVerify(args, stderr)` follows the same shape as `runUnpack`:

1. Parse flags via `flag.NewFlagSet`.
2. Resolve positionals via `resolveUnpackInputs` (0/1/2 shapes).
3. `ReadContainer(containerPath)` to obtain the manifest. The
   manifest is parsed again inside Unpack but it's small (KiB) and
   the round-trip cost is negligible.
4. `os.CreateTemp(filepath.Dir(containerPath), "miniscram-verify-*")`
   to allocate a tempfile, close it (Unpack reopens it via its own
   handle), and `defer os.Remove`.
5. Call `Unpack(UnpackOptions{BinPath, ContainerPath, OutputPath:
   tempPath, Verify: false, Force: true}, reporter)`. `Force: true`
   is required because the tempfile already exists.
6. `sha256File(tempPath)` and compare to `manifest.scram_sha256`.
7. Emit the `verifying scram sha256` step on the reporter — `Done`
   on match, `Fail` on mismatch (returning `errOutputSHA256Mismatch`).

The deferred `os.Remove` cleans up regardless of outcome. Tempfile
location follows the existing `verifyRoundTrip` convention of
`filepath.Dir(containerPath)`: the container's filesystem already
holds a multi-hundred-MB-ish artifact, so the temporary recovered
scram fits.

No new exported helpers. No refactor of `Unpack` or `verifyRoundTrip`.
Pack's existing `verifyRoundTrip` keeps its current shape; A2
deliberately does not extract a shared `rebuildAndHashScram`
helper — the duplicated logic is a `Unpack(...) → sha256File →
compare` block in two places, ~12 lines total. A refactor would be
worth doing only if a third caller appears.

`IterateDeltaRecords` (introduced by A1) is **not** consumed by
verify. Verify's job is full content reconstruction; record
enumeration alone wouldn't catch payload corruption.

## Errors and exit codes

| Condition | Exit |
| --- | --- |
| success | 0 |
| usage error (bad flags, wrong arg count, missing files) | 1 |
| `errBinSHA256Mismatch` (from Unpack step 3) | 5 |
| `errOutputSHA256Mismatch` (from verify's step 6) | 3 |
| any other error during read/build/apply | 4 |

Reuses the existing sentinels `errBinSHA256Mismatch` and
`errOutputSHA256Mismatch` already defined in `unpack.go`. The exit
mapping lives in a new `verifyErrorToExit` mirroring
`unpackErrorToExit`. Missing input files surface as `os.Stat` errors
out of `resolveUnpackInputs` and route through the usage path
(exit 1), matching unpack.

## Wiring

`main.go`:

- Add `"verify"` case in `run()` dispatching to
  `runVerify(args[1:], stderr)`. Stdout is not threaded — verify
  produces no stdout output.
- Add `verify` to the help-subcommand switch (`miniscram help verify`).
- Add `runVerify` and `verifyErrorToExit` functions.

`help.go`:

- New `verifyHelpText` block listing usage, positional shapes,
  options, and exit codes.
- Top-level `topHelpText` gains a `verify` line in the COMMANDS list.
- New `printVerifyHelp` follows the existing pattern.

## Testing

`verify_test.go` (new). Hermetic — uses the same in-memory pack
helpers (`writeSynthDiscFiles`, `Pack`) that `inspect_test.go` and
`unpack_test.go` already use. No real disc data.

| Test | Asserts |
| --- | --- |
| `TestVerifyOK` | Pack a synthetic disc, run verify on the result, expect exit 0 and no `*.scram` or stray temp file in the container's directory. |
| `TestVerifyFailContainerTampered` | Pack, mutate one byte inside the delta payload region of the container on disk. Verify exits 3 with both hashes (manifest's and computed) in the error message. |
| `TestVerifyFailWrongBin` | Pack with bin A, run verify with a same-sized but different bin B. Exits 5 via `errBinSHA256Mismatch`. |
| `TestVerifyTempCleanup` | After both success and failure paths, no `miniscram-verify-*` file remains in the container's directory. |
| `TestCLIVerifyDiscovery` | 0-arg (cwd), 1-arg (stem), 2-arg (explicit) all succeed against the same container. Mirrors `TestCLIPackDiscovers` in `main_test.go`. |
| `TestVerifyUsageErrors` | Three positionals → exit 1. Unknown flag → exit 1. `--help` / `-h` → exit 0 with help text. Missing bin or container file → exit 1. |
| `TestVerifyHelp` | `miniscram help verify` prints `verifyHelpText`. |

The byte-tampering test (`TestVerifyFailContainerTampered`) is the
load-bearing one — it covers the failure mode A2 exists to catch.
Other failure modes (wrong bin, missing files) are already
exercisable via unpack and are tested for verify mostly to confirm
the exit-code mapping.

Golden values for human reporter output are kept simple: assert
substrings on specific lines (matches the existing test style in
`unpack_test.go`).

## File/LOC summary

| File | Action | Approx LOC |
| --- | --- | --- |
| `verify.go` | new | ~80 |
| `verify_test.go` | new | ~150 |
| `main.go` | dispatch, help case, `verifyErrorToExit` | +15 |
| `help.go` | top-level mention, verify help block | +30 |

Total: ~275 LOC. TASKS.md sized A2 at "~120 LOC, half a day"; the
budget undercounts tests, which here drive most of the line count.

## Out of scope (deferred to later items)

- **`--json` output.** When/if a real archivist tool needs it, the
  natural shape is `{"ok": bool, "manifest_sha256": "...",
  "computed_sha256": "..."}` — easy to add later without breaking
  the text path.
- **A3 / layout-failure diagnostics.** Verify cannot trigger
  `LayoutMismatchError`, so the heuristic message is irrelevant
  here.
- **C1 / md5+sha1 manifest fields.** When added, verify will
  automatically compare them along with sha256 because the manifest
  drives the comparison set. No verify-specific change needed.
- **Refactor of pack's `verifyRoundTrip`** to share logic with A2's
  rebuild. Worth doing if a third call site appears (e.g., A4 batch
  mode's `--continue` predicate).
- **C2 / fsck.** Verify's job is content; structural framing is C2's
  territory.
