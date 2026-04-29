# miniscram verify Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement TASKS.md item A2 — a `miniscram verify [<bin> <miniscram>]` subcommand that rebuilds the recovered `.scram` to a tempfile, hashes it, compares against `manifest.scram_sha256`, then deletes the tempfile.

**Architecture:** A new `Verify(opts, r)` Go function (mirroring `Pack` and `Unpack`) does the work. Internally it calls `Unpack(..., Verify: false, Force: true)` against a tempfile under the container's directory, then sha256s and compares. A thin `runVerify` wraps it for the CLI. No refactor of `Unpack` or pack.go's `verifyRoundTrip` — A2 is a new caller, not a refactor.

**Tech Stack:** Go stdlib only. Reuses existing `Unpack`, `ReadContainer`, `sha256File`, `resolveUnpackInputs`, `Reporter`, and exit-code constants.

**Variance from spec:** The spec (in `docs/superpowers/specs/2026-04-28-miniscram-verify-design.md`) describes the work happening inside `runVerify` directly. The plan splits it into a `Verify(opts, r)` Go function plus a thin `runVerify` CLI wrapper. This matches the existing `Pack`/`runPack` and `Unpack`/`runUnpack` shape (which the spec invokes by analogy with "follows the same shape as runUnpack") and gives unit-testable seams. No behavioral change.

---

## File Structure

| File | Role |
| --- | --- |
| `verify.go` (new) | `VerifyOptions` struct, `Verify(opts, r)` function. ~60 LOC. |
| `verify_test.go` (new) | Function-level and CLI-level tests, all hermetic. ~180 LOC. |
| `main.go` (modify) | `verify` dispatch in `run()`, `runVerify`, `verifyErrorToExit`, help-subcommand case. ~25 LOC added. |
| `help.go` (modify) | `verifyHelpText`, `printVerifyHelp`, top-level `verify` mention. ~30 LOC added. |

---

## Task 1: Verify Go function with tests

**Goal:** Implement the core `Verify` function and prove it correct end-to-end at the function level. Three behavioral tests (OK, tampered manifest, wrong bin) plus a tempfile-cleanup assertion.

**Files:**
- Create: `verify.go`
- Create: `verify_test.go`

**Acceptance Criteria:**
- [ ] `Verify(VerifyOptions{BinPath, ContainerPath}, Reporter) error` returns `nil` for a valid container.
- [ ] Returns `errOutputSHA256Mismatch` (wrapped via `%w`) when `manifest.scram_sha256` is tampered.
- [ ] Returns `errBinSHA256Mismatch` when the supplied bin doesn't match `manifest.bin_sha256`.
- [ ] No `miniscram-verify-*` tempfile remains in the container's directory after either success or failure.
- [ ] Reporter receives a final `verifying scram sha256` step (Done on success, Fail on mismatch).

**Verify:** `go test ./... -run TestVerify -v` → all four `TestVerify*` tests pass.

**Steps:**

- [ ] **Step 1: Write `verify_test.go` with the four behavioral tests (red phase)**

```go
// /home/hugh/miniscram/verify_test.go
package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packForVerify packs a synthetic disc and returns the bin path,
// container path, dir, and parsed manifest. Reused by every verify
// test that needs a known-good baseline container.
func packForVerify(t *testing.T) (binPath, containerPath, dir string, m *Manifest) {
	t.Helper()
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, -48, 10)
	containerPath = filepath.Join(dir, "x.miniscram")
	rep := NewReporter(io.Discard, true)
	if err := Pack(PackOptions{
		BinPath: binPath, CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, rep); err != nil {
		t.Fatal(err)
	}
	mm, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	return binPath, containerPath, dir, mm
}

func assertNoVerifyTempfile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "miniscram-verify-") {
			t.Errorf("tempfile not cleaned up: %s", e.Name())
		}
	}
}

func TestVerifySynthDiscOK(t *testing.T) {
	binPath, containerPath, dir, _ := packForVerify(t)
	if err := Verify(VerifyOptions{
		BinPath: binPath, ContainerPath: containerPath,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	assertNoVerifyTempfile(t, dir)
}

func TestVerifyDetectsScramSHA256Mismatch(t *testing.T) {
	binPath, containerPath, dir, m := packForVerify(t)

	// Locate the recorded scram_sha256 string inside the container's
	// JSON manifest and flip one bit. The recovered scram still hashes
	// to the original (correct) value, but the manifest now disagrees.
	data, err := os.ReadFile(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(data, []byte(m.ScramSHA256))
	if idx < 0 {
		t.Fatal("scram_sha256 string not present in container")
	}
	data[idx] ^= 1
	if err := os.WriteFile(containerPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	err = Verify(VerifyOptions{
		BinPath: binPath, ContainerPath: containerPath,
	}, NewReporter(io.Discard, true))
	if !errors.Is(err, errOutputSHA256Mismatch) {
		t.Fatalf("expected errOutputSHA256Mismatch, got %v", err)
	}
	assertNoVerifyTempfile(t, dir)
}

func TestVerifyDetectsWrongBin(t *testing.T) {
	_, containerPath, dir, _ := packForVerify(t)
	wrongBin := filepath.Join(dir, "wrong.bin")
	if err := os.WriteFile(wrongBin, []byte("not the right bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Verify(VerifyOptions{
		BinPath: wrongBin, ContainerPath: containerPath,
	}, NewReporter(io.Discard, true))
	if !errors.Is(err, errBinSHA256Mismatch) {
		t.Fatalf("expected errBinSHA256Mismatch, got %v", err)
	}
	assertNoVerifyTempfile(t, dir)
}
```

- [ ] **Step 2: Run tests to verify they fail (compile error: undefined Verify/VerifyOptions)**

Run: `go test ./... -run TestVerify -v`
Expected: build failure — `undefined: Verify`, `undefined: VerifyOptions`.

- [ ] **Step 3: Write `verify.go`**

```go
// /home/hugh/miniscram/verify.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// VerifyOptions holds inputs for Verify.
type VerifyOptions struct {
	BinPath       string
	ContainerPath string
}

// Verify performs a non-destructive integrity check: rebuild the
// recovered .scram into a temp file, hash it, compare against
// manifest.scram_sha256, then delete the temp file. Returns
// errBinSHA256Mismatch on wrong bin (via Unpack), errOutputSHA256Mismatch
// on hash mismatch, or any I/O error encountered along the way.
func Verify(opts VerifyOptions, r Reporter) error {
	if r == nil {
		r = quietReporter{}
	}

	// Read the manifest up front so we have scram_sha256 for the final
	// compare. ReadContainer is called again inside Unpack but the
	// manifest is small (KiB) and re-parsing is negligible.
	m, _, err := ReadContainer(opts.ContainerPath)
	if err != nil {
		return err
	}

	// Allocate a tempfile next to the container. The rebuild produces
	// a scram-sized file (often hundreds of MB); the container's
	// filesystem already accommodated similar artifacts at pack time.
	tmp, err := os.CreateTemp(filepath.Dir(opts.ContainerPath), "miniscram-verify-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	// Reuse the unpack path: scramble-test, ReadContainer, bin sha
	// check, BuildEpsilonHat, ApplyDelta. Verify=false skips Unpack's
	// own final hash; Force=true allows writing into the tempfile we
	// just created.
	if err := Unpack(UnpackOptions{
		BinPath:       opts.BinPath,
		ContainerPath: opts.ContainerPath,
		OutputPath:    tmpPath,
		Verify:        false,
		Force:         true,
	}, r); err != nil {
		return err
	}

	st := r.Step("verifying scram sha256")
	got, err := sha256File(tmpPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	if got != m.ScramSHA256 {
		err := fmt.Errorf("%w: computed %s, manifest %s", errOutputSHA256Mismatch, got, m.ScramSHA256)
		st.Fail(err)
		return err
	}
	st.Done("matches")
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run TestVerify -v`
Expected: PASS — `TestVerifySynthDiscOK`, `TestVerifyDetectsScramSHA256Mismatch`, `TestVerifyDetectsWrongBin`.

- [ ] **Step 5: Run full test suite and vet**

Run: `go test ./... && go vet ./...`
Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add verify.go verify_test.go
git commit -m "$(cat <<'EOF'
verify: add Verify function with hash-compare integrity check

Rebuilds via Unpack(Verify:false) into a tempfile under the
container's directory, sha256s the result, compares to
manifest.scram_sha256, then removes the tempfile. No new helpers;
reuses Unpack, ReadContainer, sha256File, and the existing
errBinSHA256Mismatch / errOutputSHA256Mismatch sentinels.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: CLI wiring (subcommand, help, exit codes) + CLI tests

**Goal:** Expose `Verify` as `miniscram verify`. Add dispatch, help text, error-to-exit mapping, and CLI-level tests covering discovery shapes, exit codes, and help.

**Files:**
- Modify: `main.go`
- Modify: `help.go`
- Modify: `verify_test.go` (append CLI tests)

**Acceptance Criteria:**
- [ ] `miniscram verify <bin> <container>` exits 0 on a valid pair.
- [ ] Wrong bin → exit 5; tampered scram_sha256 → exit 3; 3+ positionals or unknown flag or missing input file → exit 1.
- [ ] `miniscram verify --help`, `miniscram verify -h`, and `miniscram help verify` all print verify help (containing `USAGE:`).
- [ ] Top-level `miniscram help` (and bare `miniscram`) list `verify` in COMMANDS.
- [ ] Discovery shapes 0-arg (cwd) / 1-arg (stem) / 2-arg (explicit) all reach a successful Verify.
- [ ] `go test ./...` passes; `go vet ./...` clean.

**Verify:** `go test ./... && go vet ./...`

**Steps:**

- [ ] **Step 1: Add `verifyHelpText`, `printVerifyHelp`, and top-level mention in `help.go`**

Edit `help.go`:

In `topHelpText`, add the verify line in COMMANDS so the block reads:

```go
const topHelpText = `miniscram — compactly preserve scrambled CD-ROM dumps alongside .bin images.

USAGE:
    miniscram <command> [arguments] [options]
    miniscram <command> --help
    miniscram --version

COMMANDS:
    pack       pack a .scram into a compact .miniscram container
    unpack     reproduce a .scram from .bin + .miniscram
    verify     non-destructive integrity check of a .miniscram
    inspect    pretty-print a .miniscram container (read-only)
    help       show this help, or 'miniscram help <command>'

ABOUT:
    miniscram stores the bytes of a .scram (Redumper's scrambled
    intermediate CD-ROM dump) as a small structured delta against the
    unscrambled .bin final dump. With this tool and the .bin, you
    can reproduce the original .scram byte-for-byte. Implements the
    method from Hauenstein, "Compact Preservation of Scrambled CD-ROM
    Data" (IJCSIT, August 2022), specialised for Redumper output.

EXIT CODES:
    0    success
    1    usage / input error
    2    layout mismatch
    3    verification failed
    4    I/O error
    5    wrong .bin for this .miniscram
`
```

Add `printVerifyHelp` next to the other `print*Help` functions:

```go
func printVerifyHelp(w io.Writer) {
	fmt.Fprint(w, verifyHelpText)
}
```

Add `verifyHelpText` near the other `*HelpText` constants:

```go
const verifyHelpText = `USAGE:
    miniscram verify [<bin> <in.miniscram>] [options]

ARGUMENTS (optional — discovered from cwd if omitted):
    <bin>             path to the unscrambled CD image (Redumper *.bin)
    <in.miniscram>    .miniscram container produced by 'miniscram pack'

OPTIONS:
    -q, --quiet       suppress progress output.
    -h, --help        show this help.

DESCRIPTION:
    Rebuilds the original .scram in a temporary file, hashes it,
    compares against the container's recorded scram_sha256, and
    deletes the temporary file. Used to confirm a .miniscram still
    decodes correctly without producing a multi-hundred-MB output.

EXIT CODES:
    0    success
    1    usage / input error
    3    verification failed (computed sha256 != manifest.scram_sha256)
    4    I/O error
    5    wrong .bin (sha256 mismatch with manifest.bin_sha256)
`
```

- [ ] **Step 2: Add `verify` dispatch and `runVerify` / `verifyErrorToExit` to `main.go`**

In `run()`, add the `verify` case before the `inspect` case so the relevant block reads:

```go
	case "pack":
		return runPack(args[1:], stderr)
	case "unpack":
		return runUnpack(args[1:], stderr)
	case "verify":
		return runVerify(args[1:], stderr)
	case "inspect":
		return runInspect(args[1:], stdout, stderr)
```

In the help-subcommand switch, add the verify case:

```go
	case "help", "--help", "-h":
		if len(args) >= 2 {
			switch args[1] {
			case "pack":
				printPackHelp(stderr)
				return exitOK
			case "unpack":
				printUnpackHelp(stderr)
				return exitOK
			case "verify":
				printVerifyHelp(stderr)
				return exitOK
			case "inspect":
				printInspectHelp(stderr)
				return exitOK
			}
		}
```

Add `runVerify` next to `runUnpack`:

```go
func runVerify(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	quiet := fs.Bool("q", false, "quiet")
	quietLong := fs.Bool("quiet", false, "quiet")
	help := fs.Bool("help", false, "show help for verify")
	helpShort := fs.Bool("h", false, "show help for verify")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *help || *helpShort {
		printVerifyHelp(stderr)
		return exitOK
	}
	beQuiet := *quiet || *quietLong

	in, err := resolveUnpackInputs(fs.Args())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	rep := NewReporter(stderr, beQuiet)
	if err := Verify(VerifyOptions{
		BinPath: in.Bin, ContainerPath: in.Container,
	}, rep); err != nil {
		return verifyErrorToExit(err)
	}
	return exitOK
}
```

Add `verifyErrorToExit` next to `unpackErrorToExit`:

```go
func verifyErrorToExit(err error) int {
	switch {
	case errors.Is(err, errBinSHA256Mismatch):
		return exitWrongBin
	case errors.Is(err, errOutputSHA256Mismatch):
		return exitVerifyFail
	default:
		return exitIO
	}
}
```

- [ ] **Step 3: Append CLI tests to `verify_test.go`**

Add at the end of `verify_test.go`:

```go
func TestCLIVerifyOK(t *testing.T) {
	binPath, containerPath, _, _ := packForVerify(t)
	var stderr bytes.Buffer
	code := run([]string{"verify", binPath, containerPath}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
}

func TestCLIVerifyExitCodes(t *testing.T) {
	binPath, containerPath, dir, m := packForVerify(t)

	// Wrong bin → exit 5.
	wrongBin := filepath.Join(dir, "wrong.bin")
	if err := os.WriteFile(wrongBin, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := run([]string{"verify", wrongBin, containerPath}, io.Discard, &stderr)
	if code != exitWrongBin {
		t.Fatalf("wrong-bin exit %d, want %d; stderr=%s", code, exitWrongBin, stderr.String())
	}

	// Tampered scram_sha256 → exit 3.
	data, err := os.ReadFile(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(data, []byte(m.ScramSHA256))
	if idx < 0 {
		t.Fatal("scram_sha256 not present in container")
	}
	data[idx] ^= 1
	if err := os.WriteFile(containerPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	code = run([]string{"verify", binPath, containerPath}, io.Discard, &stderr)
	if code != exitVerifyFail {
		t.Fatalf("tampered exit %d, want %d; stderr=%s", code, exitVerifyFail, stderr.String())
	}
}

func TestCLIVerifyDiscovery(t *testing.T) {
	binPath, containerPath, dir, _ := packForVerify(t)

	t.Run("zero-arg-cwd", func(t *testing.T) {
		cwd, _ := os.Getwd()
		t.Cleanup(func() { os.Chdir(cwd) })
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		code := run([]string{"verify"}, io.Discard, &stderr)
		if code != exitOK {
			t.Fatalf("exit %d; stderr=%s", code, stderr.String())
		}
	})

	t.Run("one-arg-stem", func(t *testing.T) {
		stem := strings.TrimSuffix(containerPath, ".miniscram")
		var stderr bytes.Buffer
		code := run([]string{"verify", stem}, io.Discard, &stderr)
		if code != exitOK {
			t.Fatalf("exit %d; stderr=%s", code, stderr.String())
		}
	})

	t.Run("two-arg-explicit", func(t *testing.T) {
		var stderr bytes.Buffer
		code := run([]string{"verify", binPath, containerPath}, io.Discard, &stderr)
		if code != exitOK {
			t.Fatalf("exit %d; stderr=%s", code, stderr.String())
		}
	})
}

func TestCLIVerifyHelp(t *testing.T) {
	// verify --help
	var stderr bytes.Buffer
	code := run([]string{"verify", "--help"}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("verify --help exit %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("USAGE:")) {
		t.Fatalf("verify --help missing USAGE; stderr=%s", stderr.String())
	}

	// help verify
	stderr.Reset()
	code = run([]string{"help", "verify"}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("help verify exit %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("USAGE:")) {
		t.Fatalf("help verify missing USAGE; stderr=%s", stderr.String())
	}

	// top-level help mentions verify
	stderr.Reset()
	code = run([]string{"--help"}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("top --help exit %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("verify")) {
		t.Fatalf("top help doesn't mention verify; stderr=%s", stderr.String())
	}
}

func TestCLIVerifyUsageErrors(t *testing.T) {
	// 3 positionals
	var stderr bytes.Buffer
	code := run([]string{"verify", "a", "b", "c"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("3-positional exit %d, want %d", code, exitUsage)
	}
	// unknown flag
	stderr.Reset()
	code = run([]string{"verify", "--no-such-flag"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("bad flag exit %d, want %d", code, exitUsage)
	}
	// missing input files (caught at resolveUnpackInputs)
	stderr.Reset()
	code = run([]string{"verify", "/no/such/bin", "/no/such/container.miniscram"}, io.Discard, &stderr)
	// 2 positionals don't trigger discovery, so the resolver returns
	// the explicit pair without checking existence; the I/O failure
	// surfaces from ReadContainer/Unpack and routes to exitIO.
	if code != exitIO {
		t.Fatalf("missing files exit %d, want %d (exitIO)", code, exitIO)
	}
	// missing input via stem (DiscoverUnpackFromArg checks existence)
	stderr.Reset()
	code = run([]string{"verify", "/no/such/stem"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("missing-stem exit %d, want %d", code, exitUsage)
	}
}
```

- [ ] **Step 4: Run focused tests**

Run: `go test ./... -run TestCLIVerify -v`
Expected: PASS — `TestCLIVerifyOK`, `TestCLIVerifyExitCodes`, `TestCLIVerifyDiscovery` (with three subtests), `TestCLIVerifyHelp`, `TestCLIVerifyUsageErrors`.

- [ ] **Step 5: Run full test suite and vet**

Run: `go test ./... && go vet ./...`
Expected: all green.

- [ ] **Step 6: Build smoke check**

Run: `go build -o /tmp/miniscram-verify-build ./... && /tmp/miniscram-verify-build help verify && rm /tmp/miniscram-verify-build`
Expected: builds; help-verify prints USAGE.

- [ ] **Step 7: Commit**

```bash
git add main.go help.go verify_test.go
git commit -m "$(cat <<'EOF'
verify: wire CLI subcommand and help

Adds 'miniscram verify' dispatch, runVerify wrapper around the
Verify Go function, verifyErrorToExit mapping (3 verify-fail,
4 I/O, 5 wrong-bin, 1 usage), and help text. Top-level help
gains a verify line in COMMANDS. CLI tests cover discovery
shapes, exit codes, and help.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** All seven testing entries from the spec map to tests in this plan: TestVerifyOK→TestVerifySynthDiscOK; TestVerifyFailContainerTampered→TestVerifyDetectsScramSHA256Mismatch; TestVerifyFailWrongBin→TestVerifyDetectsWrongBin; TestVerifyTempCleanup→assertNoVerifyTempfile invoked from each function-level test; TestCLIVerifyDiscovery→TestCLIVerifyDiscovery; TestVerifyUsageErrors→TestCLIVerifyUsageErrors; TestVerifyHelp→TestCLIVerifyHelp. The spec's "TestVerifyNoOutputFile" is folded into TestVerifySynthDiscOK's tempfile-cleanup assertion (no `*.scram` is ever created at a permanent path because OutputPath is the tempfile).
- **Spec deviation:** The spec assumes missing input files at 2-positional-explicit shape go to exit 1 ("missing files surface as os.Stat errors out of resolveUnpackInputs"). In practice, `resolveUnpackInputs` only stats the inputs in the 0-arg and 1-arg discovery paths; the 2-arg path returns the pair verbatim and existence is first checked at ReadContainer/Unpack time, surfacing as exit 4. The plan tests both: stem-missing → exit 1 (resolver path), explicit-missing → exit 4 (downstream path). This is also how `runUnpack` already behaves, so verify is consistent.
- **Type consistency:** `VerifyOptions` and `Verify` are defined in Task 1 and used in Task 2. `verifyErrorToExit` is defined in Task 2 only and used only in Task 2. No symbol referenced before defined.
- **No placeholders:** Every code block is complete. Every command has expected output.
