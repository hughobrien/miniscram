# CLAUDE.md — agent guide for miniscram

Pointers for AI agents (Claude Code, Codex, etc.) working in this repo.
Human-facing docs live in [`README.md`](./README.md); architectural
design docs in [`docs/superpowers/specs/`](./docs/superpowers/specs/).

## Repository layout

- `*.go` — single Go package `main`, ~5K lines. Notable files: `chunks.go` (v2 chunk-framing primitives + per-tag codecs), `manifest.go` (container WriteContainer/ReadContainer orchestration), `ecma130.go` (CD-ROM scrambler/EDC/ECC tables + `buildScrambleTable`).
- `docs/superpowers/specs/` — design specs (dated). Authoritative
  source of architectural intent.
- `TASKS.md` — maintainer's work plan; recommendations there reflect
  Hugh's judgment, not auto-generated suggestions.
- `test-discs/<name>/` — gitignored real-disc fixtures (multi-GB
  redumper dumps, see README's Demonstrations section).

## Spec references (gitignored)

The repo root contains two PDFs that are not committed:

- `ECMA-130_2nd_edition_june_1996.pdf` — CD-ROM physical/logical
  layer. Clauses cited throughout `ecma130.go`. Page-to-clause map:
  §14 → PDF p. 20–22, §15 → p. 22, Annex A → p. 31–35, Annex B → p. 37.
- `Compact representation of scrambled cdrom data.pdf` — Hauenstein
  (IJCSIT 2022), [doi:10.5121/ijcsit.2022.14401](https://doi.org/10.5121/ijcsit.2022.14401).
  Conceptual inspiration for the delta-against-prediction approach.
  miniscram's container format, override-record delta encoding, and
  write-offset handling are its own — the paper uses xdelta3 against
  DiscImageCreator output.

Both are freely available from their original publishers.

## Upstream we draw from

- [redumper](https://github.com/superg/redumper) — clone locally for
  cross-reference. License-compatible (both GPL-3.0). When lifting
  code from redumper, add an attribution comment at the lift point
  that names the upstream file.

Existing lifts:

- `buildScrambleTable` in `ecma130.go` is a Go port of redumper's
  `Scrambler::_TABLE` lambda from `cd/cd_scrambler.ixx`. Inline
  comments quoting ECMA-130 Annex B are reproduced verbatim.

## Tables pinned by SHA-256

Three generated tables are pinned and verified at process start. Any
builder drift trips the corresponding `init()` and the process panics:

- `expectedScrambleTableSHA256` — `ecma130.go`, scrambler section.
- `expectedEDCTableSHA256` — `ecma130.go`, EDC section.
- `expectedGFTablesSHA256` — `ecma130.go`, ECC section.

Don't change these constants without independently regenerating the
table from ECMA-130. The pin is a guard against silent table drift
when the builder code is edited.

## Critical thresholds and gates

- `layoutMismatchAbortRatio = 0.05` (`builder.go`) — pack aborts when
  more than 5% of disc sectors disagree with the bin-driven prediction.
  Measured against total disc sectors (leadin + pregap + bin + leadout).
- `writeOffsetLimit` in `validateSyncCandidate` (`pack.go`) — max ±2
  sectors (±4704 bytes) between bin and scram.
- `validModes` whitelist (`cue.go`) — only `MODE1/2352`,
  `MODE2/2352`, `AUDIO` accepted.
- `ParseCue` rejects multi-track-per-FILE cuesheets (Redumper
  convention is one TRACK per FILE).

## Tests

- `go test ./...` — fast tests with synthetic fixtures.
- `go test -tags redump_data ./...` — runs `e2e_redump_test.go` against
  real Redumper dumps stored in `test-discs/<name>/` (gitignored,
  e.g. `test-discs/half-life/`). Each fixture row asserts byte-exact
  round-trip plus per-fixture bounds (error count, delta size,
  container size).

### Property tests are first-class

When a function has a clean invariant — round-trip
(`f(g(x)) == x`), idempotence (`f(f(x)) == f(x)`), or agreement with
an oracle (`f(x) == reference(x)` for some authoritative
implementation) — prefer `testing/quick`-style randomized property
tests over (or alongside) example-based fixtures. They catch the
edge cases nobody thought of, especially for byte-format and
algorithmic code where the input space is enormous. See TASKS.md
Theme E for an open task to expand coverage.

## Workflow conventions

- **PR workflow.** `main` is protected on GitHub: require PR, require
  `build + test` CI, require linear history, no force-push, no branch
  deletion. Work on a feature branch, push, open a PR. PRs may be left
  open until a release window.
- **Stage files explicitly** — `git add path/to/file.go`. Avoid
  `git add -A` because it sweeps multi-GB fixture data (e.g.,
  `test-discs/half-life/`) into the index.
- `/tmp` is RAM-backed (2 GB tmpfs). Pack/verify artifacts can hit
  ~1.3 GiB peak for half-life — write them next to the cuesheet, not
  in `/tmp`.
- Commit message style: `type: terse subject`, body explains *why*
  when non-obvious. Recent log shows the convention.

## Tooling

This repo is developed with the **superpowers-extended-cc** plugin for
Claude Code, which provides skills for brainstorming, plan-writing,
plan-execution, code review, and TDD-aware implementation. The
`docs/superpowers/specs/` directory is produced by that workflow.

When in this repo, prefer those skills for non-trivial work:
brainstorming → write-plan → execute-plan → requesting-code-review.
For one-off fixes you can skip straight to implementation.

## Don't commit

The following must stay out of git (already gitignored):

- `test-discs/<name>/` fixture directories (multi-GB).
- `miniscram` (build artifact).
- `*.pdf` (the spec references — freely available upstream).
