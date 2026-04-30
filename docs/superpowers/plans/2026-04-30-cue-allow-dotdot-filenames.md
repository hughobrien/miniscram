# Allow `..` Substrings in Cue FILE Names — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the `strings.Contains(rawName, "..")` substring check in `ParseCue` with explicit equality against `.` and `..` so that legitimate filenames like `F.E.A.R..bin` parse, while real path traversal stays blocked.

**Architecture:** Single-line behaviour change in `cue.go` plus three new test sub-tests in `cue_test.go`. No new files, no API change.

**Tech Stack:** Go (single package `main`), existing `testing` patterns.

**Spec:** [docs/superpowers/specs/2026-04-30-cue-allow-dotdot-filenames-design.md](../specs/2026-04-30-cue-allow-dotdot-filenames-design.md)

---

## File Structure

- `cue.go` — modify the path-safety check at line 86 in `ParseCue`.
- `cue_test.go` — add one accept sub-test (`dotdot-in-name`) and two reject sub-tests (`dot-name`, `dotdot-name`).

No other files involved.

---

### Task 1: Tighten cue path-safety check

**Goal:** Filenames containing `..` as a substring (but not equal to `..`) parse cleanly. Filenames that are exactly `.` or `..` are rejected. All existing accept and reject cases continue to behave identically.

**Files:**
- Modify: `cue.go` (line 86, inside `ParseCue` FILE arm)
- Modify: `cue_test.go` (extend `TestParseCueAccepts` and `TestParseCueRejects`)

**Acceptance Criteria:**
- [ ] `F.E.A.R..bin` parses; `Track.Filename == "F.E.A.R..bin"`
- [ ] `.` is rejected with the existing path-not-supported error
- [ ] `..` is rejected with the existing path-not-supported error
- [ ] All existing `TestParseCueAccepts` and `TestParseCueRejects` sub-tests still pass (regression guard for `../bad.bin` and `subdir/x.bin`)
- [ ] `go test ./...` passes

**Verify:** `go test -run 'TestParseCue' -v ./...` → all sub-tests PASS

**Steps:**

- [ ] **Step 1: Add the failing tests in `cue_test.go`**

In `TestParseCueAccepts`, append this sub-test after the `multi-file` sub-test (i.e. at the end of the function, before its closing `}`):

```go
	t.Run("dotdot-in-name", func(t *testing.T) {
		src := "FILE \"F.E.A.R..bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n"
		tracks, err := ParseCue(strings.NewReader(src))
		if err != nil || len(tracks) != 1 || tracks[0].Filename != "F.E.A.R..bin" {
			t.Fatalf("err=%v len=%d filename=%q; want nil,1,F.E.A.R..bin", err, len(tracks), tracks[0].Filename)
		}
	})
```

In `TestParseCueRejects`, append two entries to the `cases` slice, after the `path-separator` row:

```go
		{"dot-name", "FILE \".\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n"},
		{"dotdot-name", "FILE \"..\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n"},
```

- [ ] **Step 2: Run tests; expect the new accept sub-test to fail**

Run: `cd /home/hugh/miniscram && go test -run TestParseCueAccepts -v ./...`
Expected: `--- FAIL: TestParseCueAccepts/dotdot-in-name` with the error `FILE references with paths not supported: "F.E.A.R..bin"`. The two new reject sub-tests will already pass because the current code does reject `.` (no, actually `.` slips through today since it has no `/`, no `\`, and no `..` substring) and `..` (current code's `Contains(rawName, "..")` matches itself).

So at this step, expect: `dotdot-in-name` fails (real bug); `dot-name` fails (would reject after fix, currently accepts so no error returned — this is the second new failure you should see); `dotdot-name` passes (current code already rejects).

- [ ] **Step 3: Apply the cue.go fix**

In `/home/hugh/miniscram/cue.go`, find this block in `ParseCue`'s `FILE` arm:

```go
			if strings.ContainsAny(rawName, `/\`) || strings.Contains(rawName, "..") {
				return nil, fmt.Errorf("FILE references with paths not supported: %q", rawName)
			}
```

Replace with:

```go
			if strings.ContainsAny(rawName, `/\`) || rawName == "." || rawName == ".." {
				return nil, fmt.Errorf("FILE references with paths not supported: %q", rawName)
			}
```

That's the entire code change.

- [ ] **Step 4: Run tests; expect all pass**

Run: `cd /home/hugh/miniscram && go test -run TestParseCue -v ./...`
Expected: every sub-test of `TestParseCueAccepts` and `TestParseCueRejects` PASS, including the three new ones.

- [ ] **Step 5: Run the full test suite**

Run: `cd /home/hugh/miniscram && go test ./...`
Expected: `ok  github.com/hughobrien/miniscram  ...` — no other regressions.

- [ ] **Step 6: Smoke-check on the real failure**

Run:
```bash
cd /home/hugh/miniscram && go build -o /tmp/miniscram .
rm -rf /tmp/repro && mkdir -p /tmp/repro
for f in "/roms/fear/F2X37H~D"/*; do ln -s "$f" /tmp/repro/; done
/tmp/miniscram pack "/tmp/repro/F.E.A.R..cue" -o /tmp/repro/out.miniscram --keep-source 2>&1 | tail -5
echo "EXIT=${PIPESTATUS[0]}"
rm -rf /tmp/repro
```

Expected: pack progresses past `resolving cue` (it will then fail or succeed depending on disc characteristics, but the path-not-supported error from cue.go must be gone). Exit may be non-zero for unrelated reasons — what matters is the `FILE references with paths not supported` line no longer appears in stderr.

- [ ] **Step 7: Commit**

```bash
cd /home/hugh/miniscram
git add cue.go cue_test.go
git commit -m "$(cat <<'EOF'
fix(cue): allow ".." substrings in FILE names

The path-safety check in ParseCue used strings.Contains(rawName, ".."),
which rejected legitimate filenames containing the substring (e.g.
"F.E.A.R..bin", surfaced by the redumper sweep on /roms/fear/...).
Replace with explicit equality against the special path components
"." and "..". Once / and \ are excluded, the entire rawName is one
path component, so substring matching of ".." has no security value.
EOF
)"
```

---

## Self-Review

**Spec coverage:**
- Replace substring `..` check with equality → Step 3.
- New accept sub-test for `F.E.A.R..bin` → Step 1.
- New reject sub-tests for `.` and `..` → Step 1.
- Existing reject sub-tests must still pass → Step 4 covers this implicitly (full `TestParseCue` re-run).
- Full suite passes → Step 5.

**Placeholder scan:** None. Each step has the actual code, exact paths, and the expected output.

**Type consistency:** No types involved beyond existing `Track.Filename` (string), used identically in the new accept sub-test as in the existing `single-file` sub-test (cue_test.go:43–48).
