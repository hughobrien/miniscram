# Allow `..` substrings in cue FILE names

Date: 2026-04-30

## Motivation

A sweep of `miniscram pack` over the user's redumper corpus failed
on `/roms/fear/F2X37H~D/F.E.A.R..cue`:

```
resolving cue ... FAIL FILE references with paths not supported: "F.E.A.R..bin"
```

The cuesheet's `FILE` line is well-formed — `F.E.A.R..bin` is the
literal filename redumper produced (the title ends in `.`, the
extension begins with `.`). The miniscram cue parser rejects it
because the path-safety check looks for the substring `..`
anywhere in the name.

## Design

Replace the substring check with explicit equality against the two
special path components:

**Before** (`cue.go:86`):
```go
if strings.ContainsAny(rawName, `/\`) || strings.Contains(rawName, "..") {
    return nil, fmt.Errorf("FILE references with paths not supported: %q", rawName)
}
```

**After:**
```go
if strings.ContainsAny(rawName, `/\`) || rawName == "." || rawName == ".." {
    return nil, fmt.Errorf("FILE references with paths not supported: %q", rawName)
}
```

### Why this is sufficient

Once `/` and `\` are rejected, the entire `rawName` is a single path
component. A path component refers to a directory (and so traverses
the filesystem in a way miniscram doesn't support) only when it
equals exactly `.` (current dir) or `..` (parent dir). Substrings
of those values inside a longer name don't traverse — `F.E.A.R..bin`
is a literal filename, not a path expression.

### Tests

`cue_test.go::TestParseCueAccepts` — new sub-test:
- `dotdot-in-name`: cuesheet with `FILE "F.E.A.R..bin" BINARY` plus
  `TRACK 01 MODE1/2352` and `INDEX 01 00:00:00`. Assert one track
  parsed with `Filename == "F.E.A.R..bin"`.

`cue_test.go::TestParseCueRejects` — two new sub-tests:
- `dot-name`: `FILE "." BINARY ...` → rejected
- `dotdot-name`: `FILE ".." BINARY ...` → rejected

Existing rejects (`../bad.bin`, `subdir/x.bin`) continue to be
caught by the unchanged `/\` ContainsAny check; their fixtures stay
as-is and serve as the regression guard.

## Out of scope

- `filepath.Clean` / normalization
- Charset/locale checks
- NUL byte rejection
- Windows reserved names (CON, NUL, etc.)

None of those are involved in this failure. Scope is exactly one
substring check → one equality check, plus three new test entries.

## Risk

- **Behavior change for `.` and `..` literal filenames.** The old
  check would have caught `..` (substring matches itself). The new
  check still rejects it explicitly. `.` was previously accepted
  (no `..` substring, no path separator); the new check rejects it.
  No real-world cue is expected to use `.` as a filename, so this
  tightens an edge case rather than regressing real behaviour.
- **Regression guard.** All existing `TestParseCueAccepts` and
  `TestParseCueRejects` sub-tests must still pass. The `..bad.bin`
  reject test exercises the `/\` check (it contains `/`), so it's
  unaffected.
