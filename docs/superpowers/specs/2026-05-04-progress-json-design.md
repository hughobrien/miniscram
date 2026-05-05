# `miniscram --progress=json`: structured step events on stderr

Date: 2026-05-04
Status: design
Issue: [#23](https://github.com/hughobrien/miniscram/issues/23)
Affects: root `miniscram` module (`reporter.go`, `pack.go`, `unpack.go`,
`verify.go`, `main.go`, tests). Does not change the GUI module in this
spec — GUI consumption is a small follow-up.

## Motivation

The miniscram-gui prototype's running strip currently parses the CLI's
stderr line-by-line as plain text (`hashing scram ... OK …`). It works
for the existing fixtures but is fragile: any reformatting of the CLI's
human output ripples into the GUI's parser. A structured, line-delimited
JSON output gives the GUI (and any future scripted consumer) a stable
machine-readable contract while leaving the human format untouched.

The miniscram-gui design spec
(`2026-05-04-miniscram-gui-unpack-design.md`) explicitly named this as
a clean follow-up if line-tail proved jittery. It's being landed early
so the structured contract exists before the GUI rewrite needs it,
rather than after.

## Non-goals

- **Per-step progress** (current/total bytes within a long step). The
  existing `Reporter`/`StepHandle` interface has no such notion;
  threading one in would touch hashers and delta application across
  three files. Defer until step-boundary events demonstrably aren't
  enough.
- **1 Hz heartbeat ticks.** Same reasoning — emit only on real Reporter
  API calls in this round.
- **GUI rewrite to consume JSON events.** The GUI's stderr line-tail
  works today; switching it over is a separate small follow-up that
  depends on this spec landing first.
- **Schema versioning.** No `version` field. If a v2 ever lands, the
  field is added then; readers that don't see it assume v1.

## Architecture

A new `jsonReporter` lives alongside `textReporter` and `quietReporter`
in `reporter.go`. Same `Reporter` and `StepHandle` interfaces — no
changes to call sites in `pack.go`/`unpack.go`/`verify.go`.

A new constructor `NewJSONReporter(w io.Writer) Reporter` is added next
to the existing `NewReporter(w io.Writer, quiet bool) Reporter`. The
existing constructor's signature is unchanged so the eight call sites
across the test files don't churn.

The CLI exposes `--progress=json` on the `pack`, `unpack`, and `verify`
subcommands. When set, stderr becomes NDJSON only — the human text
reporter is replaced, not teed. Human users don't set this flag; the
GUI does.

Top-level errors that fire before a `Reporter` is constructed (e.g.,
"cue file not found") remain plain text on stderr. Consumers should
treat any non-JSON stderr line as an opaque error string and rely on
the process's non-zero exit code as authoritative.

## Event format

Newline-delimited JSON (NDJSON) on stderr. One event per line. Field
names snake_case to match `inspect --json`'s existing convention.

| Reporter API call             | JSON event                                                    |
|-------------------------------|---------------------------------------------------------------|
| `Step("hashing scram")`       | `{"type":"step","label":"hashing scram"}`                     |
| `StepHandle.Done("12ab")`     | `{"type":"done","label":"hashing scram","msg":"12ab"}`        |
| `StepHandle.Fail(err)`        | `{"type":"fail","label":"hashing scram","error":"<err>"}`     |
| `Reporter.Info("…")`          | `{"type":"info","msg":"…"}`                                   |
| `Reporter.Warn("…")`          | `{"type":"warn","msg":"…"}`                                   |

The `label` field is repeated on `done` and `fail` so a consumer can
pair events without keeping `step → in_flight` state. The emitter
side is trivial — `jsonStep` stores its label on construction.

`msg` is omitted on `done` events when the original `Done` call passed
an empty format string (rare in practice; matches the existing text
output's "no trailing message" rendering). `error` is the result of
`err.Error()`.

Concrete trace (FL_v1 pack, abbreviated to first three steps):

```
{"type":"step","label":"resolving cue FL_v1.cue"}
{"type":"done","label":"resolving cue FL_v1.cue","msg":"1 track(s), 729914976 bytes total"}
{"type":"step","label":"detecting write offset"}
{"type":"done","label":"detecting write offset","msg":"-48 bytes"}
{"type":"step","label":"checking constant offset"}
{"type":"done","label":"checking constant offset"}
```

(Note the empty `msg` is omitted from the third `done` because the
existing `pack.go` calls `Done("")` for that step.)

## Components

### `reporter.go` — `jsonReporter` + `NewJSONReporter`

The event payload is a single struct with `omitempty` on the optional
fields, so field order in the emitted JSON is stable (type, label,
msg, error) and absent fields don't appear. Map-based encoding sorts
alphabetically and would put `error` first on fail events — readable
but feels backwards.

```go
type progressEvent struct {
    Type  string `json:"type"`
    Label string `json:"label,omitempty"`
    Msg   string `json:"msg,omitempty"`
    Error string `json:"error,omitempty"`
}

type jsonReporter struct {
    enc *json.Encoder
}

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

The encoder writes one JSON object per `Encode` call followed by a
newline — the NDJSON shape is free.

### `main.go` — flag + reporter selection

Each of `runPack`, `runUnpack`, `runVerify` already has a `--quiet`
flag. Add `--progress`:

```go
progress := fs.String("progress", "", "machine-readable progress format; only 'json' is accepted")
```

After flag parsing, before constructing the Reporter:

```go
if *progress != "" && *progress != "json" {
    return fmt.Errorf("invalid --progress=%q (only 'json' is accepted)", *progress)
}
if *progress == "json" && *quiet {
    return fmt.Errorf("--progress=json and --quiet are mutually exclusive")
}

var rep Reporter
switch {
case *progress == "json":
    rep = NewJSONReporter(os.Stderr)
case *quiet:
    rep = NewReporter(os.Stderr, true)
default:
    rep = NewReporter(os.Stderr, false)
}
```

The exact dispatch shape will match each subcommand's existing
post-flag-parse layout.

### `help.go` — flag listing

Each of pack/unpack/verify's help text gets one line:

```
    --progress=json  emit NDJSON progress events on stderr (suppresses
                     human text); useful for scripted consumers
```

## Error handling

| Scenario                                           | Behavior                                                         |
|----------------------------------------------------|------------------------------------------------------------------|
| `--progress=text` or other unknown value          | Usage error before run; non-zero exit.                           |
| `--progress=json --quiet`                          | Usage error before run; non-zero exit.                           |
| Top-level error before any Reporter exists        | Plain text on stderr; non-zero exit. Consumer relies on exit code. |
| Step.Fail mid-run                                  | `{"type":"fail",…}` event; process returns the error to main; non-zero exit. |
| Encoder write error (broken pipe)                  | Silently swallowed (the existing text reporter also can't reach a closed stderr). The subprocess will hit a write error on its next emit and either keep going or get killed by SIGPIPE. |

## Testing

### Unit tests in `reporter_test.go`

A new `TestJSONReporter` exercises every Reporter API method against a
`bytes.Buffer` and asserts the exact NDJSON byte sequence:

```go
func TestJSONReporter(t *testing.T) {
    var buf bytes.Buffer
    r := NewJSONReporter(&buf)

    s := r.Step("hashing scram")
    s.Done("c98323550138")

    s2 := r.Step("checking constant offset")
    s2.Done("") // empty msg — omitted from output

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
        ``,
    }, "\n")
    if got := buf.String(); got != want {
        t.Errorf("got:\n%s\nwant:\n%s", got, want)
    }
}
```

Field order in each event line is `type → label → msg → error` —
struct-declared order, with `omitempty` collapsing absent fields.

### One end-to-end test in `cli_test.go`

Run `pack --progress=json` against the existing synthetic fixture
(see `cli_test.go:148` and `cli_test.go:182` for the pattern), capture
stderr, parse line-by-line as JSON, assert the expected event
sequence:

- First event is `{"type":"step","label":"resolving cue …"}`
- Some middle event is `{"type":"step","label":"hashing scram"}`
- Last successful event before exit is `{"type":"done","label":"verifying scram hashes",…}` (or whichever step pack ends on)

The exact assertion is "the steps appear in the expected order" rather
than "exact byte match" — message details can shift over time without
breaking the contract.

### Coverage of `--quiet` interaction

A short `cli_test.go` test that runs the CLI with `--progress=json
--quiet` and asserts non-zero exit + a usage error containing
"mutually exclusive".

## Out of scope (follow-up specs)

- **Per-step progress events** with `current`/`total` numbers.
- **1 Hz heartbeat ticks** during long steps.
- **`miniscram-gui` switchover** to consume NDJSON instead of stderr
  line-tail. Small, mechanical change in
  `tools/miniscram-gui/runner.go`'s `readStderr`. Lands in a separate
  PR after this one merges.
