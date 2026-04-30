# Preserve Fail-Sectors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mirror redumper's `Scrambler::descramble()` decision in
miniscram's `BuildEpsilonHat` so bin sectors that redumper passed
through (the `.fail` cases enumerated under
`~/redumper/tests/unscramble/`) are no longer needlessly re-scrambled
by the predictor — eliminating an entire 2352-byte override per such
sector.

**Architecture:** A pure classifier `classifyBinSector` lives in
`ecma130.go` next to the scramble primitives. It is gated by
the existing `trackModeAt(...) != "AUDIO"` branch in
`BuildEpsilonHat`. The decision tree mirrors
`cd/cd_scrambler.ixx:23-61`. Pack and unpack both go through
`BuildEpsilonHat`, so a single change is symmetric on both sides.
Container format version is bumped to `0x00` as a sentinel —
the in-flight `format/drop-scrambler-hash` branch will rebase
atop this and pick the real number.

**Tech Stack:** Go (stdlib only). `testing/quick` for property
tests. Test fixtures copied from `~/redumper/tests/unscramble/`
under GPL-3.0 (license-compatible with miniscram).

**Spec:** `docs/superpowers/specs/2026-04-29-miniscram-preserve-fail-sectors-design.md`

**Branch:** `feat/preserve-fail-sectors` (do **not** merge to main on
completion; PR stays open until the user integrates).

---

## File Structure

| File | Status | Responsibility |
|------|--------|----------------|
| `ecma130.go` | modify | Add `classifyBinSector` + `isZeroed` next to `Scramble`. |
| `builder.go` | modify | Replace unconditional `Scramble(&sec)` at line 174 with classifier-gated call; thread `passThroughs` count into return tuple. |
| `pack.go` | modify | `buildHatAndDelta` and the reporter step text consume the new `passThroughs` count. |
| `unpack.go` | modify | Adjust the `BuildEpsilonHat` call site for the new return arity. |
| `builder_test.go` | modify | Adjust three call sites for new return arity (discard the count). |
| `manifest.go` | modify | `containerVersion` constant `0x01 → 0x00` (sentinel); update rejection error message. |
| `manifest_test.go` | modify | Update any hard-coded version byte expectations (none found via grep, but verify). |
| `testdata/unscramble/` | create | Copy 46 fixture files verbatim from `~/redumper/tests/unscramble/`. |
| `testdata/unscramble/README.md` | create | Attribution + GPL-3.0 inheritance note. |
| `unscramble_oracle_test.go` | create | Test-only Go port of redumper's `descramble()`, used as the ground-truth oracle. |
| `unscramble_fixtures_test.go` | create | Walk fixture dir; oracle → bin form → assert classifier verdict matches filename label. |
| `unscramble_property_test.go` | create | `testing/quick`-based randomized check of classifier vs oracle. |
| `fixtures_test.go` | modify | New helper `synthDiscWithFailSector`. |
| `e2e_test.go` | modify | Round-trip case using the new helper. |
| `e2e_redump_test.go` | modify | Tighten `MaxDeltaBytes` / `MaxContainerBytes` for the half-life row after measuring. |
| `NOTICE` | unchanged | Already updated on this branch. |
| `CLAUDE.md` | unchanged | Property-tests note already on this branch. |
| `TASKS.md` | unchanged | Theme E already on this branch. |

---

## Task 1: Copy redumper unscramble fixtures

**Goal:** Get the 46 redumper test fixtures into the repo so subsequent
tasks have ground truth to test against.

**Files:**
- Create: `testdata/unscramble/01_invalid_sync.0.pass` through
  `testdata/unscramble/46_zeroed_sector.10350.fail` (46 files; copy verbatim).
- Create: `testdata/unscramble/README.md`

**Acceptance Criteria:**
- [ ] All 46 fixture files present, each byte-identical to the
      upstream copy at `~/redumper/tests/unscramble/`.
- [ ] `testdata/unscramble/README.md` credits upstream and notes
      GPL-3.0 inheritance.
- [ ] `go build ./...` still succeeds (no Go files touched, sanity check).

**Verify:**
```
diff -r ~/redumper/tests/unscramble/ testdata/unscramble/ | grep -v 'README.md'
```
Expected: empty output (only difference is the new README.md).

**Steps:**

- [ ] **Step 1: Copy the fixtures**

```bash
mkdir -p testdata/unscramble
cp ~/redumper/tests/unscramble/* testdata/unscramble/
```

- [ ] **Step 2: Verify count and integrity**

Run: `ls testdata/unscramble/ | wc -l`
Expected: `46`

Run: `diff -r ~/redumper/tests/unscramble/ testdata/unscramble/`
Expected: empty (no diffs).

- [ ] **Step 3: Write the README**

Write `testdata/unscramble/README.md`:

```markdown
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
```

- [ ] **Step 4: Stage and commit**

```bash
git add testdata/unscramble/
git commit -m "test: import redumper unscramble fixtures verbatim

46 sector fixtures from redumper/tests/unscramble/ enumerate every
pass/fail case in Scrambler::descramble(). Will be consumed by
unscramble_fixtures_test.go to pin classifyBinSector to redumper's
behaviour."
```

---

## Task 2: Implement test-side oracle (Go port of redumper's descramble)

**Goal:** A Go port of `cd/cd_scrambler.ixx::descramble` lives in
test code and serves as the ground-truth oracle for both fixture-
and property-tests of the production classifier.

**Files:**
- Create: `unscramble_oracle_test.go`

**Acceptance Criteria:**
- [ ] `oracleDescramble(scrambled []byte, lba *int32) (binForm []byte, verdict bool)`
      returns the same verdict redumper would for any 2352-byte
      input.
- [ ] When called against each of the 46 fixtures, it returns
      `verdict == true` for `.pass` files and `verdict == false`
      for `.fail` files.
- [ ] Returns the bin form: descrambled bytes when verdict==true,
      the original scrambled bytes when verdict==false (matching
      redumper's "re-scramble back" path).
- [ ] Lives in `_test.go` and is **not** built into the production
      binary.

**Verify:**
```
go test ./... -run TestOracleAgainstFixtures -v
```
Expected: `--- PASS: TestOracleAgainstFixtures (...)`, 46 sub-tests.

**Steps:**

- [ ] **Step 1: Write `unscramble_oracle_test.go`**

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// oracleDescramble is a Go port of redumper's
// Scrambler::descramble (cd/cd_scrambler.ixx:23-61). Test-only
// ground-truth oracle. Returns the bin form and verdict that
// redumper would produce given a scrambled sector and an
// expected-LBA hint (nil = no hint).
//
// Note: this helper deliberately does not call the production
// `isZeroed` (which is added in Task 3) to keep this test file
// self-contained. The inline `bytes.Count(buf, []byte{0}) == len(buf)`
// expresses the same predicate idiomatically.
func oracleDescramble(scrambled []byte, lba *int32) (binForm []byte, verdict bool) {
	// Defensive copy so caller's input is unchanged.
	buf := make([]byte, len(scrambled))
	copy(buf, scrambled)

	// is_zeroed → return false, leave scrambled bytes (bin == scram).
	if bytes.Count(buf, []byte{0}) == len(buf) || len(buf) < SyncLen+3 {
		return buf, false
	}

	// process(): XOR with scramble table — same as Scramble.
	for i := SyncLen; i < len(buf); i++ {
		buf[i] ^= scrambleTable[i]
	}

	// Strong MSF check.
	if lba != nil {
		decoded := BCDMSFToLBA([3]byte{buf[12], buf[13], buf[14]})
		if decoded == *lba {
			return buf, true
		}
	}

	// Sync match? (Sync field is invariant under scrambling, so this
	// is equivalent to "did the original scrambled sector start with
	// the canonical sync bytes?".)
	if bytes.Equal(buf[:SyncLen], Sync[:]) {
		mode := buf[15]
		switch mode {
		case 1, 2:
			return buf, true
		case 0:
			// Mode 0 with zeroed user-data ⇒ pass.
			ud := buf[16 : 16+2048]
			if bytes.Count(ud, []byte{0}) == len(ud) {
				return buf, true
			}
		}
	}

	// Failure: re-scramble back so bin == original scram.
	for i := SyncLen; i < len(buf); i++ {
		buf[i] ^= scrambleTable[i]
	}
	return buf, false
}

// TestOracleAgainstFixtures pins oracleDescramble to redumper's
// own pass/fail labels on the 46 imported fixtures.
func TestOracleAgainstFixtures(t *testing.T) {
	entries, err := os.ReadDir("testdata/unscramble")
	if err != nil {
		t.Fatalf("read testdata/unscramble: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no fixtures found under testdata/unscramble")
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			tokens := strings.Split(name, ".")
			if len(tokens) != 3 {
				t.Fatalf("malformed fixture name: %s", name)
			}
			var lbaPtr *int32
			if tokens[1] != "null" {
				v, err := strconv.ParseInt(tokens[1], 10, 32)
				if err != nil {
					t.Fatalf("bad LBA in %s: %v", name, err)
				}
				lba := int32(v)
				lbaPtr = &lba
			}
			expectPass := tokens[2] == "pass"
			data, err := os.ReadFile(filepath.Join("testdata/unscramble", name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			_, verdict := oracleDescramble(data, lbaPtr)
			if verdict != expectPass {
				t.Errorf("verdict=%v, want %v", verdict, expectPass)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test (should pass immediately — pure port)**

Run: `go test ./... -run TestOracleAgainstFixtures -v`
Expected: 46 PASS sub-tests, 0 FAIL.

If any sub-test fails, the port has drifted from
`cd/cd_scrambler.ixx`. Re-read upstream and reconcile before moving on.

- [ ] **Step 3: Commit**

```bash
git add unscramble_oracle_test.go
git commit -m "test: oracle port of redumper's descramble for fixture pinning

Pure Go port of Scrambler::descramble from cd/cd_scrambler.ixx:23-61,
test-only. Pinned against all 46 imported fixtures: verdict matches
redumper's pass/fail label for every entry. Used in subsequent tasks
as ground truth for classifyBinSector property/fixture tests."
```

---

## Task 3: Implement classifyBinSector + isZeroed

**Goal:** Production classifier in `ecma130.go` that mirrors
redumper's descramble decision but operates on the *bin* sector
(rather than the scrambled .scram sector). Tested against the 46
fixtures via the oracle from Task 2.

**Files:**
- Modify: `ecma130.go`
- Create: `unscramble_fixtures_test.go`

**Acceptance Criteria:**
- [ ] `classifyBinSector(bin []byte, expectedLBA int32) bool` is
      defined in `ecma130.go` with a doc comment quoting
      `cd/cd_scrambler.ixx:23-61` per the existing scrambler-port
      pattern.
- [ ] `isZeroed(buf []byte) bool` helper added near the classifier
      (kept package-private; no new file).
- [ ] For every fixture, the classifier verdict matches the
      filename label (pass = `true`, fail = `false`).
- [ ] No production callers yet (still added in Task 5);
      `go build ./...` succeeds.

**Verify:**
```
go test ./... -run TestClassifyAgainstFixtures -v
```
Expected: 46 PASS sub-tests.

**Steps:**

- [ ] **Step 1: Write the failing fixture test** as `unscramble_fixtures_test.go`

```go
package main

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestClassifyAgainstFixtures asserts classifyBinSector returns
// the same verdict redumper's descramble does on every entry of
// the imported fixture set.
//
// For each fixture we compute the bin form via the test oracle
// (oracleDescramble), then ask classifyBinSector to classify it.
// The classifier sees only what miniscram would see in production:
// the bin bytes plus the expected LBA.
func TestClassifyAgainstFixtures(t *testing.T) {
	entries, err := os.ReadDir("testdata/unscramble")
	if err != nil {
		t.Fatalf("read testdata/unscramble: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			tokens := strings.Split(name, ".")
			if len(tokens) != 3 {
				t.Fatalf("malformed fixture name: %s", name)
			}

			// expectedLBA passed to classifier. For .null fixtures
			// (redumper's lba=nullptr path) we use math.MinInt32
			// which BCDMSFToLBA can never produce, so the strong-
			// MSF branch will not match — exercising the sync+mode
			// fallback exclusively.
			var classifyLBA int32 = math.MinInt32
			var oracleLBA *int32
			if tokens[1] != "null" {
				v, err := strconv.ParseInt(tokens[1], 10, 32)
				if err != nil {
					t.Fatalf("bad LBA in %s: %v", name, err)
				}
				classifyLBA = int32(v)
				oracleLBA = &classifyLBA
			}

			expectPass := tokens[2] == "pass"
			data, err := os.ReadFile(filepath.Join("testdata/unscramble", name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}

			// Some fixtures are intentionally short (case 02,
			// not_enough_data). The production classifier is only
			// called from BuildEpsilonHat which always supplies
			// 2352 bytes; pad for the test.
			if len(data) < SectorSize {
				padded := make([]byte, SectorSize)
				copy(padded, data)
				data = padded
			}

			binForm, _ := oracleDescramble(data, oracleLBA)
			got := classifyBinSector(binForm, classifyLBA)
			if got != expectPass {
				t.Errorf("classifyBinSector verdict=%v, want %v", got, expectPass)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run TestClassifyAgainstFixtures -v`
Expected: build error (`undefined: classifyBinSector`).

- [ ] **Step 3: Implement the classifier in `ecma130.go`**

Add at the end of the file (or alongside existing `Scramble`):

```go
// classifyBinSector mirrors redumper's Scrambler::descramble decision
// (cd/cd_scrambler.ixx:23-61) applied to a bin sector (rather than
// the scrambled .scram sector). Returns true iff the corresponding
// .scram sector at expectedLBA holds scrambled bytes — i.e. miniscram
// must rescramble bin to predict scram. Returns false when bin already
// equals scram for this sector (redumper passed it through; we do
// likewise).
//
// Decision tree:
//   1. all-zero bin sector              → false (case 46: zeroed_sector)
//   2. bin[0:SyncLen] != Sync           → false (fail-type-1: invalid sync)
//   3. MSF in bin matches expectedLBA   → true  (strong MSF check)
//   4. mode byte ∈ {1, 2}               → true  (sync+mode check)
//   5. mode == 0 && user-data zeroed    → true  (sync+mode-0-zeroed)
//   6. otherwise                        → false (fail-type-2)
//
// Caller is responsible for not invoking this on AUDIO-track
// sectors; bin in audio tracks is raw PCM and never scrambled.
func classifyBinSector(bin []byte, expectedLBA int32) bool {
	if len(bin) < SectorSize {
		return false
	}
	if isZeroed(bin) {
		return false
	}
	if !bytes.Equal(bin[:SyncLen], Sync[:]) {
		return false
	}
	if BCDMSFToLBA([3]byte{bin[12], bin[13], bin[14]}) == expectedLBA {
		return true
	}
	switch bin[15] {
	case 1, 2:
		return true
	case 0:
		// Mode 0 with zeroed user-data: bytes [16, 16+2048)
		// matches redumper's s->mode2.user_data slice
		// (CD-ROM Mode 2 Form 0/1: 2048-byte user-data field).
		return isZeroed(bin[16 : 16+2048])
	}
	return false
}

// isZeroed reports whether buf is all zeros.
func isZeroed(buf []byte) bool {
	for _, b := range buf {
		if b != 0 {
			return false
		}
	}
	return true
}
```

You'll need to add `"bytes"` to the import block of `ecma130.go` if it isn't there.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./... -run TestClassifyAgainstFixtures -v`
Expected: 46 PASS sub-tests.

If any fixture fails, examine the descrambled vs scrambled bytes
manually and reconcile against `cd/cd_scrambler.ixx`. Most likely
suspects: wrong slice bounds in step 5 (mode-0 zeroed-data check)
or a stray `bytes.Equal` argument order.

- [ ] **Step 5: Run the full test suite to confirm no regressions**

Run: `go test ./...`
Expected: ok across all packages.

- [ ] **Step 6: Commit**

```bash
git add ecma130.go unscramble_fixtures_test.go
git commit -m "feat: classifyBinSector mirrors redumper's descramble decision

Pure classifier next to Scramble in ecma130.go. Pinned via fixture
test against all 46 entries from redumper/tests/unscramble/ — verdict
matches redumper's own pass/fail labels. Not yet wired into
BuildEpsilonHat; that comes in the next task.

Refs cd/cd_scrambler.ixx:23-61."
```

---

## Task 4: Add property test for classifier vs oracle

**Goal:** Randomized property test asserting `classifyBinSector`
agrees with the oracle on arbitrary inputs, catching synthesis gaps
the hand-curated fixtures don't cover.

**Files:**
- Create: `unscramble_property_test.go`

**Acceptance Criteria:**
- [ ] `TestClassifyMatchesOracleProperty` runs at least 1000
      `quick.Check` iterations.
- [ ] Each iteration: generate random sector + LBA, apply oracle
      to produce bin form + verdict, assert classifier agrees.
- [ ] Test runs as part of plain `go test ./...` (no build tag).

**Verify:**
```
go test ./... -run TestClassifyMatchesOracleProperty -v
```
Expected: PASS.

**Steps:**

- [ ] **Step 1: Write `unscramble_property_test.go`**

```go
package main

import (
	"math/rand"
	"testing"
	"testing/quick"
)

// TestClassifyMatchesOracleProperty: for any 2352-byte sector and
// any expected LBA, classifyBinSector applied to the bin form
// returned by the oracle must match the oracle's pass/fail verdict.
//
// This catches edge cases the 46-entry fixture set doesn't exercise.
func TestClassifyMatchesOracleProperty(t *testing.T) {
	cfg := &quick.Config{MaxCount: 1000, Rand: rand.New(rand.NewSource(1))}

	property := func(seed int64, lbaSign bool, lbaMag int32, useNullLBA bool) bool {
		// Generate a deterministic 2352-byte sector from the seed.
		r := rand.New(rand.NewSource(seed))
		sector := make([]byte, SectorSize)
		r.Read(sector)

		// LBA hint. With useNullLBA, simulate the redumper
		// lba=nullptr case by passing math.MinInt32 to the
		// classifier and nil to the oracle.
		var classifyLBA int32
		var oracleLBA *int32
		if useNullLBA {
			classifyLBA = math.MinInt32
		} else {
			lba := int32(lbaMag % (99 * 60 * 75))
			if lbaSign {
				lba = -lba - 150
			}
			classifyLBA = lba
			oracleLBA = &lba
		}

		binForm, oracleVerdict := oracleDescramble(sector, oracleLBA)
		got := classifyBinSector(binForm, classifyLBA)
		return got == oracleVerdict
	}

	if err := quick.Check(property, cfg); err != nil {
		t.Errorf("classifier disagreed with oracle: %v", err)
	}
}
```

You'll need `"math"` in the import block.

- [ ] **Step 2: Run the property test**

Run: `go test ./... -run TestClassifyMatchesOracleProperty -v`
Expected: PASS.

If a counter-example is reported, the classifier and oracle diverge
on that input — examine and fix whichever is wrong (likely the
classifier, since the oracle is the closer transliteration).

- [ ] **Step 3: Commit**

```bash
git add unscramble_property_test.go
git commit -m "test: property test classifyBinSector vs oracle (1000 iterations)

testing/quick fuzzes arbitrary sectors and LBA hints. classifier
must agree with oracleDescramble's verdict on every input — catches
synthesis gaps the 46-entry fixture corpus doesn't cover."
```

---

## Task 5: Wire classifier into BuildEpsilonHat

**Goal:** Replace the unconditional `Scramble(&sec)` call in the
bin-region branch with a classifier-gated call, and thread the
pass-through count through the function's return tuple.

**Files:**
- Modify: `builder.go` (lines 116, 122, 174-176, surrounding return signature)
- Modify: `unpack.go:112`
- Modify: `pack.go:483-525, 127`
- Modify: `builder_test.go:100, 122, 139`

**Acceptance Criteria:**
- [ ] `BuildEpsilonHat` returns
      `(errLBAs []int32, mismatchedSectors int, passThroughs int, err error)`.
- [ ] `buildHatAndDelta` returns
      `(hatPath, deltaPath string, errs []int32, deltaSize int64, passThroughs int, err error)`.
- [ ] All call sites updated; `go build ./...` succeeds.
- [ ] Existing tests still pass: `go test ./...`.
- [ ] For synthetic discs (always-pass sectors), `passThroughs == 0`
      — easy to verify in builder_test.

**Verify:**
```
go test ./...
```
Expected: ok across all packages, no new failures.

**Steps:**

- [ ] **Step 1: Update `BuildEpsilonHat` signature in `builder.go`**

Find the function signature at `builder.go:116`:

```go
func BuildEpsilonHat(
	out io.Writer,
	p BuildParams,
	bin io.Reader,
	scram io.Reader,
	onMismatch func(off int64, scramRun []byte),
) ([]int32, int, error) {
```

Change return type to `([]int32, int, int, error)`.

Add a counter and modify the bin-region branch (around line 169-178). Current code:

```go
case lba < p.BinFirstLBA+p.BinSectorCount:
	if _, err := io.ReadFull(bin, binBuf); err != nil {
		return nil, 0, fmt.Errorf("reading bin LBA %d: %w", lba, err)
	}
	copy(sec[:], binBuf)
	if trackModeAt(p.Tracks, lba) != "AUDIO" {
		Scramble(&sec)
	}
```

Replace with:

```go
case lba < p.BinFirstLBA+p.BinSectorCount:
	if _, err := io.ReadFull(bin, binBuf); err != nil {
		return nil, 0, 0, fmt.Errorf("reading bin LBA %d: %w", lba, err)
	}
	copy(sec[:], binBuf)
	if trackModeAt(p.Tracks, lba) != "AUDIO" {
		// Mirror redumper's Scrambler::descramble() decision:
		// scramble only when the bin sector is in descrambled form
		// (a "pass" case). For "fail" cases, .bin == .scram and
		// passing through preserves the original disc bytes
		// without an override.
		if classifyBinSector(sec[:], lba) {
			Scramble(&sec)
		} else {
			passThroughs++
		}
	}
```

Add `var passThroughs int` near the top of the function, alongside
`var errLBAs []int32` (around line 144).

Update every `return nil, 0, ...` to `return nil, 0, 0, ...`, and
the final return statement at the end of the function to include
`passThroughs`. Search for `return nil, mismatchedSectors,` and
`return errLBAs, mismatchedSectors,` patterns.

- [ ] **Step 2: Update `unpack.go:112`**

Current:
```go
if _, _, err := BuildEpsilonHat(hatFile, params, binReader, nil, nil); err != nil {
```

Change to:
```go
if _, _, _, err := BuildEpsilonHat(hatFile, params, binReader, nil, nil); err != nil {
```

- [ ] **Step 3: Update `pack.go:525` and `buildHatAndDelta` signature**

Current at line 525:
```go
errs, mismatched, err := BuildEpsilonHat(hatFile, params, binReader, scramFile, enc.Append)
```

Change to:
```go
errs, mismatched, passThroughs, err := BuildEpsilonHat(hatFile, params, binReader, scramFile, enc.Append)
```

Update `buildHatAndDelta`'s signature at `pack.go:483`:
```go
func buildHatAndDelta(opts PackOptions, files []ResolvedFile, tracks []Track, scramSize int64, writeOffsetBytes int, binSectors int32) (string, string, []int32, int64, int, error) {
```

Update every `return "", "", nil, 0, ...` in the function body to
`return "", "", nil, 0, 0, ...` (one extra zero for passThroughs).

Update the final return at line 550:
```go
return hatPath, deltaPath, errs, deltaInfo.Size(), passThroughs, nil
```

Update the caller at `pack.go:127`:
```go
hatPath, deltaPath, errSectors, deltaSize, passThroughs, err := buildHatAndDelta(opts, resolved.Files, tracks, scramSize, writeOffsetBytes, binSectors)
```

(`passThroughs` will be used by Task 6's reporter change; for now
just stash it.)

- [ ] **Step 4: Update `builder_test.go` call sites**

Three occurrences. Update each to discard the new return:

```go
errs, mismatched, _, err := BuildEpsilonHat(&hat, params, bytes.NewReader(bin), bytes.NewReader(scram), enc.Append)
```

(line 100, 122, 139 — same pattern, possibly different variable
names like `errLBAs`).

- [ ] **Step 5: Run the test suite**

Run: `go test ./...`
Expected: all green.

If anything fails, the most likely culprit is a missed return-statement
update inside `BuildEpsilonHat` — `go vet` will complain about wrong
arity at the function level. Run `go vet ./...` to spot.

- [ ] **Step 6: Commit**

```bash
git add builder.go unpack.go pack.go builder_test.go
git commit -m "feat: classifier-gated scramble in BuildEpsilonHat

Bin sectors that miniscram's classifyBinSector flags as 'fail' (i.e.
sectors redumper passed through unchanged on its way to the .bin)
are no longer needlessly re-scrambled. For these sectors, .bin ==
.scram for the LBA, so passing through eliminates an entire 2352-byte
override.

BuildEpsilonHat and buildHatAndDelta gain a passThroughs return
value, threaded for upcoming reporter use."
```

---

## Task 6: Plumb pass-through count through pack reporter

**Goal:** Surface the pass-through count in the
`building scram prediction + delta` reporter step.

**Files:**
- Modify: `pack.go:142` (the `st.Done` line in the bin-build step).

**Acceptance Criteria:**
- [ ] Reporter prints `N override(s), M pass-through(s), delta K bytes`
      where M is the new pass-through count.
- [ ] No new build errors.
- [ ] No test regressions: `go test ./...`.

**Verify:**

Run a synthetic pack (no real fixture needed) and inspect stderr; or
run an existing test that exercises the reporter — see
`pack_test.go::TestPackReporterStepText` if it exists, otherwise eyeball.

**Steps:**

- [ ] **Step 1: Update the reporter's `st.Done` call**

Find at `pack.go:142`:
```go
st.Done("%d override(s), delta %d bytes", len(errSectors), deltaSize)
```

Change to:
```go
st.Done("%d override(s), %d pass-through(s), delta %d bytes",
	len(errSectors), passThroughs, deltaSize)
```

- [ ] **Step 2: Run all tests**

Run: `go test ./...`
Expected: ok.

- [ ] **Step 3: Manually exercise the reporter (sanity check)**

Build the binary and run pack against a small synthetic input, or
run a test that captures stderr (most reporter-aware tests do).

If a reporter-text test exists that hard-codes the prior format
string, update it to match the new format.

- [ ] **Step 4: Commit**

```bash
git add pack.go
git commit -m "feat: report pass-through count in pack reporter

The 'building scram prediction + delta' step now reports both the
override count and the pass-through count. Pass-throughs are sectors
classifyBinSector flagged as 'fail' — bin == scram, no scramble, no
override needed."
```

---

## Task 7: Bump container format version to 0 (sentinel)

**Goal:** Set `containerVersion` to `0x00` and update the
unsupported-version error message to reference the rule change.
The `0x00` value is a sentinel signalling the
`format/drop-scrambler-hash` branch to rebase atop this work and
pick the real number.

**Files:**
- Modify: `manifest.go:17, 152`

**Acceptance Criteria:**
- [ ] `containerVersion = byte(0x00)`.
- [ ] Unsupported-version error message names the rule change so
      a user packing/unpacking with mismatched binaries gets a
      useful diagnostic.
- [ ] All tests still pass: `go test ./...`. (No test should hard-
      code the version byte literally; verify via grep.)

**Verify:**
```
grep -n "0x01\|0x02" *.go *_test.go | grep -i "version\|container"
```
Expected: empty (no version-byte literals).

```
go test ./...
```
Expected: ok.

**Steps:**

- [ ] **Step 1: Update the constant**

`manifest.go:17`:
```go
containerVersion = byte(0x00) // v0 sentinel — see TASKS.md / spec
```

- [ ] **Step 2: Update the error message**

`manifest.go:152`:
```go
return nil, [32]byte{}, nil, fmt.Errorf("unsupported container version 0x%02x (this build expects 0x%02x); the prediction-rule change in feat/preserve-fail-sectors broke wire compatibility — re-pack from .bin",
	header[4], containerVersion)
```

- [ ] **Step 3: Run all tests**

Run: `go test ./...`
Expected: ok.

If `manifest_test.go` constructs containers via the constant rather
than a literal, no change needed there. (Verified at plan-write time
via grep.)

- [ ] **Step 4: Commit**

```bash
git add manifest.go
git commit -m "format: bump container version to 0x00 sentinel

The classifier-gated prediction in BuildEpsilonHat is not wire-
compatible with v1/v2 containers — override records were computed
against the old always-scramble prediction, so a v3 reader applied
them at the wrong byte offsets. Bump version to 0x00 as a sentinel:
the in-flight format/drop-scrambler-hash branch (which also bumps
the version for its chunk-binary refactor) will rebase atop this
work and pick the real number when both land."
```

---

## Task 8: Add synthDiscWithFailSector helper + e2e

**Goal:** A synthetic disc fixture exercising the new pass-through
code path end-to-end: pack → unpack → byte-equal scram.

**Files:**
- Modify: `fixtures_test.go` (add `synthDiscWithFailSector` helper).
- Modify: `e2e_test.go` (add round-trip case).

**Acceptance Criteria:**
- [ ] `synthDiscWithFailSector(t, totalSectors int, failLBA int32)`
      returns a SynthDisc whose data track has one hand-crafted
      "fail" sector at `failLBA` (e.g. valid sync but mode byte
      `0xF7`).
- [ ] `TestE2EFailSectorRoundTrip` packs and unpacks the fixture;
      output `.scram` is byte-equal to input.
- [ ] During pack, the reporter shows ≥ 1 pass-through.
- [ ] No regressions in other tests.

**Verify:**
```
go test ./... -run TestE2EFailSectorRoundTrip -v
```
Expected: PASS.

**Steps:**

- [ ] **Step 1: Add the helper to `fixtures_test.go`**

Read the existing `makeSynthDisc` to understand its shape, then
add a sibling helper. The fail sector is built by:

```go
// synthDiscWithFailSector returns a synthetic single-track Mode 1
// disc whose data track contains one "fail" sector at failOffset
// (in sectors from track start). The fail sector has a valid
// scrambled sync but an invalid mode byte (0xF7), so redumper
// would have passed it through unchanged — the sector appears as
// scrambled bytes in both .bin and .scram.
//
// Used by TestE2EFailSectorRoundTrip to verify miniscram's pass-
// through path round-trips byte-for-byte.
func synthDiscWithFailSector(t *testing.T, mainSectors, leadoutSectors int, failOffset int) SynthDisc {
	t.Helper()
	disc := makeSynthDisc(t, SynthOpts{
		MainSectors:    mainSectors,
		LeadoutSectors: leadoutSectors,
		Mode:           "MODE1/2352",
		ModeByte:       0x01,
	})
	// Replace the sector at failOffset with a hand-crafted fail
	// sector: valid scrambled sync (= unscrambled sync, since the
	// scramble table is zero in the sync region) + invalid mode.
	// The bytes after byte 15 are arbitrary scrambled-looking
	// noise so the sector is not all-zero (otherwise it'd hit
	// case 46 instead of fail-type-2).
	failBytes := make([]byte, SectorSize)
	copy(failBytes[:SyncLen], Sync[:])
	failBytes[15] = 0xF7
	for i := 16; i < SectorSize; i++ {
		failBytes[i] = byte(i*7 + 13) // arbitrary deterministic noise
	}
	binOffset := failOffset * SectorSize
	scramOffset := (-(int(disc.LeadinLBA)) + failOffset) * SectorSize // relative to scram start
	// Both bin and scram contain identical fail bytes (redumper
	// passed through, so .bin == .scram for this sector).
	copy(disc.Bin[binOffset:binOffset+SectorSize], failBytes)
	copy(disc.Scram[scramOffset:scramOffset+SectorSize], failBytes)
	return disc
}
```

(Adapt to the actual SynthDisc struct/field names — read
`fixtures_test.go` first to confirm.)

- [ ] **Step 2: Add the e2e test to `e2e_test.go`**

```go
func TestE2EFailSectorRoundTrip(t *testing.T) {
	disc := synthDiscWithFailSector(t, 100, 10, 50)
	// pack
	containerPath := filepath.Join(t.TempDir(), "out.miniscram")
	rep := quietReporter{}
	if err := Pack(PackOptions{
		CuePath:    disc.CuePath(),
		ScramPath:  disc.ScramPath(),
		OutputPath: containerPath,
		LeadinLBA:  disc.LeadinLBA,
		Verify:     true,
	}, rep); err != nil {
		t.Fatalf("pack: %v", err)
	}
	// unpack
	outScram := filepath.Join(t.TempDir(), "out.scram")
	if err := Unpack(UnpackOptions{
		ContainerPath: containerPath,
		BinPath:       disc.BinPath(),
		OutputPath:    outScram,
		LeadinLBA:     disc.LeadinLBA,
	}, rep); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	// byte-equal
	got, err := os.ReadFile(outScram)
	if err != nil {
		t.Fatalf("read out scram: %v", err)
	}
	if !bytes.Equal(got, disc.Scram) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(disc.Scram))
	}
}
```

(Adapt to the actual `Pack` / `Unpack` option names and
`SynthDisc` accessor methods — read existing e2e tests first.)

- [ ] **Step 3: Run the test**

Run: `go test ./... -run TestE2EFailSectorRoundTrip -v`
Expected: PASS.

If round-trip fails, the most likely bug is in
`synthDiscWithFailSector`'s offset arithmetic — confirm the bin
and scram offsets actually correspond to the same LBA.

- [ ] **Step 4: Run all tests**

Run: `go test ./...`
Expected: ok across all packages.

- [ ] **Step 5: Commit**

```bash
git add fixtures_test.go e2e_test.go
git commit -m "test: e2e round-trip with a hand-crafted fail sector

synthDiscWithFailSector builds a Mode 1 disc whose data track
contains one sector with valid sync + invalid mode (0xF7). Bin
and scram hold identical bytes for that LBA (redumper would have
passed through). TestE2EFailSectorRoundTrip exercises the new
pass-through path end-to-end: pack predicts byte-identically, no
override is emitted, and unpack reproduces the original .scram
byte-for-byte."
```

---

## Task 9: Tighten half-life e2e bounds (optional, fixture-gated)

**Goal:** With the half-life fixture present, measure the new
delta and container sizes and tighten the bound assertions in
`e2e_redump_test.go`.

This task is **optional** and only runs when the half-life fixture
is available (gitignored at `test-discs/half-life/`). If the
fixture is not present in this worktree (likely — fresh worktree),
either symlink it from a prior worktree or skip this task and the
bounds stay at the prior conservative values (the test still
passes, just doesn't tighten).

**Files:**
- Modify: `e2e_redump_test.go:64-65` (half-life bounds).

**Acceptance Criteria:**
- [ ] `MaxDeltaBytes` and `MaxContainerBytes` updated to the
      measured values × ~1.1 (10% headroom).
- [ ] `go test -tags redump_data ./... -run TestE2ERoundTripRealDiscs/half-life`
      passes.

**Verify:**
```
go test -tags redump_data ./... -run TestE2ERoundTripRealDiscs/half-life -v
```
Expected: PASS, with the new tighter bounds.

**Steps:**

- [ ] **Step 1: Make the fixture available**

If `test-discs/half-life/` is missing in this worktree:

```bash
# From the worktree root.
ln -s /home/hugh/miniscram/test-discs test-discs
```

(Or copy if symlinks aren't preferred. The fixture is multi-GB.)

Verify: `ls test-discs/half-life/HALFLIFE.cue test-discs/half-life/HALFLIFE.scram`

- [ ] **Step 2: Run the build-tagged test with verbose output**

Run: `go test -tags redump_data ./... -run TestE2ERoundTripRealDiscs/half-life -v`

Look for the reporter line `building scram prediction + delta ... K override(s), M pass-through(s), delta L bytes`. Note `L` (delta size). Note the resulting container size (printed by the test or visible via `du -b /tmp/...miniscram` if the test leaves it).

- [ ] **Step 3: Tighten the bounds**

Update `e2e_redump_test.go` for the half-life row. If new delta is
~1.5 MB, set:

```go
MaxDeltaBytes:     2 * 1024 * 1024,    // was 15 MB; new floor measured 2026-04-29
MaxContainerBytes: 2 * 1024 * 1024,
```

(Adjust to actual measurement plus headroom.)

- [ ] **Step 4: Re-run the test to confirm**

Run: `go test -tags redump_data ./... -run TestE2ERoundTripRealDiscs/half-life -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add e2e_redump_test.go
git commit -m "test: tighten half-life e2e bounds post fail-sector fix

The classifier-gated predictor reduces the half-life delta from
~5 MB to ~$NEW MB by passing through fail-pattern sectors instead
of wrapping them in 2352-byte overrides. Tightening the bounds
locks in the improvement; future regressions in the predictor
will surface as bound failures.

Measured 2026-04-29 against test-discs/half-life/."
```

---

## Done. What's next?

After Task 8 (or 9), the branch contains:

- 3 docs commits (already on the branch from brainstorming):
  `81cde27`, `3f67304`, `f39e5e0`.
- 8–9 implementation commits (from these tasks).

**Do not merge to main.** PR stays open. The
`format/drop-scrambler-hash` branch will rebase atop this branch
and pick the real format-version number when its bigger refactor
lands.

Push the branch when ready:

```bash
git push -u origin feat/preserve-fail-sectors
```
