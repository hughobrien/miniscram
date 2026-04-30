# PSX Write-Offset Multi-Sector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `BuildEpsilonHat` correctly handle write offsets whose magnitude exceeds one sector (`|WriteOffsetBytes| > SectorSize`), so PSX dumps with offsets up to ±2 sectors no longer panic.

**Architecture:** Single-line behaviour change in `builder.go`'s per-sector loop: drain whole sectors of `skipFirst` via an early `continue` before the partial-sector slice. New example tests in `builder_test.go` exercising offsets at the per-sector and max-limit boundaries, plus one property test sweeping the full ±2-sector range.

**Tech Stack:** Go (single package `main`), existing `synthDiscRaw` helper in `builder_test.go`.

**Spec:** [docs/superpowers/specs/2026-04-30-psx-write-offset-multi-sector-design.md](../specs/2026-04-30-psx-write-offset-multi-sector-design.md)

---

## File Structure

- `builder.go` — modify the skipFirst handler at lines 191-195 inside `BuildEpsilonHat`'s per-sector loop.
- `builder_test.go` — extend `TestBuilderCleanRoundTrip`'s table with four new rows; add `TestBuildEpsilonHatNoPanicAcrossOffsetRange`.

No new files. No API changes.

---

### Task 1: Drain whole sectors of skipFirst in BuildEpsilonHat

**Goal:** `BuildEpsilonHat` accepts any `WriteOffsetBytes` in `[-2*SectorSize, +2*SectorSize]` (i.e. `[-4704, +4704]`) without panicking, and round-trips byte-exact for clean discs.

**Files:**
- Modify: `builder.go` (lines 191-195, inside the per-sector loop in `BuildEpsilonHat`)
- Modify: `builder_test.go` (extend `TestBuilderCleanRoundTrip` table; add new property test)

**Acceptance Criteria:**
- [ ] Negative offset of `-SectorSize` (`-2352`) round-trips byte-exact
- [ ] Negative offset of `-2588` (FF IX SLUS-01251 value) round-trips byte-exact
- [ ] Negative offset of `-2*SectorSize` (`-4704`) round-trips byte-exact
- [ ] Positive offset of `+2588` round-trips byte-exact
- [ ] Property test: offsets in `{-4704, -3000, -2353, -2352, -2351, -1000, -48, 0, 48, 1000, 2351, 2352, 2353, 3000, 4704}` all return without panic and produce output of size `ScramSize`
- [ ] All existing `TestBuilderCleanRoundTrip` sub-tests still pass
- [ ] `go test ./...` passes

**Verify:** `go test -run 'TestBuilder|TestBuildEpsilonHatNoPanic' -v ./...` → all sub-tests PASS

**Steps:**

- [ ] **Step 1: Add the failing tests in `builder_test.go`**

In `TestBuilderCleanRoundTrip`'s case table (currently three rows ending with `mode2-neg-offset, 0x02, "MODE2/2352", -48`), append:

```go
		{"mode1-neg-offset-one-sector", 0x01, "MODE1/2352", -SectorSize},
		{"mode1-neg-offset-multi-sector", 0x01, "MODE1/2352", -2588},
		{"mode1-neg-offset-max", 0x01, "MODE1/2352", -2 * SectorSize},
		{"mode1-pos-offset-multi-sector", 0x01, "MODE1/2352", 2588},
```

After `TestBuilderCleanRoundTrip`'s closing brace, append the property test:

```go
func TestBuildEpsilonHatNoPanicAcrossOffsetRange(t *testing.T) {
	offsets := []int{-4704, -3000, -2353, -2352, -2351, -1000, -48, 0, 48, 1000, 2351, 2352, 2353, 3000, 4704}
	for _, off := range offsets {
		off := off
		t.Run(fmt.Sprintf("offset=%d", off), func(t *testing.T) {
			bin, scram, params := synthDiscRaw(t, 100, off, 10, 0x01, "MODE1/2352")
			var hat bytes.Buffer
			_, _, _, err := BuildEpsilonHat(&hat, params, bytes.NewReader(bin), bytes.NewReader(scram), nil)
			if err != nil {
				t.Fatalf("offset %d: %v", off, err)
			}
			if int64(hat.Len()) != params.ScramSize {
				t.Fatalf("offset %d: ε̂ size %d != scramSize %d", off, hat.Len(), params.ScramSize)
			}
		})
	}
}
```

`fmt` is already imported in `builder_test.go` (used elsewhere); confirm with a quick scan, and if not, add it to the import block.

- [ ] **Step 2: Run tests; expect failures on the multi-sector negative cases**

Run: `cd /home/hugh/miniscram && go test -run TestBuilder -v ./...`

Expected: `mode1-neg-offset-one-sector`, `mode1-neg-offset-multi-sector`, `mode1-neg-offset-max` all FAIL with the runtime panic `slice bounds out of range`. The positive-offset multi-sector test passes (the positive path is already correct). The property test fails on the negative offsets `-2352`, `-2353`, `-3000`, `-4704`.

- [ ] **Step 3: Apply the builder.go fix**

In `/home/hugh/miniscram/builder.go`, find this block in the per-sector loop (around line 191):

```go
		secBytes := sec[:]
		if skipFirst > 0 {
			secBytes = secBytes[skipFirst:]
			skipFirst = 0
		}
```

Replace with:

```go
		secBytes := sec[:]
		if skipFirst >= len(sec) {
			skipFirst -= len(sec)
			continue
		}
		if skipFirst > 0 {
			secBytes = secBytes[skipFirst:]
			skipFirst = 0
		}
```

The `continue` skips the rest of the iteration body (write, scram-compare, run-state). The `bin` reader's `io.ReadFull` ran earlier in the iteration so its position stays in lockstep with the loop's `lba`. `written`, `scramCur`, and `run` are unchanged when nothing is emitted.

- [ ] **Step 4: Run tests; expect pass**

Run: `cd /home/hugh/miniscram && go test -run TestBuilder -v ./...`

Expected: all sub-tests pass — original three plus the four new ones plus the property test's 15 offsets.

- [ ] **Step 5: Run the full test suite**

Run: `cd /home/hugh/miniscram && go test ./...`

Expected: `ok  github.com/hughobrien/miniscram  ...`

- [ ] **Step 6: End-to-end smoke check on FF VIII disc 1**

Run:

```bash
cd /home/hugh/miniscram && go build -o /tmp/miniscram .
rm -rf /tmp/repro && mkdir -p /tmp/repro
for f in /roms/final-fantasy-viii/SLUS-00892/*; do ln -s "$f" /tmp/repro/; done
time /tmp/miniscram pack /tmp/repro/SLUS-00892.cue -o /tmp/repro/out.miniscram --keep-source 2>&1 | tail -10
echo "EXIT=${PIPESTATUS[0]}"
ls -lh /tmp/repro/out.miniscram 2>&1
rm -rf /tmp/repro
```

Expected: stderr last line is `verifying scram hashes ... OK all three match`. Exit 0. The pack reports a non-zero "disagreeing sectors" count if the disc has protection, or zero if clean. Either way, the panic is gone and verify succeeds. Record the wall time and final container size for the PR description.

- [ ] **Step 7: Commit**

```bash
cd /home/hugh/miniscram
git add builder.go builder_test.go
git commit -m "$(cat <<'EOF'
fix(builder): drain whole sectors of skipFirst across multi-sector offsets

BuildEpsilonHat panicked on |WriteOffsetBytes| >= SectorSize because
the per-sector loop applied secBytes[skipFirst:] against a 2352-byte
sector with skipFirst as large as 4704 (the writeOffsetLimit). Fix:
when skipFirst >= len(sec), reduce skipFirst by len(sec) and continue
to the next iteration. The bin reader's io.ReadFull happens earlier
in the iteration body, so the bin position stays aligned across the
skipped sectors.

Surfaced by the redumper sweep on /roms: all 8 PSX dumps (FF VIII × 4,
FF IX × 4) panicked with slice-bounds-out-of-range. Smoke-checked
against /roms/final-fantasy-viii/SLUS-00892/SLUS-00892.cue: pack +
verify now succeed.
EOF
)"
```

---

## Self-Review

**Spec coverage:**
- Fix at builder.go skipFirst handler → Step 3.
- Example tests for `-SectorSize`, `-2588`, `-4704`, `+2588` → Step 1.
- Property test across the 15 offsets → Step 1.
- Existing offset cases preserved → Step 4 (full TestBuilder run).
- E2E smoke verification on FF VIII disc 1 (user's choice) → Step 6.
- Commit message records the bug origin and the smoke check → Step 7.

**Placeholder scan:** None. Each step has the actual code to write, exact paths, and exact verify commands.

**Type consistency:** `synthDiscRaw(t, mainSectors, writeOffset, leadoutSectors, modeByte, modeStr)` is the existing helper signature used by all four new table rows and the property test. `BuildEpsilonHat(out, p, bin, scram, onMismatch)` is the unchanged signature called identically in the property test as in the existing `TestBuilderCleanRoundTrip`. `SectorSize` is the package-level constant already used by surrounding code.
