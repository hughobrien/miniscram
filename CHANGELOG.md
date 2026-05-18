# Changelog

All notable changes to miniscram are recorded here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.4.1] - 2026-05-17

### Fixed

- **GUI queue-row error text wraps instead of truncating** (#54). The
  `reasonLabel` helper replaced `MaxLines=1` with `WrapPolicy=WrapGraphemes`
  so messages like "cue contains only AUDIO tracks; nothing for miniscram
  to scramble-pack" wrap at character boundaries instead of being cut off
  with ellipsis.
- **GUI "Delete N unused .scrams" button now reliably appears** after
  audio-only pack failures (#54). The `unused-scram` NDJSON event is now
  dispatched directly from the `readStderr` goroutine via an
  `onUnusedScram` callback, bypassing a Gio frame-coalescing race that
  previously dropped the event when a `fail` event arrived immediately
  after and overwrote `state.LastLine`.
- **Delete button label now reads "(audio)"** to clarify that these are
  audio-track scrams safe to remove.

## [1.4.0] - 2026-05-16

### Added

- **`--remove-unused-scram` flag on `miniscram pack`.** Opt-in (default
  keep). When set, removes the source `.scram` after Pack rejects an
  audio-only cue. Idempotent — ENOENT is silently swallowed. Inverse
  default vs. `--keep-source`: the success path is recoverable from
  the container, the audio-only reject is not.
- **GUI batch-cleanup button.** A bottom-of-queue red "Delete N unused
  .scrams (X MiB)" button appears whenever the queue has accumulated
  one or more audio-only rejects, draining them all on one click.
  Dismiss with `×` to leave the files on disk. Accumulator persists
  across queue runs in the session; deduped by path.
- **`Reporter.UnusedScram(path, size)` event.** Emitted by `Pack`
  before the audio-only fail, carrying the source `.scram` path and
  byte size. Text reporter prints a one-line `--remove-unused-scram`
  hint; JSON reporter emits `{"type":"unused-scram","path":...,"size":...}`.

### Fixed

- **Audio-only cuesheets fail fast** (#50). Pack now rejects audio-only
  cues at the top with `ErrAudioOnlyDisc` instead of letting
  `detectWriteOffset` scan the entire `.scram` (~750 MB+ for a typical
  audio CD) before failing with a cryptic "no plausible scrambled
  sync field found" message. Exits in under a second with a clear
  message.
- **GUI no longer renders raw NDJSON** in the failure toast and queue
  sidebar (#50). The runner's `actionResult.Error` now passes through
  `prettyProgressLine` so users see the human-readable error string
  instead of `{"type":"fail","label":"...","error":"..."}`. Sidebar
  failed-rows also cap at one line with ellipsis to keep the queue
  visually tidy when several rows fail.

## [1.3.0] - 2026-05-15

### Added

- **Enhanced CD (multi-session) pack/unpack support.** Discs that
  combine an audio session 1 with a data session 2 (CD-Extra layout
  — Girls Aloud, Moving Shadow comps, most enhanced-CD music
  releases) now pack and round-trip byte-exact. `ParseCue`
  understands `REM SESSION NN`; Pack detects each inter-session gap
  from the scram by locking onto the next session's first scrambled
  sync, bound-checks it against `[11400, 30000]` sectors, and
  shifts every later track's `FirstLBA` accordingly. The builder
  routes emission through a new `regionAt` classifier covering all
  seven structural regions (leadin / pregap / bin / gap-leadout /
  gap-leadin / gap-pregap / leadout).
- **`SESS` critical chunk** carries per-track session numbers in
  the v0.2 container format. Only emitted when at least one
  session > 1, so single-session containers stay byte-identical to
  prior v0.2 output. Old readers fail cleanly on new multi-session
  containers with "unsupported critical chunk SESS"; old
  single-session containers still decode on new code.
- **Audio-leading pregap fix.** Track-1-AUDIO discs now emit silent
  audio (zeros) in the 150-sector pregap instead of scrambled
  Mode 1 zero — matches what Redumper actually dumps and avoids
  ~150 sectors of spurious overrides per audio-leading disc.

### Fixed

- **GUI surfaces the captured pack-failure reason** on failed queue
  rows instead of a bare "fail" tag (#44). The error string was
  already captured in `it.Reason`; the queue widget just wasn't
  rendering it.
- **`Unpack` surfaces missing-bin-file errors as a step.** The
  per-track stat loop now lives inside a `"resolving bin files"`
  reporter step, so a missing-or-moved bin file produces a clean
  failure line rather than a silent exit 3. Caught while validating
  the multi-session pack against a real CD-Extra dump.

## [1.2.6] - 2026-05-05

### Changed

- **Each platform ships as a single archive** containing both
  binaries pre-paired (`miniscram` next to `miniscram-gui`), plus
  `LICENSE` and `README.md`. `miniscram-vX.Y.Z-linux-amd64.tar.gz`
  for Linux, `.zip` for Windows. The GUI's `resolveMiniscram()`
  picks the CLI up from the sibling slot on first launch — one
  download, working install, no "miniscram CLI not found" banner.
  Loose per-binary downloads are no longer published.

## [1.2.5] - 2026-05-05

### Changed

- **Releases now ship miniscram-gui binaries alongside the CLI** for
  linux and windows on amd64. The release workflow gained a
  per-platform native-runner matrix for the GUI build (Gio + CGO
  rules out the cross-compile path the CLI uses). darwin/amd64 was
  dropped from the matrix when the macos-13 GUI runner hung on the
  first attempt; arm64 not yet wired up.
- **Only the latest release carries downloadable assets.** Older
  release tags + notes remain so changelog and issue links stay
  anchored, but binaries are auto-pruned by the release workflow on
  every new tag. The v1.0.0 - v1.2.3 sweep that prompted the new
  policy was done by hand; v1.2.4's CLI assets get pruned by this
  release.

## [1.2.4] - 2026-05-05

### Fixed

- **Queue worker left the right pane on the source cue after each
  pack.** The single-file flow already auto-loaded the result
  `.miniscram` (v1.2.3), but `queue.runWorker` did not — so a queue
  drain ended on the last item's stale "missing scram" cue view even
  though the pack had succeeded. Mirrored the single-file behaviour:
  on `success + autoFollow` the worker now calls `mdl.load(out)`
  after recording the result. For the last item this is the final
  view; mid-queue it briefly shows the result before the next
  iteration's `mdl.load(nextCue)` replaces it. Gated on autoFollow
  so a user who has manually loaded a different file mid-queue
  isn't yanked.

## [1.2.3] - 2026-05-05

### Fixed

- **Cue view stuck on "missing scram" right after a successful pack.**
  `model.handleActionResult` left the right pane on the source cue
  view after pack returned. Pack consumes the sibling `.scram` by
  default, so the cue view immediately re-rendered as
  `Missing .scram next to cue — pack can't run` even though the
  operation had just succeeded. After wiring up the success toast,
  the handler now calls `m.load(res.Output)` to flip the right pane
  onto the freshly written `.miniscram`, so the user lands on the
  inspect view of the result they just produced. The success toast
  survives the reload (`load()` does not touch `m.toast`). Queue
  worker path is unchanged: `queue.runWorker` already invokes
  `mdl.load(item.CuePath)` per item under autoFollow, and the
  single-file `handleActionResult` is only reached when no queue
  worker owns the done channel.

## [1.2.2] - 2026-05-04

### Fixed

- **GUI hard-crashed when opening a `.miniscram` file if the CLI
  wasn't on `PATH`.** `model.load` shelled out via bare
  `exec.Command("miniscram", ...)` instead of using the GUI's
  resolved CLI path (`m.cliBinary`, populated from
  `resolveMiniscram()` which finds `miniscram` next to or two dirs
  above the GUI binary). When the bare-name lookup failed, `m.kind`
  had already been set to `"miniscram"` while `m.meta` stayed nil,
  so `body()` dispatched to `miniscramView` and the first
  `mdl.meta.*` deref tripped a SIGSEGV. The two `inspect` call sites
  in `tools/miniscram-gui/main.go` now use `m.cliBinary`, and
  `m.kind` is stamped only after the JSON parse succeeds — so any
  future inspect/parse failure falls through to the existing
  error-surfacing `emptyView` instead of rendering a half-loaded
  state.

## [1.2.1] - 2026-05-01

### Fixed

- **Sector-count narration off-by-one.** The "building scram
  prediction" step printed `scram.Size / SectorSize`, a truncating
  divide that undercounted by 1 whenever the scram wasn't a whole
  multiple of 2352. Freelancer's 836,338,152-byte scram reported
  355585 but the loop iterates 355586 times. Switched to `TotalLBAs`
  — the same ceiling-division helper `BuildEpsilonHat` uses — so
  narration matches the work performed.
- **Reporter trailing space on empty messages.** `Done("")` and
  `Fail` with an empty error message rendered `... OK \n` /
  `... FAIL \n` with a stray space before the newline; both now
  render cleanly.

### Changed

- Dropped the redundant runtime "self-test scrambler table" step.
  The startup pin in `ecma130.go`'s `init()` already panics before
  `main()` if the builder drifts, so the runtime wrapper was never
  reachable in a healthy binary. Pack and unpack each emit one
  fewer narration line.

### Added

- `scripts/sweep` — SQLite-backed corpus harness that walks
  `*.cue`/`*.scram` pairs under a root, runs `pack --keep-source`
  (with default verify) per case, and records
  PASS/FAIL/CRASH/TIMEOUT in a durable database. Each invocation
  processes up to 10 cases, so a long sweep survives interruption.
  Lives in a nested Go module so its SQLite driver stays out of
  the binary's dependency graph. Used to validate the step-output
  cleanup against the 119-disc redumper corpus: 119/119 PASS.

## [1.2.0] - 2026-04-30

### Added

- **PlayStation (PSX) disc support.** miniscram now packs and verifies
  PSX dumps end-to-end: `MODE2/2352` data tracks combined with write
  offsets larger than a single sector (PSX masters routinely produce
  these). Demonstrated against all 8 PSX dumps in the redumper
  corpus (Final Fantasy VIII × 4, Final Fantasy IX × 4); a new
  README demo walks SLUS-00892 (FF VIII disc 1) through the pack +
  inspect + unpack workflow with byte-exact round-trip.
- `testing/quick` property test for `BuildEpsilonHat` across the
  full `[-2*SectorSize, +2*SectorSize]` write-offset range. Draws
  random offsets from 200 seeds and asserts no panic and
  `hat.Len() == ScramSize`. Complements the deterministic
  boundary-cliff table.

### Fixed

- **PSX panic on multi-sector write offsets.** `BuildEpsilonHat`
  panicked with `slice bounds out of range [skipFirst:2352]` when
  `|WriteOffsetBytes|` exceeded one sector. The per-sector loop's
  `skipFirst` handler now drains whole sectors via an early
  `continue` before applying the partial-sector slice. The `bin`
  reader's `io.ReadFull` runs earlier in the iteration body, so its
  position stays in lockstep with `lba` across the skip.
  ([#15](https://github.com/hughobrien/miniscram/pull/15))
- **Cue parser polite rejection of non-cue input.** Feeding
  `miniscram pack` an `.iso` (or any binary blob) now returns
  `does not look like a cuesheet (no FILE/TRACK/REM/... in first 4096 bytes)`
  via a 4 KiB head-sniff in `ParseCue`. Previously a stdlib
  `bufio.Scanner: token too long` error leaked through, and
  multi-GB hostile inputs streamed for 90 s+ before failing. Bounds
  runtime to a 4 KiB read.
  ([#13](https://github.com/hughobrien/miniscram/pull/13))
- **`--quiet` no longer swallows error messages.** `miniscram pack
  --quiet` on a failing input exited non-zero with empty stderr.
  The quiet reporter now still emits `<step-label>: <err>` on
  `Step.Fail` while keeping `Step.Done`, `Info`, and `Warn` silent.
  ([#13](https://github.com/hughobrien/miniscram/pull/13))
- **Cue FILE names containing `..` substrings.** Filenames like
  `F.E.A.R..bin` (legitimate redumper output where the title ends
  in `.` and the extension begins with `.`) were rejected as path
  traversal. The path-safety check now compares for exact equality
  against `.` and `..` rather than substring containment.
  ([#14](https://github.com/hughobrien/miniscram/pull/14))

### Tested

- Full redumper-corpus sweep over 119 CD dumps spanning Mode 1,
  Mode 2/PSX, mixed-mode hybrid (data + audio), and multi-FILE
  multi-disc games — **119/119 PASS** with byte-exact round-trip
  verification (~26 s per disc average over SMB).

## [1.1.1] - 2026-04-30

### Fixed

- Subcommand flag parsing rejected positional arguments followed by
  flags (e.g. `miniscram pack disc.cue -o out.miniscram --keep-source`)
  with "expected exactly one positional argument; got N". Go's
  `flag.Parse` stops at the first non-flag token; `parseSubcommand`
  now peels positionals off and re-parses until exhausted, so flags
  and positionals can appear in any order. Reported in
  [#11](https://github.com/hughobrien/miniscram/issues/11).

## [1.1.0] - 2026-04-30

### Added

- `--keep-source` flag on `miniscram pack` (preserve `.scram` after
  a verified round-trip; useful when iterating against a fixture).
- Classifier-gated prediction: bin sectors that redumper passed
  through unchanged (zeroed, invalid sync, valid sync + bad mode/MSF)
  are no longer re-scrambled by the predictor — saves a 2352-byte
  override per such sector. Pinned via 46 imported redumper test
  fixtures and a 1000-iteration property test against a Go-port
  oracle of `Scrambler::descramble`.
- Property tests for v2 codecs (`MFST` / `TRKS` / `HASH` round-trip),
  `Scramble` involution, BCD-MSF round-trip, and full
  `WriteContainer` / `ReadContainer` round-trip.
- 19-sub-test corruption-rejection battery covering every named v2
  read-time error path (bad magic, wrong version, mid-chunk
  truncation, CRC mismatch, length-cap exceeded, unknown critical /
  ancillary chunks, missing required, duplicate critical, MFST not
  first, hash-after-DLTA accepted).

### Changed

- **Container format v1 → v2 (wire break).** PNG/CHD-style chunks:
  5-byte file header (magic + version) followed by `MFST` / `TRKS` /
  `HASH` / `DLTA` chunks, each length-prefixed with a CRC-32/IEEE
  trailer over `(tag || payload)`. 16 MiB length sanity cap on
  non-`DLTA` chunks. PNG critical/ancillary case-bit reserved for
  forward-compat without a version bump. No migration code — a v2
  binary rejects v1 containers with an error pointing at the source
  repo to build a matching commit.
- Pack reporter now prints
  `N disagreeing sector(s) → R override record(s), P pass-through(s), delta D bytes`.
  Previously the label `N override(s)` conflicted with `inspect`'s
  `override_records: R` (a different, larger number — byte-run
  granularity, not sector granularity).
- Reporter no longer prefixes each line with `[hh:mm:ss]`. Adds no
  value for an interactive CLI; clutters terminal output and docs.
- README demo blocks condensed for the three "size headline" fixtures
  (Half-Life, Max Payne 2, Deus Ex) — only `ls -lh` is shown for those.
  Freelancer is the comprehensive end-to-end walkthrough:
  `sha256sum` → pack → `ls` → inspect → verify → unpack → `ls` →
  `sha256sum`, proving byte-equality with an external tool.

### Removed

- 32-byte in-header scrambler-table SHA-256. The build-startup pin
  in `ecma130.go` is the actual drift guard; the in-header copy was
  always redundant given the version-byte gate.
- ISO-8601 `created_utc` string in the manifest — replaced by
  `created_unix` (int64 BE seconds since the Unix epoch). Display
  formatting moved to the `inspect` print site.
- `(go1.x.y)` runtime suffix on `tool_version`. Forensics noise that
  doesn't affect output bytes.

## [1.0.0] - 2026-04-29

Initial public release.

### Features

- `miniscram pack` — converts a Redumper `.scram` into a compact
  `.miniscram` container by storing the binary delta against an
  ECMA-130-reconstructed prediction of the scrambled image. Removes
  the source `.scram` after verified round-trip by default.
- `miniscram unpack` — reproduces the `.scram` from `.bin` +
  `.miniscram`, byte-for-byte.
- `miniscram verify` — non-destructive integrity check that rebuilds
  the recovered `.scram` to a temp file and compares hashes against
  the manifest.
- `miniscram inspect` — read-only pretty-print of a `.miniscram`
  container, with `--full` and `--json` modes.

### Container format (v1)

- 41-byte binary header (`MSCM` magic + version + scrambler-table
  SHA-256 + manifest length).
- UTF-8 JSON manifest (tool version, write offset, lead-in LBA,
  per-track and full-disc MD5/SHA-1/SHA-256 hashes).
- Big-endian override-record delta payload, zlib-compressed
  (`BestCompression`).

### Demonstrated against

Real Redumper dumps:
[Deus Ex (PC)](http://redump.org/disc/14933/),
[Half-Life GOTY (PC)](http://redump.org/disc/25966/),
[Freelancer (PC)](http://redump.org/disc/42536/),
[Max Payne 2: The Fall of Max Payne (PC, Play disc)](http://redump.org/disc/10508/).

### Acknowledgments

- Method inspired by Hauenstein,
  [*Compact Preservation of Scrambled CD-ROM Data*](https://doi.org/10.5121/ijcsit.2022.14401)
  (IJCSIT, August 2022).
- Scrambler implementation adapted from
  [redumper](https://github.com/superg/redumper) (GPL-3.0).
- Test fixtures from the
  [redump.org](https://redump.org) preservation community.

[1.2.1]: https://github.com/hughobrien/miniscram/releases/tag/v1.2.1
[1.2.0]: https://github.com/hughobrien/miniscram/releases/tag/v1.2.0
[1.1.1]: https://github.com/hughobrien/miniscram/releases/tag/v1.1.1
[1.1.0]: https://github.com/hughobrien/miniscram/releases/tag/v1.1.0
[1.0.0]: https://github.com/hughobrien/miniscram/releases/tag/v1.0.0
