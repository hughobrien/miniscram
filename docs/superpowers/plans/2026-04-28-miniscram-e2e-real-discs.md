# miniscram e2e Real-Disc Fixture Suite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement TASKS.md item B1 (and the data-track portion of B3) — a `redump_data`-tagged table-driven end-to-end test suite that round-trips one or more real Redumper-dumped CDs through `Pack → ReadContainer → Unpack` and asserts byte-equality plus per-fixture bounds (error count, delta size, container size). Replaces the existing pair of Deus Ex-specific tests.

**Architecture:** Single test file rewrite. A package-level `[]realDiscFixture` slice declares each dataset's filesystem path, expected error count, and bound thresholds. Two top-level test functions (`TestE2ERoundTripRealDiscs`, `TestE2EEDCAndECCRealDiscs`) range over the slice, calling `t.Run` for each fixture; each sub-test skips via `t.Skipf` when its data files are absent. No new helpers added outside the test file.

**Tech Stack:** Go stdlib + existing miniscram package symbols (`Pack`, `Unpack`, `ReadContainer`, `NewReporter`, `ComputeEDC`, `ComputeECC`, `SectorSize`). Build tag `//go:build redump_data` gates the entire file.

**Variance from spec:** Filesystem paths are `/home/hugh/miniscram/<dataset>/` (matching the original test's convention and the actual data layout on this machine). The spec listed `/data/roms/redumper/...` based on the user's pasted terminal output, which was from a different working directory; verified via `ls` that the data lives under the project directory. Otherwise the plan implements exactly what the spec at `docs/superpowers/specs/2026-04-28-miniscram-e2e-real-discs-design.md` describes.

---

## File Structure

| File | Role |
| --- | --- |
| `e2e_redump_test.go` (rewrite) | Table-driven `TestE2ERoundTripRealDiscs`, `TestE2EEDCAndECCRealDiscs`, `realDiscFixture` struct, `realDiscFixtures` slice, `filesEqual` helper. ~200 LOC total. |

The work is a single file. One task is enough — splitting "introduce table-drive" from "add Freelancer row" would interleave diffs in the same file and produce no independently-testable intermediate state (DX data isn't on disk; nothing to verify the refactor-only intermediate against).

---

## Task 1: Rewrite `e2e_redump_test.go` as table-driven multi-fixture suite

**Goal:** Replace `TestE2EDeusEx` + `TestEDCAndECCAgainstDeusEx` with a table-driven suite covering DX (baseline) and Freelancer (SafeDisc 2.70.030, 588 errors). Each fixture skips when its files are absent. HL1 is **not** included — that fixture requires multi-FILE `.cue` support which is out of scope for this cycle.

**Files:**
- Modify (full rewrite): `e2e_redump_test.go`

**Acceptance Criteria:**
- [ ] `go test -tags redump_data -run TestE2ERoundTripRealDiscs -v ./...` passes when run against the Freelancer dataset present at `/home/hugh/miniscram/freelancer/`.
- [ ] The `freelancer` sub-test asserts `m.ErrorSectorCount == 588`, `m.DeltaSize < 5*1024*1024`, container size `< 5*1024*1024`, and recovered `.scram` is byte-equal to original.
- [ ] The `deus-ex` sub-test calls `t.Skipf` (data absent) and the test reports it as SKIP.
- [ ] `realDiscFixtures` slice contains exactly two entries (`deus-ex`, `freelancer`). HL1 is **not** present.
- [ ] `go test ./...` (no `-tags` flag) passes — the file is excluded from default builds.
- [ ] `go vet -tags redump_data ./...` clean.
- [ ] `TestE2EEDCAndECCRealDiscs/freelancer` passes (sample LBAs 0/100/1000/100000 are unprotected on Freelancer).

**Verify:**

```bash
go test -tags redump_data -run TestE2E -v ./... && \
  go vet -tags redump_data ./... && \
  go test ./...
```

Expected output: `TestE2ERoundTripRealDiscs/deus-ex` SKIP, `/freelancer` PASS; `TestE2EEDCAndECCRealDiscs/deus-ex` SKIP, `/freelancer` PASS; default suite PASS; vet clean.

**Steps:**

- [ ] **Step 1: Read the current test file to confirm baseline understanding**

Read `/home/hugh/miniscram/e2e_redump_test.go`. Note the current shape: build-tagged file, two test functions hard-coded to Deus Ex constants, a `filesEqual` helper. We're rewriting all of it.

- [ ] **Step 2: Write the new test file (full rewrite)**

Replace the entire contents of `/home/hugh/miniscram/e2e_redump_test.go` with the following:

```go
//go:build redump_data

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// realDiscFixture configures a single dataset's e2e expectations.
// Add a new entry to realDiscFixtures (below) when a new dataset is
// available. Each sub-test skips independently when its files aren't
// present, so adding a row never causes failures on machines without
// that dataset.
type realDiscFixture struct {
	Name              string  // sub-test name, e.g. "deus-ex"
	Dir               string  // absolute path to the dataset directory
	Stem              string  // filename stem (no extension)
	ExpectedErrors    int32   // assert manifest.ErrorSectorCount == this
	MaxDeltaBytes     int64   // assert manifest.DeltaSize < this
	MaxContainerBytes int64   // assert os.Stat(container).Size() < this
	EDCSampleLBAs     []int64 // LBAs to sample in TestE2EEDCAndECCRealDiscs (must be Mode 1, unprotected)
}

// realDiscFixtures is the authoritative dataset list. Keep entries
// sorted alphabetically by Name. HL1 (multi-track + audio) is
// intentionally absent — its Redumper output uses one .bin per track,
// which miniscram's cue.go currently ignores. Add HL1 here once
// multi-FILE .cue support lands.
var realDiscFixtures = []realDiscFixture{
	{
		Name:              "deus-ex",
		Dir:               "/home/hugh/miniscram/deus-ex",
		Stem:              "DeusEx_v1002f",
		ExpectedErrors:    0,
		MaxDeltaBytes:     1024,
		MaxContainerBytes: 2048,
		EDCSampleLBAs:     []int64{0, 100, 1000, 100000},
	},
	{
		Name: "freelancer",
		Dir:  "/home/hugh/miniscram/freelancer",
		Stem: "FL_v1",
		// SafeDisc 2.70.030; per redump.org submission, 588 deliberately
		// corrupted sectors. Round-trip byte-equality plus this exact
		// count proves miniscram captures the protection losslessly.
		ExpectedErrors:    588,
		MaxDeltaBytes:     5 * 1024 * 1024,
		MaxContainerBytes: 5 * 1024 * 1024,
		// SafeDisc protection clusters near end-of-disc; LBAs in the
		// first 100k are well clear of it.
		EDCSampleLBAs: []int64{0, 100, 1000, 100000},
	},
}

// fixturePresent reports whether all three Redumper output files for a
// fixture exist on disk. Used to gate every sub-test with a single
// check rather than letting Pack fail with a confusing message later.
func fixturePresent(f realDiscFixture) bool {
	for _, ext := range []string{".bin", ".cue", ".scram"} {
		if _, err := os.Stat(filepath.Join(f.Dir, f.Stem+ext)); err != nil {
			return false
		}
	}
	return true
}

// TestE2ERoundTripRealDiscs runs Pack → ReadContainer → Unpack against
// each configured fixture, asserts per-fixture bounds, and confirms
// the recovered .scram is byte-equal to the original.
func TestE2ERoundTripRealDiscs(t *testing.T) {
	for _, f := range realDiscFixtures {
		f := f // capture for the closure
		t.Run(f.Name, func(t *testing.T) {
			if !fixturePresent(f) {
				t.Skipf("dataset not present at %s", f.Dir)
			}
			binPath := filepath.Join(f.Dir, f.Stem+".bin")
			cuePath := filepath.Join(f.Dir, f.Stem+".cue")
			scramPath := filepath.Join(f.Dir, f.Stem+".scram")

			// Use a temp dir on the same filesystem as the dataset to
			// avoid /tmp overflow (the test produces ~scram-sized
			// intermediate files — hundreds of MB).
			tmp, err := os.MkdirTemp(f.Dir, "miniscram-e2e-*")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(tmp) })

			containerPath := filepath.Join(tmp, f.Stem+".miniscram")
			rep := NewReporter(io.Discard, true)

			if err := Pack(PackOptions{
				BinPath:    binPath,
				CuePath:    cuePath,
				ScramPath:  scramPath,
				OutputPath: containerPath,
				Verify:     true,
			}, rep); err != nil {
				t.Fatalf("Pack: %v", err)
			}

			m, _, err := ReadContainer(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			if m.ErrorSectorCount != f.ExpectedErrors {
				t.Errorf("error_sector_count = %d; expected %d", m.ErrorSectorCount, f.ExpectedErrors)
			}
			if m.DeltaSize >= f.MaxDeltaBytes {
				t.Errorf("delta is %d bytes; expected < %d", m.DeltaSize, f.MaxDeltaBytes)
			}
			containerInfo, err := os.Stat(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			if containerInfo.Size() >= f.MaxContainerBytes {
				t.Errorf(".miniscram is %d bytes; expected < %d", containerInfo.Size(), f.MaxContainerBytes)
			}

			outPath := filepath.Join(tmp, f.Stem+".scram.recovered")
			if err := Unpack(UnpackOptions{
				BinPath:       binPath,
				ContainerPath: containerPath,
				OutputPath:    outPath,
				Verify:        true,
			}, rep); err != nil {
				t.Fatalf("Unpack: %v", err)
			}
			if !filesEqual(t, outPath, scramPath) {
				t.Fatal("recovered .scram differs from original")
			}
		})
	}
}

// TestE2EEDCAndECCRealDiscs verifies that miniscram's ComputeEDC /
// ComputeECC agree with the EDC/ECC stored in real Redumper bins. This
// is a sanity check on the bin format itself — failures here mean
// either the dataset is corrupt or EDC/ECC computation is broken, not
// that miniscram's pack/unpack flow is wrong.
func TestE2EEDCAndECCRealDiscs(t *testing.T) {
	for _, f := range realDiscFixtures {
		f := f
		t.Run(f.Name, func(t *testing.T) {
			binPath := filepath.Join(f.Dir, f.Stem+".bin")
			if _, err := os.Stat(binPath); err != nil {
				t.Skipf("dataset not present at %s", f.Dir)
			}
			file, err := os.Open(binPath)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			for _, lba := range f.EDCSampleLBAs {
				var sec [SectorSize]byte
				if _, err := file.ReadAt(sec[:], lba*SectorSize); err != nil {
					t.Fatalf("reading sector %d: %v", lba, err)
				}
				// EDC over [0:2064] should equal stored bytes [2064:2068].
				gotEDC := ComputeEDC(sec[:2064])
				var wantEDC [4]byte
				copy(wantEDC[:], sec[2064:2068])
				if gotEDC != wantEDC {
					t.Errorf("LBA %d EDC: got %x; stored %x", lba, gotEDC, wantEDC)
				}
				// ECC over [12:2076] should equal stored bytes [2076:2352].
				var test [SectorSize]byte = sec
				for i := 2076; i < SectorSize; i++ {
					test[i] = 0
				}
				ComputeECC(&test)
				if !bytes.Equal(test[2076:], sec[2076:]) {
					t.Errorf("LBA %d ECC differs", lba)
				}
			}
		})
	}
}

// filesEqual compares two files in 1-MiB chunks. Test helper, kept
// in this file because no other test file needs it.
func filesEqual(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	defer fb.Close()
	bufA := make([]byte, 1<<20)
	bufB := make([]byte, 1<<20)
	for {
		nA, errA := io.ReadFull(fa, bufA)
		nB, errB := io.ReadFull(fb, bufB)
		if nA != nB {
			return false
		}
		if !bytes.Equal(bufA[:nA], bufB[:nB]) {
			return false
		}
		if errA == io.EOF || errA == io.ErrUnexpectedEOF {
			return errB == io.EOF || errB == io.ErrUnexpectedEOF
		}
		if errA != nil {
			t.Fatal(errA)
		}
	}
}
```

- [ ] **Step 3: Run the redump_data-tagged test suite**

```bash
go test -tags redump_data -run TestE2E -v ./...
```

Expected output (with only Freelancer present at `/home/hugh/miniscram/freelancer/`):

```
=== RUN   TestE2ERoundTripRealDiscs
=== RUN   TestE2ERoundTripRealDiscs/deus-ex
    e2e_redump_test.go:NN: dataset not present at /home/hugh/miniscram/deus-ex
--- SKIP: TestE2ERoundTripRealDiscs/deus-ex (0.00s)
=== RUN   TestE2ERoundTripRealDiscs/freelancer
--- PASS: TestE2ERoundTripRealDiscs/freelancer (...s)
--- PASS: TestE2ERoundTripRealDiscs (...s)
=== RUN   TestE2EEDCAndECCRealDiscs
=== RUN   TestE2EEDCAndECCRealDiscs/deus-ex
    e2e_redump_test.go:NN: dataset not present at /home/hugh/miniscram/deus-ex
--- SKIP: TestE2EEDCAndECCRealDiscs/deus-ex (0.00s)
=== RUN   TestE2EEDCAndECCRealDiscs/freelancer
--- PASS: TestE2EEDCAndECCRealDiscs/freelancer (...s)
--- PASS: TestE2EEDCAndECCRealDiscs (...s)
PASS
```

If `TestE2ERoundTripRealDiscs/freelancer` fails on a delta or container size assertion, the bound was set too tight; record the actual values from the failure message and update the fixture's `MaxDeltaBytes` / `MaxContainerBytes` to a generous-but-not-absurd value (e.g., 2× actual). Do not relax `ExpectedErrors`.

If it fails on `error_sector_count` mismatch, that's a real signal — either the disc identification is wrong or miniscram's lockstep error detector is broken. Do not adjust the expected count without checking the disc's redump.org submission info.

If `TestE2EEDCAndECCRealDiscs/freelancer` fails on a sample LBA, it means that LBA is part of the SafeDisc protection (deliberately corrupted). Pick a different LBA (e.g., add 50 to the failing one) and retry.

- [ ] **Step 4: Run the default test suite to confirm the file is excluded without the tag**

```bash
go test ./...
```

Expected: PASS, with no `TestE2E*` lines (the file's `//go:build redump_data` tag excludes it from this run).

- [ ] **Step 5: Run `go vet` with the tag to catch dead code or unused imports**

```bash
go vet -tags redump_data ./...
```

Expected: no output (clean).

- [ ] **Step 6: Commit**

```bash
git add e2e_redump_test.go
git commit -m "$(cat <<'EOF'
e2e: rewrite TestE2EDeusEx as table-driven multi-fixture suite

Replaces the two Deus Ex-specific tests (TestE2EDeusEx,
TestEDCAndECCAgainstDeusEx) with TestE2ERoundTripRealDiscs and
TestE2EEDCAndECCRealDiscs, both ranging over a package-level
realDiscFixtures slice. Each sub-test skips independently when
its data files are absent, so the suite degrades gracefully on
machines that have only some of the configured datasets.

Adds the Freelancer fixture (SafeDisc 2.70.030, 588 errors): this
is the first real-world protected-disc round-trip miniscram has
ever been tested against. Byte-equal recovery proves losslessness
through the structured-delta path; the 588-count assertion proves
the lockstep detector flags every intentional error.

HL1 (multi-track + audio) is not added — its Redumper output uses
per-track .bin files which cue.go's FILE-line-ignoring parser
can't currently handle. Add HL1 to realDiscFixtures once
multi-FILE .cue support lands.

Closes TASKS.md B1; partial progress on B3.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** All seven spec acceptance criteria map to assertions in the plan. (1) "real protected disc tested e2e" → Freelancer fixture row + Pack/Unpack/byte-compare assertions. (2) "round-trip byte-equal" → `filesEqual` check. (3) "error_sector_count non-zero and matches known count" → `m.ErrorSectorCount == 588` assertion. (4) "spot-check waived" → no spot-check code, justified in spec. (5) "DX baseline regression" → DX fixture row with `MaxDeltaBytes: 1024, MaxContainerBytes: 2048` (will fail loudly if the small-delta property regresses on a clean disc, when DX data is present). (6) "B3 deferral noted" → comment in `realDiscFixtures` and in the commit message. (7) "EDC/ECC sanity test generalized" → `TestE2EEDCAndECCRealDiscs` table-driven over the same fixtures.
- **Placeholder scan:** No TBDs/TODOs. Bound thresholds are deliberately generous and the failure-mode adjustment is documented in Step 3.
- **Type consistency:** `realDiscFixture` struct, `realDiscFixtures` slice, `fixturePresent` helper, `filesEqual` helper, two test functions — all defined inline in the same file in dependency order.
- **No external symbol dependencies introduced:** All Pack/Unpack/ReadContainer/NewReporter/ComputeEDC/ComputeECC/SectorSize calls match existing exported signatures.
- **Recovery from first-run surprises** (delta/container bounds too tight, LBA hits a protected sector) is documented in Step 3 with the policy: tighten/swap, never relax the error count.

## Post-implementation variance

The plan landed as written for Task 1 (commit `25b46a3`). First-run on Freelancer revealed two issues, addressed in two follow-on commits:

- **`pack.go` lead-in detection bug** (commit `57e05b3`, after revert `e0787e9` of unauthorized commit `05eee78`): real Redumper dumps of protected discs have non-zero lead-in data that produces coincidental sync-pattern matches; the original `detectWriteOffset` aborted on these. Fix iterates sync candidates and accepts the first one with valid BCD MSF + sample-aligned offset.
- **Metric definition mismatch** (Task 2, commit `735da03`): `manifest.ErrorSectorCount` counts override sectors (data errors + lead-in noise + boundaries), not Redumper's "errors count" (data-track ECC/EDC only). Test now asserts a separately-computed data-track ECC/EDC count via the `countDataTrackErrors` helper; the 588-for-Freelancer signature is preserved. Manifest's count gets a soft sentinel; bounds loosened to 15 MiB.
