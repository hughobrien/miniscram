# `miniscram --progress=json` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `--progress=json` flag to `pack`/`unpack`/`verify` that emits one NDJSON event per Reporter call on stderr, replacing the human text reporter when set.

**Architecture:** A new `jsonReporter` next to `textReporter` and `quietReporter` in `reporter.go`, exposed via an additive `NewJSONReporter(w io.Writer) Reporter` constructor. The existing `NewReporter(w, quiet bool)` signature is unchanged so the eight test call sites don't churn. CLI plumbing lives in `parseSubcommand` (common flag) + each `runFoo`'s reporter dispatch.

**Tech Stack:** Go (single `main` package), `encoding/json` (stdlib), `flag` (stdlib), existing `Reporter`/`StepHandle` interfaces.

**Spec:** [`docs/superpowers/specs/2026-05-04-progress-json-design.md`](../specs/2026-05-04-progress-json-design.md)

---

## File Structure

No new files. Edits in five existing files:

- **Modify:** `reporter.go` — add `progressEvent` struct, `jsonReporter`, `jsonStep`, `NewJSONReporter`. ~50 lines added.
- **Modify:** `reporter_test.go` — add `TestJSONReporter`. ~55 lines.
- **Modify:** `main.go` — add `progress string` field to `commonFlags`, parse `--progress` in `parseSubcommand`, validate the value + the mutual-exclusivity with `--quiet`, switch reporter constructor in each of `runPack` / `runUnpack` / `runVerify`. ~25 lines.
- **Modify:** `help.go` — add the `--progress=json` line to each of `packHelpText`, `unpackHelpText`, `verifyHelpText`. 3 × 1 lines.
- **Modify:** `cli_test.go` — add an end-to-end test that runs `pack --progress=json` against the existing synthetic fixture and asserts the event sequence; add a small test for the `--progress=json --quiet` rejection. ~85 lines.

---

## Task 1: `jsonReporter` + unit test

**Goal:** A new Reporter implementation that emits NDJSON events to its writer, with full unit-test coverage. Lands without touching the CLI — no behavior change yet.

**Files:**
- Modify: `reporter.go`
- Modify: `reporter_test.go`

**Acceptance Criteria:**
- [ ] `progressEvent` struct exists with fields `Type`, `Label`, `Msg`, `Error` — all snake_case in JSON, all but `Type` are `omitempty`.
- [ ] `NewJSONReporter(w io.Writer) Reporter` exists and returns a working reporter.
- [ ] `TestJSONReporter` covers Step → Done (with msg), Step → Done (empty msg), Step → Fail, Info, Warn — asserts exact NDJSON byte output.
- [ ] `go vet ./...` clean. `go test ./...` passes (existing tests unaffected; new test green).

**Verify:** `go vet ./... && go test -race -count=1 -run TestJSONReporter -v ./...` → `--- PASS: TestJSONReporter`

**Steps:**

- [ ] **Step 1: Add the event struct + jsonReporter/jsonStep + constructor to `reporter.go`.**

Find the existing import block and add `encoding/json` (alphabetical order). Then append below the `quietReporter` block (after the `runStep` helper, before `isStderrTTY`) the new types and constructor:

```go
// progressEvent is the wire shape for --progress=json output. Field
// order in the struct = field order in emitted JSON (with omitempty
// collapsing absent fields).
type progressEvent struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Msg   string `json:"msg,omitempty"`
	Error string `json:"error,omitempty"`
}

// jsonReporter emits one NDJSON event per Reporter API call. Used
// when --progress=json is set; replaces text on stderr.
type jsonReporter struct {
	enc *json.Encoder
}

// NewJSONReporter returns a Reporter that emits one NDJSON event per
// call. Writes go to w; errors from w (e.g. broken pipe) are silently
// swallowed, matching the textReporter's behavior on closed stderr.
func NewJSONReporter(w io.Writer) Reporter {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // labels never contain HTML; cleaner output
	return &jsonReporter{enc: enc}
}

func (r *jsonReporter) Step(label string) StepHandle {
	_ = r.enc.Encode(progressEvent{Type: "step", Label: label})
	return &jsonStep{enc: r.enc, label: label}
}

func (r *jsonReporter) Info(format string, args ...any) {
	_ = r.enc.Encode(progressEvent{Type: "info", Msg: fmt.Sprintf(format, args...)})
}

func (r *jsonReporter) Warn(format string, args ...any) {
	_ = r.enc.Encode(progressEvent{Type: "warn", Msg: fmt.Sprintf(format, args...)})
}

type jsonStep struct {
	enc   *json.Encoder
	label string
	done  bool
}

func (s *jsonStep) Done(format string, args ...any) {
	if s.done {
		return
	}
	s.done = true
	_ = s.enc.Encode(progressEvent{Type: "done", Label: s.label, Msg: fmt.Sprintf(format, args...)})
}

func (s *jsonStep) Fail(err error) {
	if s.done {
		return
	}
	s.done = true
	_ = s.enc.Encode(progressEvent{Type: "fail", Label: s.label, Error: err.Error()})
}
```

The `json.Encoder` writes the JSON object plus a trailing newline per `Encode` call — that's NDJSON for free.

- [ ] **Step 2: Run `go vet`.**

Run: `go vet ./...`
Expected: clean (no output).

- [ ] **Step 3: Add `TestJSONReporter` to `reporter_test.go`.**

Append the test at the end of the file:

```go
func TestJSONReporter(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONReporter(&buf)

	s := r.Step("hashing scram")
	s.Done("c98323550138")

	s2 := r.Step("checking constant offset")
	s2.Done("") // empty msg — Msg field is omitempty so it disappears from output

	s3 := r.Step("layout sanity")
	s3.Fail(errors.New("layout mismatch ratio 0.07 exceeds 0.05"))

	r.Info("hello")
	r.Warn("careful")

	want := strings.Join([]string{
		`{"type":"step","label":"hashing scram"}`,
		`{"type":"done","label":"hashing scram","msg":"c98323550138"}`,
		`{"type":"step","label":"checking constant offset"}`,
		`{"type":"done","label":"checking constant offset"}`,
		`{"type":"step","label":"layout sanity"}`,
		`{"type":"fail","label":"layout sanity","error":"layout mismatch ratio 0.07 exceeds 0.05"}`,
		`{"type":"info","msg":"hello"}`,
		`{"type":"warn","msg":"careful"}`,
		``, // trailing newline from the last Encode
	}, "\n")
	if got := buf.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
```

If `bytes`, `errors`, `strings` aren't already imported in `reporter_test.go`, add them. Existing imports are likely just `bytes` + `testing`; add `errors` and `strings` to the import block.

- [ ] **Step 4: Run the new test in isolation, then the whole suite.**

Run: `go test -race -count=1 -run TestJSONReporter -v ./...`
Expected: `--- PASS: TestJSONReporter`.

Run: `go test -race -count=1 ./...`
Expected: existing tests still PASS, new one PASS.

If the encoder's exact output differs from the expected (e.g. unexpected escaping), inspect the diff and adjust either the encoder setup or the expected string. The struct field order is determined by Go's reflection, which respects declaration order — so `type → label → msg → error` is the contract.

- [ ] **Step 5: Commit.**

```bash
git add reporter.go reporter_test.go
git commit -m "reporter: add jsonReporter + NewJSONReporter

A new Reporter implementation that emits one NDJSON event per API
call (Step/Done/Fail/Info/Warn) to its writer. Field order in each
event is type → label → msg → error via struct ordering with
omitempty on the optionals.

NewJSONReporter is additive — the existing NewReporter(w, quiet)
signature stays unchanged so the eight test call sites don't churn.
The CLI flag wiring lands next."
```

---

## Task 2: Wire `--progress=json` through `parseSubcommand` + per-subcommand reporter dispatch

**Goal:** The CLI accepts `--progress=json` on `pack`/`unpack`/`verify`. When set, the subcommand uses `NewJSONReporter`. `--progress=json` together with `--quiet` is rejected with a usage error before the run starts.

**Files:**
- Modify: `main.go`
- Modify: `help.go`

**Acceptance Criteria:**
- [ ] `commonFlags` gains a `progress string` field.
- [ ] `parseSubcommand` registers a `--progress` string flag and validates it (only `""` or `"json"` accepted; rejects others with a usage error).
- [ ] `parseSubcommand` rejects `--progress=json --quiet` combinations with a clear usage error before the run starts.
- [ ] Each of `runPack`/`runUnpack`/`runVerify` selects the JSON reporter when `common.progress == "json"`.
- [ ] `pack`/`unpack`/`verify` help text lists `--progress=json`.
- [ ] `go vet ./...` clean. `go test ./...` passes (existing tests still green).

**Verify:** `go vet ./... && go test -race -count=1 ./...` → all PASS, plus a manual smoke `go run . pack --progress=invalid /tmp/no.cue 2>&1` exits with usage error.

**Steps:**

- [ ] **Step 1: Extend `commonFlags` in `main.go`.**

Find the struct around line 70 and add the `progress` field:

```go
// commonFlags is the set of flags every subcommand shares.
type commonFlags struct {
	quiet    bool
	progress string // "" (default text) or "json"
}
```

- [ ] **Step 2: Register and validate `--progress` in `parseSubcommand`.**

In `parseSubcommand` (around lines 85–115), add the flag registration after the existing quiet flags and add validation in the result-building section:

```go
quiet := fs.Bool("q", false, "quiet")
quietLong := fs.Bool("quiet", false, "quiet")
progress := fs.String("progress", "", "machine-readable progress format; only 'json' is accepted")
help := fs.Bool("h", false, "help")
helpLong := fs.Bool("help", false, "help")
```

Then replace the final return statement:

```go
return positional, commonFlags{quiet: *quiet || *quietLong}, 0, true
```

with:

```go
isQuiet := *quiet || *quietLong
if *progress != "" && *progress != "json" {
	fmt.Fprintf(stderr, "invalid --progress=%q (only 'json' is accepted)\n", *progress)
	fmt.Fprint(stderr, helpText)
	return nil, commonFlags{}, exitUsage, false
}
if *progress == "json" && isQuiet {
	fmt.Fprintln(stderr, "--progress=json and --quiet are mutually exclusive")
	fmt.Fprint(stderr, helpText)
	return nil, commonFlags{}, exitUsage, false
}
return positional, commonFlags{quiet: isQuiet, progress: *progress}, 0, true
```

- [ ] **Step 3: Switch reporter construction in `runPack`, `runUnpack`, `runVerify`.**

Find each `rep := NewReporter(stderr, common.quiet)` line (currently at main.go:157, :195, :213) and replace each with:

```go
var rep Reporter
switch common.progress {
case "json":
	rep = NewJSONReporter(stderr)
default:
	rep = NewReporter(stderr, common.quiet)
}
```

The three call sites are otherwise identical — copy/paste this block in each place.

- [ ] **Step 4: Add `--progress=json` to the help texts in `help.go`.**

Find each of `packHelpText`, `unpackHelpText`, `verifyHelpText` and add a line above the `--quiet` line:

```
    --progress=json    emit NDJSON progress events on stderr
                       (suppresses human text; for scripted consumers).
```

Match each existing block's column alignment.

- [ ] **Step 5: Sanity build + vet + test.**

Run: `go vet ./... && go build ./... && go test -race -count=1 ./...`
Expected: all clean / PASS.

- [ ] **Step 6: Manual smoke — invalid value, mutual-exclusivity, help text.**

```bash
go run . pack --progress=banana /tmp/no.cue 2>&1 | head -3
# expected: invalid --progress="banana" (only 'json' is accepted)

go run . pack --progress=json --quiet /tmp/no.cue 2>&1 | head -3
# expected: --progress=json and --quiet are mutually exclusive

go run . pack --help 2>&1 | grep -A1 progress
# expected: --progress=json line visible in help
```

(Each invocation will exit non-zero, but the stderr output is what matters.)

- [ ] **Step 7: Commit.**

```bash
git add main.go help.go
git commit -m "cli: wire --progress=json through pack/unpack/verify

Adds a string --progress flag to parseSubcommand. Only 'json' is
accepted today; anything else is a usage error. --progress=json
together with --quiet is also rejected before the run starts (the
semantics of 'JSON-quiet' would be murky — pick one).

Each of runPack/runUnpack/runVerify now switches its reporter
constructor on common.progress: NewJSONReporter when 'json', the
existing NewReporter(stderr, quiet) otherwise. The text path is
unchanged — human users never set the flag."
```

---

## Task 3: End-to-end CLI tests

**Goal:** A regression-proof test that running `pack --progress=json` against the existing synthetic fixture emits the expected NDJSON event sequence on stderr. A second tiny test asserts the `--progress=json --quiet` rejection.

**Files:**
- Modify: `cli_test.go`

**Acceptance Criteria:**
- [ ] A new test `TestCLI_PackProgressJSON` runs the pack subcommand against the existing synthetic fixture (same setup as the existing tests at `cli_test.go:148` and `cli_test.go:182`), captures stderr, parses each non-empty line as a `progressEvent`, and asserts that the expected step labels appear in the documented order, with one `done` event per `step` event.
- [ ] A new test `TestCLI_ProgressJSONQuietConflict` asserts that running `pack --progress=json --quiet` exits with `exitUsage` and stderr contains `mutually exclusive`.
- [ ] `go vet ./...` clean. `go test ./...` passes.

**Verify:** `go test -race -count=1 -run 'TestCLI_PackProgressJSON|TestCLI_ProgressJSONQuietConflict' -v ./...` → both PASS.

**Steps:**

- [ ] **Step 1: Append the two new tests to the end of `cli_test.go`.**

The existing tests use these helpers (visible at `cli_test.go:140-170`):

- `synthDisc(t, SynthOpts{MainSectors: 50, LeadoutSectors: 5})` — produces a disc spec.
- `writeFixture(t, dir, disc)` — writes the bin/scram/cue triple, returns `(binPath, scramPath, cuePath)`.
- `run(args []string, stdout io.Writer, stderr io.Writer) int` — top-level CLI dispatcher used by all CLI tests.

Mirror that pattern exactly:

```go
func TestCLIPackProgressJSON(t *testing.T) {
	dir := t.TempDir()
	disc := synthDisc(t, SynthOpts{MainSectors: 50, LeadoutSectors: 5})
	_, _, cuePath := writeFixture(t, dir, disc)

	var stderr bytes.Buffer
	if code := run([]string{"pack", "--progress=json", cuePath}, io.Discard, &stderr); code != exitOK {
		t.Fatalf("pack exit %d; stderr:\n%s", code, stderr.String())
	}

	// Parse stderr line-by-line as progressEvent. Skip blank lines.
	var events []progressEvent
	for _, line := range strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev progressEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("non-JSON stderr line %q: %v\nfull stderr:\n%s", line, err, stderr.String())
		}
		events = append(events, ev)
	}

	// Each step must have a matching done; no fails on a happy-path run.
	openSteps := map[string]bool{}
	for _, ev := range events {
		switch ev.Type {
		case "step":
			openSteps[ev.Label] = true
		case "done":
			if !openSteps[ev.Label] {
				t.Errorf("done with no matching step: %q", ev.Label)
			}
			delete(openSteps, ev.Label)
		case "fail":
			t.Errorf("unexpected fail on happy path: %+v", ev)
		case "info", "warn":
			// fine; no assertions on contents
		default:
			t.Errorf("unknown event type %q in %+v", ev.Type, ev)
		}
	}
	if len(openSteps) != 0 {
		t.Errorf("steps opened but not closed: %v", openSteps)
	}

	// Sanity: at minimum the expected pack pipeline emits these labels.
	// Match by substring because labels include filenames (e.g. "resolving cue /tmp/.../foo.cue").
	requiredSubstrings := []string{
		"hashing scram",
		"building scram prediction + delta",
		"writing container",
	}
	for _, want := range requiredSubstrings {
		found := false
		for _, ev := range events {
			if ev.Type == "step" && strings.Contains(ev.Label, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing required step matching %q in event stream:\n%s", want, stderr.String())
		}
	}
}

func TestCLIProgressJSONQuietConflict(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"pack", "--progress=json", "--quiet", "/tmp/does-not-exist.cue"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Errorf("exit = %d, want exitUsage (%d)", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing 'mutually exclusive'; got:\n%s", stderr.String())
	}
}
```

If `encoding/json` and `strings` aren't already imported in `cli_test.go`, add them. `bytes` and `io` are likely already there (other tests use them).

- [ ] **Step 2: Run the new tests.**

Run: `go test -race -count=1 -run 'TestCLI_PackProgressJSON|TestCLI_ProgressJSONQuietConflict' -v ./...`
Expected: both `PASS`.

- [ ] **Step 3: Run the full suite.**

Run: `go test -race -count=1 ./...`
Expected: all PASS.

- [ ] **Step 4: Commit.**

```bash
git add cli_test.go
git commit -m "cli_test: end-to-end coverage for --progress=json

TestCLI_PackProgressJSON runs pack --progress=json against the
synthetic fixture and verifies that the stderr stream is NDJSON,
every step has a matching done, no fails on the happy path, and the
expected core-step labels appear in the event stream.

TestCLI_ProgressJSONQuietConflict pins the usage error for the
mutually-exclusive flag combo."
```

---

## Out-of-scope (per the spec)

- **Per-step progress events** with `current`/`total` numbers — defer until step-boundary events demonstrably aren't enough.
- **1 Hz heartbeat ticks** during long steps — same.
- **`miniscram-gui` switchover** to consume NDJSON instead of stderr line-tail — small mechanical change in `tools/miniscram-gui/runner.go`'s `readStderr`. Lands in a separate PR after this one merges.
- **Schema versioning** — no `version` field. If v2 ever lands, add it then.
