# Content-defined bin resolution for unpack/verify

Date: 2026-06-06
Status: approved (design)
Depends on: range-capable bin-resolution groundwork (PR #72) — the
  transient `Track.FileOffset`, `hashTracks`, and `assignFileOffsets`.
  (Background: `2026-06-06-combined-cuesheet-support-design.md`, whose
  pack-side was descoped; only the unpack groundwork remains.)
Semver impact: MINOR (new CLI flag + new resolution behavior; no
  container-format change; existing containers unpack unchanged)

## Motivation

miniscram lets you discard a Redumper `.scram` and recover it byte-exact
from the `.bin` + a small `.miniscram` delta. Today, `unpack` finds the
bin by the **exact filenames** the container recorded at pack time:
each track is resolved as `filepath.Join(containerDir, track.Filename)`,
and a missing or differently-named file is a hard failure.

That couples recovery to one specific on-disk layout, which blocks the
storage lifecycle users actually want:

```
redumper dump        → scram + split bins + cue
miniscram pack       → game.miniscram        (delete scram)
chdman createcd      → game.chd              (delete bins + cue)
  ── archive: game.chd + game.miniscram (minimal footprint) ──
  ... anytime ...
chdman extractcd     → game.bin + game.cue   (one COMBINED bin)
miniscram unpack game.miniscram              → original scram, byte-exact
```

The bin you get back from `extractcd` is a single combined file with a
different name and a different partitioning than the per-track bins the
container was packed against. Under today's filename-based resolution,
`unpack` can't use it. The result: `.chd + .miniscram` is *almost* a
complete, compact archive of a scrambled dump — but you can't actually
close the loop without keeping the original bins around.

## Core principle: content-defined, filenames are hints

The container already records, per track, the **size** and the
**MD5/SHA-1/SHA-256** of that track's bytes. Those hashes are the
authoritative identity of the data. The recorded **filename** is only a
convenience for locating the bytes quickly.

This design makes that explicit: **unpack resolves bin bytes by content,
not by name.** Filenames become hints used to find a candidate fast; the
per-track hashes are what actually decide whether the bytes are correct.
A wrong or shifted bin fails loudly on a hash mismatch — never a silent
bad scram.

## Why this is cheap: reconstruction is already layout-independent

The delta does not depend on how the bin is split into files. The
scram-prediction + delta pipeline runs off the **concatenated bin byte
stream** keyed by each track's `(FirstLBA, Mode)` — never file
boundaries. So a delta packed against split bins applies unchanged to the
combined bin's bytes, because the concatenated stream is identical. This
feature's cross-layout round-trip test (pack split, unpack against a
single combined bin → byte-exact scram) verifies that directly.

So the reconstruction core (`BuildEpsilonHat` / `ApplyDelta`, driven by
`BuildParams` from the manifest's `FirstLBA`/`Mode`/`Size`) needs **zero
changes**. The only thing that changes is *where unpack sources the bin
bytes from*.

## Design

### Bin source resolution (the one new step)

Replace `Unpack`'s filename-only "resolving bin files" step with a
resolver that produces, for the manifest's tracks, a mapping onto
whatever bin bytes are actually available. Resolution precedence:

1. **Explicit `--bin <path>`** — use that single file as the byte source.
2. **Named files all present** — every `track.Filename` resolves in
   `containerDir`. Concatenate them in track order. (This is today's
   behavior, including combined-packed containers where several tracks
   share one filename. Backward compatible.)
3. **Exactly one `*.bin` in `containerDir`** — use it as the byte source
   (the `extractcd` "drop one combined bin, just run unpack" case).
4. **Otherwise** — fail with a message listing what was found and
   suggesting `--bin`.

For cases 1 and 3 the single source file's name need not match any
recorded filename. The resolver maps the manifest tracks onto that file
by **cumulative offset** (track *k* occupies
`[Σ sizes[0..k-1], +sizes[k])`), exactly the contiguous tiling
`ResolveCue`/`assignFileOffsets` already compute.

Implementation note: the resolver rewrites the in-memory
`manifest.Tracks` `Filename`/`FileOffset` to point at the resolved
source, then the **existing** `hashTracks` verification, file dedup, and
`OpenBinStream` streaming all work unchanged. Reconstruction still reads
`FirstLBA`/`Mode`/`Size` from the same (otherwise untouched) tracks. No
change to the container on disk.

### Verification (unchanged in spirit, now load-bearing)

1. **Size precheck (cheap):** the chosen source's total byte length must
   equal `manifest.BinSize()` (the sum of per-track sizes). Mismatch →
   fail before hashing.
2. **Per-track content check:** `hashTracks` over each track's resolved
   `[offset, size)` range must match the recorded per-track hashes. This
   is the authoritative gate — it confirms the bytes are exactly what was
   packed, regardless of which file or layout they came from.

A wrong disc, a truncated bin, or a chdman variant that shifted bytes all
trip the hash check and abort with `errBinHashMismatch`. There is no path
that produces a scram without all per-track hashes matching, and the
existing final scram-hash check in `verify` remains a backstop.

### CLI / UX

- `unpack` and `verify` gain an optional `--bin <path>` flag (a single
  combined bin to source from). When omitted, resolution falls through
  the precedence above, so the common case — `extractcd` leaves one
  `game.bin` next to `game.miniscram`, you run `miniscram unpack
  game.miniscram` — "just works" with no flag.
- `--bin` and the auto-detect are equivalent in safety: both are
  validated by size + per-track hashes.
- Help text documents that bin resolution is content-defined: the
  container's filenames are hints; any bin with matching content works.

## Backward compatibility

- Containers and the on-disk format are unchanged. Any existing
  `.miniscram` unpacks exactly as before — when the named files are
  present, precedence rule 2 selects them and behavior is identical.
- The only behavioral change is *additive*: when the named files are
  **absent**, unpack now tries `--bin` / single-bin auto-detect instead
  of failing immediately.
- New `--bin` flag → MINOR per the project semver policy.

## Failure modes (all loud)

- Wrong bin (different disc), right size → per-track hash mismatch →
  `errBinHashMismatch`, no scram written.
- Truncated / wrong-size bin → size precheck fails fast.
- Ambiguous directory (named files missing, multiple `*.bin`, none via
  `--bin`) → resolution error listing candidates, suggesting `--bin`.
- chdman variant that shifts bytes (pregap/write-offset) → hash mismatch.
  (For clean Redumper↔chdman round-trips the combined bin is a pure
  repartition with no shift, verified byte-exact on a real disc.)

## Affected code

- `unpack.go` — new `resolveBinSource` step replacing the filename-only
  loop in `Unpack`; `UnpackOptions`/`VerifyOptions` gain `BinPath string`.
  `Verify` already delegates to `Unpack`, so it inherits the behavior;
  thread `BinPath` through.
- `main.go` — register `--bin` on the `unpack` and `verify` subcommands
  (via the existing `parseSubcommand` configure closure); update help text.
- No changes to `pack.go`, the chunk codec, or the reconstruction core.

## Out of scope (YAGNI)

- Splitting a combined bin back into exact Redumper per-track files
  (that's bchunk/chdman's job and only needed for redump-format
  resubmission; it's deterministic from the cue).
- Reading bin bytes directly from a `.chd` (would remove the `extractcd`
  step, but is a much larger feature; `extractcd` + content-defined
  unpack already closes the lifecycle).
- Multi-source heterogeneous resolution (e.g. some tracks from named
  files, others from a combined bin). Resolution picks one source for the
  whole disc.

## Testing

- **Cross-layout round-trip (centerpiece):** synth disc, `pack` against
  the split layout, then `unpack` against the **combined** bin (named
  files absent, single bin present) → recovered scram byte-exact. And the
  reverse: pack combined, unpack against split named files.
- **Auto-detect precedence:** named files present → used (regression);
  named files absent + one `*.bin` → auto-consumed; absent + multiple
  `*.bin` → error; `--bin` overrides.
- **Content gate is real:** point unpack at a wrong bin of the right size
  → `errBinHashMismatch`, no output written. Truncated bin → size
  precheck fails.
- **Property test:** for a synth disc, the scram recovered via the
  named-files path and via the single-combined-bin path are identical.
- **E2E (`redump_data`):** using the `Vampire_play` combined fixture,
  pack as usual, then unpack sourcing the bin under a name that does *not*
  match the container's recorded `Vampire_play.bin` (e.g. a renamed
  copy/symlink, resolved via auto-detect or `--bin`) → byte-exact scram.
  This proves recovery is filename-independent end-to-end on a real disc.
