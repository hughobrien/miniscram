# Cue Non-Cue Rejection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `miniscram pack` give a polite, fast, visible error when fed a non-cuesheet input (DVD .iso, binary blob, anything without cue keywords).

**Architecture:** Two surgical changes — a 4 KiB head-sniff in `ParseCue` that returns a sentinel "not a cuesheet" error before the streaming scanner can run away on hostile input, and a `quietReporter` change that keeps `Fail` output visible (only `Done`/`Info`/`Warn` are suppressed in `--quiet`). Both changes are TDD.

**Tech Stack:** Go (single package `main`), `bufio`/`bytes`/`io` from stdlib, existing `testing` + `testing/quick` patterns.

**Spec:** [docs/superpowers/specs/2026-04-30-cue-non-cue-rejection-design.md](../specs/2026-04-30-cue-non-cue-rejection-design.md)

---

## File Structure

- `cue.go` — modify `ParseCue` (add head-sniff). Add unexported sentinel `errNotACuesheet`.
- `cue_test.go` — add `TestParseCueRejectsNonCueInput`, add `TestParseCueRejectsRandomBinaryProperty`.
- `reporter.go` — modify `quietReporter` to carry a writer and emit on `Fail`.
- `reporter_test.go` — replace `TestReporterQuietProducesNoOutput` with two tests: `TestQuietReporterEmitsFailures` and `TestQuietReporterSilencesProgress`.

No new files. No changes to `main.go` (the existing `NewReporter(stderr, common.quiet)` call sites already pass the right writer).

---

### Task 1: ParseCue head-sniff rejects non-cue input

**Goal:** `ParseCue` reads at most 4 KiB before deciding whether the input looks like a cuesheet. Inputs lacking any cue keyword in that window get a sentinel error; valid cuesheets parse exactly as before.

**Files:**
- Modify: `cue.go` (add sentinel `errNotACuesheet`, modify `ParseCue` body before the `bufio.NewScanner` call)
- Modify: `cue_test.go` (add two new tests)

**Acceptance Criteria:**
- [ ] `ParseCue(strings.NewReader(<binary 8 KiB>))` returns error containing `does not look like a cuesheet`.
- [ ] The error wraps `errNotACuesheet` (`errors.Is(err, errNotACuesheet)` is true).
- [ ] All existing cases in `TestParseCueAccepts` and `TestResolveCue` still pass.
- [ ] A `testing/quick` property test of 50+ random binary inputs returns the sentinel without panicking.
- [ ] On a 4 GB hostile input (random bytes, no newlines), `ParseCue` returns within milliseconds — bounded by reading 4 KiB, not by streaming the whole reader.

**Verify:** `go test -run 'TestParseCue|TestResolveCue' -v ./...` → all pass

**Steps:**

- [ ] **Step 1: Write the failing tests in `cue_test.go`**

Append to `cue_test.go`:

```go
func TestParseCueRejectsNonCueInput(t *testing.T) {
	// 8 KiB of 0xFF, no newlines, no keywords — looks like a binary
	// blob (e.g. a DVD .iso fed in by mistake).
	blob := bytes.Repeat([]byte{0xFF}, 8*1024)
	_, err := ParseCue(bytes.NewReader(blob))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errNotACuesheet) {
		t.Fatalf("expected errors.Is(err, errNotACuesheet), got %v", err)
	}
	if !strings.Contains(err.Error(), "does not look like a cuesheet") {
		t.Fatalf("expected friendly message, got %q", err.Error())
	}
}

func TestParseCueRejectsRandomBinaryProperty(t *testing.T) {
	// Random binary input ≥ 4 KiB with no cue keywords must always
	// return the sentinel without panicking.
	cueKeywords := []string{"FILE", "TRACK", "INDEX", "REM",
		"PERFORMER", "TITLE", "CATALOG", "PREGAP"}
	check := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))
		buf := make([]byte, 4*1024+r.Intn(4*1024))
		r.Read(buf)
		// Force-strip newlines and any accidental keyword to
		// guarantee the sniff rejects.
		for i, b := range buf {
			if b == '\n' || b == '\r' {
				buf[i] = 0
			}
		}
		s := string(buf)
		for _, kw := range cueKeywords {
			if strings.Contains(s, kw) {
				return true // skip seed; can't guarantee rejection
			}
		}
		_, err := ParseCue(bytes.NewReader(buf))
		return errors.Is(err, errNotACuesheet)
	}
	cfg := &quick.Config{MaxCount: 50}
	if err := quick.Check(check, cfg); err != nil {
		t.Fatal(err)
	}
}
```

Add the imports if missing — `cue_test.go` will need `bytes`, `errors`, `math/rand`, `testing/quick`. Existing imports in `cue_test.go`: check before adding.

- [ ] **Step 2: Run tests; expect compile failure (errNotACuesheet undefined)**

Run: `go test -run TestParseCueRejectsNonCueInput ./...`
Expected: build error `undefined: errNotACuesheet`.

- [ ] **Step 3: Add the sentinel and head-sniff to `cue.go`**

Add at the top of `cue.go` (after the imports block, before `Track`):

```go
// errNotACuesheet is the sentinel returned by ParseCue when the input
// has no cue keywords in its first cueSniffBytes. Wrap with
// errors.Is to detect.
var errNotACuesheet = errors.New("does not look like a cuesheet")

// cueSniffBytes is how far ParseCue reads before deciding the input
// is not a cuesheet. Real redumper cuesheets are tens to hundreds of
// bytes; 4 KiB is generous even for unusually REM-heavy cues.
const cueSniffBytes = 4 * 1024

// cueKeywords are the tokens ParseCue's main loop dispatches on (or
// explicitly ignores in the default arm). Presence of any one of
// these in the head buffer is the test for "this is a cuesheet".
var cueKeywords = [][]byte{
	[]byte("FILE"), []byte("TRACK"), []byte("INDEX"),
	[]byte("REM"), []byte("PERFORMER"), []byte("TITLE"),
	[]byte("CATALOG"), []byte("PREGAP"),
}
```

Add `"errors"` and `"bytes"` to the import block if not already present.

Modify `ParseCue` to do the sniff first. Replace the existing function header + first line:

```go
func ParseCue(r io.Reader) ([]Track, error) {
	scanner := bufio.NewScanner(r)
```

with:

```go
func ParseCue(r io.Reader) ([]Track, error) {
	// Read up to cueSniffBytes into a buffer before scanning. If the
	// buffer holds none of the cue keywords, return errNotACuesheet
	// without engaging the scanner — bounds runtime on hostile input
	// (e.g. a multi-GB .iso accidentally passed as the cue arg).
	head := make([]byte, cueSniffBytes)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	head = head[:n]
	if !containsAnyKeyword(head, cueKeywords) {
		return nil, fmt.Errorf("%w (no FILE/TRACK/REM/... in first %d bytes)",
			errNotACuesheet, cueSniffBytes)
	}
	scanner := bufio.NewScanner(io.MultiReader(bytes.NewReader(head), r))
```

Add a helper at the bottom of `cue.go`:

```go
func containsAnyKeyword(buf []byte, keywords [][]byte) bool {
	for _, kw := range keywords {
		if bytes.Contains(buf, kw) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests; expect pass**

Run: `go test -run 'TestParseCue|TestResolveCue' -v ./...`
Expected: all tests pass, including the new ones and all existing ones.

- [ ] **Step 5: Sanity-check on a real DVD .iso**

Run:
```bash
go build -o /tmp/miniscram .
/tmp/miniscram pack /roms/grand-theft-auto-3/GTA3.iso -o /tmp/decline.miniscram --keep-source 2>&1 | tail -3
echo "EXIT=$?"
rm -f /tmp/decline.miniscram
```

Expected stderr (last line): `resolving cue /roms/grand-theft-auto-3/GTA3.iso ... FAIL does not look like a cuesheet (no FILE/TRACK/REM/... in first 4096 bytes)`. Exit non-zero. Should return in <1 second (vs 90s+ before).

- [ ] **Step 6: Commit**

```bash
git add cue.go cue_test.go
git commit -m "$(cat <<'EOF'
fix(cue): head-sniff rejects non-cuesheet input with friendly error

ParseCue now reads up to 4 KiB before scanning and returns a sentinel
errNotACuesheet if no cue keyword is present. Fixes opaque
"bufio.Scanner: token too long" surfacing for non-cue input (e.g.
DVD .iso accidentally passed as the cue arg) and bounds runtime so
the parser returns in milliseconds rather than streaming the whole
multi-GB input.
EOF
)"
```

---

### Task 2: quietReporter surfaces failures

**Goal:** `--quiet` silences progress (`Step.Done`, `Info`, `Warn`) but keeps failures (`Step.Fail`) visible on stderr. Fixes the "exit 4 with empty stderr" footgun.

**Files:**
- Modify: `reporter.go` (`quietReporter` becomes stateful, `quietStep.Fail` writes)
- Modify: `reporter_test.go` (replace `TestReporterQuietProducesNoOutput` with two narrower tests)

**Acceptance Criteria:**
- [ ] `NewReporter(w, true)` returns a quiet reporter that writes nothing on `Step.Done`, `Info`, `Warn`.
- [ ] On `Step.Fail`, the quiet reporter writes a single line containing the step label and the error message to `w`.
- [ ] No call sites in `main.go` need to change (the writer is already plumbed through `NewReporter(stderr, common.quiet)`).
- [ ] `go test ./...` passes.

**Verify:** `go test -run TestReporter -v ./...` → all pass

**Steps:**

- [ ] **Step 1: Replace the existing quiet test with the new contract**

Edit `reporter_test.go`. Replace the existing `TestReporterQuietProducesNoOutput` (whole function) with:

```go
func TestQuietReporterEmitsFailures(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, true)
	r.Step("resolving cue").Fail(errors.New("does not look like a cuesheet"))
	out := buf.String()
	if !strings.Contains(out, "resolving cue") {
		t.Fatalf("missing label in %q", out)
	}
	if !strings.Contains(out, "does not look like a cuesheet") {
		t.Fatalf("missing error text in %q", out)
	}
}

func TestQuietReporterSilencesProgress(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, true)
	r.Step("a").Done("done")
	r.Info("ignored")
	r.Warn("ignored")
	if buf.Len() != 0 {
		t.Fatalf("quiet reporter wrote %q on Done/Info/Warn", buf.String())
	}
}
```

- [ ] **Step 2: Run tests; expect failure (quiet reporter still silent)**

Run: `go test -run 'TestQuietReporterEmitsFailures' -v ./...`
Expected: FAIL — `missing label in ""` (the current quiet reporter writes nothing).

- [ ] **Step 3: Update `quietReporter` to carry a writer and emit on Fail**

Edit `reporter.go`. Find the quiet types (currently lines 81–90):

```go
type quietReporter struct{}

func (quietReporter) Step(string) StepHandle { return quietStep{} }
func (quietReporter) Info(string, ...any)    {}
func (quietReporter) Warn(string, ...any)    {}

type quietStep struct{}

func (quietStep) Done(string, ...any) {}
func (quietStep) Fail(error)          {}
```

Replace with:

```go
// quietReporter discards progress (Step.Done, Info, Warn) but still
// surfaces failures via Step.Fail to its writer. This keeps `--quiet`
// useful: the user opted out of progress, not out of error visibility.
type quietReporter struct{ w io.Writer }

func (q quietReporter) Step(label string) StepHandle {
	return quietStep{w: q.w, label: label}
}
func (quietReporter) Info(string, ...any) {}
func (quietReporter) Warn(string, ...any) {}

type quietStep struct {
	w     io.Writer
	label string
}

func (quietStep) Done(string, ...any) {}
func (s quietStep) Fail(err error) {
	fmt.Fprintf(s.w, "%s: %v\n", s.label, err)
}
```

Update `NewReporter` (line 27–32) to pass the writer to `quietReporter`:

```go
func NewReporter(w io.Writer, quiet bool) Reporter {
	if quiet {
		return quietReporter{w: w}
	}
	return &textReporter{w: w, tty: isStderrTTY(w)}
}
```

- [ ] **Step 4: Run tests; expect pass**

Run: `go test -run TestReporter -v ./...`
Expected: all tests pass — `TestReporterStep`, `TestReporterInfoAndWarn`, `TestQuietReporterEmitsFailures`, `TestQuietReporterSilencesProgress`.

- [ ] **Step 5: Sanity-check on a real DVD .iso with --quiet**

Run:
```bash
go build -o /tmp/miniscram .
/tmp/miniscram pack /roms/grand-theft-auto-3/GTA3.iso -o /tmp/decline.miniscram --keep-source --quiet 2>&1 | tail -1
echo "EXIT=$?"
rm -f /tmp/decline.miniscram
```

Expected: a single stderr line like `resolving cue: does not look like a cuesheet (no FILE/TRACK/REM/... in first 4096 bytes)`. Exit non-zero. Compare to the empty-stderr behavior before this fix.

- [ ] **Step 6: Run the full test suite**

Run: `go test ./...`
Expected: all pass (no regressions in anything that uses the reporter or cue parser).

- [ ] **Step 7: Commit**

```bash
git add reporter.go reporter_test.go
git commit -m "$(cat <<'EOF'
fix(reporter): quiet mode keeps failures visible on stderr

quietReporter now carries the writer it was constructed with and
Step.Fail emits "<label>: <err>" on stderr. Step.Done, Info, and
Warn remain silent. Fixes the case where `pack --quiet` against a
non-cue input exits non-zero with empty stderr — the user gets one
line explaining what went wrong.
EOF
)"
```

---

## Self-Review

**Spec coverage:**
- Fix A (head-sniff) → Task 1.
- Fix B (quietReporter Fail) → Task 2.
- Test list from spec (`TestParseCueRejectsNonCueInput`, property test, regression on existing fixtures, `TestQuietReporterEmitsFailures`, `TestQuietReporterSilencesProgress`) → all present in Tasks 1–2.
- Issue 3 (pathological runtime) → covered by Task 1 step 5 (sanity check confirms <1s on a 4 GB .iso).

**Placeholder scan:** None. All steps include the actual code, exact commands, expected output.

**Type consistency:** `errNotACuesheet`, `cueSniffBytes`, `cueKeywords`, `containsAnyKeyword` — defined in Task 1, not referenced elsewhere. `quietReporter{w: w}`, `quietStep{w, label}` — internal to `reporter.go`, called only from `NewReporter` and the embedded `Step` method, both updated in Task 2.

**Out of scope (per spec):** No broader cue parser changes, no DVD-specific detection, no `Warn` change in quiet mode. Plan respects this.
