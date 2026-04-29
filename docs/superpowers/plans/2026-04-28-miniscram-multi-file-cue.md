# miniscram Multi-FILE Cue Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement TASKS.md-implicit item B1.5 — multi-FILE `.cue` parsing + per-track hashes + CLI simplification (single positional per subcommand). Unblocks B3 (HL1 e2e fixture).

**Architecture:** Cue parser captures FILE→TRACK association; new `ResolveCue` resolves filesystem paths and computes absolute LBAs; new `OpenBinStream` chains per-track files into one `io.Reader` via `io.MultiReader`. Pack/Unpack/Verify are reworked to consume the multi-bin stream; PackOptions/UnpackOptions/VerifyOptions drop `BinPath` (cue or manifest is authoritative). Manifest schema bumps v0.3 → v0.4 with per-track Size/Filename/MD5/SHA1/SHA256 fields and a top-level whole-disc roll-up. CLI drops cwd-discovery; every subcommand takes one explicit positional.

**Tech Stack:** Go stdlib only (`io.MultiReader`, `io.MultiWriter`, `crypto/md5`, `crypto/sha1`, `crypto/sha256`, `encoding/hex`, `os.Stat`).

**Variance from spec:** None.

---

## File Structure

| File | Role |
| --- | --- |
| `cue.go` | Track struct extended; ParseCue handles FILE; new `ResolveCue`, `OpenBinStream`, `CueResolved`, `ResolvedFile`. |
| `manifest.go` | `containerVersion` 0x03→0x04; v0.3→v0.4 migration message. |
| `pack.go` | PackOptions drops BinPath. New flow: ResolveCue → single hashing pass over track files (per-track + roll-up via fan-out MultiWriter) → BuildEpsilonHatAndDelta over multi-bin stream → write container. `hashReader` extracted; `hashFile` becomes a wrapper. |
| `unpack.go` | UnpackOptions drops BinPath. Manifest's tracks tell unpack the filenames in container's directory; stat-check sizes; single hashing pass for per-track + roll-up; rebuild via OpenBinStream. |
| `verify.go` | VerifyOptions drops BinPath. Same flow as unpack to a tempfile. |
| `main.go` | runPack/runUnpack/runVerify rewritten for single positional. |
| `help.go` | All four help texts rewritten. |
| `discover.go` | Deleted entirely. |
| `inspect.go` | `formatHumanInspect` adds per-track filename/size/hashes display. |

Five tasks, linear dependency chain (each blocks the next).

---

## Task 1: Track struct extension + ParseCue multi-FILE

**Goal:** Extend `Track` with new fields (`Size`, `Filename`, `MD5`, `SHA1`, `SHA256`). Update `ParseCue` to track the current FILE, attach the filename to subsequent TRACKs, reject non-BINARY FILEs, reject path-traversal filenames, reject multi-track-per-FILE cues. Discard INDEX 01's MSF value (no longer needed).

**Files:**
- Modify: `/home/hugh/miniscram/cue.go`
- Modify: `/home/hugh/miniscram/cue_test.go`

**Acceptance Criteria:**
- [ ] `Track` struct has 5 new fields with snake_case JSON tags (`size`, `filename`, `md5`, `sha1`, `sha256`).
- [ ] `ParseCue` on a 28-track HL1-shape cue (Track 01 MODE1/2352, Tracks 02-28 AUDIO, one FILE per track) returns 28 Tracks with correct Number/Mode/Filename.
- [ ] `ParseCue` on the existing single-track synth cue still works (existing tests pass).
- [ ] `FILE "x.bin" WAVE` → error.
- [ ] `FILE "../bad.bin" BINARY` → error.
- [ ] A cue with two TRACKs in one FILE → error.
- [ ] Filename in a `FILE` line preserves spaces (e.g., `"HALFLIFE (Track 01).bin"`).

**Verify:** `go test ./... -run TestParseCue -v && go vet ./...`

**Steps:**

- [ ] **Step 1: Write the new cue tests in `cue_test.go`**

Append to `/home/hugh/miniscram/cue_test.go` (add `strings` to imports if not present):

```go
func TestParseCue_MultiFile(t *testing.T) {
	const cue = `FILE "HALFLIFE (Track 01).bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
FILE "HALFLIFE (Track 02).bin" BINARY
  TRACK 02 AUDIO
    INDEX 00 00:00:00
    INDEX 01 00:02:00
FILE "HALFLIFE (Track 03).bin" BINARY
  TRACK 03 AUDIO
    INDEX 01 00:00:00
`
	tracks, err := ParseCue(strings.NewReader(cue))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 3 {
		t.Fatalf("got %d tracks, want 3", len(tracks))
	}
	want := []struct {
		num      int
		mode     string
		filename string
	}{
		{1, "MODE1/2352", "HALFLIFE (Track 01).bin"},
		{2, "AUDIO", "HALFLIFE (Track 02).bin"},
		{3, "AUDIO", "HALFLIFE (Track 03).bin"},
	}
	for i, w := range want {
		if tracks[i].Number != w.num {
			t.Errorf("track[%d].Number = %d; want %d", i, tracks[i].Number, w.num)
		}
		if tracks[i].Mode != w.mode {
			t.Errorf("track[%d].Mode = %q; want %q", i, tracks[i].Mode, w.mode)
		}
		if tracks[i].Filename != w.filename {
			t.Errorf("track[%d].Filename = %q; want %q", i, tracks[i].Filename, w.filename)
		}
	}
}

func TestParseCue_RejectsNonBinaryFile(t *testing.T) {
	const cue = `FILE "x.wav" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
`
	_, err := ParseCue(strings.NewReader(cue))
	if err == nil {
		t.Fatal("expected error for WAVE FILE type")
	}
	if !strings.Contains(err.Error(), "BINARY") {
		t.Errorf("error doesn't mention BINARY: %v", err)
	}
}

func TestParseCue_RejectsRelativeTraversal(t *testing.T) {
	const cue = `FILE "../bad.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
`
	_, err := ParseCue(strings.NewReader(cue))
	if err == nil {
		t.Fatal("expected error for path-traversal filename")
	}
}

func TestParseCue_RejectsPathSeparatorInFilename(t *testing.T) {
	const cue = `FILE "subdir/x.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
`
	_, err := ParseCue(strings.NewReader(cue))
	if err == nil {
		t.Fatal("expected error for filename with path separator")
	}
}

func TestParseCue_RejectsMultiTrackPerFile(t *testing.T) {
	const cue = `FILE "shared.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    INDEX 01 02:00:00
`
	_, err := ParseCue(strings.NewReader(cue))
	if err == nil {
		t.Fatal("expected error for multi-track-per-FILE")
	}
}

func TestParseCue_SingleFileStillWorks(t *testing.T) {
	const cue = `FILE "x.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
`
	tracks, err := ParseCue(strings.NewReader(cue))
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
	if tracks[0].Filename != "x.bin" {
		t.Errorf("Filename = %q; want %q", tracks[0].Filename, "x.bin")
	}
}
```

- [ ] **Step 2: Run tests — expect failures (Filename field doesn't exist, FILE rejection logic absent)**

Run: `go test ./... -run TestParseCue -v`
Expected: build failure on `Filename` field; tests for FILE rejection / multi-track-per-FILE fail because the parser silently ignores these.

- [ ] **Step 3: Update `Track` struct and `ParseCue` in `cue.go`**

Replace the `Track` definition in `/home/hugh/miniscram/cue.go` lines 12-17 with:

```go
// Track is a single track entry in a cuesheet, augmented with
// filesystem metadata at pack time.
type Track struct {
	Number   int    `json:"number"`
	Mode     string `json:"mode"`        // "MODE1/2352", "MODE2/2352", or "AUDIO"
	FirstLBA int32  `json:"first_lba"`   // absolute LBA where this track's FILE begins (set by ResolveCue)
	Size     int64  `json:"size"`        // bytes in this track's .bin file (set by ResolveCue)
	Filename string `json:"filename"`    // basename of source .bin (set by ParseCue)
	MD5      string `json:"md5"`         // lowercase hex (set at pack time)
	SHA1     string `json:"sha1"`
	SHA256   string `json:"sha256"`
}
```

Replace the body of `ParseCue` (lines 32-103) with:

```go
// ParseCue extracts FILE / TRACK / MODE associations from a cuesheet.
// It is a deliberate subset of the cue spec — enough for Redumper
// output (one TRACK per FILE), no more.
//
// Returned Tracks have Number, Mode, and Filename populated.
// FirstLBA / Size / hashes are populated downstream (ResolveCue, Pack).
//
// Rejects non-BINARY FILE types, path-bearing filenames (containing
// any of `/`, `\`, `..`), and cues where a single FILE contains more
// than one TRACK (Redumper never produces this shape).
func ParseCue(r io.Reader) ([]Track, error) {
	scanner := bufio.NewScanner(r)
	var tracks []Track
	var cur *Track
	var hasIndex01 bool
	var currentFile string  // basename of the most recent FILE line
	var fileTrackCount int  // number of TRACKs seen in currentFile (must end at 0 or 1)
	flushTrack := func() error {
		if cur == nil {
			return nil
		}
		if !hasIndex01 {
			return fmt.Errorf("track %d has no INDEX 01", cur.Number)
		}
		tracks = append(tracks, *cur)
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "REM ") || line == "REM" {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "FILE":
			if len(fields) < 3 {
				return nil, fmt.Errorf("malformed FILE line: %q", line)
			}
			// FILE "name with spaces.bin" BINARY — split on the trailing
			// type token; everything between fields[0] and the type is the
			// quoted name.
			typeTok := fields[len(fields)-1]
			if typeTok != "BINARY" {
				return nil, fmt.Errorf("unsupported FILE type %q (only BINARY is supported)", typeTok)
			}
			rawName := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "FILE"), typeTok))
			rawName = strings.TrimSpace(rawName)
			rawName = strings.TrimPrefix(rawName, `"`)
			rawName = strings.TrimSuffix(rawName, `"`)
			if rawName == "" {
				return nil, fmt.Errorf("empty FILE name: %q", line)
			}
			if strings.ContainsAny(rawName, `/\`) || strings.Contains(rawName, "..") {
				return nil, fmt.Errorf("FILE references with paths not supported: %q", rawName)
			}
			// Flush any in-progress TRACK before changing FILE context.
			if err := flushTrack(); err != nil {
				return nil, err
			}
			cur = nil
			hasIndex01 = false
			currentFile = rawName
			fileTrackCount = 0
		case "TRACK":
			if currentFile == "" {
				return nil, fmt.Errorf("TRACK before any FILE: %q", line)
			}
			if err := flushTrack(); err != nil {
				return nil, err
			}
			fileTrackCount++
			if fileTrackCount > 1 {
				return nil, fmt.Errorf("FILE %q contains more than one TRACK; multi-track-per-FILE cues are unsupported", currentFile)
			}
			if len(fields) < 3 {
				return nil, fmt.Errorf("malformed TRACK line: %q", line)
			}
			n, err := strconv.Atoi(fields[1])
			if err != nil {
				return nil, fmt.Errorf("bad track number %q: %v", fields[1], err)
			}
			mode := fields[2]
			if !validModes[mode] {
				return nil, fmt.Errorf("unsupported track mode %q (expected MODE1/2352, MODE2/2352, or AUDIO)", mode)
			}
			cur = &Track{Number: n, Mode: mode, Filename: currentFile}
			hasIndex01 = false
		case "INDEX":
			if cur == nil {
				return nil, fmt.Errorf("INDEX before TRACK: %q", line)
			}
			if len(fields) < 3 {
				return nil, fmt.Errorf("malformed INDEX line: %q", line)
			}
			if fields[1] != "01" {
				continue // ignore INDEX 00 and others
			}
			// Parse the MSF for validation only; the value is unused
			// (see spec: FirstLBA is the file-start LBA, computed by
			// ResolveCue, not the INDEX 01 within-file LBA).
			if _, err := parseMSF(fields[2]); err != nil {
				return nil, fmt.Errorf("bad MSF in %q: %v", line, err)
			}
			hasIndex01 = true
		default:
			// PERFORMER, TITLE, CATALOG, PREGAP, etc. — ignored.
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := flushTrack(); err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("cuesheet contains no tracks")
	}
	return tracks, nil
}
```

- [ ] **Step 4: Run tests — expect new ones PASS**

Run: `go test ./... -run TestParseCue -v`
Expected: all 6 ParseCue tests pass.

- [ ] **Step 5: Run full suite + vet**

Run: `go test ./... && go vet ./...`
Expected: full suite likely fails because downstream code expects Track fields populated by ResolveCue (not yet implemented). That's OK — Tasks 2-5 close the loop. **Document any failures in the commit message and proceed**; the foundation must compile.

If compile fails (not just test fails), inspect. Most likely cause: existing Pack/Unpack code references `track.FirstLBA` from ParseCue's old contract. With ParseCue no longer setting FirstLBA, Pack's line that does `BinFirstLBA: tracks[0].FirstLBA` may now be 0. That's correct behavior pre-Task-3 (Task 3 will plumb ResolveCue in). For Task 1, ensure the package compiles even if downstream tests fail.

- [ ] **Step 6: Commit**

```bash
git add cue.go cue_test.go
git commit -m "$(cat <<'EOF'
cue: extend Track struct and parser for multi-FILE cues

Track gains Size, Filename, MD5, SHA1, SHA256 (with snake_case JSON
tags). ParseCue now captures FILE→TRACK association, attaching the
basename to subsequent Tracks; rejects non-BINARY FILE types,
path-bearing filenames, and multi-track-per-FILE cues (Redumper
never produces these shapes). INDEX 01's MSF is parsed for
validation but discarded — FirstLBA is the file-start LBA computed
by the downstream ResolveCue helper, not the INDEX 01 within-file
LBA.

Foundation for B1.5 (multi-FILE cue support, unblocking HL1 e2e
coverage). Downstream Pack/Unpack/Verify still consume the old
single-FILE shape and will be reworked in subsequent tasks.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: ResolveCue + OpenBinStream + hashReader

**Goal:** Add `ResolveCue` (parse + filesystem stat + cumulative LBA), `OpenBinStream` (`io.MultiReader` over per-track files with combined Close), and extract `hashReader` from `hashFile` so multi-bin streams can reuse it.

**Files:**
- Modify: `/home/hugh/miniscram/cue.go` (add ResolveCue, OpenBinStream, supporting types)
- Modify: `/home/hugh/miniscram/pack.go` (extract hashReader; hashFile becomes a wrapper)
- Modify: `/home/hugh/miniscram/cue_test.go` (TestResolveCue, TestOpenBinStream, TestHashReader)

**Acceptance Criteria:**
- [ ] `ResolveCue` against a 3-FILE cue (with files of known sizes) produces tracks with `FirstLBA[0]=0`, `FirstLBA[1]=size0/2352`, `FirstLBA[2]=(size0+size1)/2352`. Each track's `Size` matches the file size on disk.
- [ ] `OpenBinStream` over 3 small files (e.g., `[]byte("aaa")`, `[]byte("bb")`, `[]byte("c")`) returns a reader that produces `"aaabbc"`. The returned Close func closes every underlying file (verified via subsequent read returning EOF or close error).
- [ ] `hashReader(io.Reader)` returns the same `FileHashes` as `hashFile(path)` when given the same content via `os.Open`.
- [ ] `hashFile(path)` is now a 3-line wrapper that opens and calls `hashReader`.

**Verify:** `go test ./... -run "TestResolveCue|TestOpenBinStream|TestHashReader|TestHashFile" -v && go vet ./...`

**Steps:**

- [ ] **Step 1: Write tests in `cue_test.go`**

Append:

```go
func TestResolveCue_ComputesAbsoluteLBAs(t *testing.T) {
	dir := t.TempDir()
	// Three files of known sizes (in sectors): 100, 50, 25.
	makeFile := func(name string, sectors int) {
		path := filepath.Join(dir, name)
		buf := make([]byte, sectors*SectorSize)
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	makeFile("a.bin", 100)
	makeFile("b.bin", 50)
	makeFile("c.bin", 25)

	cue := `FILE "a.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
FILE "b.bin" BINARY
  TRACK 02 AUDIO
    INDEX 01 00:00:00
FILE "c.bin" BINARY
  TRACK 03 AUDIO
    INDEX 01 00:00:00
`
	cuePath := filepath.Join(dir, "x.cue")
	if err := os.WriteFile(cuePath, []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveCue(cuePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Tracks) != 3 {
		t.Fatalf("got %d tracks, want 3", len(resolved.Tracks))
	}
	wants := []struct {
		first int32
		size  int64
	}{
		{0, 100 * SectorSize},
		{100, 50 * SectorSize},
		{150, 25 * SectorSize},
	}
	for i, w := range wants {
		if resolved.Tracks[i].FirstLBA != w.first {
			t.Errorf("Tracks[%d].FirstLBA = %d; want %d", i, resolved.Tracks[i].FirstLBA, w.first)
		}
		if resolved.Tracks[i].Size != w.size {
			t.Errorf("Tracks[%d].Size = %d; want %d", i, resolved.Tracks[i].Size, w.size)
		}
	}
	if len(resolved.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(resolved.Files))
	}
}

func TestResolveCue_MissingFile(t *testing.T) {
	dir := t.TempDir()
	cue := `FILE "missing.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
`
	cuePath := filepath.Join(dir, "x.cue")
	if err := os.WriteFile(cuePath, []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveCue(cuePath)
	if err == nil {
		t.Fatal("expected error when referenced file is missing")
	}
}

func TestOpenBinStream_ReadsConcatenated(t *testing.T) {
	dir := t.TempDir()
	type fileSpec struct {
		name    string
		content []byte
	}
	specs := []fileSpec{
		{"a.bin", []byte("aaa")},
		{"b.bin", []byte("bb")},
		{"c.bin", []byte("c")},
	}
	var files []ResolvedFile
	for _, s := range specs {
		path := filepath.Join(dir, s.name)
		if err := os.WriteFile(path, s.content, 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, ResolvedFile{Path: path, Size: int64(len(s.content))})
	}
	r, closer, err := OpenBinStream(files)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "aaabbc" {
		t.Errorf("got %q, want %q", string(got), "aaabbc")
	}
	if err := closer(); err != nil {
		t.Errorf("closer returned %v", err)
	}
}

func TestHashReader_MatchesHashFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "data")
	content := []byte("hello multi-FILE world")
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		t.Fatal(err)
	}
	viaFile, err := hashFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	viaReader, err := hashReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if viaFile != viaReader {
		t.Errorf("hashFile=%v, hashReader=%v", viaFile, viaReader)
	}
}
```

(Add `bytes`, `io`, `os`, `path/filepath` imports to cue_test.go if not present.)

- [ ] **Step 2: Run tests — expect failures (ResolveCue / OpenBinStream / hashReader undefined)**

Run: `go test ./... -run "TestResolveCue|TestOpenBinStream|TestHashReader" -v`
Expected: build failure on undefined symbols.

- [ ] **Step 3: Add helpers to `cue.go`**

Append (end of file, after `parseMSF`):

```go
// CueResolved holds the result of ResolveCue: tracks with their
// absolute FirstLBA, Size, and Filename populated, plus an ordered
// list of files for streaming.
type CueResolved struct {
	Tracks []Track
	Files  []ResolvedFile
}

// ResolvedFile is one .bin file resolved to an absolute path.
type ResolvedFile struct {
	Path string
	Size int64
}

// ResolveCue parses cuePath, resolves each FILE entry's path relative
// to cuePath's directory, stats the file for its size, and computes
// each Track's absolute FirstLBA as the cumulative sum of prior
// files' sectors. Each Track also gets its Size populated from
// os.Stat.
//
// Each Track is associated with exactly one File (one TRACK per FILE
// is enforced by ParseCue).
func ResolveCue(cuePath string) (CueResolved, error) {
	f, err := os.Open(cuePath)
	if err != nil {
		return CueResolved{}, err
	}
	defer f.Close()
	tracks, err := ParseCue(f)
	if err != nil {
		return CueResolved{}, err
	}
	cueDir := filepath.Dir(cuePath)
	var cumulativeLBA int32
	var files []ResolvedFile
	for i := range tracks {
		path := filepath.Join(cueDir, tracks[i].Filename)
		info, err := os.Stat(path)
		if err != nil {
			return CueResolved{}, fmt.Errorf("track %d (%s): %w", tracks[i].Number, tracks[i].Filename, err)
		}
		size := info.Size()
		if size%int64(SectorSize) != 0 {
			return CueResolved{}, fmt.Errorf("track %d (%s) size %d is not a multiple of sector size %d",
				tracks[i].Number, tracks[i].Filename, size, SectorSize)
		}
		tracks[i].FirstLBA = cumulativeLBA
		tracks[i].Size = size
		files = append(files, ResolvedFile{Path: path, Size: size})
		cumulativeLBA += int32(size / int64(SectorSize))
	}
	return CueResolved{Tracks: tracks, Files: files}, nil
}

// OpenBinStream opens every file in cue order and returns an io.Reader
// that yields the concatenated content, plus a closer that closes
// every underlying file. The caller MUST call the closer.
//
// On error during opening, any files already opened are closed before
// returning.
func OpenBinStream(files []ResolvedFile) (io.Reader, func() error, error) {
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("OpenBinStream: empty file list")
	}
	opened := make([]*os.File, 0, len(files))
	closeAll := func() error {
		var firstErr error
		for _, f := range opened {
			if err := f.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
	readers := make([]io.Reader, 0, len(files))
	for _, rf := range files {
		f, err := os.Open(rf.Path)
		if err != nil {
			_ = closeAll()
			return nil, nil, err
		}
		opened = append(opened, f)
		readers = append(readers, f)
	}
	return io.MultiReader(readers...), closeAll, nil
}
```

Add `os`, `path/filepath`, and `io` (already imported) to cue.go's import block. The current imports are `bufio`, `fmt`, `io`, `strconv`, `strings`. Add `os` and `path/filepath`.

- [ ] **Step 4: Extract `hashReader` from `hashFile` in `pack.go`**

Replace `hashFile` (around lines 209-225 — confirm current line range with `grep -n 'func hashFile' pack.go`) with:

```go
// hashReader streams r through MD5, SHA-1, and SHA-256 in a single
// pass and returns all three as lowercase hex.
func hashReader(r io.Reader) (FileHashes, error) {
	m, s1, s256 := md5.New(), sha1.New(), sha256.New()
	w := io.MultiWriter(m, s1, s256)
	if _, err := io.Copy(w, r); err != nil {
		return FileHashes{}, err
	}
	return FileHashes{
		MD5:    hex.EncodeToString(m.Sum(nil)),
		SHA1:   hex.EncodeToString(s1.Sum(nil)),
		SHA256: hex.EncodeToString(s256.Sum(nil)),
	}, nil
}

// hashFile is a thin wrapper around hashReader that opens path.
func hashFile(path string) (FileHashes, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileHashes{}, err
	}
	defer f.Close()
	return hashReader(f)
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./... -run "TestResolveCue|TestOpenBinStream|TestHashReader|TestHashFile" -v
```

Expected: all named tests pass.

- [ ] **Step 6: Run full suite + vet**

Run: `go test ./... && go vet ./...`
Expected: still failing on downstream Pack/Unpack tests (pre-Task-3); vet should be clean.

- [ ] **Step 7: Commit**

```bash
git add cue.go pack.go cue_test.go
git commit -m "$(cat <<'EOF'
cue: add ResolveCue, OpenBinStream; extract hashReader

ResolveCue parses a cue, stats each FILE on disk relative to the
cue's directory, computes each Track's absolute FirstLBA as the
cumulative sum of prior files' sectors, and populates Track.Size
from os.Stat. Returns ordered ResolvedFile list for streaming.

OpenBinStream chains the per-track .bin files via io.MultiReader,
returning the reader plus a close-all func. On open-error, files
already opened are closed before return.

hashReader is extracted from hashFile so multi-bin streams reuse
the same single-pass MD5+SHA1+SHA256 logic. hashFile becomes a
3-line wrapper that opens and delegates.

Foundation for Task 3 (Pack rework using these helpers).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Manifest v0.4 bump + Pack rework with single hashing pass

**Goal:** Bump container version 0x03→0x04 and `FormatVersion: 4`. Update v0.3→v0.4 migration error. Rework Pack to use the cue-driven flow: ResolveCue → single hashing pass over track files (per-track + roll-up via fan-out MultiWriter) → BuildEpsilonHatAndDelta over multi-bin stream → write container with populated track entries + roll-up. PackOptions drops BinPath. Update verifyRoundTrip.

**Files:**
- Modify: `/home/hugh/miniscram/manifest.go`
- Modify: `/home/hugh/miniscram/pack.go`
- Modify: `/home/hugh/miniscram/pack_test.go` (add new tests; rewrite existing)

**Acceptance Criteria:**
- [ ] `containerVersion = 0x04`. v0.3→v0.4 migration message.
- [ ] `Pack(PackOptions{CuePath, ScramPath, OutputPath, ...}, r)` no longer takes BinPath.
- [ ] After Pack on a single-FILE synth disc: manifest has 1 track entry with Size + Filename + per-track hashes; top-level bin_md5/sha1/sha256 == per-track hashes (single-FILE invariant).
- [ ] After Pack on a multi-FILE synth disc (3 tracks): manifest has 3 track entries with their own Size+Filename+hashes; top-level bin hashes equal hash of bytewise concat.
- [ ] `verifyRoundTrip` updated; round-trip self-check still works.
- [ ] Hand-built v0.3 container produces the v0.3→v0.4 migration error.
- [ ] `go test ./...` passes (existing tests adapted to new PackOptions shape).

**Verify:** `go test ./... -v && go vet ./...`

**Steps:**

- [ ] **Step 1: Bump version constants and message in `manifest.go`**

In `/home/hugh/miniscram/manifest.go`:

```go
containerVersion    = byte(0x04)
```

Update the `ReadContainer` migration error (around line 122). Replace `"v0.2 .miniscram files cannot be read"` with:

```go
		return nil, nil, fmt.Errorf("unsupported container version 0x%02x (this build expects 0x%02x); "+
			"v0.3 .miniscram files cannot be read directly by this build — re-pack from the original .bin",
			header[4], containerVersion)
```

- [ ] **Step 2: Update PackOptions in `pack.go` (drop BinPath)**

Find `PackOptions` (around line 25 — `grep -n 'type PackOptions' pack.go`):

```go
type PackOptions struct {
	CuePath          string
	ScramPath        string
	OutputPath       string
	LeadinLBA        int32
	Verify           bool
}
```

Drop `BinPath` if present.

- [ ] **Step 3: Rework `Pack` body**

The new Pack flow:

```go
func Pack(opts PackOptions, r Reporter) error {
	if r == nil {
		r = quietReporter{}
	}

	st := r.Step("running scramble-table self-test")
	if err := CheckScrambleTable(); err != nil {
		st.Fail(err)
		return err
	}
	st.Done("ok")

	// 1. resolve cue (parse + stat + cumulative LBAs).
	st = r.Step("resolving cue " + opts.CuePath)
	resolved, err := ResolveCue(opts.CuePath)
	if err != nil {
		st.Fail(err)
		return err
	}
	tracks := resolved.Tracks
	binSize := int64(0)
	for _, f := range resolved.Files {
		binSize += f.Size
	}
	binSectors := int32(binSize / int64(SectorSize))
	st.Done("%d track(s), %d bytes total", len(tracks), binSize)

	// 2. validate scram size.
	scramInfo, err := os.Stat(opts.ScramPath)
	if err != nil {
		return err
	}
	scramSize := scramInfo.Size()
	if scramSize != binSize {
		return fmt.Errorf("scram size %d != bin size %d (cue ordered concat)", scramSize, binSize)
	}

	// 3. detect write offset.
	st = r.Step("detecting write offset")
	writeOffsetBytes, err := detectWriteOffset(opts.ScramPath, opts.LeadinLBA)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%d bytes", writeOffsetBytes)

	// 4. constant-offset check.
	st = r.Step("checking constant offset")
	if err := checkConstantOffset(opts.ScramPath, scramSize); err != nil {
		st.Fail(err)
		return err
	}
	st.Done("ok")

	// 5. single hashing pass over track files: per-track + disc-level
	//    roll-up via fan-out MultiWriter.
	st = r.Step("hashing tracks")
	rollupMD5, rollupSHA1, rollupSHA256 := md5.New(), sha1.New(), sha256.New()
	rollupWriter := io.MultiWriter(rollupMD5, rollupSHA1, rollupSHA256)
	for i, rf := range resolved.Files {
		f, err := os.Open(rf.Path)
		if err != nil {
			st.Fail(err)
			return err
		}
		trackMD5, trackSHA1, trackSHA256 := md5.New(), sha1.New(), sha256.New()
		w := io.MultiWriter(trackMD5, trackSHA1, trackSHA256, rollupWriter)
		if _, err := io.Copy(w, f); err != nil {
			f.Close()
			st.Fail(err)
			return err
		}
		f.Close()
		tracks[i].MD5 = hex.EncodeToString(trackMD5.Sum(nil))
		tracks[i].SHA1 = hex.EncodeToString(trackSHA1.Sum(nil))
		tracks[i].SHA256 = hex.EncodeToString(trackSHA256.Sum(nil))
	}
	binHashes := FileHashes{
		MD5:    hex.EncodeToString(rollupMD5.Sum(nil)),
		SHA1:   hex.EncodeToString(rollupSHA1.Sum(nil)),
		SHA256: hex.EncodeToString(rollupSHA256.Sum(nil)),
	}
	st.Done("%d track(s) hashed", len(tracks))

	st = r.Step("hashing scram")
	scramHashes, err := hashFile(opts.ScramPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%s", scramHashes.SHA256[:12])

	// 6. build ε̂ + delta in one pass over the multi-bin stream.
	st = r.Step("building ε̂ + delta")
	hatPath, deltaPath, errSectors, deltaSize, err := buildHatAndDelta(opts, resolved.Files, tracks, scramSize, writeOffsetBytes, binSectors)
	if err != nil {
		st.Fail(err)
		return err
	}
	defer os.Remove(deltaPath)
	hatRemoved := false
	defer func() {
		if !hatRemoved {
			_ = os.Remove(hatPath)
		}
	}()
	if err := os.Remove(hatPath); err == nil {
		hatRemoved = true
	}
	st.Done("%d override(s), delta %d bytes", len(errSectors), deltaSize)

	// 7. assemble manifest and write container.
	m := &Manifest{
		FormatVersion:        4,
		ToolVersion:          toolVersion + " (" + runtime.Version() + ")",
		CreatedUTC:           time.Now().UTC().Format(time.RFC3339),
		ScramSize:            scramSize,
		ScramMD5:             scramHashes.MD5,
		ScramSHA1:            scramHashes.SHA1,
		ScramSHA256:          scramHashes.SHA256,
		BinSize:              binSize,
		BinMD5:               binHashes.MD5,
		BinSHA1:              binHashes.SHA1,
		BinSHA256:            binHashes.SHA256,
		WriteOffsetBytes:     writeOffsetBytes,
		LeadinLBA:            opts.LeadinLBA,
		Tracks:               tracks,
		BinFirstLBA:          tracks[0].FirstLBA,
		BinSectorCount:       binSectors,
		ErrorSectors:         errSectors,
		ErrorSectorCount:     len(errSectors),
		DeltaSize:            deltaSize,
		ScramblerTableSHA256: expectedScrambleTableSHA256,
	}

	st = r.Step("writing container")
	deltaF, err := os.Open(deltaPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	if err := WriteContainer(opts.OutputPath, m, deltaF); err != nil {
		deltaF.Close()
		st.Fail(err)
		return err
	}
	deltaF.Close()
	st.Done("%s", opts.OutputPath)

	// 8. verify by round-tripping (unless --no-verify).
	if !opts.Verify {
		r.Warn("verification skipped (--no-verify)")
		return nil
	}
	st = r.Step("verifying round-trip")
	if err := verifyRoundTrip(opts.OutputPath, resolved.Files, m); err != nil {
		st.Fail(err)
		_ = os.Remove(opts.OutputPath)
		return err
	}
	st.Done("ok")
	return nil
}
```

- [ ] **Step 4: Update `buildHatAndDelta` to take the resolved files**

Find `buildHatAndDelta` (around line 350 — `grep -n 'func buildHatAndDelta' pack.go`). Change its signature from `(opts PackOptions, tracks []Track, ...)` to `(opts PackOptions, files []ResolvedFile, tracks []Track, ...)`. Inside, replace the existing single-bin `os.Open(opts.BinPath)` with:

```go
	binReader, closeBin, err := OpenBinStream(files)
	if err != nil {
		// existing cleanup
		return "", "", nil, 0, err
	}
	defer closeBin()
```

Pass `binReader` (which is `io.Reader`) to `BuildEpsilonHatAndDelta` in place of `binFile`. The downstream call shape doesn't change (BuildEpsilonHatAndDelta already takes `io.Reader`).

- [ ] **Step 5: Update `verifyRoundTrip`**

Find `verifyRoundTrip` (around line 470). Update its signature from `(containerPath, binPath string, want *Manifest)` to `(containerPath string, files []ResolvedFile, want *Manifest)` and update the body to use OpenBinStream. The existing logic that calls `Unpack` will need adjusting after Task 4 (Unpack signature changes); for Task 3, simulate by inlining the rebuild — or, simplest, defer the verifyRoundTrip update to Task 4 and temporarily wrap with `if !opts.Verify` to skip. Actually for clarity: keep Verify functional in Task 3 by giving verifyRoundTrip the explicit cue-files and using OpenBinStream + `BuildEpsilonHat` + `ApplyDelta` inline. Update once Task 4 lands.

For Task 3 specifically, replace verifyRoundTrip's body with:

```go
func verifyRoundTrip(containerPath string, files []ResolvedFile, want *Manifest) error {
	// Recovered .scram is the same size as the original — keep it next
	// to the container output.
	tmpOut, err := os.CreateTemp(filepath.Dir(containerPath), "miniscram-verify-*")
	if err != nil {
		return err
	}
	tmpOutPath := tmpOut.Name()
	tmpOut.Close()
	defer os.Remove(tmpOutPath)

	// Build ε̂ from the multi-bin stream into the tempfile.
	hatFile, err := os.OpenFile(tmpOutPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	binReader, closeBin, err := OpenBinStream(files)
	if err != nil {
		hatFile.Close()
		return err
	}
	params := BuildParams{
		LeadinLBA:        want.LeadinLBA,
		WriteOffsetBytes: want.WriteOffsetBytes,
		ScramSize:        want.ScramSize,
		BinFirstLBA:      want.BinFirstLBA,
		BinSectorCount:   want.BinSectorCount,
		Tracks:           want.Tracks,
	}
	if _, err := BuildEpsilonHat(hatFile, params, binReader, nil); err != nil {
		closeBin()
		hatFile.Close()
		return err
	}
	closeBin()
	if err := hatFile.Sync(); err != nil {
		hatFile.Close()
		return err
	}
	hatFile.Close()

	// Apply delta from the container.
	_, deltaBytes, err := ReadContainer(containerPath)
	if err != nil {
		return err
	}
	outFile, err := os.OpenFile(tmpOutPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := ApplyDelta(outFile, bytes.NewReader(deltaBytes)); err != nil {
		outFile.Close()
		return err
	}
	if err := outFile.Sync(); err != nil {
		outFile.Close()
		return err
	}
	outFile.Close()

	// Hash the result and compare against the manifest's recorded scram hashes.
	got, err := hashFile(tmpOutPath)
	if err != nil {
		return err
	}
	wantHashes := FileHashes{MD5: want.ScramMD5, SHA1: want.ScramSHA1, SHA256: want.ScramSHA256}
	if err := compareHashes(got, wantHashes); err != nil {
		return fmt.Errorf("%w: round-trip hash mismatch: %v", errVerifyMismatch, err)
	}
	return nil
}
```

This inlines the rebuild rather than going through Unpack (which still has the old signature in Task 3). Task 4 reworks Unpack; the verifyRoundTrip helper here remains as-is (it's pack-internal).

Add `bytes` to pack.go imports if missing.

- [ ] **Step 6: Update `pack_test.go` — adapt existing tests, add new test**

Existing tests like `TestPackCleanDisc` and `TestPackDetectsNegativeWriteOffset` use `PackOptions{BinPath, CuePath, ScramPath, OutputPath, LeadinLBA, Verify}`. Drop `BinPath`. The synth helper `writeSynthDiscFiles` writes a single-FILE cue with the .bin in the same dir, so `ResolveCue(cuePath)` finds it. Mechanical edit.

Append `TestPackPopulatesPerTrackAndRollupHashes`:

```go
func TestPackPopulatesPerTrackAndRollupHashes(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	_ = binPath // .bin lives next to .cue; ResolveCue finds it via cue
	containerPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	m, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.FormatVersion != 4 {
		t.Errorf("FormatVersion = %d; want 4", m.FormatVersion)
	}
	if len(m.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(m.Tracks))
	}
	tr := m.Tracks[0]
	if tr.MD5 == "" || tr.SHA1 == "" || tr.SHA256 == "" {
		t.Errorf("track hashes empty: %+v", tr)
	}
	if tr.Size == 0 {
		t.Errorf("track size = 0")
	}
	if tr.Filename == "" {
		t.Errorf("track filename empty")
	}
	// Single-FILE: per-track hashes should equal top-level roll-up.
	if tr.MD5 != m.BinMD5 || tr.SHA1 != m.BinSHA1 || tr.SHA256 != m.BinSHA256 {
		t.Errorf("single-FILE: per-track hashes don't match roll-up")
	}
}

func TestReadContainerRejectsV3(t *testing.T) {
	dir := t.TempDir()
	v3 := filepath.Join(dir, "v3.miniscram")
	header := []byte{'M', 'S', 'C', 'M', 0x03, 0, 0, 0, 0}
	if err := os.WriteFile(v3, header, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadContainer(v3)
	if err == nil {
		t.Fatal("expected error reading v0.3 container")
	}
	if !strings.Contains(err.Error(), "v0.3") {
		t.Errorf("error doesn't mention v0.3: %v", err)
	}
}
```

- [ ] **Step 7: Run tests + vet**

Run: `go test ./... -v && go vet ./...`
Expected: pack tests pass; unpack/verify tests still failing (they reference the old single-bin shape — Task 4 fixes them); vet clean.

If unpack/verify tests prevent the package from compiling at all (e.g., because they reference `UnpackOptions{BinPath: ...}` which still exists in Task 3), they should still compile — UnpackOptions hasn't changed yet. Task 4 changes UnpackOptions and rewrites those tests.

The failures at this point should be runtime test failures (round-trip mismatches because Pack now produces v4 but Unpack reads v3-shape), not compile errors.

Actually, since `containerVersion=0x04` and `FormatVersion=4`, any test that builds a v0.3 container by hand or expects ReadContainer to accept 0x03 will fail. Identify these:
- TestCLIInspectRejectsV2 — already exercises the rejection path; update it to write `0x03` and assert "v0.3" message → it becomes TestCLIInspectRejectsV3 (or rename to TestCLIInspectRejectsOlderVersions for stability).
- TestContainerRoundtrip in manifest_test.go — sets FormatVersion: 3; bump to 4.
- The rejection text inside this test — bump from "v0.2" to "v0.3".

Update those mechanically as part of Task 3.

- [ ] **Step 8: Commit**

```bash
git add manifest.go pack.go pack_test.go inspect_test.go manifest_test.go
git commit -m "$(cat <<'EOF'
manifest+pack: bump v0.3→v0.4; per-track + roll-up hashes; cue-driven Pack

PackOptions drops BinPath. Pack now resolves the cue (ResolveCue),
hashes each track file in a single fan-out pass (per-track md5/sha1/
sha256 + disc-level roll-up via shared MultiWriter), and feeds
BuildEpsilonHatAndDelta a multi-bin io.Reader via OpenBinStream.
verifyRoundTrip rebuilds against the resolved files inline (no
Unpack dependency in pack-time round-trip).

Container version byte 0x03→0x04. Manifest.FormatVersion 3→4.
v0.3 containers are rejected with the same migration-error pattern
v0.2 used. Test fixtures (TestContainerRoundtrip, TestCLIInspect-
RejectsV2→V3, etc.) updated mechanically.

Per-track Track{Size, Filename, MD5, SHA1, SHA256} fields populated;
top-level BinMD5/SHA1/SHA256 stays as the whole-disc concat-hash
roll-up (option C from the brainstorm — archivey belt-and-braces,
parity with redump.org templates).

Unpack and Verify reworks come in Task 4 (still single-bin shape;
their tests will fail until then).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Unpack + Verify rework with per-track verification

**Goal:** Unpack and Verify derive bin sources from manifest's track filenames in the container's directory. Stat each file for size; single hashing pass over track files (per-track + roll-up); rebuild via OpenBinStream; final scram hash check. UnpackOptions and VerifyOptions drop BinPath.

**Files:**
- Modify: `/home/hugh/miniscram/unpack.go`
- Modify: `/home/hugh/miniscram/verify.go`
- Modify: `/home/hugh/miniscram/unpack_test.go`
- Modify: `/home/hugh/miniscram/verify_test.go`

**Acceptance Criteria:**
- [ ] `UnpackOptions{ContainerPath, OutputPath, Verify, Force, SuppressVerifyWarning}` (no BinPath).
- [ ] `VerifyOptions{ContainerPath}` (no BinPath).
- [ ] Unpack against a single-FILE container (synth disc) round-trips byte-equal.
- [ ] Tampering one of bin_md5/sha1/sha256 in manifest → errBinHashMismatch (exit 5). Same for any per-track hash in `Tracks[i].MD5/SHA1/SHA256`.
- [ ] Tampering Tracks[0].Size in manifest → errBinHashMismatch (size doesn't match on-disk file).
- [ ] Truncating a track's .bin file on disk → errBinHashMismatch (size mismatch caught at stat).
- [ ] Verify exhibits the same behavior to a tempfile.
- [ ] `go test ./...` PASS, `go vet ./...` clean.

**Verify:** `go test ./... -v && go vet ./...`

**Steps:**

- [ ] **Step 1: Update UnpackOptions in `unpack.go`**

```go
type UnpackOptions struct {
	ContainerPath          string
	OutputPath             string
	Verify                 bool
	Force                  bool
	SuppressVerifyWarning  bool
}
```

- [ ] **Step 2: Rewrite `Unpack` body**

```go
func Unpack(opts UnpackOptions, r Reporter) error {
	if r == nil {
		r = quietReporter{}
	}

	st := r.Step("running scramble-table self-test")
	if err := CheckScrambleTable(); err != nil {
		st.Fail(err)
		return err
	}
	st.Done("ok")

	if !opts.Force {
		if _, err := os.Stat(opts.OutputPath); err == nil {
			return fmt.Errorf("output %s already exists (pass -f / --force to overwrite)", opts.OutputPath)
		}
	}

	st = r.Step("reading container " + opts.ContainerPath)
	m, delta, err := ReadContainer(opts.ContainerPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("manifest %d bytes, delta %d bytes", deltaJSONSize(m), len(delta))

	// Resolve track files relative to the container's directory.
	containerDir := filepath.Dir(opts.ContainerPath)
	files := make([]ResolvedFile, len(m.Tracks))
	for i, tr := range m.Tracks {
		path := filepath.Join(containerDir, tr.Filename)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("track %d (%s): %w", tr.Number, tr.Filename, err)
		}
		if info.Size() != tr.Size {
			return fmt.Errorf("%w: track %d (%s) size on disk %d != manifest %d",
				errBinHashMismatch, tr.Number, tr.Filename, info.Size(), tr.Size)
		}
		files[i] = ResolvedFile{Path: path, Size: tr.Size}
	}

	// Single hashing pass: per-track + disc-level roll-up.
	st = r.Step("verifying bin hashes")
	rollupMD5, rollupSHA1, rollupSHA256 := md5.New(), sha1.New(), sha256.New()
	rollupWriter := io.MultiWriter(rollupMD5, rollupSHA1, rollupSHA256)
	for i, rf := range files {
		f, err := os.Open(rf.Path)
		if err != nil {
			st.Fail(err)
			return err
		}
		trackMD5, trackSHA1, trackSHA256 := md5.New(), sha1.New(), sha256.New()
		w := io.MultiWriter(trackMD5, trackSHA1, trackSHA256, rollupWriter)
		if _, err := io.Copy(w, f); err != nil {
			f.Close()
			st.Fail(err)
			return err
		}
		f.Close()
		got := FileHashes{
			MD5:    hex.EncodeToString(trackMD5.Sum(nil)),
			SHA1:   hex.EncodeToString(trackSHA1.Sum(nil)),
			SHA256: hex.EncodeToString(trackSHA256.Sum(nil)),
		}
		want := FileHashes{MD5: m.Tracks[i].MD5, SHA1: m.Tracks[i].SHA1, SHA256: m.Tracks[i].SHA256}
		if cmpErr := compareHashes(got, want); cmpErr != nil {
			err := fmt.Errorf("%w: track %d (%s): %v", errBinHashMismatch, m.Tracks[i].Number, m.Tracks[i].Filename, cmpErr)
			st.Fail(err)
			return err
		}
	}
	gotRoll := FileHashes{
		MD5:    hex.EncodeToString(rollupMD5.Sum(nil)),
		SHA1:   hex.EncodeToString(rollupSHA1.Sum(nil)),
		SHA256: hex.EncodeToString(rollupSHA256.Sum(nil)),
	}
	wantRoll := FileHashes{MD5: m.BinMD5, SHA1: m.BinSHA1, SHA256: m.BinSHA256}
	if cmpErr := compareHashes(gotRoll, wantRoll); cmpErr != nil {
		err := fmt.Errorf("%w: roll-up: %v", errBinHashMismatch, cmpErr)
		st.Fail(err)
		return err
	}
	st.Done("all tracks + roll-up match")

	// Build ε̂ to a tempfile next to the output path.
	st = r.Step("building ε̂")
	hatFile, err := os.CreateTemp(filepath.Dir(opts.OutputPath), "miniscram-unpack-hat-*")
	if err != nil {
		st.Fail(err)
		return err
	}
	hatPath := hatFile.Name()
	defer os.Remove(hatPath)
	binReader, closeBin, err := OpenBinStream(files)
	if err != nil {
		hatFile.Close()
		st.Fail(err)
		return err
	}
	params := BuildParams{
		LeadinLBA:        m.LeadinLBA,
		WriteOffsetBytes: m.WriteOffsetBytes,
		ScramSize:        m.ScramSize,
		BinFirstLBA:      m.BinFirstLBA,
		BinSectorCount:   m.BinSectorCount,
		Tracks:           m.Tracks,
	}
	if _, err := BuildEpsilonHat(hatFile, params, binReader, nil); err != nil {
		closeBin()
		hatFile.Close()
		st.Fail(err)
		return err
	}
	closeBin()
	if err := hatFile.Sync(); err != nil {
		hatFile.Close()
		st.Fail(err)
		return err
	}
	hatFile.Close()
	st.Done("ok")

	// Move ε̂ into place at OutputPath.
	if err := os.Rename(hatPath, opts.OutputPath); err != nil {
		hatF, oerr := os.Open(hatPath)
		if oerr != nil {
			return oerr
		}
		outF, oerr := os.Create(opts.OutputPath)
		if oerr != nil {
			hatF.Close()
			return oerr
		}
		_, cerr := io.Copy(outF, hatF)
		hatF.Close()
		outF.Close()
		os.Remove(hatPath)
		if cerr != nil {
			return cerr
		}
	}

	// Apply delta in-place.
	st = r.Step("applying delta")
	outFile, err := os.OpenFile(opts.OutputPath, os.O_RDWR, 0)
	if err != nil {
		st.Fail(err)
		return err
	}
	if err := ApplyDelta(outFile, bytes.NewReader(delta)); err != nil {
		outFile.Close()
		st.Fail(err)
		return err
	}
	if err := outFile.Sync(); err != nil {
		outFile.Close()
		st.Fail(err)
		return err
	}
	outFile.Close()
	st.Done("%d byte(s) of delta applied", len(delta))

	// Verify recovered scram hashes (unless skipped).
	if !opts.Verify {
		if !opts.SuppressVerifyWarning {
			r.Warn("verification skipped (--no-verify)")
		}
		return nil
	}
	st = r.Step("verifying output hashes")
	outHashes, err := hashFile(opts.OutputPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	wantOut := FileHashes{MD5: m.ScramMD5, SHA1: m.ScramSHA1, SHA256: m.ScramSHA256}
	if cmpErr := compareHashes(outHashes, wantOut); cmpErr != nil {
		_ = os.Remove(opts.OutputPath)
		err := fmt.Errorf("%w: %v", errOutputHashMismatch, cmpErr)
		st.Fail(err)
		return err
	}
	st.Done("all three match")
	return nil
}
```

Add `crypto/md5`, `crypto/sha1`, `crypto/sha256`, `encoding/hex` to unpack.go imports if missing.

- [ ] **Step 3: Rework `verify.go` — VerifyOptions and Verify body**

```go
type VerifyOptions struct {
	ContainerPath string
}

func Verify(opts VerifyOptions, r Reporter) error {
	if r == nil {
		r = quietReporter{}
	}

	st := r.Step("reading manifest")
	m, _, err := ReadContainer(opts.ContainerPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("ok")

	tmp, err := os.CreateTemp(filepath.Dir(opts.ContainerPath), "miniscram-verify-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	if err := Unpack(UnpackOptions{
		ContainerPath:          opts.ContainerPath,
		OutputPath:             tmpPath,
		Verify:                 false,
		Force:                  true,
		SuppressVerifyWarning:  true,
	}, r); err != nil {
		return err
	}

	st = r.Step("verifying scram hashes")
	got, err := hashFile(tmpPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	wantHashes := FileHashes{MD5: m.ScramMD5, SHA1: m.ScramSHA1, SHA256: m.ScramSHA256}
	if cmpErr := compareHashes(got, wantHashes); cmpErr != nil {
		err := fmt.Errorf("%w: %v", errOutputHashMismatch, cmpErr)
		st.Fail(err)
		return err
	}
	st.Done("all three match")
	return nil
}
```

`m` is unused in this version (we hashed via the manifest inside Unpack). Remove the unused `m` if Go complains, OR keep the read-manifest step for the `verifying scram hashes` comparison.

Actually the `m` is used: `wantHashes := FileHashes{MD5: m.ScramMD5, ...}`. Keep it.

- [ ] **Step 4: Update tests in `unpack_test.go`**

Rewrite `TestUnpackRoundTripSynthDisc` and `TestUnpackRejectsWrongBin` for the new shape:

```go
func TestUnpackRoundTripSynthDisc(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, -48, 10)
	_ = binPath
	containerPath := filepath.Join(dir, "x.miniscram")
	rep := NewReporter(io.Discard, true)
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, rep); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "x.scram.recovered")
	if err := Unpack(UnpackOptions{
		ContainerPath: containerPath,
		OutputPath:    outPath,
		Verify:        true,
	}, rep); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(scramPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip differs (got %d bytes, want %d)", len(got), len(want))
	}
}

func TestUnpackRefusesOverwrite(t *testing.T) {
	_, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	containerPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "exists.scram")
	if err := os.WriteFile(outPath, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Unpack(UnpackOptions{
		ContainerPath: containerPath,
		OutputPath:    outPath,
		Verify:        true, Force: false,
	}, NewReporter(io.Discard, true))
	if err == nil {
		t.Fatal("expected error refusing to overwrite")
	}
}

func TestUnpackRejectsTrackFileSizeMismatch(t *testing.T) {
	_, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	containerPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	// Truncate the .bin file by one sector.
	binPathInDir := filepath.Join(dir, "x.bin")
	info, err := os.Stat(binPathInDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(binPathInDir, info.Size()-int64(SectorSize)); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "x.scram.recovered")
	err = Unpack(UnpackOptions{
		ContainerPath: containerPath,
		OutputPath:    outPath,
		Verify:        true,
	}, NewReporter(io.Discard, true))
	if !errors.Is(err, errBinHashMismatch) {
		t.Fatalf("expected errBinHashMismatch on truncated track, got %v", err)
	}
}

func TestUnpackVerifiesAllThreeBinHashes(t *testing.T) {
	for _, hashName := range []string{"bin_md5", "bin_sha1", "bin_sha256"} {
		t.Run(hashName, func(t *testing.T) {
			_, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
			containerPath := filepath.Join(dir, "x.miniscram")
			if err := Pack(PackOptions{
				CuePath: cuePath, ScramPath: scramPath,
				OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
			}, NewReporter(io.Discard, true)); err != nil {
				t.Fatal(err)
			}
			m, _, err := ReadContainer(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			var target string
			switch hashName {
			case "bin_md5":
				target = m.BinMD5
			case "bin_sha1":
				target = m.BinSHA1
			case "bin_sha256":
				target = m.BinSHA256
			}
			data, err := os.ReadFile(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			idx := bytes.Index(data, []byte(target))
			if idx < 0 {
				t.Fatalf("hash %q not in container", hashName)
			}
			data[idx] ^= 1
			if err := os.WriteFile(containerPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
			outPath := filepath.Join(dir, "out.scram")
			err = Unpack(UnpackOptions{
				ContainerPath: containerPath,
				OutputPath:    outPath,
				Verify:        true,
			}, NewReporter(io.Discard, true))
			if !errors.Is(err, errBinHashMismatch) {
				t.Fatalf("expected errBinHashMismatch tampering %s, got %v", hashName, err)
			}
		})
	}
}

func TestUnpackVerifiesAllThreeOutputHashes(t *testing.T) {
	for _, hashName := range []string{"scram_md5", "scram_sha1", "scram_sha256"} {
		t.Run(hashName, func(t *testing.T) {
			_, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
			containerPath := filepath.Join(dir, "x.miniscram")
			if err := Pack(PackOptions{
				CuePath: cuePath, ScramPath: scramPath,
				OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
			}, NewReporter(io.Discard, true)); err != nil {
				t.Fatal(err)
			}
			m, _, err := ReadContainer(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			var target string
			switch hashName {
			case "scram_md5":
				target = m.ScramMD5
			case "scram_sha1":
				target = m.ScramSHA1
			case "scram_sha256":
				target = m.ScramSHA256
			}
			data, err := os.ReadFile(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			idx := bytes.Index(data, []byte(target))
			if idx < 0 {
				t.Fatalf("hash %q not in container", hashName)
			}
			data[idx] ^= 1
			if err := os.WriteFile(containerPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
			outPath := filepath.Join(dir, "out.scram")
			err = Unpack(UnpackOptions{
				ContainerPath: containerPath,
				OutputPath:    outPath,
				Verify:        true,
			}, NewReporter(io.Discard, true))
			if !errors.Is(err, errOutputHashMismatch) {
				t.Fatalf("expected errOutputHashMismatch tampering %s, got %v", hashName, err)
			}
		})
	}
}
```

Drop the existing `TestUnpackRejectsWrongBin` (the wrong-bin scenario is now subsumed by track size + hash mismatch).

- [ ] **Step 5: Update `verify_test.go`**

`packForVerify` helper updates: drop bin path return, change Pack call.

```go
func packForVerify(t *testing.T) (containerPath, dir string, m *Manifest) {
	t.Helper()
	_, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, -48, 10)
	containerPath = filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	mm, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	return containerPath, dir, mm
}
```

Update all callers. `Verify(VerifyOptions{ContainerPath: ...})` (no BinPath).

The existing per-hash tampering tests (`TestVerifyDetectsScramHashMismatchAllThree` etc.) need their `packForVerify` calls adjusted; the test bodies are otherwise unchanged.

`TestVerifyDetectsTruncatedContainer` — adapt similarly.

- [ ] **Step 6: Run tests + vet**

Run: `go test ./... -v && go vet ./...`
Expected: pack/unpack/verify tests all pass; CLI tests still failing (Task 5 fixes them); vet clean.

- [ ] **Step 7: Commit**

```bash
git add unpack.go verify.go unpack_test.go verify_test.go
git commit -m "$(cat <<'EOF'
unpack+verify: rework for multi-FILE bins and per-track verification

UnpackOptions and VerifyOptions drop BinPath. Unpack derives bin
files from the manifest's per-track filenames in the container's
directory; stat-checks each file's size against Track.Size; runs
a single hashing pass with per-track + disc-level roll-up via
fan-out MultiWriter; rebuilds via OpenBinStream; checks scram
hashes at the end.

Verify wraps Unpack(Verify:false) to a tempfile and compares scram
hashes — mechanically the same shape as before, just without the
BinPath input.

Per-hash tampering tests cover: track md5/sha1/sha256 (each tampered
in isolation); top-level bin md5/sha1/sha256 (each tampered in
isolation); track file size mismatch (truncate on disk). All
exit 5 (errBinHashMismatch).

CLI surface (main.go/help.go) remains unchanged in this commit;
that's Task 5.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: CLI single-positional rewrite + discover.go deletion + inspect display

**Goal:** Rewrite `runPack`/`runUnpack`/`runVerify` to take exactly one positional. Update help texts. Delete `discover.go`. Update `inspect.go` to show per-track entries with their hashes/filename/size.

**Files:**
- Modify: `/home/hugh/miniscram/main.go`
- Modify: `/home/hugh/miniscram/help.go`
- Delete: `/home/hugh/miniscram/discover.go`
- Modify: `/home/hugh/miniscram/inspect.go`
- Modify: `/home/hugh/miniscram/inspect_test.go`
- Modify: `/home/hugh/miniscram/main_test.go` (rewrite TestCLIPackDiscovers → TestCLIPackSinglePositional)

**Acceptance Criteria:**
- [ ] `pack <cue>`, `unpack <miniscram>`, `verify <miniscram>` each accept exactly one positional.
- [ ] Zero or two-or-more positionals → exit 1 with usage error.
- [ ] Default output: pack → `<cue-stem>.miniscram` next to cue. Unpack → `<container-stem>.scram` next to container.
- [ ] `inspect` displays per-track filename/size/md5/sha1/sha256 for every track.
- [ ] `discover.go` is gone; nothing references its symbols.
- [ ] Top-level help unchanged in shape; per-subcommand help texts updated.
- [ ] `go test ./...` PASS, `go vet ./...` clean, `go build ./...` succeeds.

**Verify:** `go test ./... -v && go vet ./... && go build -o /tmp/miniscram-b15-smoke ./... && rm /tmp/miniscram-b15-smoke`

**Steps:**

- [ ] **Step 1: Delete `discover.go`**

```bash
rm /home/hugh/miniscram/discover.go
```

- [ ] **Step 2: Add inline default-output helpers in `main.go`**

Insert near the existing helpers (next to `pickFirst` etc.):

```go
func DefaultPackOutput(cuePath string) string {
	return strings.TrimSuffix(cuePath, filepath.Ext(cuePath)) + ".miniscram"
}

func DefaultUnpackOutput(containerPath string) string {
	return strings.TrimSuffix(containerPath, filepath.Ext(containerPath)) + ".scram"
}
```

Add `path/filepath` to main.go's imports if not already present.

- [ ] **Step 3: Rewrite `runPack`**

Replace the existing `runPack` body. The new flow takes exactly one positional (the cue path), derives scram path from cue stem, derives output path from cue stem unless `-o` is given.

```go
func runPack(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	output := fs.String("o", "", "output path (alias --output)")
	outputLong := fs.String("output", "", "output path")
	keepSource := fs.Bool("keep-source", false, "do not remove .scram after verification")
	noVerify := fs.Bool("no-verify", false, "skip inline round-trip verification")
	allowCrossFS := fs.Bool("allow-cross-fs", false, "allow auto-delete across filesystems")
	force := fs.Bool("f", false, "overwrite output if it exists (alias --force)")
	forceLong := fs.Bool("force", false, "overwrite output if it exists")
	quiet := fs.Bool("q", false, "suppress progress (alias --quiet)")
	quietLong := fs.Bool("quiet", false, "suppress progress")
	help := fs.Bool("help", false, "show help for pack")
	helpShort := fs.Bool("h", false, "show help for pack")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *help || *helpShort {
		printPackHelp(stderr)
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "expected exactly one positional argument (cue path); got %d\n", fs.NArg())
		printPackHelp(stderr)
		return exitUsage
	}
	cuePath := fs.Arg(0)
	scramPath := strings.TrimSuffix(cuePath, filepath.Ext(cuePath)) + ".scram"
	out := pickFirst(*output, *outputLong)
	if out == "" {
		out = DefaultPackOutput(cuePath)
	}
	beQuiet := *quiet || *quietLong
	beForce := *force || *forceLong

	if !beForce {
		if _, err := os.Stat(out); err == nil {
			fmt.Fprintf(stderr, "output %s already exists (pass -f / --force to overwrite)\n", out)
			return exitUsage
		}
	}
	noVerifyImpliesKeep := *noVerify && !*keepSource
	if *noVerify {
		*keepSource = true
	}
	rep := NewReporter(stderr, beQuiet)
	if noVerifyImpliesKeep {
		rep.Info("--no-verify implies --keep-source; original .scram will be kept")
	}
	err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath, OutputPath: out,
		Verify: !*noVerify,
	}, rep)
	if err != nil {
		return packErrorToExit(err)
	}
	if !*keepSource {
		if removed, removeErr := maybeRemoveSource(scramPath, out, *allowCrossFS, rep); removeErr != nil {
			rep.Warn("source removal skipped: %v", removeErr)
		} else if removed {
			rep.Info("removed source %s", scramPath)
		}
	}
	return exitOK
}
```

The `LeadinLBA` field is unused here (defaulted to zero). Real Redumper output expects `LBALeadinStart` (-45150); the existing CLI didn't expose this either, defaulting via the synth path. Confirm by reading the current Pack flow for any LeadinLBA defaulting; if `Pack` requires it explicitly, set it in PackOptions: `LeadinLBA: LBALeadinStart` for real-disc CLI use.

Actually, looking at the existing `runPack` (pre-Task-5): it doesn't set LeadinLBA explicitly; it falls through to `Pack` which uses `opts.LeadinLBA` (zero by default). For real Redumper output to work, the CLI must set `LeadinLBA: LBALeadinStart`. Add that:

```go
	err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath, OutputPath: out,
		LeadinLBA: LBALeadinStart,
		Verify: !*noVerify,
	}, rep)
```

(Tests pass `LBAPregapStart` explicitly; the CLI defaults to `LBALeadinStart` for Redumper input.)

- [ ] **Step 4: Rewrite `runUnpack`**

```go
func runUnpack(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("unpack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	output := fs.String("o", "", "output path")
	outputLong := fs.String("output", "", "output path")
	noVerify := fs.Bool("no-verify", false, "skip output hash verification")
	force := fs.Bool("f", false, "overwrite output")
	forceLong := fs.Bool("force", false, "overwrite output")
	quiet := fs.Bool("q", false, "quiet")
	quietLong := fs.Bool("quiet", false, "quiet")
	help := fs.Bool("help", false, "show help for unpack")
	helpShort := fs.Bool("h", false, "show help for unpack")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *help || *helpShort {
		printUnpackHelp(stderr)
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "expected exactly one positional argument (container path); got %d\n", fs.NArg())
		printUnpackHelp(stderr)
		return exitUsage
	}
	containerPath := fs.Arg(0)
	out := pickFirst(*output, *outputLong)
	if out == "" {
		out = DefaultUnpackOutput(containerPath)
	}
	beQuiet := *quiet || *quietLong
	beForce := *force || *forceLong

	rep := NewReporter(stderr, beQuiet)
	err := Unpack(UnpackOptions{
		ContainerPath: containerPath, OutputPath: out,
		Verify: !*noVerify, Force: beForce,
	}, rep)
	if err != nil {
		return unpackErrorToExit(err)
	}
	return exitOK
}
```

- [ ] **Step 5: Rewrite `runVerify`**

```go
func runVerify(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	quiet := fs.Bool("q", false, "quiet")
	quietLong := fs.Bool("quiet", false, "quiet")
	help := fs.Bool("help", false, "show help for verify")
	helpShort := fs.Bool("h", false, "show help for verify")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *help || *helpShort {
		printVerifyHelp(stderr)
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "expected exactly one positional argument (container path); got %d\n", fs.NArg())
		printVerifyHelp(stderr)
		return exitUsage
	}
	containerPath := fs.Arg(0)
	beQuiet := *quiet || *quietLong
	rep := NewReporter(stderr, beQuiet)
	if err := Verify(VerifyOptions{ContainerPath: containerPath}, rep); err != nil {
		return verifyErrorToExit(err)
	}
	return exitOK
}
```

- [ ] **Step 6: Rewrite help texts in `help.go`**

Replace `packHelpText`, `unpackHelpText`, `verifyHelpText` with:

```go
const packHelpText = `USAGE:
    miniscram pack <cue> [-o <out.miniscram>] [options]

ARGUMENTS:
    <cue>    path to the cuesheet (Redumper *.cue). The .scram file
             is derived from the cue's stem (<stem>.scram in the
             same directory).

OPTIONS:
    -o, --output <path>    where to write the .miniscram container.
                           default: <cue-stem>.miniscram next to <cue>.
    -f, --force            overwrite existing output.
    --keep-source          do not remove <scram> after verified pack.
    --no-verify            skip inline round-trip verification.
                           implies --keep-source.
    --allow-cross-fs       permit auto-delete of <scram> when <out>
                           is on a different filesystem.
    -q, --quiet            suppress progress output.
    -h, --help             show this help.
`

const unpackHelpText = `USAGE:
    miniscram unpack <in.miniscram> [-o <out.scram>] [options]

ARGUMENTS:
    <in.miniscram>    .miniscram container produced by 'miniscram pack'.
                      The track .bin files referenced by the manifest
                      must exist in the same directory as the container.

OPTIONS:
    -o, --output <path>    where to write the reconstructed .scram.
                           default: <miniscram-stem>.scram next to
                           <in.miniscram>.
    -f, --force            overwrite existing output.
    --no-verify            skip output hash verification (md5/sha1/sha256).
    -q, --quiet            suppress progress output.
    -h, --help             show this help.
`

const verifyHelpText = `USAGE:
    miniscram verify <in.miniscram> [options]

ARGUMENTS:
    <in.miniscram>    .miniscram container produced by 'miniscram pack'.
                      Track .bin files must exist in the same directory.

OPTIONS:
    -q, --quiet       suppress progress output.
    -h, --help        show this help.

DESCRIPTION:
    Rebuilds the original .scram in a temporary file, hashes it
    (md5 + sha1 + sha256), compares against the container's recorded
    hashes, and deletes the temporary file. Used to confirm a
    .miniscram still decodes correctly without producing a
    multi-hundred-MB output.

EXIT CODES:
    0    success
    1    usage / input error
    3    verification failed (one or more of md5/sha1/sha256 mismatched)
    4    I/O error
    5    wrong .bin (one or more recorded hashes mismatched)
`
```

- [ ] **Step 7: Update `inspect.go`'s `formatHumanInspect`**

Find the `tracks:` section in `formatHumanInspect` (around line 41-50). Replace with a richer per-track display:

```go
	b.WriteString("tracks:\n")
	maxMode := 0
	for _, t := range m.Tracks {
		if len(t.Mode) > maxMode {
			maxMode = len(t.Mode)
		}
	}
	for _, t := range m.Tracks {
		fmt.Fprintf(&b, "  track %d: %-*s  first_lba=%d  size=%d  filename=%s\n",
			t.Number, maxMode, t.Mode, t.FirstLBA, t.Size, t.Filename)
		fmt.Fprintf(&b, "    md5:    %s\n", t.MD5)
		fmt.Fprintf(&b, "    sha1:   %s\n", t.SHA1)
		fmt.Fprintf(&b, "    sha256: %s\n", t.SHA256)
	}
```

- [ ] **Step 8: Update `inspect_test.go` and `main_test.go`**

In `inspect_test.go`:
- Existing tests that check tracks output (`TestInspectFormatHumanCleanDelta`, `TestInspectFormatHumanTrackPadding`) — keep but extend assertions to verify the new per-track lines (md5/sha1/sha256 substrings appear).
- `TestInspectShowsAllSixHashes` already exists — it only checks top-level lines. Extend to also assert per-track hash substrings appear.
- Existing `sampleManifest()` helper produces `Tracks: []Track{{Number: 1, Mode: "MODE1/2352", FirstLBA: 0}}`. Update to also set `Size`, `Filename`, `MD5`, `SHA1`, `SHA256` to non-empty placeholders so the inspect tests find the new lines.

In `main_test.go`:
- Drop `TestCLIPackDiscovers` (cwd discovery is gone).
- Add:
  ```go
  func TestCLIPackRequiresOnePositional(t *testing.T) {
      var stderr bytes.Buffer
      // Zero positionals.
      code := run([]string{"pack"}, io.Discard, &stderr)
      if code != exitUsage {
          t.Fatalf("zero positionals exit %d, want %d", code, exitUsage)
      }
      // Two positionals.
      stderr.Reset()
      code = run([]string{"pack", "a.cue", "b.scram"}, io.Discard, &stderr)
      if code != exitUsage {
          t.Fatalf("two positionals exit %d, want %d", code, exitUsage)
      }
  }
  ```
- Same for unpack and verify.

- [ ] **Step 9: Update `e2e_redump_test.go` for the new pack signature**

`TestE2ERoundTripRealDiscs` calls `Pack` with the old `BinPath` field. Update:

```go
			if err := Pack(PackOptions{
				CuePath:    cuePath,
				ScramPath:  scramPath,
				OutputPath: containerPath,
				Verify:     true,
			}, rep); err != nil {
```

(LeadinLBA defaults to zero in this test path; if real Redumper data needs LBALeadinStart, set it explicitly.)

`TestE2ERoundTripRealDiscs` also calls `Unpack` with `BinPath`. Update to drop it:

```go
			if err := Unpack(UnpackOptions{
				ContainerPath: containerPath,
				OutputPath:    outPath,
				Verify:        true,
			}, rep); err != nil {
```

- [ ] **Step 10: Run tests + vet + build**

```bash
go test ./... -v && go vet ./... && go build -o /tmp/miniscram-b15-smoke ./...
```

Expected: full suite green; vet clean; binary builds.

Smoke check:
```bash
/tmp/miniscram-b15-smoke pack --help
/tmp/miniscram-b15-smoke unpack --help
/tmp/miniscram-b15-smoke verify --help
rm /tmp/miniscram-b15-smoke
```

Help texts should reflect the new single-positional shape.

- [ ] **Step 11: Commit**

```bash
git add main.go help.go inspect.go inspect_test.go main_test.go e2e_redump_test.go
git rm discover.go
git commit -m "$(cat <<'EOF'
cli: drop cwd-discovery; every subcommand takes one explicit positional

pack <cue>, unpack <miniscram>, verify <miniscram> all take exactly
one positional now. Zero or multiple positionals → exit 1. Default
output paths derive from the input's stem (<cue-stem>.miniscram for
pack, <container-stem>.scram for unpack); -o / --output overrides.

discover.go is deleted (DiscoverPack, DiscoverUnpack, DiscoverPackFromArg,
DiscoverUnpackFromArg, uniqueByExt, stripKnownExt all gone).
DefaultPackOutput / DefaultUnpackOutput moved inline to main.go.

inspect.go's formatHumanInspect now displays per-track filename,
size, md5, sha1, sha256 for every track. JSON output already
included these fields automatically via Manifest.Marshal.

CLI tests rewritten: TestCLIPackDiscovers → TestCLIPackRequires-
OnePositional + analogues for unpack/verify.

Closes B1.5 (multi-FILE .cue support). Unblocks B3 (HL1 e2e fixture
can now slot into realDiscFixtures with one struct literal).

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** All seven goals + non-goals from the spec map to tasks. Track extension → Task 1. ResolveCue + OpenBinStream + hashReader → Task 2. Pack rework + version bump + per-track hashing → Task 3. Unpack/Verify rework + tampering tests → Task 4. CLI simplification + inspect display + discover.go deletion → Task 5.
- **Placeholder scan:** No TBDs. Two hedges remain: (a) "find the existing helpers" / "around line N" — these are robust against minor file shifts since the implementer can grep. (b) `LeadinLBA: LBALeadinStart` decision — flagged explicitly with reasoning; implementer should set it for CLI Pack invocations to support real Redumper output.
- **Type consistency:** `FileHashes`, `hashReader`, `ResolvedFile`, `CueResolved`, all defined in earlier tasks and used in later ones. PackOptions / UnpackOptions / VerifyOptions changes ripple correctly: Task 3 changes PackOptions; Task 4 changes UnpackOptions and VerifyOptions; Task 5 wires the CLI to the new shapes.
- **Test ordering:** Tasks 3 and 4 tolerate intermediate test failures (downstream code references the old single-bin shape). Task 5 closes the loop; full suite must be green by Task 5's commit.
- **Single-FILE invariant:** spelled out in Task 3's acceptance criteria — when a cue has one FILE, the per-track entry's hashes equal the top-level roll-up. This is the bridge between old and new schemas; existing single-FILE tests pass through unchanged once the new shape is honored.
