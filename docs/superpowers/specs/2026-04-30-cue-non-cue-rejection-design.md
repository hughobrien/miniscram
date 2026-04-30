# Polite rejection of non-cue input

Date: 2026-04-30

## Motivation

A sweep of `miniscram pack` over the user's redumper corpus (~26 DVD
`.iso` files, no `.cue` alongside) surfaced three usability issues
when the tool is fed non-cue input:

1. **Opaque error message.** The cue parser uses `bufio.Scanner`
   with the default 64 KiB token limit. A binary file with no
   newlines blows the limit and surfaces the Go stdlib internal
   error verbatim:

   ```
   resolving cue /roms/.../Foo.iso ... FAIL bufio.Scanner: token too long
   ```

   The user has no way to know this means "your input doesn't look
   like a cuesheet."

2. **Pathological runtime.** When the input is huge (a 4–8 GB DVD
   `.iso`), the scanner streams the entire file before failing, and
   on slow storage (e.g. cold SMB cache) the call exceeds 90 s. One
   case in the sweep (`/roms/the-witcher/TheWitcher.iso`) timed out.

3. **`--quiet` swallows error messages.** With `--quiet`, the
   reporter is a no-op, so a failed `Pack` exits non-zero with an
   empty stderr. The user sees only an exit code (4) with no hint as
   to why.

This spec covers two minimal fixes — one in `cue.go`, one in
`reporter.go` — that resolve all three issues.

## Design

### Fix A — `cue.go::ParseCue` head sniff

Add a head-sniff before the existing scanner loop. Read up to the
first 4 KiB into a buffer. Reject with a sentinel error if the
buffer contains none of the cue keywords this parser already
recognizes:

```
FILE  TRACK  INDEX  REM  PERFORMER  TITLE  CATALOG  PREGAP
```

These are exactly the tokens the existing `switch fields[0]` arms
handle (or explicitly ignore in the `default` case). The sniff
agrees with the parser's notion of what a cuesheet contains.

After the sniff passes (or is non-applicable), resume scanning from
`io.MultiReader(bytes.NewReader(buf), r)` so a valid cue parses
exactly as before. The buffer is held in memory for the lifetime of
the parse — bounded at 4 KiB.

**Why 4 KiB:** redumper cuesheets are tens to hundreds of bytes.
A real cue's first non-comment line is `FILE "..." BINARY` within
the first ~100 bytes. 4 KiB is generous; even a heavily-`REM`-
prefixed cue clears the threshold without effort.

**Error wording (sentinel):**

```
%q does not look like a cuesheet (no FILE/TRACK/REM/... in first 4 KiB)
```

The path is the *cue path* passed by `ResolveCue`'s caller.
`ParseCue` itself takes a reader, not a path, so the path is added
by `ResolveCue` after `ParseCue` returns.

This fixes Issue 1 (message quality) and Issue 3 (runtime — the
sniff reads at most 4 KiB and bails immediately, never engaging the
scanner on hostile input).

### Fix B — `reporter.go::quietReporter` failures still print

Make `quietReporter` stateful with an `io.Writer`. `quietStep.Fail`
prints `<step-label>: <err>\n` to that writer. `Step`, `Done`,
`Info`, `Warn` stay silent.

Plumbing: `NewReporter(w, quiet)` already takes the writer. In the
quiet branch, return `quietReporter{w: w}` instead of the bare
`quietReporter{}`. `quietReporter.Step(label)` constructs a
`quietStep{w: r.w, label: label}`.

`Warn` deliberately stays silent — warnings are advisory and the
user opted into quiet. Failures are different: exit 4 with empty
stderr is bad UX regardless of the user's verbosity preference.

This fixes Issue 2.

## Tests

### `cue_test.go`

- **`TestParseCueRejectsNonCueInput`** — feeds 8 KiB of `0xFF`
  through `ParseCue`, asserts the error string contains
  `does not look like a cuesheet`.
- **`TestParseCueRejectsRandomBinary` (property)** —
  `testing/quick` generator emits random byte strings of bounded
  length (≥ 4 KiB) containing no newline and no cue keyword;
  asserts `ParseCue` returns the sentinel error and never panics.
- **`TestParseCueAcceptsExistingFixtures`** — regression guard.
  Re-runs the existing cuesheet fixtures (any already in the test
  suite) through `ParseCue`; asserts the head-sniff doesn't
  introduce false negatives.

### `reporter_test.go`

- **`TestQuietReporterEmitsFailures`** — captures a quiet reporter's
  writer to a `bytes.Buffer`, runs a step that fails, asserts the
  buffer contains both the step label and the error message. Then
  runs a successful step and asserts the buffer is unchanged.
- **`TestQuietReporterSilencesInfoAndWarn`** — same setup; calls
  `Info` and `Warn`; asserts the buffer remains empty.

## Out of scope

- Broader cue parser changes (tokenization, multi-FILE-per-TRACK
  support, charset detection).
- DVD-specific input detection. Once Fix A is in, any non-cue input
  gets the friendly error — `.iso` is not a special case.
- Surfacing `Warn` in `--quiet` mode. Separate decision; if it
  comes up, address it on its own merits.
- Reworking `errToExit` or the exit-code surface.

## Risk

- **Fix A:** the head sniff could in principle reject a contrived
  cuesheet that has no recognized keyword in the first 4 KiB. Such
  a cuesheet is not producible by redumper or any common tool.
  Mitigation: the regression test re-runs all existing fixtures.
- **Fix B:** quietReporter gaining a writer changes its zero value's
  semantics — a `quietReporter{}` literal would write to a nil
  writer. There are no callers that construct one directly (only
  `NewReporter` returns it); a search confirms this. Add a brief
  comment on the type discouraging direct construction.
