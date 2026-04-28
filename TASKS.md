# miniscram — future work

Backlog of features and improvements identified after v0.2 shipped.
Each entry is sized to be picked up cold by a future agent without
needing to interrogate the original author. Ordered roughly by my
recommendation priority within each theme.

State of the world when this file was written: v0.2 is complete and
on `main`. Pure-Go pipeline, sub-KiB delta on clean discs, byte-perfect
round-trip verified end-to-end against the Deus Ex Redumper dump in
`deus-ex/`. See `docs/superpowers/specs/2026-04-28-miniscram-v0.2-design.md`
for the architectural baseline.

---

## Theme A — Archivist UX (small, high daily value)

### A1. `miniscram inspect <miniscram>` subcommand

**Goal:** Read-only pretty-print of a `.miniscram` container for an
archivist auditing or debugging a packed file.

**Output (text on stdout):**
- Container format version, magic bytes.
- Manifest fields: tool version, created_utc, all sizes and sha256s,
  write offset, leadin LBA, track list (with mode + first LBA per
  track), bin first LBA + sector count, error_sector_count, delta
  size, scrambler table sha256.
- Number of override records in the delta payload (parsed from the
  payload header, not from the manifest's recorded count).
- For small `.miniscram` files (<1 MiB), optionally the list of
  override LBAs and their lengths (cap at 100 lines on stdout, all
  via `--full`).

**Acceptance:**
- [ ] `miniscram inspect <path>` works for any v2 container.
- [ ] Refuses v1 containers with the existing version-byte error.
- [ ] Reads container without writing or temp files.
- [ ] Output is greppable; one field per line.
- [ ] `--json` flag emits the manifest verbatim plus a `delta_records`
      array (count, offsets, lengths — no payload bytes).
- [ ] `inspect --help` documents the format.

**Files:** new `inspect.go`, `inspect_test.go`; touches `main.go`
(subcommand dispatch) and `help.go`.

**Effort:** ~150 LOC. Half a day.

**Depends on:** nothing. Pure read-only.

**Open questions:** none.

---

### A2. `miniscram verify <bin> <miniscram>` subcommand

**Goal:** Non-destructive integrity check. Rebuild ε̂, apply delta,
hash the result, compare against `manifest.scram_sha256` — but
**don't write the .scram to disk**. Catches container corruption
without taking the multi-hundred-MB disk hit of `unpack`.

**Acceptance:**
- [ ] `miniscram verify` runs without producing any output file.
- [ ] Reports OK / FAIL, with the manifest's recorded sha256 and the
      computed sha256 on FAIL.
- [ ] Same input shapes as `unpack` (cwd / stem / explicit; default
      no `-o`).
- [ ] Build ε̂ to a temp file (we need somewhere for ApplyDelta to
      WriteAt), then sha256 the temp file, then delete it. Document
      that this temporarily uses ~scram_size of disk.
- [ ] Exit codes: 0 success, 3 verification failed, 4 I/O error,
      5 wrong .bin (sha256 mismatch with manifest).
- [ ] Reporter shows the same step list as unpack minus "wrote .scram".

**Files:** new `verify.go`, `verify_test.go`; touches `main.go` and
`help.go`. Reuses `BuildEpsilonHat` and `ApplyDelta` directly.

**Effort:** ~120 LOC. Half a day.

**Depends on:** A1's manifest formatting helpers can be shared if
A1 lands first; not strictly required.

**Open questions:** Whether to add a `--full-bin-check` flag that
also verifies the bin sha256 against `manifest.bin_sha256` (we already
do this implicitly, but a fast path that skips ε̂ rebuild entirely
when the bin doesn't match manifest would save time on the common
"wrong bin" mistake).

---

### A3. Layout-failure diagnostics

**Goal:** When `BuildEpsilonHatAndDelta` returns a `LayoutMismatchError`,
the user sees "10000+ sectors differ; first mismatched LBAs:
[0, 1, 2, 3, …]". Not very helpful for debugging. Better: heuristic
analysis of the override pattern that suggests probable causes.

**Heuristics to implement:**
- All mismatched LBAs in arithmetic progression with stride N → likely
  off-by-N sector misalignment in the cue.
- Mismatches concentrated in a contiguous run at the start or end →
  likely wrong `BinFirstLBA` or `BinSectorCount`.
- Mismatches span every Nth sector → variable-offset disc (which we
  reject earlier, but `checkConstantOffset`'s 3-sample check might
  miss subtler patterns).
- Mismatches uniformly distributed → likely wrong write offset.

**Acceptance:**
- [ ] `LayoutMismatchError.Error()` includes a "Likely cause:" line
      derived from the override pattern.
- [ ] At least three distinct heuristic categories are detected and
      tested with synthetic fixtures.
- [ ] Heuristics never produce a false-positive "this is the cause"
      claim — they say "likely" or "possibly" when uncertain.

**Files:** `builder.go` (modify `LayoutMismatchError` and its `Error`
method); `builder_test.go` (new heuristic tests).

**Effort:** ~200 LOC + tests. One day.

**Depends on:** nothing.

**Open questions:** Whether the heuristic should attempt a *retry*
with a corrected guess (e.g. "trying with write_offset = -52
instead"). Probably no — too magical.

---

## Theme B — Preservation completeness (highest archival value)

### B1. Real copy-protected disc test (the *Rune* case)

**Goal:** The whole motivation of the Hauenstein paper is preserving
hidden text in error sectors of copy-protected discs (notably *Rune*,
some Securom variants, LibCrypt PSX). v0.2 has only been tested on
a clean disc; the override path that captures error sectors is
exercised only by synthetic fixtures with hand-flipped bytes.

A real-world test against a Redumper dump of a known-protected disc
would prove that:
1. miniscram correctly identifies error sectors via the lockstep
   check.
2. The structured delta captures them losslessly.
3. The round-trip reproduces them byte-for-byte (including any
   intentional EDC/ECC corruption).

**Acceptance:**
- [ ] At least one real Redumper dump of a copy-protected disc is
      added to `deus-ex/` (or a new sibling dir) — gated behind the
      `redump_data` build tag so the dataset is optional.
- [ ] An e2e test packs and unpacks the protected disc; round-trip
      is byte-equal.
- [ ] `manifest.error_sector_count` is non-zero and matches the
      disc's known intentional-error count (from the Redumper
      submission info).
- [ ] Spot-check: descramble one error sector from the recovered
      `.scram`, dump its bytes, confirm the hidden content (e.g.
      Rune's text string) is present.

**Files:** `e2e_redump_test.go` (new test); test data lives outside
git via `.gitignore`.

**Effort:** Mostly procurement (finding/downloading a known
protected disc dump in Redumper format). Test code is ~80 LOC.

**Depends on:** access to a copy-protected dump. Public databases
like redump.org list protected titles; some hashes are in the
documentation.

**Open questions:** Whether to also include LibCrypt PSX as a second
real-world fixture (different protection scheme).

---

### B2. Subchannel preservation (`.subcode` integration)

**Goal:** Redumper produces a `.subcode` file with 96 bytes of
subchannel data per sector. It contains the Q-channel (with ISRC,
MCN, copy bits), and the P/R/S/T/U/V/W channels. Currently miniscram
discards it. Preserving it brings miniscram closer to "whole-disc"
preservation.

**Architecture sketch:**
- Subchannel is independent of the main-channel data; we don't need
  EDC/ECC of any kind for it.
- Pure raw-bytes preservation — for clean discs, the subchannel is
  highly repetitive (control bits + incrementing ISRC) so it
  compresses well via the same byte-keyed override format.
- Predicted "ε̂" for subchannel: assume Q-channel is well-formed
  with sequential addresses and zero R-W, pre-emit those, diff
  against actual.
- Add a second container payload section: `[u32 main_delta_len]
  [main_delta] [u32 sub_delta_len] [sub_delta]`. Or a second
  container file `<stem>.miniscram-sub`.

**Acceptance:**
- [ ] Pack with `--include-subcode` packs the `.subcode` file
      alongside the main channel.
- [ ] Unpack reproduces the `.subcode` byte-for-byte.
- [ ] On Deus Ex, the subchannel delta is also small (likely <10 KiB).
- [ ] Manifest grows a `subcode_size` and `subcode_sha256` field.
- [ ] Without `--include-subcode`, behavior is unchanged from v0.2.

**Files:** new `subcode.go`, `subcode_test.go`; touches `pack.go`,
`unpack.go`, `manifest.go` (new fields, format_version → 3).

**Effort:** Substantial — needs design work first (single container
vs sidecar file, subchannel "ε̂" model, etc.). Brainstorm + spec +
plan + implement: 2-3 cycles. Definitely the heaviest item.

**Depends on:** B1 ideally (the protected-disc test would also
exercise the subchannel path), but not strictly required.

**Open questions:** Major. Bumping `format_version` means another
v0.1-style migration story. Worth doing alongside any other
container changes that accumulate.

---

### B3. Multi-track + audio fixture

**Goal:** v0.2's synth disc and the Deus Ex e2e are both single-track
Mode 1. The audio-track code path (`trackModeAt(...) == "AUDIO"`
skipping the scrambler) is untested end-to-end.

**Acceptance:**
- [ ] New synthetic test fixture in `builder_test.go`: a 3-track disc
      (data + audio + data) with verifiable round-trip.
- [ ] If a real multi-track Redumper dump can be sourced, add a
      build-tagged e2e test for it.

**Files:** `builder_test.go`; possibly `e2e_redump_test.go`.

**Effort:** ~150 LOC for the synthetic fixture. Real e2e depends on
data availability.

**Depends on:** nothing for the synthetic case.

**Open questions:** What `BinFirstLBA` looks like for an audio-track
disc relative to the cue's first INDEX 01 — there are convention
differences between "data starts at LBA 0" and "audio starts at
LBA 0".

---

### B4. Mode 2 (XA) test fixture

**Goal:** Mode 2 sectors have different EDC/ECC layouts (or none, in
Form 1/2 distinction) but the scrambling itself is mode-agnostic.
Currently no test exercises the Mode 2 path even though `cue.go`
accepts `MODE2/2352`.

**Acceptance:**
- [ ] Synthetic fixture with a Mode 2 track that round-trips via
      pack + unpack.
- [ ] If a real Mode 2 / VCD / PSX-XA dump is available, add a
      build-tagged e2e test.

**Files:** `builder_test.go`; possibly `e2e_redump_test.go`.

**Effort:** ~100 LOC for the synthetic case.

**Depends on:** B3 — the multi-track fixture work is similar.

---

## Theme C — Robustness & interop

### C1. Hash algorithm parity with Redumper *(shipped 2026-04-28)*

**Goal:** Redump submission templates record md5, sha1, and sha256.
miniscram currently records only sha256. Adding md5 and sha1 to the
manifest makes interop with redump.org workflows direct (no separate
hashing step needed).

**Acceptance:**
- [x] Manifest gains `bin_md5`, `bin_sha1` (alongside existing
      `bin_sha256`).
- [x] Same for `scram_md5`, `scram_sha1`, `scram_sha256`.
- [x] All three are computed in a single pass per file (single
      `io.MultiWriter` over MD5/SHA-1/SHA-256 hashes).
- [x] `inspect` shows all three.
- [x] At unpack, all three are verified (md5 + sha1 + sha256), with
      strict any-of-three policy: any single mismatch fails the
      operation (exit 5 for bin, exit 3 for output).

**Decision (open question resolved):** Strict any-of-three. We don't
expect collisions; any divergence between recorded and recomputed
hashes is treated as real signal, not a hash-impl false alarm.

**Outcome:** Container format bumped v0.2 → v0.3 (`format_version` 3,
container version byte `0x03`). v0.2 containers are rejected with the
same "re-pack from .bin" migration message v0.1 used. Sentinels
renamed `errBinSHA256Mismatch` → `errBinHashMismatch` and
`errOutputSHA256Mismatch` → `errOutputHashMismatch`. New helpers
`hashFile` and `compareHashes` in `pack.go`; `sha256File` deleted.

---

### C2. `miniscram fsck <miniscram>` subcommand

**Goal:** Validate container framing without unpacking. Catches
corruption in: magic bytes, version byte, manifest length, manifest
JSON parsing, override-record framing.

**Acceptance:**
- [ ] `miniscram fsck <path>` reports OK or lists specific framing
      errors with byte offsets.
- [ ] No file outputs; no temp files.
- [ ] Doesn't validate the *content* of overrides (that's `verify`'s
      job; this is purely structural).

**Files:** new `fsck.go`, `fsck_test.go`; `main.go`; `help.go`.

**Effort:** ~100 LOC. Half a day.

**Depends on:** nothing.

**Open questions:** Whether to fold this into `inspect --check` rather
than a separate subcommand. Probably yes — keeps the CLI smaller.

---

### C3. Cross-platform CI

**Goal:** v0.2 spec calls out Linux-only as a known untested gap.
The codebase is stdlib-only and uses no syscalls, but Windows path
quirks could bite.

**Acceptance:**
- [ ] GitHub Actions workflow runs `go test ./...` on Linux, macOS,
      and Windows runners.
- [ ] Any platform-specific bugs surfaced are fixed (likely path
      separator handling in `discover.go` is the main risk).
- [ ] Build artifacts are produced for all three platforms.

**Files:** `.github/workflows/test.yml` (new); possibly minor edits
elsewhere.

**Effort:** Half a day, mostly waiting for CI feedback.

**Depends on:** the repo being pushed to GitHub. Currently it lives
locally on `main` only.

---

## Theme D — Probably not worth doing (flagged for completeness)

These came up during brainstorming but I don't recommend pursuing
them without a clear motivating use case:

- **DVD support.** Different sector format (2048-byte data, no
  scrambling, different EDC/ECC). Would be a near-rewrite. Better
  to start a sibling tool (`mini-dvd-scram`) than bolt onto miniscram.
- **JSON output for everything.** v0.1 explicitly rejected this. Add
  per-subcommand `--json` only if real archivist tooling needs it.
- **Compression on top of the structured delta.** The delta is 4
  bytes on a clean disc and small KiBs even on protected discs.
  Compression wouldn't help meaningfully and adds a code dependency.
- **Cryptographic manifest signing.** Out of scope until anyone
  besides the original author uses miniscram. The `scrambler_table_sha256`
  field already provides defense against silent algorithm drift.
- **Streaming pack/unpack (no temp files).** Pack already streams
  ε̂ + delta in one pass; the temp file is only for the verify
  rebuild. CD-sized data fits comfortably in temp space. Worth
  revisiting only if DVD support lands.
- **Pluggable scrambling tables.** Some niche disc formats use
  non-ECMA-130 scrambling. Out of scope; that's a different tool.

---

## How to pick up a task

Each entry is sized to be picked up cold. The recommended workflow:

1. Read the spec at `docs/superpowers/specs/2026-04-28-miniscram-v0.2-design.md`
   plus the v0.1 spec it references.
2. If the task says "no design needed" (most A and C items), go
   straight to writing-plans.
3. If the task says "needs design first" (B2 in particular), run
   the brainstorming skill on it — the open questions are real.
4. The Deus Ex test data at `/home/hugh/miniscram/deus-ex/` is the
   golden reference for any real-world test. Don't commit it — it's
   already in `.gitignore`.

Recommended order if budgeting one cycle: **A1 + A2** as a paired
UX upgrade, then **C1** for Redumper interop, then **B1** as the
preservation centerpiece. Save **B2** for a later cycle since it's
the one item that really wants its own brainstorm.
