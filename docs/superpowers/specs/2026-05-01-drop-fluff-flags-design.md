# Drop fluff flags: --no-verify, --allow-cross-fs (+ inspect help tidy)

Date: 2026-05-01
Status: design

## Motivation

Three small CLI surface cleanups that have accumulated since v1:

1. **`--no-verify`** (pack and unpack) is fluff. Skipping the post-pack
   round-trip verify defeats miniscram's central safety promise (a
   verified container is what makes deleting `<scram>` safe). Skipping
   the post-unpack hash compare reproduces bytes the user can't trust.
   No test references it.

2. **`--allow-cross-fs`** is dubious in its current form:
   - It guards `os.Remove(scramPath)`, which is *not* a cross-fs
     operation — `os.Remove` only touches scram's filesystem. The
     "cross-fs" framing is misleading.
   - Pack already performed a verify round-trip (`ReadContainer`
     re-reads from disk) before reaching the delete, so any
     data-integrity worry has already resolved.
   - On Windows, `sameFilesystem` returns `true` unconditionally,
     making the flag a no-op there. Platform-asymmetric protection
     is a smell.
   - The user-intent guess it implements ("user probably wanted to
     keep source on origin fs") is paternalistic, and we already have
     `--keep-source` for exactly that.

3. **`inspect --full` help text** says "(no cap)", which reads as
   genz slang rather than "no upper limit." Also redundant — "every
   override" already conveys completeness.

## Non-goals

- Don't touch historical spec docs in `docs/superpowers/specs/`.
  They're dated snapshots and reflect the design at the time.
- Don't change verification logic. `UnpackOptions.Verify` stays as
  internal API (the `Verify` subcommand sets it to `false` to avoid
  double-hashing — see unpack.go:221). `SuppressVerifyWarning`
  becomes dead once the warn is removed and goes with it.

## Changes

### `--no-verify` removal

**`main.go` (`runPack`):**
- Drop `noVerify` flag declaration (line 135).
- Drop `noVerifyImpliesKeep` block (lines 159–166).
- `Pack` call sets nothing for `Verify` (field will be removed from
  `PackOptions`; verification is unconditional).

**`main.go` (`runUnpack`):**
- Drop `noVerify` flag declaration (line 190).
- `Unpack` call always sets `Verify: true`.

**`pack.go`:**
- Drop `Verify` field from `PackOptions`. Verification is unconditional.
- Drop the `if !opts.Verify { ... return nil }` skip branch (lines
  162–165). The round-trip `Verify` call below it always runs.
- Update the leading comment at line 161 from "verify by round-tripping
  (unless --no-verify)" to "verify by round-tripping."

**`unpack.go`:**
- Keep `UnpackOptions.Verify` (still used internally by the `Verify`
  subcommand to skip Unpack's hash compare).
- Drop the `if !opts.SuppressVerifyWarning { r.Warn("verification
  skipped (--no-verify)") }` block (lines 160–162). The warn is
  unreachable once CLI always passes `Verify: true`, and with no
  warn left to suppress, `SuppressVerifyWarning` becomes dead.
- Drop `SuppressVerifyWarning` from `UnpackOptions` (line 24).
- Drop `SuppressVerifyWarning: true` from the `Unpack(...)` call in
  `Verify` (line 226).
- The `if !opts.Verify { return nil }` early-return collapses to a
  bare `if !opts.Verify { return nil }` and stays (it's still the
  path the `Verify` subcommand takes).

**`help.go`:**
- Drop both `--no-verify` lines (57–58 in pack help; 78 in unpack help).

**`README.md`:**
- Drop `[--no-verify]` from both USAGE lines (228, 238).

### `--allow-cross-fs` removal

**`main.go`:**
- Drop `allowCrossFS` flag declaration (line 136).
- Replace the `maybeRemoveSource(scramPath, out, allowCrossFS, rep)`
  call site (line 175) with an inline `os.Remove(scramPath)` plus the
  existing reporter info/warn lines. The whole `maybeRemoveSource`
  helper goes away.

**Delete files:**
- `samefs_unix.go` (build-tagged `unix`)
- `samefs_windows.go` (build-tagged `windows`)

**`help.go`:**
- Drop the `--allow-cross-fs` lines (59–60).

### `inspect --full` help wording

**`help.go`:**
- In `inspectHelpText`, change:
  ```
      --full         append a per-record listing of every override
                     (no cap). without it, only the override count
                     is printed.
  ```
  to:
  ```
      --full         append a per-record listing of every override.
                     without it, only the override count is printed.
  ```

## Behavior changes (user-visible)

- `pack` always verifies. Anyone passing `--no-verify` today gets
  "flag provided but not defined" → exit 1 (usage). This is a
  breaking CLI change. Acceptable: project is pre-2.0 and this flag
  was a footgun.
- `unpack` always hashes. Same breaking-change story.
- `pack` always auto-deletes `<scram>` after a verified pack,
  regardless of filesystem. `--keep-source` remains the single way
  to opt out. Anyone passing `--allow-cross-fs` today gets the same
  "not defined" usage error.
- `inspect --help` reads cleanly without the "(no cap)" idiom.

## Testing

No existing tests reference the removed flags (`grep` confirms). The
behavioral change is in CLI flag set definitions, not in `Pack` /
`Unpack` core logic — those still call `Verify` exactly as before
when `Verify: true`, which is now the only path.

A targeted CLI test for the new behavior is optional; the change is
mostly deletion. If we add anything, a `cli_test.go` smoke test that
asserts `pack --no-verify` and `pack --allow-cross-fs` both exit
non-zero would document the breakage. Low value — skip unless trivial.

## Out of scope

- Renaming `UnpackOptions.Verify` to make its now-internal-only
  status explicit. The field is fine as-is; the CLI just stops
  exposing it.
- Reworking the auto-delete path beyond the flag removal. Default
  behavior (delete on verified pack, `--keep-source` to opt out)
  stands.
