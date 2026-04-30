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

### A1. `miniscram inspect <miniscram>` subcommand *(shipped 2026-04-28)*

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
- [x] `miniscram inspect <path>` works for any current-version container.
      (Container is now v3 since C1; rejects older versions with a
      migration error.)
- [x] Refuses v1 / v2 containers with the version-byte error.
- [x] Reads container without writing or temp files.
- [x] Output is greppable; one field per line.
- [x] `--json` flag emits the manifest verbatim plus a `delta_records`
      array (count, offsets, lengths — no payload bytes).
- [x] `inspect --help` documents the format.

**Files:** new `inspect.go`, `inspect_test.go`; touches `main.go`
(subcommand dispatch) and `help.go`.

**Effort:** ~150 LOC. Half a day.

**Depends on:** nothing. Pure read-only.

**Open questions:** none.

---

### A2. `miniscram verify <bin> <miniscram>` subcommand *(shipped 2026-04-28)*

**Goal:** Non-destructive integrity check. Rebuild ε̂, apply delta,
hash the result, compare against `manifest.scram_sha256` — but
**don't write the .scram to disk**. Catches container corruption
without taking the multi-hundred-MB disk hit of `unpack`.

**Acceptance:**
- [x] `miniscram verify` runs without producing any output file.
- [x] Reports OK / FAIL, with the manifest's recorded hashes and the
      computed hashes on FAIL. (After C1: all three hashes are
      compared, strict any-of-three policy.)
- [x] Same input shapes as `unpack` (cwd / stem / explicit; default
      no `-o`).
- [x] Build ε̂ to a temp file (Unpack handles this internally), hash
      the temp file, then delete it. Documented as temporarily using
      ~scram_size of disk.
- [x] Exit codes: 0 success, 3 verification failed, 4 I/O error,
      5 wrong .bin (hash mismatch with manifest).
- [x] Reporter shows the same step list as unpack minus the final
      output write.

**Decision (open question resolved):** No `--full-bin-check` flag;
the normal flow already fast-exits on wrong bin via the bin-hash
check at step 3.

**Outcome:** New `Verify` Go function in `verify.go`; thin
`runVerify` CLI wrapper in `main.go`. Wraps `Unpack(Verify:false)`
to a tempfile, then `hashFile` + `compareHashes` against the
manifest. `UnpackOptions.SuppressVerifyWarning` added to silence
the "verification skipped" warning during verify's internal unpack.

---

## Theme B — Preservation completeness (highest archival value)

### B1. Real copy-protected disc test (the *Rune* case) *(shipped 2026-04-28; Freelancer/SafeDisc fixture)*

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
- [x] At least one real Redumper dump of a copy-protected disc is
      runnable via the `redump_data` build tag. Freelancer
      (SafeDisc 2.70.030, 588 documented intentional errors) lives
      at `freelancer/` (gitignored).
- [x] An e2e test packs and unpacks the protected disc; round-trip
      is byte-equal.
- [x] Data-track ECC/EDC error count is asserted exactly. Test-side
      `countDataTrackErrors` walks the bin and counts invalid-EDC
      sectors; for Freelancer that's 588 (a stable SafeDisc-class
      signature). `manifest.error_sector_count` is *not* the same
      metric — see manifest.go's ErrorSectorCount comment for why.
- [~] Spot-check waived: byte-equal round-trip already proves every
      byte of the protection sectors (including deliberately-broken
      EDC) is preserved. No SafeDisc-specific descramble-and-look-for-
      string check needed.

**Outcome:** Table-driven `TestE2ERoundTripRealDiscs` and
`TestE2EEDCAndECCRealDiscs` in `e2e_redump_test.go`, each ranging
over a `realDiscFixtures` slice. Currently 2 fixtures: deus-ex
(skipped, data absent) and freelancer (running). HL1 multi-track
fixture deferred — needs multi-FILE `.cue` support first.

**Side effect:** Surfaced and fixed a real bug in `pack.go`'s
`detectWriteOffset` — real Redumper dumps of protected discs have
non-zero lead-in data that produces coincidental sync-pattern
matches; original code took the first match and aborted. Fix
iterates candidates and accepts the first BCD-valid + sample-
aligned offset.

---

### B3. Multi-track + audio fixture *(shipped 2026-04-28; HL1 e2e)*

**Goal:** v0.2's synth disc and the Deus Ex e2e are both single-track
Mode 1. The audio-track code path (`trackModeAt(...) == "AUDIO"`
skipping the scrambler) is untested end-to-end.

**Acceptance:**
- [~] Synthetic 3-track (data + audio + data) fixture: waived in
      favor of the real-world HL1 e2e, which exercises the same code
      path with a 28-track multi-FILE cue (1 Mode 1 + 27 audio).
- [x] Real multi-track Redumper dump tested e2e: HL1 fixture row in
      `realDiscFixtures` (commit `09c19ab`). Round-trip byte-equal
      in ~33s.

**Outcome:** Closed by combining with B1.5 (multi-FILE .cue support).
Surfaced and fixed `checkConstantOffset`'s audio-coincidence bug
(same shape as B1's `detectWriteOffset` bug — coincidental sync
sequences in PCM passing the BCD-validity check; fix is the full
LBA+offset-bound validation).

**Decision (open question resolved):** `BinFirstLBA` is the LBA where
the data track's FILE begins on the disc. Audio track FirstLBA values
are computed by ResolveCue as the cumulative sum of prior file sizes
in sectors (file_start_LBA, not INDEX 01 within-file). Pregap
silence at the start of an audio track's file inherits the audio
track's mode via `trackModeAt`, which is what the predictor needs.

---

### B4. Mode 2 (XA) test fixture *(shipped 2026-04-28)*

**Goal:** Mode 2 sectors have different EDC/ECC layouts (or none, in
Form 1/2 distinction) but the scrambling itself is mode-agnostic.
Currently no test exercises the Mode 2 path even though `cue.go`
accepts `MODE2/2352`.

**Acceptance:**
- [x] Synthetic fixture with a Mode 2 track that round-trips via
      pack + unpack. `synthDiscWithMode` helper parameterized over
      the mode byte + cue mode string; `TestBuilderCleanRoundTripMode2`
      and `TestUnpackRoundTripMode2SynthDisc` cover the full Pack →
      ReadContainer → Unpack → byte-equal cycle.
- [~] Real Mode 2 / VCD / PSX-XA e2e: skipped (no dataset).

**Outcome:** Mode 2 path confirmed working. The predictor is mode-
agnostic for non-AUDIO tracks (both Mode 1 and Mode 2 go through the
ECMA-130 scrambler unchanged); the test exists as a regression sentinel
for cue parsing + manifest preservation of `MODE2/2352`. Real-world
EDC/ECC layout differences (Form 1 vs Form 2) are irrelevant because
miniscram doesn't compute EDC/ECC at pack time.

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
- **`fsck` / `inspect --check` structural validator.** Was C2.
  Largely redundant with `verify`: ReadContainer already catches
  bad magic / bad version byte / implausible manifest length /
  malformed manifest JSON, and ApplyDelta validates override
  framing during the rebuild. The narrow value (no-bin needed,
  no scratch disk, byte-offset diagnostics) doesn't justify a
  new subcommand. If the disk-cheap-check becomes a real need,
  add `verify --structural-only` rather than a separate command.
- **Layout-failure diagnostics.** Was A3. Heuristic "likely cause"
  analysis of `LayoutMismatchError`'s mismatched-LBA pattern. Worth
  reconsidering only if a real disc actually triggers
  `LayoutMismatchError` in practice — currently zero hits across
  the dataset (clean Deus Ex, SafeDisc Freelancer). Without
  failure data to ground heuristics on, "Likely cause: …" claims
  risk being confidently wrong. Revisit when there's a real
  failure case.
- **Subchannel (`.subcode`) preservation.** Was B2. miniscram's
  role is to replace the `.scram` (save disk via structured delta
  against the `.bin`); subchannel preservation is already handled
  by Redumper's output bundle (`*_logs.zip` and friends). Re-encoding
  it inside `.miniscram` adds no archival value over keeping the
  Redumper bundle alongside.
- **Cross-platform CI.** Was C3. Multi-OS GitHub Actions matrix.
  Codebase is stdlib-only with no syscalls, so platform-specific
  bugs are unlikely. Revisit if/when the repo is pushed to GitHub
  and someone other than the author starts using it.

---

## Theme E — Test infrastructure

### E1. Expand property-test coverage

**Goal:** Lean into `testing/quick`-style randomized property tests
wherever the codebase has a clean invariant. They catch bugs that
example-based fixtures miss because the input space is too large to
enumerate, and they document the invariant in a self-checking way.

**Candidates already visible in the code:**

- `BCDMSFToLBA` / `LBAToBCDMSF` — round-trip on the addressable LBA
  range (`[-150, 99·60·75 - 150)`), assert `BCDMSFToLBA(LBAToBCDMSF(lba)) == lba`.
  (✅ covered in `format/drop-scrambler-hash`)
- `Scramble` (in `ecma130.go`) — XOR with a fixed table is its own
  inverse; assert `Scramble(Scramble(buf)) == buf` for arbitrary
  2352-byte buffers. (✅ covered in `format/drop-scrambler-hash`)
- `ScramOffset` / `TotalLBAs` (in `layout.go`) — algebraic identity
  across arbitrary `(scramSize, writeOffset)` pairs satisfying the
  precondition.
- `WriteContainer` / `ReadContainer` round-trip — given an arbitrary
  manifest + delta payload, write then read should reproduce both
  byte-for-byte. (✅ covered in `format/drop-scrambler-hash`)
- `BuildEpsilonHat` lockstep — for an arbitrary `BuildParams`,
  building twice produces identical bytes (idempotence on the same
  inputs).
- `IterateDeltaRecords` — for any structurally valid delta payload,
  iterating and re-encoding yields identical bytes.

**Bonus coverage** (not in original candidate list, added in
`format/drop-scrambler-hash` alongside the v2 chunk-format work):

- MFST / TRKS / HASH codec round-trips — encode/decode for each v2
  chunk codec on randomized inputs.

**Acceptance:**

- [x] `Scramble` involution
- [x] `BCDMSFToLBA` / `LBAToBCDMSF` round-trip
- [x] `WriteContainer` / `ReadContainer` round-trip
- [ ] (deferred) `BuildEpsilonHat` lockstep — needs full disc-layout
      generator (`BinFirstLBA`, `BinSectorCount`, scram size, tracks,
      write offset, all consistent); defer to a focused follow-up.
- [ ] (deferred) `ScramOffset` / `TotalLBAs` — needs valid
      `(scramSize, writeOffset)` precondition generator; defer.
- [ ] (deferred) `IterateDeltaRecords` — needs valid delta-stream
      generator; defer.
- [x] Property tests run as part of `go test ./...` with a default
      `quick.Config{MaxCount: 1000}` (or `100` for the expensive
      WriteContainer/ReadContainer round-trip) — no separate build
      tag, no opt-in.
- [x] Where a property test catches a real bug while being written,
      the bug fix and the test land in the same commit. (No bugs
      caught by the round of tests landed in
      `format/drop-scrambler-hash`.)

**Effort:** Iterative. ~30 min per property test once the pattern is
established. Can be picked up in slices.

**Depends on:** nothing.

**Open questions:** none — pick a candidate and write the test.

---

## How to pick up a task

Each entry is sized to be picked up cold. The recommended workflow:

1. Read the spec at `docs/superpowers/specs/2026-04-28-miniscram-v0.2-design.md`
   plus the v0.1 spec it references.
2. If the task says "no design needed" (most A and C items), go
   straight to writing-plans.
3. If the task says "needs design first" (B2 in particular), run
   the brainstorming skill on it — the open questions are real.
4. The Deus Ex test data at `test-discs/deus-ex/` is the
   golden reference for any real-world test. Don't commit it — it's
   already in `.gitignore`.

Recommended order if budgeting one cycle: **A1 + A2** as a paired
UX upgrade, then **C1** for Redumper interop, then **B1** as the
preservation centerpiece. Save **B2** for a later cycle since it's
the one item that really wants its own brainstorm.
