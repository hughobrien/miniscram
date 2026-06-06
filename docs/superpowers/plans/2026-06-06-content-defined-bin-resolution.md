# Content-Defined Bin Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `unpack`/`verify` resolve bin bytes by *content* (the per-track hashes already in the container) rather than by the exact filenames the container recorded, so a `.scram` recovers from any bin layout — including a single combined bin from `chdman extractcd` — via a new `--bin` flag or single-bin auto-detect.

**Architecture:** A new `resolveBinSource` step replaces `Unpack`'s filename-only bin resolution. It maps the manifest's tracks onto whatever bin bytes are available (explicit `--bin`, then named files, then a single `*.bin` in the dir), rewriting each track's in-memory `Filename`/`FileOffset` to point at the resolved source. The existing `hashTracks` per-track verification is the authoritative gate; the reconstruction core (`BuildEpsilonHat`/`ApplyDelta`) is untouched because it runs off the concatenated byte stream, never file boundaries.

**Tech Stack:** Go (single `package main`), standard library only. Builds on the range-capable groundwork already on this branch (`Track.FileOffset`, `hashTracks`, `assignFileOffsets`).

**Design reference:** `docs/superpowers/specs/2026-06-06-content-defined-bin-resolution-design.md`

---

## File Structure

- `unpack.go` — new `resolveBinSource` function; `Unpack` rewired to use it; `UnpackOptions`/`VerifyOptions` gain `BinPath`; `Verify` threads it through.
- `main.go` — `--bin` flag registered on the `unpack` and `verify` subcommands; passed into the option structs.
- `help.go` — `unpackHelpText`/`verifyHelpText` document `--bin` and content-defined resolution.
- `unpack_test.go` — unit tests for `resolveBinSource`; cross-layout round-trip and content-gate tests.
- `property_test.go` — property: scram recovered via named files == via single combined bin.
- `cli_test.go` — `--bin` flag wiring at the CLI level.
- `e2e_redump_test.go` — real-disc cross-layout unpack (guarded by `redump_data`).

---

## Task 1: `resolveBinSource` — content-defined bin resolution

**Files:**
- Modify: `unpack.go` (add `resolveBinSource` after the `Unpack` function, before `VerifyOptions`)
- Test: `unpack_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `unpack_test.go`:

```go
// helper: concatenate a disc's data bin followed by all its audio bins.
func combinedBinBytes(disc SynthDisc) []byte {
	out := append([]byte{}, disc.Bin...)
	for _, ab := range disc.AudioBins {
		out = append(out, ab...)
	}
	return out
}

func TestResolveBinSourceNamedFiles(t *testing.T) {
	dir := t.TempDir()
	// Two distinct per-track files (the Redumper layout).
	if err := os.WriteFile(filepath.Join(dir, "t1.bin"), make([]byte, 4*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t2.bin"), make([]byte, 3*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	tracks := []Track{
		{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize},
		{Number: 2, Filename: "t2.bin", Size: 3 * SectorSize},
	}
	baseDir, files, err := resolveBinSource(dir, "", tracks)
	if err != nil {
		t.Fatalf("resolveBinSource: %v", err)
	}
	if baseDir != dir {
		t.Errorf("baseDir = %q, want %q", baseDir, dir)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	if tracks[0].FileOffset != 0 || tracks[1].FileOffset != 0 {
		t.Errorf("named-file tracks should keep FileOffset 0; got %d, %d", tracks[0].FileOffset, tracks[1].FileOffset)
	}
	if tracks[0].Filename != "t1.bin" || tracks[1].Filename != "t2.bin" {
		t.Errorf("named-file filenames must be unchanged; got %q, %q", tracks[0].Filename, tracks[1].Filename)
	}
}

func TestResolveBinSourceSingleBinAutoDetect(t *testing.T) {
	dir := t.TempDir()
	// One combined bin; the recorded per-track filenames are NOT present.
	if err := os.WriteFile(filepath.Join(dir, "whatever.bin"), make([]byte, 7*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	tracks := []Track{
		{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize},
		{Number: 2, Filename: "t2.bin", Size: 3 * SectorSize},
	}
	baseDir, files, err := resolveBinSource(dir, "", tracks)
	if err != nil {
		t.Fatalf("resolveBinSource: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0].Path) != "whatever.bin" {
		t.Fatalf("expected single source whatever.bin; got %+v", files)
	}
	if baseDir != dir {
		t.Errorf("baseDir = %q, want %q", baseDir, dir)
	}
	// Tracks rewritten to point at the single bin, mapped by cumulative offset.
	if tracks[0].Filename != "whatever.bin" || tracks[1].Filename != "whatever.bin" {
		t.Errorf("tracks should be rewritten to whatever.bin; got %q, %q", tracks[0].Filename, tracks[1].Filename)
	}
	if tracks[0].FileOffset != 0 || tracks[1].FileOffset != 4*SectorSize {
		t.Errorf("offsets = %d, %d; want 0, %d", tracks[0].FileOffset, tracks[1].FileOffset, 4*SectorSize)
	}
}

func TestResolveBinSourceExplicitBinPath(t *testing.T) {
	dir := t.TempDir()
	binDir := t.TempDir() // somewhere other than the container dir
	binPath := filepath.Join(binDir, "combined.bin")
	if err := os.WriteFile(binPath, make([]byte, 7*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	// Named files DO exist in dir, but --bin must override them.
	if err := os.WriteFile(filepath.Join(dir, "t1.bin"), make([]byte, 4*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t2.bin"), make([]byte, 3*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	tracks := []Track{
		{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize},
		{Number: 2, Filename: "t2.bin", Size: 3 * SectorSize},
	}
	baseDir, files, err := resolveBinSource(dir, binPath, tracks)
	if err != nil {
		t.Fatalf("resolveBinSource: %v", err)
	}
	if len(files) != 1 || files[0].Path != binPath {
		t.Fatalf("expected single source %s; got %+v", binPath, files)
	}
	if baseDir != binDir {
		t.Errorf("baseDir = %q, want %q", baseDir, binDir)
	}
	if tracks[1].FileOffset != 4*SectorSize {
		t.Errorf("track 2 offset = %d, want %d", tracks[1].FileOffset, 4*SectorSize)
	}
}

func TestResolveBinSourceSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	// Single bin present, but one sector too short for the manifest total.
	if err := os.WriteFile(filepath.Join(dir, "wrong.bin"), make([]byte, 6*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	tracks := []Track{
		{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize},
		{Number: 2, Filename: "t2.bin", Size: 3 * SectorSize},
	}
	_, _, err := resolveBinSource(dir, "", tracks)
	if err == nil {
		t.Fatal("expected error on size mismatch")
	}
	if !errors.Is(err, errBinHashMismatch) {
		t.Errorf("error = %v; want wrapped errBinHashMismatch", err)
	}
}

func TestResolveBinSourceAmbiguousAndMissing(t *testing.T) {
	t.Run("multiple-bins-none-named", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.bin"), make([]byte, SectorSize), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "b.bin"), make([]byte, SectorSize), 0o644); err != nil {
			t.Fatal(err)
		}
		tracks := []Track{{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize}}
		if _, _, err := resolveBinSource(dir, "", tracks); err == nil {
			t.Fatal("expected error with multiple .bin candidates and no named files")
		}
	})
	t.Run("nothing-present", func(t *testing.T) {
		dir := t.TempDir()
		tracks := []Track{{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize}}
		if _, _, err := resolveBinSource(dir, "", tracks); err == nil {
			t.Fatal("expected error when no named files and no .bin present")
		}
	})
}
```

Confirm `unpack_test.go` imports `errors`, `os`, `path/filepath`, `testing`. Add any missing.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'TestResolveBinSource' -v`
Expected: FAIL to compile — `resolveBinSource` is undefined.

- [ ] **Step 3: Implement `resolveBinSource`**

In `unpack.go`, add this function immediately after the `Unpack` function (before `// VerifyOptions`):

```go
// resolveBinSource maps the manifest's tracks onto bin bytes available on
// disk, by content rather than by the exact filenames the container
// recorded. It rewrites each track's Filename/FileOffset to point at the
// resolved source and returns the baseDir to hash from plus the deduped
// files to stream, in track order.
//
// Precedence:
//  1. binPath != "" — that single file is the byte source (explicit override).
//  2. every recorded Filename exists in dir — use them as-is (the Redumper
//     layout; identical to the prior filename-based behavior).
//  3. exactly one *.bin in dir — that single file is the byte source.
// Otherwise an error describing what was found.
//
// A single-file source (cases 1 and 3) lays tracks out by cumulative size:
// track k spans [Σ sizes[0..k-1], +sizes[k]). The size precheck here is
// cheap; the caller still hash-verifies every track's range, so a wrong or
// shifted source fails loudly rather than producing a bad scram.
func resolveBinSource(dir, binPath string, tracks []Track) (string, []ResolvedFile, error) {
	var total int64
	for i := range tracks {
		total += tracks[i].Size
	}

	// Map every track onto one combined file by cumulative offset.
	useSingle := func(path string) (string, []ResolvedFile, error) {
		info, err := os.Stat(path)
		if err != nil {
			return "", nil, err
		}
		if info.Size() != total {
			return "", nil, fmt.Errorf("%w: bin %s is %d bytes, manifest expects %d",
				errBinHashMismatch, path, info.Size(), total)
		}
		base := filepath.Base(path)
		var off int64
		for i := range tracks {
			tracks[i].Filename = base
			tracks[i].FileOffset = off
			off += tracks[i].Size
		}
		return filepath.Dir(path), []ResolvedFile{{Path: path, Size: info.Size()}}, nil
	}

	// 1. Explicit --bin overrides everything.
	if binPath != "" {
		return useSingle(binPath)
	}

	// 2. Named files all present → use them as recorded.
	allPresent := true
	for i := range tracks {
		if _, err := os.Stat(filepath.Join(dir, tracks[i].Filename)); err != nil {
			allPresent = false
			break
		}
	}
	if allPresent {
		assignFileOffsets(tracks)
		fileTotals := map[string]int64{}
		for _, tr := range tracks {
			fileTotals[tr.Filename] += tr.Size
		}
		var files []ResolvedFile
		seen := map[string]bool{}
		for _, tr := range tracks {
			if seen[tr.Filename] {
				continue
			}
			seen[tr.Filename] = true
			path := filepath.Join(dir, tr.Filename)
			info, err := os.Stat(path)
			if err != nil {
				return "", nil, fmt.Errorf("track %d (%s): %w", tr.Number, tr.Filename, err)
			}
			if info.Size() != fileTotals[tr.Filename] {
				return "", nil, fmt.Errorf("%w: %s size on disk %d != manifest total %d",
					errBinHashMismatch, tr.Filename, info.Size(), fileTotals[tr.Filename])
			}
			files = append(files, ResolvedFile{Path: path, Size: info.Size()})
		}
		return dir, files, nil
	}

	// 3. Exactly one *.bin in dir → use it.
	bins, _ := filepath.Glob(filepath.Join(dir, "*.bin"))
	if len(bins) == 1 {
		return useSingle(bins[0])
	}
	if len(bins) == 0 {
		return "", nil, fmt.Errorf("cannot find bin data: the manifest's track files are absent from %s and no .bin is present; pass --bin <path>", dir)
	}
	return "", nil, fmt.Errorf("cannot resolve bin data: the manifest's track files are absent from %s and %d .bin files are present; pass --bin <path> to choose", dir, len(bins))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./... -run 'TestResolveBinSource' -v`
Expected: PASS (all five tests / sub-tests).

- [ ] **Step 5: Commit**

```bash
git add unpack.go unpack_test.go
git commit -m "feat: content-defined bin source resolution

resolveBinSource maps a container's tracks onto bin bytes by content:
explicit --bin, then named files, then a single .bin in the dir. Rewrites
each track's Filename/FileOffset to point at the resolved source so the
existing range-based hash verification stays the authoritative gate.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Wire `Unpack` to `resolveBinSource`

**Files:**
- Modify: `unpack.go` (`UnpackOptions` struct ~19-24; the "resolving bin files" block ~46-79)
- Test: `unpack_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `unpack_test.go`:

```go
func TestUnpackContentDefinedSingleBin(t *testing.T) {
	disc := synthDisc(t, SynthOpts{MainSectors: 12, AudioTracks: 1, WriteOffset: 8})

	// Pack against the split (one-file-per-track) layout.
	splitDir := t.TempDir()
	_, splitScram, splitCue := writeFixture(t, splitDir, disc)
	container := filepath.Join(splitDir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: splitCue, ScramPath: splitScram, OutputPath: container,
		LeadinLBA: LBAPregapStart,
	}, nil); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	// Restore in a fresh dir holding ONLY the container + a single combined
	// bin whose name does not match the container's per-track filenames.
	restoreDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(restoreDir, "combined.bin"), combinedBinBytes(disc), 0o644); err != nil {
		t.Fatal(err)
	}
	containerCopy := filepath.Join(restoreDir, "x.miniscram")
	data, err := os.ReadFile(container)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(containerCopy, data, 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(restoreDir, "out.scram")
	if err := Unpack(UnpackOptions{
		ContainerPath: containerCopy, OutputPath: out, Verify: true,
	}, nil); err != nil {
		t.Fatalf("content-defined Unpack: %v", err)
	}
	if got, want := mustHashFile(t, out), mustHashFile(t, splitScram); got != want {
		t.Fatalf("recovered scram %+v != original %+v", got, want)
	}
}

func TestUnpackContentDefinedRejectsWrongBin(t *testing.T) {
	disc := synthDisc(t, SynthOpts{MainSectors: 12, AudioTracks: 1, WriteOffset: 8})
	splitDir := t.TempDir()
	_, splitScram, splitCue := writeFixture(t, splitDir, disc)
	container := filepath.Join(splitDir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: splitCue, ScramPath: splitScram, OutputPath: container,
		LeadinLBA: LBAPregapStart,
	}, nil); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	// A combined bin of the RIGHT size but WRONG content (one byte flipped).
	restoreDir := t.TempDir()
	bad := combinedBinBytes(disc)
	bad[100] ^= 0xFF
	if err := os.WriteFile(filepath.Join(restoreDir, "combined.bin"), bad, 0o644); err != nil {
		t.Fatal(err)
	}
	containerCopy := filepath.Join(restoreDir, "x.miniscram")
	data, _ := os.ReadFile(container)
	if err := os.WriteFile(containerCopy, data, 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(restoreDir, "out.scram")
	err := Unpack(UnpackOptions{ContainerPath: containerCopy, OutputPath: out, Verify: true}, nil)
	if err == nil {
		t.Fatal("expected hash-mismatch error on wrong bin")
	}
	if !errors.Is(err, errBinHashMismatch) {
		t.Errorf("error = %v; want wrapped errBinHashMismatch", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("no output scram should be written when the bin fails verification")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'TestUnpackContentDefined' -v`
Expected: FAIL — `UnpackOptions` has no `BinPath` field is not the issue yet, but `Unpack` still resolves by exact filename, so `TestUnpackContentDefinedSingleBin` fails at "resolving bin files" (the recorded `x.bin`/`audio1.bin` are absent in `restoreDir`).

- [ ] **Step 3: Add `BinPath` to `UnpackOptions`**

In `unpack.go`, change the `UnpackOptions` struct to:

```go
// UnpackOptions holds inputs for Unpack.
type UnpackOptions struct {
	ContainerPath string
	OutputPath    string
	Verify        bool
	Force         bool
	// BinPath optionally points unpack at a single bin to source from,
	// overriding filename-based resolution. Empty = auto-resolve.
	BinPath string
}
```

- [ ] **Step 4: Replace the resolution block in `Unpack`**

In `unpack.go`, replace the entire block from `st = r.Step("resolving bin files")` through `st.Done("%d file(s), %d track(s)", len(files), len(m.Tracks))` (the `assignFileOffsets`/`fileTotals`/dedup loop) with:

```go
	st = r.Step("resolving bin files")
	containerDir := filepath.Dir(opts.ContainerPath)
	baseDir, files, err := resolveBinSource(containerDir, opts.BinPath, m.Tracks)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%d file(s), %d track(s)", len(files), len(m.Tracks))
```

Then, in the next step, change the `hashTracks` call so it hashes from the resolver's `baseDir` (not `containerDir`). Replace:

```go
	perTrack, err := hashTracks(containerDir, m.Tracks)
```

with:

```go
	perTrack, err := hashTracks(baseDir, m.Tracks)
```

(Everything else — the `for i, got := range perTrack` verification loop, `OpenBinStream(files)`, `BuildParams`, delta application — is unchanged. `containerDir` is still used by `resolveBinSource`; `baseDir` is what `hashTracks` and the bin source share.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./... -run 'TestUnpackContentDefined' -v`
Expected: PASS (both tests).

- [ ] **Step 6: Run the full suite (existing unpack/verify must be unaffected)**

Run: `go test ./...`
Expected: PASS — including all existing `TestUnpack*`, `TestVerify*`, `TestCLI*` (named-file resolution still works because precedence rule 2 selects it).

- [ ] **Step 7: Commit**

```bash
git add unpack.go unpack_test.go
git commit -m "feat: unpack resolves bins by content, not just filename

Unpack now calls resolveBinSource, so a scram recovers from a single
combined bin (auto-detected or via --bin) whose name need not match the
container's recorded per-track filenames. Wrong/short bins fail loudly on
the per-track hash check. UnpackOptions gains BinPath.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `--bin` CLI flag and `Verify` support

**Files:**
- Modify: `unpack.go` (`VerifyOptions` ~196-199; `Verify` body ~238-243)
- Modify: `main.go` (`runUnpack` ~206-240; `runVerify` ~242-261)
- Modify: `help.go` (`unpackHelpText` ~65-82; `verifyHelpText` ~84-...)
- Test: `cli_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cli_test.go`:

```go
func TestCLIUnpackVerifyWithBinFlag(t *testing.T) {
	disc := synthDisc(t, SynthOpts{MainSectors: 12, AudioTracks: 1, WriteOffset: 8})
	splitDir := t.TempDir()
	_, splitScram, splitCue := writeFixture(t, splitDir, disc)
	container := filepath.Join(splitDir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: splitCue, ScramPath: splitScram, OutputPath: container,
		LeadinLBA: LBAPregapStart,
	}, nil); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	// A combined bin in a separate dir, named arbitrarily.
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "anything.bin")
	if err := os.WriteFile(binPath, combinedBinBytes(disc), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	// verify --bin
	if code := run([]string{"verify", "--bin", binPath, container}, io.Discard, &stderr); code != exitOK {
		t.Fatalf("verify --bin exit %d (%s)", code, stderr.String())
	}
	// unpack --bin
	out := filepath.Join(t.TempDir(), "out.scram")
	stderr.Reset()
	if code := run([]string{"unpack", "-q", "--bin", binPath, "-o", out, container}, io.Discard, &stderr); code != exitOK {
		t.Fatalf("unpack --bin exit %d (%s)", code, stderr.String())
	}
	if got, want := mustHashFile(t, out), mustHashFile(t, splitScram); got != want {
		t.Fatalf("recovered scram %+v != original %+v", got, want)
	}
}
```

Confirm `cli_test.go` imports `bytes`, `io`, `os`, `path/filepath`, `testing` (it already imports most; add any missing).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run 'TestCLIUnpackVerifyWithBinFlag' -v`
Expected: FAIL — `--bin` is an unknown flag (exit `exitUsage`), so the run codes won't be `exitOK`.

- [ ] **Step 3: Add `BinPath` to `VerifyOptions` and thread it through `Verify`**

In `unpack.go`, change `VerifyOptions`:

```go
// VerifyOptions holds inputs for Verify.
type VerifyOptions struct {
	ContainerPath string
	// BinPath optionally points verify at a single bin to source from
	// (same semantics as UnpackOptions.BinPath).
	BinPath string
}
```

Then in `Verify`, pass it into the internal `Unpack` call. Change:

```go
	if err := Unpack(UnpackOptions{
		ContainerPath: opts.ContainerPath,
		OutputPath:    tmpPath,
		Verify:        false,
		Force:         true,
	}, r); err != nil {
		return err
	}
```

to:

```go
	if err := Unpack(UnpackOptions{
		ContainerPath: opts.ContainerPath,
		OutputPath:    tmpPath,
		Verify:        false,
		Force:         true,
		BinPath:       opts.BinPath,
	}, r); err != nil {
		return err
	}
```

- [ ] **Step 4: Register `--bin` on `unpack` and pass it through**

In `main.go`, `runUnpack`, add a `bin` variable and flag, and pass it to `UnpackOptions`. Replace the function's flag-parsing and `Unpack` call so it reads:

```go
func runUnpack(args []string, stderr io.Writer) int {
	var output, outputLong, bin string
	var force, forceLong bool
	positional, common, exit, ok := parseSubcommand("unpack", unpackHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&output, "o", "", "output path")
		fs.StringVar(&outputLong, "output", "", "output path")
		fs.BoolVar(&force, "f", false, "overwrite output")
		fs.BoolVar(&forceLong, "force", false, "overwrite output")
		fs.StringVar(&bin, "bin", "", "single bin file to source from")
	})
	if !ok {
		return exit
	}
	if !requireOnePositional(stderr, unpackHelpText, positional, "positional argument (container path)") {
		return exitUsage
	}
	containerPath := positional[0]
	out := pickFirst(output, outputLong)
	if out == "" {
		out = DefaultUnpackOutput(containerPath)
	}
	var rep Reporter
	switch common.progress {
	case "json":
		rep = NewJSONReporter(stderr)
	default:
		rep = NewReporter(stderr, common.quiet)
	}
	if err := Unpack(UnpackOptions{
		ContainerPath: containerPath, OutputPath: out,
		Verify: true, Force: force || forceLong, BinPath: bin,
	}, rep); err != nil {
		return errToExit(err)
	}
	return exitOK
}
```

- [ ] **Step 5: Register `--bin` on `verify` and pass it through**

In `main.go`, `runVerify`, replace the function so it registers `--bin` and passes it to `VerifyOptions`:

```go
func runVerify(args []string, stderr io.Writer) int {
	var bin string
	positional, common, exit, ok := parseSubcommand("verify", verifyHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&bin, "bin", "", "single bin file to source from")
	})
	if !ok {
		return exit
	}
	if !requireOnePositional(stderr, verifyHelpText, positional, "positional argument (container path)") {
		return exitUsage
	}
	var rep Reporter
	switch common.progress {
	case "json":
		rep = NewJSONReporter(stderr)
	default:
		rep = NewReporter(stderr, common.quiet)
	}
	if err := Verify(VerifyOptions{ContainerPath: positional[0], BinPath: bin}, rep); err != nil {
		return errToExit(err)
	}
	return exitOK
}
```

- [ ] **Step 6: Update help text**

In `help.go`, replace the `unpackHelpText` `ARGUMENTS` + `OPTIONS` sections so they document content-defined resolution and `--bin`. Replace:

```go
ARGUMENTS:
    <in.miniscram>    .miniscram container produced by 'miniscram pack'.
                      The track .bin files referenced by the manifest
                      must exist in the same directory as the container.

OPTIONS:
    -o, --output <path>    where to write the reconstructed .scram.
                           default: <miniscram-stem>.scram next to
                           <in.miniscram>.
    -f, --force            overwrite existing output.
    --progress=json        emit NDJSON progress events on stderr
                           (suppresses human text; for scripted consumers).
    -q, --quiet            suppress progress output.
    -h, --help             show this help.
```

with:

```go
ARGUMENTS:
    <in.miniscram>    .miniscram container produced by 'miniscram pack'.
                      Bin data is resolved by CONTENT: unpack uses the
                      per-track hashes, not the recorded filenames. It
                      looks for the recorded track files in the container's
                      directory, else a single .bin there, else --bin.

OPTIONS:
    -o, --output <path>    where to write the reconstructed .scram.
                           default: <miniscram-stem>.scram next to
                           <in.miniscram>.
    --bin <path>           single combined .bin to source from (e.g. one
                           produced by 'chdman extractcd'). Overrides
                           filename-based lookup; verified by hash.
    -f, --force            overwrite existing output.
    --progress=json        emit NDJSON progress events on stderr
                           (suppresses human text; for scripted consumers).
    -q, --quiet            suppress progress output.
    -h, --help             show this help.
```

Then in `verifyHelpText`, replace:

```go
ARGUMENTS:
    <in.miniscram>    .miniscram container produced by 'miniscram pack'.
                      Track .bin files must exist in the same directory.

OPTIONS:
    --progress=json   emit NDJSON progress events on stderr
                      (suppresses human text; for scripted consumers).
    -q, --quiet       suppress progress output.
    -h, --help        show this help.
```

with:

```go
ARGUMENTS:
    <in.miniscram>    .miniscram container produced by 'miniscram pack'.
                      Bin data is resolved by content (recorded track
                      files, else a single .bin in the dir, else --bin).

OPTIONS:
    --bin <path>      single combined .bin to source from; overrides
                      filename-based lookup, verified by hash.
    --progress=json   emit NDJSON progress events on stderr
                      (suppresses human text; for scripted consumers).
    -q, --quiet       suppress progress output.
    -h, --help        show this help.
```

- [ ] **Step 7: Run the test and full suite**

Run: `go test ./... -run 'TestCLIUnpackVerifyWithBinFlag' -v`
Expected: PASS.

Run: `go test ./...`
Expected: PASS (existing CLI help/flag tests still green; note `TestCLIHelp` just checks help renders, which it still does).

- [ ] **Step 8: Commit**

```bash
git add unpack.go main.go help.go cli_test.go
git commit -m "feat: --bin flag for unpack and verify

Add --bin <path> to source the scram reconstruction from a single
combined bin (e.g. chdman extractcd output), on both unpack and verify.
VerifyOptions gains BinPath. Help text documents content-defined
resolution.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Property test — named-files vs single-bin recovery agree

**Files:**
- Test: `property_test.go`

- [ ] **Step 1: Write the test**

Add to `property_test.go`:

```go
// TestRecoveryLayoutAgnostic asserts the scram recovered from the recorded
// per-track files is byte-identical to the one recovered from a single
// combined bin of the same content — the core content-defined property.
func TestRecoveryLayoutAgnostic(t *testing.T) {
	for _, audioTracks := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("audio%d", audioTracks), func(t *testing.T) {
			disc := synthDisc(t, SynthOpts{MainSectors: 20, AudioTracks: audioTracks, WriteOffset: 8})

			splitDir := t.TempDir()
			_, splitScram, splitCue := writeFixture(t, splitDir, disc)
			container := filepath.Join(splitDir, "x.miniscram")
			if err := Pack(PackOptions{
				CuePath: splitCue, ScramPath: splitScram, OutputPath: container,
				LeadinLBA: LBAPregapStart,
			}, nil); err != nil {
				t.Fatalf("Pack: %v", err)
			}

			// Recover via the named per-track files (in place).
			viaNamed := filepath.Join(splitDir, "via-named.scram")
			if err := Unpack(UnpackOptions{ContainerPath: container, OutputPath: viaNamed, Verify: true}, nil); err != nil {
				t.Fatalf("Unpack via named files: %v", err)
			}

			// Recover via a single combined bin in a fresh dir.
			restoreDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(restoreDir, "combined.bin"), combinedBinBytes(disc), 0o644); err != nil {
				t.Fatal(err)
			}
			containerCopy := filepath.Join(restoreDir, "x.miniscram")
			data, _ := os.ReadFile(container)
			if err := os.WriteFile(containerCopy, data, 0o644); err != nil {
				t.Fatal(err)
			}
			viaSingle := filepath.Join(restoreDir, "via-single.scram")
			if err := Unpack(UnpackOptions{ContainerPath: containerCopy, OutputPath: viaSingle, Verify: true}, nil); err != nil {
				t.Fatalf("Unpack via single bin: %v", err)
			}

			if mustHashFile(t, viaNamed) != mustHashFile(t, viaSingle) {
				t.Fatal("scram recovered via named files differs from via single bin")
			}
			if mustHashFile(t, viaNamed) != mustHashFile(t, splitScram) {
				t.Fatal("recovered scram differs from the original")
			}
		})
	}
}
```

Confirm `property_test.go` imports `fmt`, `os`, `path/filepath`. Add any missing.

Note on `audio0`: a data-only disc packs and recovers; the "combined bin" is just the single data bin, so both paths resolve the same single file — still a valid agreement check.

- [ ] **Step 2: Run the test**

Run: `go test ./... -run 'TestRecoveryLayoutAgnostic' -v`
Expected: PASS for all three sub-tests. If it fails, the resolver's offset mapping or the size/hash gate is wrong — fix in Task 1/2, do not weaken the assertion.

- [ ] **Step 3: Commit**

```bash
git add property_test.go
git commit -m "test: scram recovery is layout-agnostic

Property: the scram recovered from the recorded per-track files equals the
one recovered from a single combined bin of the same content, and both
equal the original.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Real-disc cross-layout unpack (redump_data)

Exercises content-defined recovery on the real `Vampire_play` disc: pack against its per-track bins, then unpack against the combined `chdman`-style `Vampire_play.bin` (a different filename and partitioning). Gitignored fixture, skips when absent.

**Files:**
- Modify: `e2e_redump_test.go` (add one `//go:build redump_data` test function)

- [ ] **Step 1: Stage the dataset locally (symlinks)**

```bash
mkdir -p test-discs/vampire-play
ln -sf "$HOME/Downloads/disc2/Vampire_play.scram"            test-discs/vampire-play/Vampire_play.scram
ln -sf "$HOME/Downloads/disc2/Vampire_play.bin"             test-discs/vampire-play/Vampire_play.bin
ln -sf "$HOME/Downloads/disc2/Vampire_play (Track 1).bin"   "test-discs/vampire-play/Vampire_play (Track 1).bin"
ln -sf "$HOME/Downloads/disc2/Vampire_play (Track 2).bin"   "test-discs/vampire-play/Vampire_play (Track 2).bin"
```

Confirm gitignored: `git check-ignore test-discs/vampire-play/Vampire_play.bin` prints the path.

- [ ] **Step 2: Add the test**

Add to `e2e_redump_test.go` (anywhere after the imports; it is already `//go:build redump_data`):

```go
// TestE2EContentDefinedUnpack packs the real Vampire_play disc from its
// per-track bins, then recovers the scram from the combined chdman-style
// Vampire_play.bin via --bin — a different filename and partitioning than
// the container recorded. Proves content-defined recovery byte-exact on a
// real disc. Skips when the dataset is absent.
func TestE2EContentDefinedUnpack(t *testing.T) {
	dir := "test-discs/vampire-play"
	scram := filepath.Join(dir, "Vampire_play.scram")
	combined := filepath.Join(dir, "Vampire_play.bin")
	t1 := filepath.Join(dir, "Vampire_play (Track 1).bin")
	t2 := filepath.Join(dir, "Vampire_play (Track 2).bin")
	for _, p := range []string{scram, combined, t1, t2} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("dataset not present: %s", p)
		}
	}

	// Work in a temp dir on the same filesystem (recovered scram is ~900 MB).
	tmp, err := os.MkdirTemp(dir, "miniscram-cd-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })

	// Split cue referencing the two per-track bins (redumper convention).
	cue := "FILE \"Vampire_play (Track 1).bin\" BINARY\n" +
		"  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n" +
		"FILE \"Vampire_play (Track 2).bin\" BINARY\n" +
		"  TRACK 02 AUDIO\n    INDEX 00 00:00:00\n    INDEX 01 00:02:00\n"
	cuePath := filepath.Join(dir, "vp_split_cd.cue")
	if err := os.WriteFile(cuePath, []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(cuePath) })

	container := filepath.Join(tmp, "Vampire_play.miniscram")
	rep := NewReporter(io.Discard, true)
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scram, OutputPath: container,
	}, rep); err != nil {
		t.Fatalf("Pack (split): %v", err)
	}

	// The container records the per-track filenames. Recover from the
	// combined bin via --bin (the container lives in tmp, which has neither
	// the per-track bins nor the combined bin, so this is purely --bin).
	out := filepath.Join(tmp, "recovered.scram")
	if err := Unpack(UnpackOptions{
		ContainerPath: container, OutputPath: out, Verify: true, BinPath: combined,
	}, rep); err != nil {
		t.Fatalf("Unpack --bin combined: %v", err)
	}
	if !filesEqual(t, out, scram) {
		t.Fatal("recovered scram differs from original")
	}
}
```

(`filesEqual` already exists in `e2e_redump_test.go`.)

- [ ] **Step 3: Run the test against the staged dataset**

Run: `go test -tags redump_data ./... -run 'TestE2EContentDefinedUnpack' -v`
Expected: PASS — packs the split layout, recovers the scram from the combined bin byte-exact (reads/rebuilds ~900 MB; allow 30-90s).

Then confirm no regression and the default build:

Run: `go test -tags redump_data ./...`
Expected: PASS (other real-disc tests skip if absent).

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit (source only — never stage test-discs/)**

```bash
git add e2e_redump_test.go
git commit -m "test: real-disc content-defined cross-layout unpack

Pack Vampire_play from its per-track bins, recover the scram from the
combined chdman-style bin via --bin, byte-exact. redump_data-gated; skips
when the dataset is absent.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

After committing, run `git status` and confirm `test-discs/` is not staged.

---

## Task 6: Changelog + manual CLI verification

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add a changelog entry**

Read `CHANGELOG.md` top to match its `## [x.y.z] - YYYY-MM-DD` / `### Added` Keep-a-Changelog format. Add a new `## [1.6.0] - 2026-06-06` section above the current latest (`1.5.2`) with:

```markdown
### Added
- Content-defined bin resolution for `unpack`/`verify`: the recovered
  `.scram` is sourced by per-track content hashes, not by the exact
  filenames the container recorded. `unpack`/`verify` look for the
  recorded track files, else a single `.bin` in the container's
  directory, else a `--bin <path>` you provide. This recovers the scram
  from any bin layout — including a single combined bin produced by
  `chdman extractcd` — so a disc can be archived as `chd + .miniscram`
  and fully restored later. A wrong or shifted bin fails loudly on the
  hash check; no container-format change.
```

If the groundwork PR (#72) already introduced a `1.6.0` heading on this branch's history, fold this bullet under it instead of creating a duplicate. (At time of writing `main`'s latest is `1.5.2` and #72 added no changelog entry, so a fresh `1.6.0` is expected.)

- [ ] **Step 2: Manual end-to-end check against the real disc**

Build, then exercise the content-defined path on the real combined bin without keeping any split layout in the dir:

```bash
cd /Users/hugh/src/miniscram && go build -o miniscram .
WORK=$(mktemp -d "$HOME/Downloads/disc2/cd-XXXXXX")
printf 'FILE "Vampire_play (Track 1).bin" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\nFILE "Vampire_play (Track 2).bin" BINARY\n  TRACK 02 AUDIO\n    INDEX 00 00:00:00\n    INDEX 01 00:02:00\n' > "$HOME/Downloads/disc2/vp_cd.cue"
./miniscram pack "$HOME/Downloads/disc2/vp_cd.cue" -o "$WORK/vp.miniscram" --keep-source --force
./miniscram verify --bin "$HOME/Downloads/disc2/Vampire_play.bin" "$WORK/vp.miniscram"
ls "$HOME/Downloads/disc2/Vampire_play.scram"   # confirm scram preserved
rm -rf "$WORK" "$HOME/Downloads/disc2/vp_cd.cue"
```

Expected: `pack` resolves 2 tracks and reports a verified round-trip; `verify --bin` ends with "all three match" (it rebuilt the scram from the combined bin and matched the recorded hashes); the source `.scram` still exists. If `verify --bin` does not print "all three match", STOP and report the actual output — do not commit.

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog for content-defined bin resolution

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review notes (for the implementer)

- **Spec coverage:** §Core principle + §Design "Bin source resolution" → Task 1 (`resolveBinSource`, precedence 1/2/3, single-file cumulative mapping). §Verification (size precheck + per-track hash) → Task 1 (`useSingle` size check) + Task 2 (existing `hashTracks` gate, now from `baseDir`). §CLI/UX (`--bin`, auto-detect, help) → Task 3. §Backward compatibility → Task 2 Step 6 (full suite, named-file path unchanged). §Failure modes → Task 1 reject tests + Task 2 wrong-bin test. §Testing (cross-layout round-trip, auto-detect precedence, content gate, property, e2e) → Tasks 1/2/4/5.
- **No format change:** nothing in this plan touches `chunks.go`/`manifest.go` serialization; `resolveBinSource` only mutates in-memory `Track.Filename`/`FileOffset` (both transient — `FileOffset` is `json:"-"` and not in the TRKS codec; `Filename` rewrite is in-memory only and never written back).
- **Type consistency:** `resolveBinSource(dir, binPath string, tracks []Track) (string, []ResolvedFile, error)` is defined in Task 1 and called in Task 2 with `(containerDir, opts.BinPath, m.Tracks)`, returning `baseDir, files, err`. `hashTracks(baseDir, m.Tracks)` consumes that `baseDir`. `UnpackOptions.BinPath`/`VerifyOptions.BinPath` (Tasks 2/3) match the `BinPath:` fields set in `main.go` (Task 3). `combinedBinBytes(disc SynthDisc) []byte` is defined in Task 1 and reused in Tasks 2/4.
- **Reconstruction untouched:** `BuildParams`/`BuildEpsilonHat`/`ApplyDelta` read `FirstLBA`/`Mode`/`Size` from `m.Tracks` (unchanged by the resolver) and the concatenated `OpenBinStream(files)`; correctness across layouts is the layout-independence the design relies on, verified directly by Tasks 4 and 5.
