# Drop fluff flags Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the fluff flags `--no-verify` and `--allow-cross-fs` from the CLI, delete dead support code, and tidy the `inspect --full` help wording.

**Architecture:** Three independent, narrowly-scoped deletion tasks against the existing single-package Go binary. Each task touches a distinct concern: cross-fs check (Task 1), verify-skip (Task 2), help-text idiom (Task 3). Each lands as one commit.

**Tech Stack:** Go (single `main` package), `flag` stdlib, `go test ./...`

**Spec:** `docs/superpowers/specs/2026-05-01-drop-fluff-flags-design.md`

---

## File Structure

No new files. Two file deletions, edits across the existing source tree:

- **Delete:** `samefs_unix.go`, `samefs_windows.go`
- **Modify:** `main.go`, `pack.go`, `unpack.go`, `help.go`, `README.md`
- **Modify (tests):** `pack_test.go`, `verify_test.go`, `cli_test.go`, `e2e_test.go`, `e2e_redump_test.go`

---

## Task 1: Remove `--allow-cross-fs` and the samefs check

**Goal:** Delete the cross-filesystem guard around source `.scram` auto-delete. Pack always removes `<scram>` after a verified pack (unless `--keep-source`), regardless of filesystem.

**Files:**
- Modify: `main.go` (drop flag, drop `maybeRemoveSource` helper, inline `os.Remove`)
- Modify: `help.go` (drop `--allow-cross-fs` lines from pack help)
- Delete: `samefs_unix.go`
- Delete: `samefs_windows.go`

**Acceptance Criteria:**
- [ ] `miniscram pack --allow-cross-fs disc.cue` exits non-zero with "flag provided but not defined: -allow-cross-fs"
- [ ] No file in the repo references `sameFilesystem`, `maybeRemoveSource`, `allowCrossFS`, or `--allow-cross-fs`
- [ ] `go test ./...` passes
- [ ] `go vet ./...` clean

**Verify:** `go build ./... && go test ./... && grep -r 'sameFilesystem\|allowCrossFS\|allow-cross-fs\|maybeRemoveSource' . --include='*.go' --include='*.md' | grep -v 'docs/superpowers/specs/2026-04-2'` → no output (historical specs are excluded by design)

**Steps:**

- [ ] **Step 1: Delete `samefs_unix.go` and `samefs_windows.go`**

```bash
rm samefs_unix.go samefs_windows.go
```

- [ ] **Step 2: Edit `main.go` — drop the flag declaration**

In `runPack` (around line 130), change:

```go
	var keepSource, noVerify, allowCrossFS, force, forceLong bool
	positional, common, exit, ok := parseSubcommand("pack", packHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&output, "o", "", "output path")
		fs.StringVar(&outputLong, "output", "", "output path")
		fs.BoolVar(&keepSource, "keep-source", false, "keep .scram after verified pack")
		fs.BoolVar(&noVerify, "no-verify", false, "skip round-trip verification")
		fs.BoolVar(&allowCrossFS, "allow-cross-fs", false, "permit auto-delete across filesystems")
		fs.BoolVar(&force, "f", false, "overwrite output")
		fs.BoolVar(&forceLong, "force", false, "overwrite output")
	})
```

to (Task 2 will remove the `noVerify` line; for Task 1 just drop `allowCrossFS`):

```go
	var keepSource, noVerify, force, forceLong bool
	positional, common, exit, ok := parseSubcommand("pack", packHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&output, "o", "", "output path")
		fs.StringVar(&outputLong, "output", "", "output path")
		fs.BoolVar(&keepSource, "keep-source", false, "keep .scram after verified pack")
		fs.BoolVar(&noVerify, "no-verify", false, "skip round-trip verification")
		fs.BoolVar(&force, "f", false, "overwrite output")
		fs.BoolVar(&forceLong, "force", false, "overwrite output")
	})
```

- [ ] **Step 3: Edit `main.go` — replace the `maybeRemoveSource` call**

In `runPack`, change:

```go
	if !keepSource {
		if removed, removeErr := maybeRemoveSource(scramPath, out, allowCrossFS, rep); removeErr != nil {
			rep.Warn("source removal skipped: %v", removeErr)
		} else if removed {
			rep.Info("removed source %s", scramPath)
		}
	}
```

to:

```go
	if !keepSource {
		if err := os.Remove(scramPath); err != nil {
			rep.Warn("source removal skipped: %v", err)
		} else {
			rep.Info("removed source %s", scramPath)
		}
	}
```

- [ ] **Step 4: Edit `main.go` — delete the `maybeRemoveSource` helper**

Delete the entire function (lines around 230–239):

```go
func maybeRemoveSource(scramPath, outPath string, allowCrossFS bool, r Reporter) (bool, error) {
	if !sameFilesystem(scramPath, outPath) && !allowCrossFS {
		return false, fmt.Errorf("output %s is on a different filesystem from %s; pass --allow-cross-fs to permit auto-delete",
			outPath, scramPath)
	}
	if err := os.Remove(scramPath); err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 5: Edit `help.go` — drop `--allow-cross-fs` from pack help**

In `packHelpText`, remove these two lines:

```
    --allow-cross-fs       permit auto-delete of <scram> when <out>
                           is on a different filesystem.
```

- [ ] **Step 6: Build and run all tests**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: clean build, clean vet, all tests pass.

- [ ] **Step 7: Verify no residual references**

Run: `grep -rn 'sameFilesystem\|allowCrossFS\|allow-cross-fs\|maybeRemoveSource' . --include='*.go' --include='*.md' | grep -v 'docs/superpowers/specs/2026-04'`
Expected: no output. (Historical specs from 2026-04 may retain references; they're frozen snapshots.)

- [ ] **Step 8: Commit**

```bash
git add main.go help.go
git rm samefs_unix.go samefs_windows.go
git commit -m "$(cat <<'EOF'
refactor: drop --allow-cross-fs and the samefs cross-fs check

The cross-fs guard around <scram> auto-delete was a no-op on Windows
(sameFilesystem returned true unconditionally) and didn't model anything
real on Unix — os.Remove only touches scram's filesystem, and Pack has
already verified the just-written container by the time we reach the
delete. The user-intent guess it implemented is already covered by
--keep-source.
EOF
)"
```

---

## Task 2: Remove `--no-verify`

**Goal:** Remove the `--no-verify` flag from both `pack` and `unpack`. Pack always verifies. Unpack always hashes the output. Internal `UnpackOptions.Verify` survives so the `Verify` subcommand can still suppress Unpack's redundant hash compare; `SuppressVerifyWarning` becomes dead and goes.

**Files:**
- Modify: `main.go` (drop flags from `runPack` and `runUnpack`, simplify call sites)
- Modify: `pack.go` (drop `Verify` field from `PackOptions`, drop skip-branch, update comment)
- Modify: `unpack.go` (drop the warn block, drop `SuppressVerifyWarning` field)
- Modify: `help.go` (drop both `--no-verify` lines)
- Modify: `README.md` (drop `[--no-verify]` from both USAGE lines)
- Modify (tests): drop `Verify: true` from 7 `PackOptions` literals: `pack_test.go:21`, `verify_test.go:23`, `cli_test.go:147`, `cli_test.go:182`, `e2e_test.go:58`, `e2e_test.go:110`, `e2e_redump_test.go:120`

**Acceptance Criteria:**
- [ ] `miniscram pack --no-verify disc.cue` exits non-zero with "flag provided but not defined: -no-verify"
- [ ] `miniscram unpack --no-verify disc.miniscram` exits non-zero with "flag provided but not defined: -no-verify"
- [ ] No file references `noVerify`, `--no-verify`, `SuppressVerifyWarning`, or `PackOptions.Verify` (the field is gone)
- [ ] `UnpackOptions.Verify` still exists (used internally by `Verify` subcommand)
- [ ] `go test ./...` passes (incl. `cli_test.go` flag-parsing test, which only exercises `--keep-source`)
- [ ] `go vet ./...` clean

**Verify:** `go build ./... && go vet ./... && go test ./...` → all pass; `grep -rn 'no-verify\|noVerify\|SuppressVerifyWarning' . --include='*.go' --include='*.md' | grep -v 'docs/superpowers/specs/2026-04'` → no output

**Steps:**

- [ ] **Step 1: Edit `pack.go` — drop `Verify` field from `PackOptions`**

Change:

```go
type PackOptions struct {
	CuePath    string
	ScramPath  string
	OutputPath string
	LeadinLBA  int32 // 0 → use LBALeadinStart
	Verify     bool
}
```

to:

```go
type PackOptions struct {
	CuePath    string
	ScramPath  string
	OutputPath string
	LeadinLBA  int32 // 0 → use LBALeadinStart
}
```

- [ ] **Step 2: Edit `pack.go` — drop the skip-verify branch and update comment**

Change (around line 161):

```go
	// 8. verify by round-tripping (unless --no-verify).
	if !opts.Verify {
		r.Warn("verification skipped (--no-verify)")
		return nil
	}
	if err := Verify(VerifyOptions{ContainerPath: opts.OutputPath}, r); err != nil {
```

to:

```go
	// 8. verify by round-tripping.
	if err := Verify(VerifyOptions{ContainerPath: opts.OutputPath}, r); err != nil {
```

- [ ] **Step 3: Edit `unpack.go` — drop `SuppressVerifyWarning` field**

Change (around line 19):

```go
type UnpackOptions struct {
	ContainerPath         string
	OutputPath            string
	Verify                bool
	Force                 bool
	SuppressVerifyWarning bool // skip the "verification skipped" Warn; for callers that perform their own verification (e.g. Verify)
}
```

to:

```go
type UnpackOptions struct {
	ContainerPath string
	OutputPath    string
	Verify        bool
	Force         bool
}
```

- [ ] **Step 4: Edit `unpack.go` — drop the warn block**

Change (around line 158):

```go
	// Verify recovered scram hashes (unless skipped).
	if !opts.Verify {
		if !opts.SuppressVerifyWarning {
			r.Warn("verification skipped (--no-verify)")
		}
		return nil
	}
```

to:

```go
	// Verify recovered scram hashes (unless caller opts out — the Verify
	// subcommand does this to avoid double-hashing the rebuilt scram).
	if !opts.Verify {
		return nil
	}
```

- [ ] **Step 5: Edit `unpack.go` — drop `SuppressVerifyWarning` from the `Verify` subcommand call**

Change (around line 221):

```go
	if err := Unpack(UnpackOptions{
		ContainerPath:         opts.ContainerPath,
		OutputPath:            tmpPath,
		Verify:                false,
		Force:                 true,
		SuppressVerifyWarning: true,
	}, r); err != nil {
```

to:

```go
	if err := Unpack(UnpackOptions{
		ContainerPath: opts.ContainerPath,
		OutputPath:    tmpPath,
		Verify:        false,
		Force:         true,
	}, r); err != nil {
```

- [ ] **Step 6: Edit `main.go` — `runPack` flag and call site**

Change:

```go
	var keepSource, noVerify, force, forceLong bool
	positional, common, exit, ok := parseSubcommand("pack", packHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&output, "o", "", "output path")
		fs.StringVar(&outputLong, "output", "", "output path")
		fs.BoolVar(&keepSource, "keep-source", false, "keep .scram after verified pack")
		fs.BoolVar(&noVerify, "no-verify", false, "skip round-trip verification")
		fs.BoolVar(&force, "f", false, "overwrite output")
		fs.BoolVar(&forceLong, "force", false, "overwrite output")
	})
```

to:

```go
	var keepSource, force, forceLong bool
	positional, common, exit, ok := parseSubcommand("pack", packHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&output, "o", "", "output path")
		fs.StringVar(&outputLong, "output", "", "output path")
		fs.BoolVar(&keepSource, "keep-source", false, "keep .scram after verified pack")
		fs.BoolVar(&force, "f", false, "overwrite output")
		fs.BoolVar(&forceLong, "force", false, "overwrite output")
	})
```

Then drop the `noVerifyImpliesKeep` block and update the `Pack` call. Change:

```go
	noVerifyImpliesKeep := noVerify && !keepSource
	if noVerify {
		keepSource = true
	}
	rep := NewReporter(stderr, common.quiet)
	if noVerifyImpliesKeep {
		rep.Info("--no-verify implies --keep-source; original .scram will be kept")
	}
	err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath, OutputPath: out,
		LeadinLBA: LBALeadinStart, Verify: !noVerify,
	}, rep)
```

to:

```go
	rep := NewReporter(stderr, common.quiet)
	err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath, OutputPath: out,
		LeadinLBA: LBALeadinStart,
	}, rep)
```

- [ ] **Step 7: Edit `main.go` — `runUnpack` flag and call site**

Change:

```go
	var noVerify, force, forceLong bool
	positional, common, exit, ok := parseSubcommand("unpack", unpackHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&output, "o", "", "output path")
		fs.StringVar(&outputLong, "output", "", "output path")
		fs.BoolVar(&noVerify, "no-verify", false, "skip output hash verification")
		fs.BoolVar(&force, "f", false, "overwrite output")
		fs.BoolVar(&forceLong, "force", false, "overwrite output")
	})
```

to:

```go
	var force, forceLong bool
	positional, common, exit, ok := parseSubcommand("unpack", unpackHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&output, "o", "", "output path")
		fs.StringVar(&outputLong, "output", "", "output path")
		fs.BoolVar(&force, "f", false, "overwrite output")
		fs.BoolVar(&forceLong, "force", false, "overwrite output")
	})
```

And change the `Unpack` call:

```go
	if err := Unpack(UnpackOptions{
		ContainerPath: containerPath, OutputPath: out,
		Verify: !noVerify, Force: force || forceLong,
	}, rep); err != nil {
```

to:

```go
	if err := Unpack(UnpackOptions{
		ContainerPath: containerPath, OutputPath: out,
		Verify: true, Force: force || forceLong,
	}, rep); err != nil {
```

- [ ] **Step 8: Edit `help.go` — drop both `--no-verify` lines**

In `packHelpText`, remove:

```
    --no-verify            skip inline round-trip verification.
                           implies --keep-source.
```

In `unpackHelpText`, remove:

```
    --no-verify            skip output hash verification (md5/sha1/sha256).
```

- [ ] **Step 9: Edit `README.md`**

Change:

```
    miniscram pack disc.cue [-o out.miniscram] [-f] [--no-verify] [--keep-source]
```

to:

```
    miniscram pack disc.cue [-o out.miniscram] [-f] [--keep-source]
```

Change:

```
    miniscram unpack disc.miniscram [-o out.scram] [-f] [--no-verify]
```

to:

```
    miniscram unpack disc.miniscram [-o out.scram] [-f]
```

- [ ] **Step 10: Edit test files — drop `Verify: true` from 7 `PackOptions` literals**

In each of these files, remove the `Verify: true` keyword from the indicated `PackOptions{...}` literal (and tidy trailing comma if needed):

- `pack_test.go:21` — `OutputPath: out, LeadinLBA: LBAPregapStart, Verify: true,` → `OutputPath: out, LeadinLBA: LBAPregapStart,`
- `verify_test.go:23` — same shape, drop `Verify: true,`
- `cli_test.go:147` — `LeadinLBA: LBAPregapStart, Verify: true,` → `LeadinLBA: LBAPregapStart,`
- `cli_test.go:182` — same shape
- `e2e_test.go:58` — drop the line `Verify:     true,` (the `Verify` keyword stands alone on its line)
- `e2e_test.go:110` — same: drop the standalone `Verify:     true,` line
- `e2e_redump_test.go:120` — drop standalone `Verify:     true,` line

Do not touch the `UnpackOptions{... Verify: true ...}` literals at `unpack_test.go:35,49,132`, `e2e_test.go:79,134`, `e2e_redump_test.go:157` — `UnpackOptions.Verify` survives.

- [ ] **Step 11: Build and run all tests**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: clean build, clean vet, all tests pass. The `cli_test.go` flag-parsing test (`TestParseSubcommandInterleaved`) registers its own flag set with only `-o` and `--keep-source`, so it is unaffected by the CLI flag removals.

- [ ] **Step 12: Manual smoke check the rejection of removed flags**

Run:
```bash
go build -o /tmp/miniscram-smoke ./...
/tmp/miniscram-smoke pack --no-verify nope.cue 2>&1 | head -3
/tmp/miniscram-smoke unpack --no-verify nope.miniscram 2>&1 | head -3
rm /tmp/miniscram-smoke
```
Expected: each prints "flag provided but not defined: -no-verify" (Go's `flag` stdlib message) and exits non-zero.

- [ ] **Step 13: Verify no residual references**

Run: `grep -rn 'no-verify\|noVerify\|SuppressVerifyWarning' . --include='*.go' --include='*.md' | grep -v 'docs/superpowers/specs/2026-04'`
Expected: no output.

- [ ] **Step 14: Commit**

```bash
git add main.go pack.go unpack.go help.go README.md \
        pack_test.go verify_test.go cli_test.go e2e_test.go e2e_redump_test.go
git commit -m "$(cat <<'EOF'
refactor: drop --no-verify from pack and unpack

A verifying pack is what makes deleting <scram> safe; --no-verify
defeated that promise and forced --keep-source as a workaround. Unpack
without the output hash compare reproduces bytes the caller can't
trust. PackOptions.Verify field goes; UnpackOptions.Verify stays as
internal API (used by the Verify subcommand to skip Unpack's hash
compare). SuppressVerifyWarning is dead with the warn message gone.
EOF
)"
```

---

## Task 3: Tidy `inspect --full` help wording

**Goal:** Drop the "(no cap)" idiom from `inspect --full`'s help text. The phrase reads as genz slang ("no cap" = "no lie") and is redundant with "every override."

**Files:**
- Modify: `help.go` (`inspectHelpText`)

**Acceptance Criteria:**
- [ ] `miniscram inspect --help` no longer shows "(no cap)"
- [ ] `go test ./...` passes (no inspect help-text snapshot tests exist; double-check)

**Verify:** `go build ./... && go test ./... && grep -n 'no cap' help.go` → no match in help.go

**Steps:**

- [ ] **Step 1: Edit `help.go` — `inspectHelpText`**

Change:

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

- [ ] **Step 2: Confirm no snapshot test references this string**

Run: `grep -rn 'no cap\|every override' . --include='*.go' --include='*.md' | grep -v 'docs/superpowers/specs/'`
Expected: only `help.go` matches `every override`; no matches for `no cap`.

- [ ] **Step 3: Build and run all tests**

Run: `go build ./... && go test ./...`
Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add help.go
git commit -m "docs: drop '(no cap)' idiom from inspect --full help"
```

---

## Self-Review Notes

- **Spec coverage:** Task 1 covers the `--allow-cross-fs` removal (spec §"`--allow-cross-fs` removal" + samefs deletion). Task 2 covers the `--no-verify` removal across main.go/pack.go/unpack.go/help.go/README.md plus the seven `PackOptions` test sites enumerated in the spec's Testing section. Task 3 covers the inspect help wording change. No spec section is uncovered.
- **Type consistency:** `PackOptions.Verify` is dropped consistently across producer (pack.go) and all 7 consumer test sites. `UnpackOptions.Verify` survives at producer (unpack.go) and is unchanged at the 6 consumer test sites + the `Verify` subcommand call site. `SuppressVerifyWarning` is removed at producer (unpack.go) and the one consumer (`Verify` subcommand in unpack.go).
- **Order matters:** Task 1 and Task 2 both edit `runPack`'s flag block in `main.go`. Doing Task 1 first leaves the `noVerify` line intact for Task 2 to remove cleanly. Task 3 is independent.
