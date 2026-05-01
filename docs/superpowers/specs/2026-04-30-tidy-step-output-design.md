# Tidy step output

Date: 2026-04-30

## Motivation

`miniscram pack` and `miniscram unpack` print step-by-step progress via
the `Reporter` in `reporter.go`. Two patterns make the output noisier
than it needs to be:

1. **Redundant scramble-table self-test step.**
   `CheckScrambleTable()` is already invoked from `init()` at
   `ecma130.go:97` — the binary panics before `main()` runs if the LFSR
   builder drifts away from the pinned SHA-256. The
   `runStep("running scramble-table self-test", …)` blocks at
   `pack.go:55-62` and `unpack.go:33-40` re-run the same check at the
   top of every Pack/Unpack and narrate it. The init-time panic is the
   real assertion; the runtime narration adds a line that contains no
   information beyond "yes, the integrity guard already ran."

2. **`Done("ok")` filler.**
   Several steps call `st.Done("ok")` because the body has nothing
   useful to narrate. `textStep.Done` formats `" ... %s %s\n"` (mark +
   message), so the literal output is `... OK ok` — the status mark
   already conveys success and the trailing `ok` is duplication. Three
   sites today: `pack.go:101` (constant-offset check), `unpack.go:125`
   (build scram prediction), `unpack.go:213` (read manifest in Verify).

The combined effect on the README's FL_v1 walkthrough: 15 lines of
pack output where 13 carry information, 6 lines of unpack output
where 5 carry information.

## Design

Three changes, in order of mechanical → narrative.

### Change 1 — drop the scramble-test `runStep` blocks

Delete `pack.go:55-62` and `unpack.go:33-40`. `CheckScrambleTable()`
remains exported and continues to be called from `ecma130.go`'s
`init()`; it is also exercised directly by `ecma130_test.go`.

The integrity guard still fires — earlier and more loudly — at process
startup. No code path that reaches Pack/Unpack can have skipped it.

### Change 2 — empty-message rendering in `textStep`

`reporter.go:66` currently does:

```go
fmt.Fprintf(s.r.w, " ... %s %s\n", mark, fmt.Sprintf(format, args...))
```

When the formatted message is empty this produces `" ... OK \n"` with a
trailing space before the newline. Change to: if the formatted message
is empty, render `" ... OK\n"`; otherwise unchanged. Same treatment in
`Fail` for symmetry (Fail callers always pass a non-nil error today,
but the format should not depend on that).

After this change, `st.Done("")` is the idiomatic "step succeeded,
nothing to add" form, distinct from `st.Done("ok")` which adds a
literal `ok` to the line.

### Change 3 — replace remaining `Done("ok")` filler

| Site | Step label | New `Done` |
|---|---|---|
| `pack.go:101` | `checking constant offset` | `st.Done("")` |
| `unpack.go:125` | `building scram prediction` | `st.Done("%d sector(s)", scramSize/SectorSize)` |
| `unpack.go:213` | `reading manifest` | `st.Done("%d track(s), %d byte scram", len(m.Tracks), m.Scram.Size)` |

The constant-offset check is a boolean — failure is informative,
success has nothing to add, so `Done("")` (now rendering as bare `OK`)
is the right shape. The other two have natural narration available
without threading new state out of helpers.

The container format version (currently `0x02`, declared as
`containerVersion` in `manifest.go`) is checked-and-discarded inside
`ReadContainer` and not surfaced into the `Manifest` struct. Threading
it out for the manifest-read narration is out of scope; the chosen
narration uses fields already in hand.

## Output preview

`miniscram pack FL_v1.cue` after the change:

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
building scram prediction ... OK 355585 sector(s)
applying delta ... OK 7084781 byte(s) of delta applied
verifying scram hashes ... OK all three match
```

`miniscram unpack FL_v1.miniscram` after:

```
reading container FL_v1.miniscram ... OK delta 7084781 bytes
verifying bin hashes ... OK all tracks match
building scram prediction ... OK 355585 sector(s)
applying delta ... OK 7084781 byte(s) of delta applied
verifying output hashes ... OK all three match
```

## Tests

### `reporter_test.go`

- **`TestStepDoneEmptyMessage`** — calls `r.Step("foo").Done("")` on a
  text reporter, asserts the buffer contains `... OK\n` with no
  trailing space before the newline. Mirror case for `Done("bar")`
  asserting `... OK bar\n` (regression guard for the existing format).

Existing tests in `reporter_test.go` use substring assertions and
remain valid. Pack/unpack tests use the quiet reporter or substring
matches and are unaffected by the narration changes.

### Manual walkthrough

Re-run the README's FL_v1 demonstration (or any `test-discs/<name>/`
fixture under `-tags redump_data`) and confirm the output matches the
preview above.

## README

The fenced output blocks at lines 42–56 and 86–91 of `README.md`
include the current strings verbatim. Update them to match the new
output. Other prose in the README does not reference the removed lines.

## Out of scope

- Threading the container format version out of `ReadContainer` to
  narrate `v2, …` on the manifest-read step. Worth doing if a future
  format bump (v3) lands; not motivated today.
- Wider Reporter API changes (separate progress vs result channels,
  structured output mode, `--verbose`). The current line-per-step
  model is fine.
- Folding `checking constant offset` into the preceding
  `detecting write offset` step. The two checks are conceptually
  distinct and the failure messages reference different code paths;
  keep them as separate steps.
- Touching `verifying output hashes` / `verifying scram hashes` /
  `verifying bin hashes` Done lines (`all three match`,
  `all tracks match`). These already carry information.

## Risk

- **Change 1:** if a future caller of `CheckScrambleTable` exists
  outside `init()` and the test, the deletions still leave the symbol
  in place. The narration disappears but the function does not.
  Confirmed by grep at design time: no callers besides `init()` and
  `ecma130_test.go`.
- **Change 2:** the format tweak is a one-line conditional. The
  existing `reporter_test.go` substring assertions don't pin the
  trailing space, so no breakage there. The new test pins the new
  shape so a future regression would be caught.
- **Change 3:** narration string changes are visible in any test that
  greps for the literal `OK ok` (none exist) or for the previous Done
  text. Confirmed by grep at design time.
