# Tidy step output — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove redundant scramble-table self-test narration and replace `Done("ok")` filler so `pack`/`unpack` step output carries information on every line.

**Architecture:** Two commits. First commit tightens `textStep.Done` so an empty message renders cleanly as `... OK\n`. Second commit deletes the redundant runtime self-test blocks (init() already asserts the table) and replaces the three remaining `Done("ok")` call-sites with either bare `Done("")` or informative narration. README's FL_v1 walkthrough updated to match.

**Tech Stack:** Go 1.22+, single package `main`, standard library only. Tests live next to source as `*_test.go`.

**Spec:** `docs/superpowers/specs/2026-04-30-tidy-step-output-design.md`

---

### Task 1: Tighten `textStep.Done` for empty messages

**Goal:** `Done("")` renders as `... OK\n` (no trailing space). `Done("msg")` unchanged. Pinned by a new test.

**Files:**
- Modify: `reporter.go:57-79` (Done and Fail)
- Modify: `reporter_test.go` (add one new test)

**Acceptance Criteria:**
- [ ] New test `TestStepDoneEmptyMessage` passes; pre-change it fails on the trailing-space buffer mismatch.
- [ ] All existing tests in `go test ./...` still pass.
- [ ] `Done("baz")` continues to render as `... OK baz\n`.

**Verify:** `go test -run TestStepDoneEmptyMessage ./...` → `ok` ; `go test ./...` → all pass.

**Steps:**

- [ ] **Step 1: Write the failing test**

Add to `reporter_test.go` (after `TestReporterStep`, before `TestReporterInfoAndWarn`):

```go
func TestStepDoneEmptyMessage(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, false)
	r.Step("foo").Done("")
	if got, want := buf.String(), "foo ... OK\n"; got != want {
		t.Fatalf("Done(\"\") = %q, want %q", got, want)
	}

	buf.Reset()
	r.Step("bar").Done("baz")
	if got, want := buf.String(), "bar ... OK baz\n"; got != want {
		t.Fatalf("Done(\"baz\") = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestStepDoneEmptyMessage ./...`
Expected: FAIL with `Done("") = "foo ... OK \n", want "foo ... OK\n"` (note the extra space before `\n` in the actual output).

- [ ] **Step 3: Tighten `textStep.Done`**

Replace the body of `textStep.Done` at `reporter.go:57-67`. Before:

```go
func (s *textStep) Done(format string, args ...any) {
	if s.done {
		return
	}
	s.done = true
	mark := "OK"
	if s.r.tty {
		mark = "✓"
	}
	fmt.Fprintf(s.r.w, " ... %s %s\n", mark, fmt.Sprintf(format, args...))
}
```

After:

```go
func (s *textStep) Done(format string, args ...any) {
	if s.done {
		return
	}
	s.done = true
	mark := "OK"
	if s.r.tty {
		mark = "✓"
	}
	msg := fmt.Sprintf(format, args...)
	if msg == "" {
		fmt.Fprintf(s.r.w, " ... %s\n", mark)
		return
	}
	fmt.Fprintf(s.r.w, " ... %s %s\n", mark, msg)
}
```

- [ ] **Step 4: Apply the same treatment to `textStep.Fail`**

Replace the body of `textStep.Fail` at `reporter.go:69-79`. Before:

```go
func (s *textStep) Fail(err error) {
	if s.done {
		return
	}
	s.done = true
	mark := "FAIL"
	if s.r.tty {
		mark = "✗"
	}
	fmt.Fprintf(s.r.w, " ... %s %s\n", mark, err.Error())
}
```

After:

```go
func (s *textStep) Fail(err error) {
	if s.done {
		return
	}
	s.done = true
	mark := "FAIL"
	if s.r.tty {
		mark = "✗"
	}
	msg := err.Error()
	if msg == "" {
		fmt.Fprintf(s.r.w, " ... %s\n", mark)
		return
	}
	fmt.Fprintf(s.r.w, " ... %s %s\n", mark, msg)
}
```

(No caller exercises the empty branch today; the change is for symmetry with `Done`.)

- [ ] **Step 5: Run tests to verify**

Run: `go test -run TestStepDoneEmptyMessage ./...`
Expected: PASS.

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add reporter.go reporter_test.go
git commit -m "$(cat <<'EOF'
refactor(reporter): render textStep.Done/Fail cleanly with empty msg

Done("") and Fail with empty err.Error() now render "... OK\n" /
"... FAIL\n" instead of "... OK \n" / "... FAIL \n" (trailing space
before newline). Done("") becomes the idiomatic "step succeeded,
nothing to add" form, distinct from Done("ok") which adds a literal
"ok" to the line.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Drop redundant self-test, replace `Done("ok")` filler, update README

**Goal:** Pack/unpack step output stops printing the redundant scramble-table self-test line and the three `OK ok` filler lines. Each remaining step prints either bare `OK` (constant-offset check) or informative narration (manifest read, scram prediction).

**Files:**
- Modify: `pack.go:55-62` (delete self-test runStep block); `pack.go:101` (Done call)
- Modify: `unpack.go:33-40` (delete self-test runStep block); `unpack.go:125` (Done call); `unpack.go:213` (Done call)
- Modify: `README.md:42-56` and `README.md:86-91` (update FL_v1 output blocks)

**Acceptance Criteria:**
- [ ] `go build ./...` succeeds.
- [ ] `go test ./...` all pass (no fixture/output tests should break — they use the quiet reporter or substring assertions).
- [ ] `grep -n "scramble-table self-test" *.go` returns no matches.
- [ ] `grep -n 'Done("ok")' *.go` returns no matches.
- [ ] `grep -n "OK ok" README.md` returns no matches.
- [ ] `CheckScrambleTable` still callable: `grep -n "CheckScrambleTable" *.go` shows callers in `ecma130.go` (definition + init) and `ecma130_test.go` only.
- [ ] Manual run against a fixture (or `go run . pack` against any cue) prints output matching the spec's preview block.

**Verify:** `go test ./...` ; `grep -n "OK ok" README.md *.go` (expect no matches) ; `grep -n "scramble-table self-test" *.go` (expect no matches).

**Steps:**

- [ ] **Step 1: Delete the self-test block in `pack.go`**

Remove `pack.go:55-62`. Before:

```go
	if err := runStep(r, "running scramble-table self-test", func() (string, error) {
		if err := CheckScrambleTable(); err != nil {
			return "", err
		}
		return "ok", nil
	}); err != nil {
		return err
	}

	// 1. resolve cue (parse + stat + cumulative LBAs).
```

After (the comment for step 1 immediately follows the `if r == nil { ... }` block):

```go
	if r == nil {
		r = quietReporter{w: io.Discard}
	}

	// 1. resolve cue (parse + stat + cumulative LBAs).
```

(Note: this also removes a blank line. Confirm the result has exactly one blank line between the `r == nil` guard and the `// 1.` comment to match the surrounding spacing.)

- [ ] **Step 2: Delete the self-test block in `unpack.go`**

Remove `unpack.go:33-40`. Before:

```go
	if r == nil {
		r = quietReporter{w: io.Discard}
	}

	if err := runStep(r, "running scramble-table self-test", func() (string, error) {
		if err := CheckScrambleTable(); err != nil {
			return "", err
		}
		return "ok", nil
	}); err != nil {
		return err
	}

	if !opts.Force {
```

After:

```go
	if r == nil {
		r = quietReporter{w: io.Discard}
	}

	if !opts.Force {
```

- [ ] **Step 3: Replace `Done("ok")` at `pack.go:101`**

The step is "checking constant offset". Before:

```go
	st.Done("ok")
```

After:

```go
	st.Done("")
```

- [ ] **Step 4: Replace `Done("ok")` at `unpack.go:125`**

The step is "building scram prediction". `m.Scram.Size` is bound at this scope (used at line 107). Before:

```go
	st.Done("ok")
```

After:

```go
	st.Done("%d sector(s)", m.Scram.Size/SectorSize)
```

(`SectorSize` is an untyped constant in `layout.go:6`, so it adapts to the `int64` type of `m.Scram.Size` — no cast needed.)

- [ ] **Step 5: Replace `Done("ok")` at `unpack.go:213`**

The step is "reading manifest" inside `Verify`. `m` is bound on the preceding line. Before:

```go
	st.Done("ok")
```

After:

```go
	st.Done("%d track(s), %d byte scram", len(m.Tracks), m.Scram.Size)
```

- [ ] **Step 6: Update README.md FL_v1 pack output**

Update the pack code block at `README.md:42-56`. Before (verbatim):

```
running scramble-table self-test ... OK ok
resolving cue FL_v1.cue ... OK 1 track(s), 729914976 bytes total
detecting write offset ... OK -48 bytes
checking constant offset ... OK ok
hashing tracks ... OK 1 track(s) hashed
hashing scram ... OK c98323550138
building scram prediction + delta ... OK 2812 disagreeing sector(s) → 45927 override record(s), 0 pass-through(s), delta 7084781 bytes
writing container ... OK FL_v1.miniscram
reading manifest ... OK ok
running scramble-table self-test ... OK ok
reading container FL_v1.miniscram ... OK delta 7084781 bytes
verifying bin hashes ... OK all tracks match
building scram prediction ... OK ok
applying delta ... OK 7084781 byte(s) of delta applied
verifying scram hashes ... OK all three match
```

After:

```
resolving cue FL_v1.cue ... OK 1 track(s), 729914976 bytes total
detecting write offset ... OK -48 bytes
checking constant offset ... OK
hashing tracks ... OK 1 track(s) hashed
hashing scram ... OK c98323550138
building scram prediction + delta ... OK 2812 disagreeing sector(s) → 45927 override record(s), 0 pass-through(s), delta 7084781 bytes
writing container ... OK FL_v1.miniscram
reading manifest ... OK 1 track(s), 836338152 byte scram
reading container FL_v1.miniscram ... OK delta 7084781 bytes
verifying bin hashes ... OK all tracks match
building scram prediction ... OK 355586 sector(s)
applying delta ... OK 7084781 byte(s) of delta applied
verifying scram hashes ... OK all three match
```

(`TotalLBAs(836338152, -48) = 355586` — accounts for write offset and the trailing partial sector. The FL_v1 scram is non-aligned: `836338152 mod 2352 = 2232`, so a truncating divide would undercount by 1.)

- [ ] **Step 7: Update README.md FL_v1 unpack output**

Update the unpack code block at `README.md:86-91`. Before:

```
running scramble-table self-test ... OK ok
reading container FL_v1.miniscram ... OK delta 7084781 bytes
verifying bin hashes ... OK all tracks match
building scram prediction ... OK ok
applying delta ... OK 7084781 byte(s) of delta applied
verifying output hashes ... OK all three match
```

After:

```
reading container FL_v1.miniscram ... OK delta 7084781 bytes
verifying bin hashes ... OK all tracks match
building scram prediction ... OK 355586 sector(s)
applying delta ... OK 7084781 byte(s) of delta applied
verifying output hashes ... OK all three match
```

- [ ] **Step 8: Build and test**

Run: `go build ./...`
Expected: success (no output).

Run: `go test ./...`
Expected: all PASS.

Run: `grep -n "OK ok" README.md *.go`
Expected: no matches.

Run: `grep -n "scramble-table self-test" *.go`
Expected: no matches.

Run: `grep -n "CheckScrambleTable" *.go`
Expected: three matches — the definition and `init()` call in `ecma130.go`, plus the test in `ecma130_test.go`. No other callers.

- [ ] **Step 9: Optional manual smoke test**

If a `test-discs/<name>/` fixture is present:

```bash
go test -tags redump_data -run TestRedumpRoundTrip ./...
```

Expected: PASS. (This exercises pack + unpack + verify against a real disc; the changed narration strings must not break any substring checks.)

- [ ] **Step 10: Commit**

```bash
git add pack.go unpack.go README.md
git commit -m "$(cat <<'EOF'
refactor: drop redundant self-test step, replace Done("ok") filler

CheckScrambleTable() is already called from ecma130.go's init() — the
binary panics before main() if the LFSR table drifts. Dropping the
runtime runStep wrapper in Pack/Unpack removes a redundant narration
line per command. Three remaining Done("ok") filler sites become
either Done("") (constant-offset check) or carry information
(manifest track/size, scram sector count). README's FL_v1 walkthrough
output blocks updated to match.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Self-review

**Spec coverage:** Spec has three changes. Task 1 covers Change 2 (reporter rendering + new test). Task 2 covers Change 1 (drop self-test) and Change 3 (replace filler) plus the README update from the spec's "README" section. No spec section uncovered.

**Placeholder scan:** No TBD/TODO/"add appropriate". Every step has the actual code. Commands have expected output.

**Type consistency:** `m.Scram.Size` is `int64` per `manifest.go:31`. `SectorSize` is an untyped constant in `layout.go:6` (`= 2352`), so `m.Scram.Size/SectorSize` evaluates as `int64` with no cast. `len(m.Tracks)` is `int`, formatted with `%d` — fine.

**Risk recap:** Both commits are small. Task 1's reporter change is gated by a new test that pins exact output. Task 2's deletions and call-site changes are mechanical; the existing test suite (substring asserts in `reporter_test.go`, quiet reporter in pack/unpack tests) won't break, and the optional `redump_data` smoke test gives belt-and-suspenders coverage.
