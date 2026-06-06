# Combined (multi-track-per-FILE) Cuesheet Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let miniscram `pack`/`unpack`/`verify` natively ingest combined multi-track-per-FILE cuesheets (the layout MAME's `chdman createcd`/`extractcd` produces — all tracks in one `.bin`), in addition to redumper's one-track-per-FILE output.

**Architecture:** Generalize a `Track` from "a whole file" to "a byte range `[FileOffset, FileOffset+Size)` within a file." `ParseCue` stops rejecting multiple TRACKs per FILE and captures each track's lowest INDEX frame; `ResolveCue` derives per-track byte ranges within a shared file from those frames; hashing becomes range-based. The scramble/prediction/delta pipeline is untouched because it already runs off the concatenated byte stream plus per-track `(FirstLBA, Mode)`. No container-format change: `FileOffset` is transient (re-derived at read time), so this is a MINOR release.

**Tech Stack:** Go (single `package main`), standard library only (`crypto/*`, `io`, `os`, `path/filepath`). Tests use `testing` with the existing `synthDisc`/`writeFixture` helpers in `fixtures_test.go`.

**Design reference:** `docs/superpowers/specs/2026-06-06-combined-cuesheet-support-design.md`

---

## File Structure

- `cue.go` — `Track` struct (+ transient `IndexFrame`, `FileOffset`); `ParseCue` (accept multi-track FILE, capture lowest INDEX frame); `ResolveCue` (group-by-FILE, combined offset/size/FirstLBA derivation + validation).
- `pack.go` — new `hashTracks` (range-based, replaces `hashTrackFiles`); wire `Pack` to it.
- `manifest.go` — new `assignFileOffsets` (re-derive offsets from per-track `Size` at read time).
- `unpack.go` — dedup files sharing a `Filename`, call `assignFileOffsets`, range-based hash verification, per-file size check.
- `cue_test.go` — ParseCue accept/reject updates; ResolveCue combined + rejection tests.
- `pack_test.go` — `hashTracks`/`assignFileOffsets` unit tests; combined pack→unpack round-trip.
- `fixtures_test.go` — new `writeCombinedFixture` + `framesToMSF` helpers.
- `property_test.go` — split-vs-combined container-agreement property test.
- `e2e_redump_test.go` — combined fixture row + `FileOffset`-aware helper fixes (guarded by `//go:build redump_data`).

---

## Task 1: Accept multi-track-per-FILE in ParseCue; capture lowest INDEX frame

**Files:**
- Modify: `cue.go` (Track struct ~36-44; ParseCue ~94-218)
- Test: `cue_test.go` (TestParseCueRejects ~69-90; add an accept test)

- [ ] **Step 1: Update the failing reject test to an accept test**

In `cue_test.go`, `TestParseCueRejects` has this case (around line 80) — **delete the entire line**:

```go
		{"multi-track-per-file", "FILE \"shared.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n  TRACK 02 AUDIO\n    INDEX 01 02:00:00\n"},
```

Then add this new test function at the end of `cue_test.go`:

```go
func TestParseCueAcceptsMultiTrackPerFile(t *testing.T) {
	src := "FILE \"combined.bin\" BINARY\n" +
		"  TRACK 01 MODE1/2352\n" +
		"    INDEX 01 00:00:00\n" +
		"  TRACK 02 AUDIO\n" +
		"    INDEX 00 00:09:00\n" +
		"    INDEX 01 00:11:00\n"
	got, err := ParseCue(strings.NewReader(src))
	if err != nil {
		t.Fatalf("ParseCue: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tracks, want 2", len(got))
	}
	if got[0].Filename != "combined.bin" || got[1].Filename != "combined.bin" {
		t.Errorf("both tracks should share Filename combined.bin; got %q, %q", got[0].Filename, got[1].Filename)
	}
	// IndexFrame is the lowest INDEX of each track (file-relative frames).
	// Track 1: INDEX 01 00:00:00 = 0. Track 2: lowest is INDEX 00 00:09:00 =
	// 9*75 = 675.
	if got[0].IndexFrame != 0 {
		t.Errorf("track 1 IndexFrame = %d, want 0", got[0].IndexFrame)
	}
	if got[1].IndexFrame != 675 {
		t.Errorf("track 2 IndexFrame = %d, want 675 (INDEX 00 00:09:00)", got[1].IndexFrame)
	}
}
```

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./... -run 'TestParseCueAcceptsMultiTrackPerFile|TestParseCueRejects' -v`
Expected: `TestParseCueAcceptsMultiTrackPerFile` FAILs — ParseCue returns the "more than one TRACK" error (and `IndexFrame` field does not yet exist, so it may also fail to compile; that is expected before Step 3).

- [ ] **Step 3: Add transient fields to Track**

In `cue.go`, extend the `Track` struct (currently ends at the `Hashes` field) by adding two transient fields. The full struct becomes:

```go
// Track is a single track entry in a cuesheet, augmented with
// filesystem metadata at pack time.
type Track struct {
	Number   int        `json:"number"`
	Session  int        `json:"session,omitempty"`
	Mode     string     `json:"mode"`
	FirstLBA int32      `json:"first_lba"`
	Filename string     `json:"filename"`
	Size     int64      `json:"size"`
	Hashes   FileHashes `json:"hashes"`

	// IndexFrame is the file-relative frame number of this track's lowest
	// INDEX (INDEX 00 if present, else INDEX 01), captured by ParseCue.
	// Transient: consumed by ResolveCue to derive FileOffset; never
	// serialized (the TRKS chunk codec ignores it).
	IndexFrame int32 `json:"-"`
	// FileOffset is the byte offset of this track's data within Filename.
	// 0 for one-track-per-FILE cues. Transient: re-derived at read time
	// by ResolveCue (pack) or assignFileOffsets (unpack); never serialized.
	FileOffset int64 `json:"-"`
}
```

- [ ] **Step 4: Capture the lowest INDEX frame and drop the multi-track rejection in ParseCue**

In `cue.go`, `ParseCue`:

(a) Replace the declarations near the top of the function body (currently `var cur *Track` / `var hasIndex01 bool` / `var currentFile string` / `var fileTrackCount int`) with — note `fileTrackCount` is removed and two trackers are added:

```go
	var tracks []Track
	var cur *Track
	var hasIndex01 bool
	var curMinFrame int32 // lowest INDEX frame seen for cur (file-relative)
	var curHasIndex bool  // whether any INDEX line was seen for cur
	var currentFile string // basename of the most recent FILE line
	currentSession := 1   // bumped by REM SESSION NN; stamped on every TRACK
```

(b) Replace the `flushTrack` closure so it stamps `IndexFrame` before appending:

```go
	flushTrack := func() error {
		if cur == nil {
			return nil
		}
		if !hasIndex01 {
			return fmt.Errorf("track %d has no INDEX 01", cur.Number)
		}
		cur.IndexFrame = curMinFrame
		tracks = append(tracks, *cur)
		return nil
	}
```

(c) In the `case "FILE":` arm, delete the line `fileTrackCount = 0` (the only remaining reference to the removed variable in that arm).

(d) In the `case "TRACK":` arm, delete these now-obsolete lines:

```go
			fileTrackCount++
			if fileTrackCount > 1 {
				return nil, fmt.Errorf("FILE %q contains more than one TRACK; multi-track-per-FILE cues are unsupported", currentFile)
			}
```

and, after the line `cur = &Track{Number: n, Mode: mode, Filename: currentFile, Session: currentSession}` and its `hasIndex01 = false`, reset the new trackers:

```go
			cur = &Track{Number: n, Mode: mode, Filename: currentFile, Session: currentSession}
			hasIndex01 = false
			curMinFrame = 0
			curHasIndex = false
```

(e) Replace the `case "INDEX":` arm body (currently it skips non-`01` indices and parses MSF only for validation) with one that parses every INDEX and tracks the minimum:

```go
		case "INDEX":
			if cur == nil {
				return nil, fmt.Errorf("INDEX before TRACK: %q", line)
			}
			if len(fields) < 3 {
				return nil, fmt.Errorf("malformed INDEX line: %q", line)
			}
			frame, err := parseMSF(fields[2])
			if err != nil {
				return nil, fmt.Errorf("bad MSF in %q: %v", line, err)
			}
			if !curHasIndex || frame < curMinFrame {
				curMinFrame = frame
				curHasIndex = true
			}
			if fields[1] == "01" {
				hasIndex01 = true
			}
```

(f) Update the doc comment above `ParseCue`: change the sentence describing the multi-track rejection. Replace:

```go
// Rejects non-BINARY FILE types, path-bearing filenames (containing
// any of `/`, `\`, `..`), and cues where a single FILE contains more
// than one TRACK (Redumper never produces this shape).
```

with:

```go
// Rejects non-BINARY FILE types and path-bearing filenames (containing
// any of `/`, `\`, `..`). A FILE may contain multiple TRACKs (combined
// chdman-style layout); each track records its lowest INDEX frame in
// IndexFrame for ResolveCue to turn into a within-file byte range.
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./... -run 'TestParseCue' -v`
Expected: PASS — including `TestParseCueAcceptsMultiTrackPerFile`, `TestParseCueAccepts`, `TestParseCueRejects`, `TestParseCueRemSession`.

- [ ] **Step 6: Commit**

```bash
git add cue.go cue_test.go
git commit -m "feat: parse multi-track-per-FILE cues, capture lowest INDEX frame

ParseCue no longer rejects combined (chdman-style) cuesheets. Each track
records its lowest INDEX frame (transient IndexFrame) for ResolveCue to
turn into a within-file byte range.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Derive per-track byte ranges in ResolveCue

**Files:**
- Modify: `cue.go` (`ResolveCue` ~276-306)
- Test: `cue_test.go` (add combined resolve + rejection tests)

- [ ] **Step 1: Write the failing test**

Add to `cue_test.go`:

```go
func TestResolveCueCombined(t *testing.T) {
	dir := t.TempDir()
	// Combined bin: 4-sector data track + 3-sector audio track = 7 sectors.
	const dataSectors, audioSectors = 4, 3
	combined := make([]byte, (dataSectors+audioSectors)*SectorSize)
	if err := os.WriteFile(filepath.Join(dir, "combined.bin"), combined, 0o644); err != nil {
		t.Fatal(err)
	}
	// Audio track starts at file-frame 4 (00:00:04).
	cue := "FILE \"combined.bin\" BINARY\n" +
		"  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n" +
		"  TRACK 02 AUDIO\n    INDEX 01 00:00:04\n"
	cuePath := filepath.Join(dir, "combined.cue")
	if err := os.WriteFile(cuePath, []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveCue(cuePath)
	if err != nil {
		t.Fatalf("ResolveCue: %v", err)
	}
	if len(resolved.Files) != 1 {
		t.Fatalf("Files = %d, want 1 (combined cue has one FILE)", len(resolved.Files))
	}
	want := []Track{
		{Number: 1, FileOffset: 0, Size: dataSectors * SectorSize, FirstLBA: 0},
		{Number: 2, FileOffset: dataSectors * SectorSize, Size: audioSectors * SectorSize, FirstLBA: dataSectors},
	}
	for i, w := range want {
		got := resolved.Tracks[i]
		if got.FileOffset != w.FileOffset || got.Size != w.Size || got.FirstLBA != w.FirstLBA {
			t.Errorf("track %d: got FileOffset=%d Size=%d FirstLBA=%d; want FileOffset=%d Size=%d FirstLBA=%d",
				got.Number, got.FileOffset, got.Size, got.FirstLBA, w.FileOffset, w.Size, w.FirstLBA)
		}
	}
}

func TestResolveCueCombinedRejects(t *testing.T) {
	cases := []struct {
		name      string
		cue       string
		fileSects int // combined.bin size in sectors
	}{
		{
			name: "first-track-not-at-zero",
			cue: "FILE \"combined.bin\" BINARY\n" +
				"  TRACK 01 MODE1/2352\n    INDEX 01 00:00:02\n" +
				"  TRACK 02 AUDIO\n    INDEX 01 00:00:04\n",
			fileSects: 7,
		},
		{
			name: "non-monotonic-index",
			cue: "FILE \"combined.bin\" BINARY\n" +
				"  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n" +
				"  TRACK 02 AUDIO\n    INDEX 01 00:00:00\n",
			fileSects: 7,
		},
		{
			name: "index-beyond-file",
			cue: "FILE \"combined.bin\" BINARY\n" +
				"  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n" +
				"  TRACK 02 AUDIO\n    INDEX 01 00:01:00\n", // frame 75 >> 7-sector file
			fileSects: 7,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "combined.bin"), make([]byte, tc.fileSects*SectorSize), 0o644); err != nil {
				t.Fatal(err)
			}
			cuePath := filepath.Join(dir, "combined.cue")
			if err := os.WriteFile(cuePath, []byte(tc.cue), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ResolveCue(cuePath); err == nil {
				t.Fatalf("ResolveCue accepted invalid combined cue %q", tc.name)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run 'TestResolveCueCombined' -v`
Expected: FAIL — `TestResolveCueCombined` reports wrong `FileOffset`/`Size` (the current loop assigns one file per track and ignores INDEX), and `TestResolveCueCombinedRejects` cases are accepted.

- [ ] **Step 3: Rewrite ResolveCue to group by FILE**

In `cue.go`, replace the entire body of `ResolveCue` (from `cueDir := filepath.Dir(cuePath)` through the `return CueResolved{...}` at the end) with:

```go
	cueDir := filepath.Dir(cuePath)
	var files []ResolvedFile
	var cumulativeLBA int32

	// ParseCue emits tracks in cue order; a change of Filename starts a
	// new FILE group. Each group maps to exactly one .bin on disk.
	for i := 0; i < len(tracks); {
		fname := tracks[i].Filename
		j := i
		for j < len(tracks) && tracks[j].Filename == fname {
			j++
		}
		group := tracks[i:j] // sub-slice shares backing array with tracks

		path := filepath.Join(cueDir, fname)
		info, err := os.Stat(path)
		if err != nil {
			return CueResolved{}, fmt.Errorf("track %d (%s): %w", group[0].Number, fname, err)
		}
		fileSize := info.Size()
		if fileSize%int64(SectorSize) != 0 {
			return CueResolved{}, fmt.Errorf("file %s size %d is not a multiple of sector size %d",
				fname, fileSize, SectorSize)
		}
		files = append(files, ResolvedFile{Path: path, Size: fileSize})
		fileSectors := int32(fileSize / int64(SectorSize))

		if len(group) == 1 {
			// One TRACK per FILE (Redumper convention): the whole file is
			// the track. Identical to the original behaviour.
			group[0].FileOffset = 0
			group[0].Size = fileSize
			group[0].FirstLBA = cumulativeLBA
		} else {
			// Multiple TRACKs in one FILE (combined / chdman layout): derive
			// each track's byte range from its lowest INDEX frame. The data
			// track must begin at the file start; INDEX frames must strictly
			// increase and stay within the file. Contiguous derivation means
			// the spans tile the file exactly (no gaps or overlaps).
			if group[0].IndexFrame != 0 {
				return CueResolved{}, fmt.Errorf("file %s: first track %d does not start at offset 0 (lowest INDEX frame %d)",
					fname, group[0].Number, group[0].IndexFrame)
			}
			for k := range group {
				if k > 0 && group[k].IndexFrame <= group[k-1].IndexFrame {
					return CueResolved{}, fmt.Errorf("file %s: track %d INDEX frame %d not greater than previous %d",
						fname, group[k].Number, group[k].IndexFrame, group[k-1].IndexFrame)
				}
				if group[k].IndexFrame >= fileSectors {
					return CueResolved{}, fmt.Errorf("file %s: track %d INDEX frame %d is beyond file length (%d sectors)",
						fname, group[k].Number, group[k].IndexFrame, fileSectors)
				}
				group[k].FileOffset = int64(group[k].IndexFrame) * int64(SectorSize)
				group[k].FirstLBA = cumulativeLBA + group[k].IndexFrame
			}
			for k := range group {
				endFrame := fileSectors
				if k+1 < len(group) {
					endFrame = group[k+1].IndexFrame
				}
				group[k].Size = int64(endFrame-group[k].IndexFrame) * int64(SectorSize)
			}
		}
		cumulativeLBA += fileSectors
		i = j
	}
	return CueResolved{Tracks: tracks, Files: files}, nil
```

Also update the `ResolveCue` doc comment. Replace:

```go
// Each Track is associated with exactly one File (one TRACK per FILE
// is enforced by ParseCue).
```

with:

```go
// A FILE may hold one TRACK (whole-file = track, the Redumper case) or
// several (combined chdman layout), in which case each track's
// FileOffset/Size are derived from the lowest INDEX frames and validated
// to tile the file exactly.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run 'TestResolveCue' -v`
Expected: PASS — `TestResolveCue` (existing single-track), `TestResolveCueCombined`, and all `TestResolveCueCombinedRejects` sub-tests.

- [ ] **Step 5: Run the full cue + manifest suite for regressions**

Run: `go test ./... -run 'Cue|Resolve|OpenBinStream' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cue.go cue_test.go
git commit -m "feat: derive per-track byte ranges for combined cues in ResolveCue

Group tracks by FILE; for multi-track FILEs derive FileOffset/Size/FirstLBA
from the lowest INDEX frames and validate the spans tile the file. Single-
track FILEs keep the whole-file-is-the-track behaviour unchanged.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Range-based hashing and offset re-derivation helpers

**Files:**
- Modify: `pack.go` (add `hashTracks` near `hashTrackFiles` ~266)
- Modify: `manifest.go` (add `assignFileOffsets`)
- Test: `pack_test.go` (add unit tests)

- [ ] **Step 1: Write the failing tests**

Add to `pack_test.go`:

```go
func TestHashTracksRangesMatchWholeFiles(t *testing.T) {
	dir := t.TempDir()
	// Two logical tracks concatenated into one combined file.
	a := bytes.Repeat([]byte{0xAA}, 3*SectorSize)
	b := bytes.Repeat([]byte{0xBB}, 2*SectorSize)
	combined := append(append([]byte{}, a...), b...)
	if err := os.WriteFile(filepath.Join(dir, "combined.bin"), combined, 0o644); err != nil {
		t.Fatal(err)
	}
	// Also write the two ranges as standalone files to hash independently.
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), a, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	tracks := []Track{
		{Number: 1, Filename: "combined.bin", FileOffset: 0, Size: int64(len(a))},
		{Number: 2, Filename: "combined.bin", FileOffset: int64(len(a)), Size: int64(len(b))},
	}
	got, err := hashTracks(dir, tracks)
	if err != nil {
		t.Fatalf("hashTracks: %v", err)
	}
	wantA := mustHashFile(t, filepath.Join(dir, "a.bin"))
	wantB := mustHashFile(t, filepath.Join(dir, "b.bin"))
	if got[0] != wantA {
		t.Errorf("track 1 range hash %+v != standalone a.bin %+v", got[0], wantA)
	}
	if got[1] != wantB {
		t.Errorf("track 2 range hash %+v != standalone b.bin %+v", got[1], wantB)
	}
}

func TestAssignFileOffsets(t *testing.T) {
	tracks := []Track{
		{Number: 1, Filename: "combined.bin", Size: 4 * SectorSize},
		{Number: 2, Filename: "combined.bin", Size: 3 * SectorSize},
		{Number: 3, Filename: "other.bin", Size: 5 * SectorSize},
	}
	assignFileOffsets(tracks)
	wantOffsets := []int64{0, 4 * SectorSize, 0}
	for i, w := range wantOffsets {
		if tracks[i].FileOffset != w {
			t.Errorf("track %d FileOffset = %d, want %d", tracks[i].Number, tracks[i].FileOffset, w)
		}
	}
}
```

Confirm `pack_test.go` imports `bytes`, `os`, and `path/filepath`. If any is missing, add it to the import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./... -run 'TestHashTracksRangesMatchWholeFiles|TestAssignFileOffsets' -v`
Expected: FAIL to compile — `hashTracks` and `assignFileOffsets` are undefined.

- [ ] **Step 3: Add hashTracks to pack.go**

In `pack.go`, immediately after the `hashTrackFiles` function, add:

```go
// hashTracks returns per-track MD5/SHA-1/SHA-256 over each track's byte
// range [FileOffset, FileOffset+Size) within filepath.Join(baseDir,
// track.Filename), in track order. For one-track-per-FILE cues FileOffset
// is 0 and Size is the whole file, so this reproduces hashTrackFiles. For
// combined (multi-track-per-FILE) cues each track hashes only its own
// range. Tracks must have FileOffset/Size populated (by ResolveCue at pack
// time or assignFileOffsets at unpack time).
func hashTracks(baseDir string, tracks []Track) ([]FileHashes, error) {
	perTrack := make([]FileHashes, len(tracks))
	for i, t := range tracks {
		f, err := os.Open(filepath.Join(baseDir, t.Filename))
		if err != nil {
			return nil, err
		}
		sr := io.NewSectionReader(f, t.FileOffset, t.Size)
		m, s1, s256 := md5.New(), sha1.New(), sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(m, s1, s256), sr)
		f.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		perTrack[i] = FileHashes{
			MD5:    hex.EncodeToString(m.Sum(nil)),
			SHA1:   hex.EncodeToString(s1.Sum(nil)),
			SHA256: hex.EncodeToString(s256.Sum(nil)),
		}
	}
	return perTrack, nil
}
```

- [ ] **Step 4: Add assignFileOffsets to manifest.go**

In `manifest.go`, after the `BinSectorCount` method (~line 60), add:

```go
// assignFileOffsets fills each track's FileOffset for tracks read back
// from a container, where FileOffset is not serialized. Tracks sharing a
// Filename (combined cues) get sequential offsets accumulated from their
// Sizes in manifest order; one-track-per-FILE tracks get offset 0. Mirrors
// the offsets ResolveCue computes at pack time.
func assignFileOffsets(tracks []Track) {
	offsets := map[string]int64{}
	for i := range tracks {
		tracks[i].FileOffset = offsets[tracks[i].Filename]
		offsets[tracks[i].Filename] += tracks[i].Size
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./... -run 'TestHashTracksRangesMatchWholeFiles|TestAssignFileOffsets' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pack.go manifest.go pack_test.go
git commit -m "feat: range-based hashTracks and assignFileOffsets helpers

hashTracks hashes each track's [FileOffset, FileOffset+Size) range so a
combined .bin yields correct per-track hashes. assignFileOffsets rebuilds
those offsets from per-track Size at read time (FileOffset is transient).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Wire pack + unpack to the range model; full combined round-trip

**Files:**
- Modify: `pack.go` (`Pack` hashing call ~146-155; remove `hashTrackFiles` ~261-287)
- Modify: `unpack.go` (file resolution + hash verification ~46-81)
- Test: `fixtures_test.go` (add `writeCombinedFixture` + `framesToMSF`); `pack_test.go` (round-trip test)

- [ ] **Step 1: Add the combined-fixture helpers**

In `fixtures_test.go`, add:

```go
// writeCombinedFixture writes a single combined .bin (data track followed
// by all audio tracks), a multi-track-per-FILE cue referencing it, and the
// scram — the layout chdman createcd/extractcd produces. Assumes the disc's
// data track is MODE1/2352 (synthDisc's default). Returns scram/cue paths.
func writeCombinedFixture(t *testing.T, dir string, disc SynthDisc) (scramPath, cuePath string) {
	t.Helper()
	combined := append([]byte{}, disc.Bin...)
	for _, ab := range disc.AudioBins {
		combined = append(combined, ab...)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "combined.bin"), combined, 0o644))
	scramPath = filepath.Join(dir, "combined.scram")
	cuePath = filepath.Join(dir, "combined.cue")
	must(os.WriteFile(scramPath, disc.Scram, 0o644))

	dataSectors := int32(len(disc.Bin) / SectorSize)
	var b strings.Builder
	fmt.Fprintf(&b, "FILE \"combined.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n")
	frame := dataSectors
	for a := range disc.AudioBins {
		fmt.Fprintf(&b, "  TRACK %02d AUDIO\n    INDEX 01 %s\n", a+2, framesToMSF(frame))
		frame += int32(len(disc.AudioBins[a]) / SectorSize)
	}
	must(os.WriteFile(cuePath, []byte(b.String()), 0o644))
	return scramPath, cuePath
}

// framesToMSF formats a frame count as decimal mm:ss:ff for a cue INDEX.
func framesToMSF(frames int32) string {
	const fps = MSFFramesPerSecond
	return fmt.Sprintf("%02d:%02d:%02d", frames/(60*fps), (frames/fps)%60, frames%fps)
}
```

- [ ] **Step 2: Write the failing round-trip test**

Add to `pack_test.go`:

```go
func TestPackUnpackCombinedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	disc := synthDisc(t, SynthOpts{MainSectors: 12, AudioTracks: 1, WriteOffset: 8})
	scramPath, cuePath := writeCombinedFixture(t, dir, disc)
	containerPath := filepath.Join(dir, "combined.miniscram")

	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath, OutputPath: containerPath,
		LeadinLBA: LBAPregapStart, // synthDisc uses a 150-sector pregap, no full lead-in
	}, nil); err != nil {
		t.Fatalf("Pack combined cue: %v", err)
	}

	// The container must record two tracks both pointing at combined.bin.
	m, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tracks) != 2 {
		t.Fatalf("container has %d tracks, want 2", len(m.Tracks))
	}
	for _, tr := range m.Tracks {
		if tr.Filename != "combined.bin" {
			t.Errorf("track %d Filename = %q, want combined.bin", tr.Number, tr.Filename)
		}
	}

	// Unpack with verification: the recovered scram must equal the original.
	outPath := filepath.Join(dir, "combined.scram.recovered")
	if err := Unpack(UnpackOptions{
		ContainerPath: containerPath, OutputPath: outPath, Verify: true,
	}, nil); err != nil {
		t.Fatalf("Unpack combined: %v", err)
	}
	got := mustHashFile(t, outPath)
	want := mustHashFile(t, scramPath)
	if got != want {
		t.Fatalf("recovered scram hash %+v != original %+v", got, want)
	}
}
```

- [ ] **Step 3: Run the round-trip test to verify it fails**

Run: `go test ./... -run 'TestPackUnpackCombinedRoundTrip' -v`
Expected: FAIL — `Pack` panics or errors because `hashTrackFiles(resolved.Files)` returns 1 hash for 2 tracks (`tracks[i].Hashes = perTrack[i]` indexes out of range), and unpack's per-track file resolution opens combined.bin twice.

- [ ] **Step 4: Wire Pack to hashTracks**

In `pack.go`, in `Pack`, replace:

```go
	st = r.Step("hashing tracks")
	perTrack, err := hashTrackFiles(resolved.Files)
```

with:

```go
	st = r.Step("hashing tracks")
	perTrack, err := hashTracks(filepath.Dir(opts.CuePath), tracks)
```

(The surrounding loop `for i := range tracks { tracks[i].Hashes = perTrack[i] }` is now correctly 1:1 with tracks.)

- [ ] **Step 5: Wire Unpack to dedup files + range hashing**

In `unpack.go`, replace the block from `st = r.Step("resolving bin files")` through the end of the `st.Done("all tracks match")` step (the file-resolution loop and the bin-hash verification loop, ~lines 46-81) with:

```go
	st = r.Step("resolving bin files")
	containerDir := filepath.Dir(opts.ContainerPath)
	assignFileOffsets(m.Tracks)
	// Sum per-file sizes so a combined .bin (several tracks sharing one
	// Filename) is validated against the total of its tracks' ranges.
	fileTotals := map[string]int64{}
	for _, tr := range m.Tracks {
		fileTotals[tr.Filename] += tr.Size
	}
	var files []ResolvedFile
	seen := map[string]bool{}
	for _, tr := range m.Tracks {
		if seen[tr.Filename] {
			continue
		}
		seen[tr.Filename] = true
		path := filepath.Join(containerDir, tr.Filename)
		info, err := os.Stat(path)
		if err != nil {
			wrapped := fmt.Errorf("track %d (%s): %w", tr.Number, tr.Filename, err)
			st.Fail(wrapped)
			return wrapped
		}
		if info.Size() != fileTotals[tr.Filename] {
			wrapped := fmt.Errorf("%w: %s size on disk %d != manifest total %d",
				errBinHashMismatch, tr.Filename, info.Size(), fileTotals[tr.Filename])
			st.Fail(wrapped)
			return wrapped
		}
		files = append(files, ResolvedFile{Path: path, Size: info.Size()})
	}
	st.Done("%d file(s), %d track(s)", len(files), len(m.Tracks))

	st = r.Step("verifying bin hashes")
	perTrack, err := hashTracks(containerDir, m.Tracks)
	if err != nil {
		st.Fail(err)
		return err
	}
	for i, got := range perTrack {
		want := m.Tracks[i].Hashes
		if cmpErr := compareHashes(got, want); cmpErr != nil {
			err := fmt.Errorf("%w: track %d (%s): %v", errBinHashMismatch, m.Tracks[i].Number, m.Tracks[i].Filename, cmpErr)
			st.Fail(err)
			return err
		}
	}
	st.Done("all tracks match")
```

(The later `binReader, closeBin, err := OpenBinStream(files)` now receives the deduped file list — a combined bin is streamed once.)

- [ ] **Step 6: Remove the now-unused hashTrackFiles**

In `pack.go`, delete the entire `hashTrackFiles` function (its doc comment and body, ~lines 261-287). Confirm no remaining references:

Run: `grep -rn hashTrackFiles .`
Expected: no matches.

- [ ] **Step 7: Run the round-trip test and the full suite**

Run: `go test ./... -run 'TestPackUnpackCombinedRoundTrip' -v`
Expected: PASS.

Run: `go test ./...`
Expected: PASS (all existing single-track pack/unpack/verify tests still green).

- [ ] **Step 8: Commit**

```bash
git add pack.go unpack.go fixtures_test.go pack_test.go
git commit -m "feat: pack/unpack combined cues via per-track byte ranges

Pack hashes per-track ranges; Unpack dedups tracks sharing a Filename,
re-derives offsets, and verifies range hashes. Removes hashTrackFiles.
Adds a synthetic combined-cue round-trip test.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Split-vs-combined container agreement property test

**Files:**
- Test: `property_test.go` (add agreement test)

- [ ] **Step 1: Write the failing test**

Add to `property_test.go`:

```go
// TestSplitCombinedContainersAgree is the design's centerpiece invariant:
// packing the same disc as a one-track-per-FILE (split) cue and as a
// combined multi-track-per-FILE cue must produce containers that are
// identical except for per-track Filename/FileOffset — same FirstLBA, Mode,
// Size, per-track Hashes, scram hashes, and delta bytes.
func TestSplitCombinedContainersAgree(t *testing.T) {
	for _, audioTracks := range []int{1, 2} {
		t.Run(fmt.Sprintf("audio%d", audioTracks), func(t *testing.T) {
			disc := synthDisc(t, SynthOpts{MainSectors: 20, AudioTracks: audioTracks, WriteOffset: 8})

			splitDir := t.TempDir()
			_, splitScram, splitCue := writeFixture(t, splitDir, disc)
			splitContainer := filepath.Join(splitDir, "split.miniscram")
			if err := Pack(PackOptions{CuePath: splitCue, ScramPath: splitScram, OutputPath: splitContainer, LeadinLBA: LBAPregapStart}, nil); err != nil {
				t.Fatalf("Pack split: %v", err)
			}

			combinedDir := t.TempDir()
			combScram, combCue := writeCombinedFixture(t, combinedDir, disc)
			combContainer := filepath.Join(combinedDir, "combined.miniscram")
			if err := Pack(PackOptions{CuePath: combCue, ScramPath: combScram, OutputPath: combContainer, LeadinLBA: LBAPregapStart}, nil); err != nil {
				t.Fatalf("Pack combined: %v", err)
			}

			ms, deltaS, err := ReadContainer(splitContainer)
			if err != nil {
				t.Fatal(err)
			}
			mc, deltaC, err := ReadContainer(combContainer)
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(deltaS, deltaC) {
				t.Errorf("delta payloads differ: split %d bytes, combined %d bytes", len(deltaS), len(deltaC))
			}
			if ms.Scram.Hashes != mc.Scram.Hashes {
				t.Errorf("scram hashes differ: split %+v, combined %+v", ms.Scram.Hashes, mc.Scram.Hashes)
			}
			if len(ms.Tracks) != len(mc.Tracks) {
				t.Fatalf("track counts differ: split %d, combined %d", len(ms.Tracks), len(mc.Tracks))
			}
			for i := range ms.Tracks {
				s, c := ms.Tracks[i], mc.Tracks[i]
				if s.FirstLBA != c.FirstLBA || s.Mode != c.Mode || s.Size != c.Size || s.Hashes != c.Hashes {
					t.Errorf("track %d disagrees:\n split: FirstLBA=%d Mode=%s Size=%d Hashes=%+v\n comb:  FirstLBA=%d Mode=%s Size=%d Hashes=%+v",
						i+1, s.FirstLBA, s.Mode, s.Size, s.Hashes, c.FirstLBA, c.Mode, c.Size, c.Hashes)
				}
			}
		})
	}
}
```

Confirm `property_test.go` imports `bytes`, `fmt`, and `path/filepath`. Add any that are missing.

- [ ] **Step 2: Run the test**

Run: `go test ./... -run 'TestSplitCombinedContainersAgree' -v`
Expected: PASS (Tasks 1-4 already make the two packs agree). If it fails, the offset/FirstLBA derivation in Task 2 or hashing in Task 3 is wrong — fix there, not by weakening the assertion.

- [ ] **Step 3: Commit**

```bash
git add property_test.go
git commit -m "test: split and combined cues produce equivalent containers

Property: same disc packed as one-track-per-FILE vs combined yields equal
delta, scram hashes, and per-track FirstLBA/Mode/Size/Hashes (modulo
Filename/FileOffset). Encodes the design's core agreement invariant.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: End-to-end real-disc fixture row (redump_data)

This task wires a real combined-cue dataset (`Vampire_play`) into the `redump_data`-gated e2e suite and makes the e2e helpers honor `FileOffset` so they are correct for any combined layout. The dataset is gitignored and must be staged locally; the sub-test skips when absent, so this never breaks machines without it.

**Files:**
- Modify: `e2e_redump_test.go` (add fixture row; `assignFileOffsets` after ReadContainer; `FileOffset`-aware `countDataTrackErrors`; robust data-track path in `TestE2EEDCAndECCRealDiscs`)

- [ ] **Step 1: Stage the dataset locally**

The combined cue/bin/scram already exist in `~/Downloads/disc2`. Copy them into a gitignored fixture dir under the stem `Vampire_play`:

```bash
mkdir -p test-discs/vampire-play
cp "$HOME/Downloads/disc2/Vampire_play.cue"   test-discs/vampire-play/
cp "$HOME/Downloads/disc2/Vampire_play.bin"   test-discs/vampire-play/
cp "$HOME/Downloads/disc2/Vampire_play.scram" test-discs/vampire-play/
```

Confirm `test-discs/` is gitignored (it is, per `.gitignore`): `git check-ignore test-discs/vampire-play/Vampire_play.bin` should print the path.

- [ ] **Step 2: Make countDataTrackErrors honor FileOffset**

In `e2e_redump_test.go`, in `countDataTrackErrors`, change the per-sector read offset so it starts at the track's range within its file. Replace:

```go
		nSectors := tr.Size / int64(SectorSize)
		for i := int64(0); i < nSectors; i++ {
			offset := i * int64(SectorSize)
```

with:

```go
		nSectors := tr.Size / int64(SectorSize)
		for i := int64(0); i < nSectors; i++ {
			offset := tr.FileOffset + i*int64(SectorSize)
```

- [ ] **Step 3: Call assignFileOffsets in the round-trip test**

In `e2e_redump_test.go`, `TestE2ERoundTripRealDiscs`, right after `m, delta, err := ReadContainer(containerPath)` and its error check, add:

```go
			// FileOffset is not serialized; re-derive it so countDataTrackErrors
			// reads each data track's range within a combined .bin.
			assignFileOffsets(m.Tracks)
```

- [ ] **Step 4: Make the EDC/ECC test resolve the data track robustly**

In `e2e_redump_test.go`, `TestE2EEDCAndECCRealDiscs`, the loop currently sets `dataTrackPath = resolved.Files[i].Path` (indexing Files by track index, which breaks when Files has fewer entries than Tracks). Replace the data-track-finding block:

```go
			// Find the first data track and read its file directly.
			var dataTrackPath string
			for i, tr := range resolved.Tracks {
				if tr.IsData() {
					dataTrackPath = resolved.Files[i].Path
					break
				}
			}
			if dataTrackPath == "" {
				t.Fatal("no data track found in cue")
			}
			file, err := os.Open(dataTrackPath)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			for _, lba := range f.EDCSampleLBAs {
				var sec [SectorSize]byte
				if _, err := file.ReadAt(sec[:], lba*SectorSize); err != nil {
					t.Fatalf("reading sector %d: %v", lba, err)
				}
```

with a version that joins by Filename and reads at the track's FileOffset:

```go
			// Find the first data track and read its file directly, honoring
			// its FileOffset within a (possibly combined) .bin.
			var dataTrack Track
			found := false
			for _, tr := range resolved.Tracks {
				if tr.IsData() {
					dataTrack = tr
					found = true
					break
				}
			}
			if !found {
				t.Fatal("no data track found in cue")
			}
			file, err := os.Open(filepath.Join(f.Dir, dataTrack.Filename))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			for _, lba := range f.EDCSampleLBAs {
				var sec [SectorSize]byte
				if _, err := file.ReadAt(sec[:], dataTrack.FileOffset+lba*SectorSize); err != nil {
					t.Fatalf("reading sector %d: %v", lba, err)
				}
```

(The rest of the loop body — EDC/ECC checks — is unchanged.)

- [ ] **Step 5: Add the fixture row**

In `e2e_redump_test.go`, add this entry to `realDiscFixtures` (keep the slice sorted alphabetically by `Name` — this goes after `half-life`):

```go
	{
		Name: "vampire-play",
		Dir:  "test-discs/vampire-play",
		Stem: "Vampire_play",
		// chdman-style COMBINED cue: one Vampire_play.bin holds the MODE1
		// data track followed by the AUDIO track. Clean disc, 0 EDC/ECC
		// errors on the data track per the Redumper log. Exercises native
		// multi-track-per-FILE pack/unpack.
		ExpectedDataTrackErrors: 0,
		MaxDeltaBytes:           1024 * 1024,
		MaxContainerBytes:       1024 * 1024,
		// Data track spans LBAs 0..307598 (MODE1); samples stay well inside.
		EDCSampleLBAs: []int64{0, 100, 1000, 100000},
	},
```

- [ ] **Step 6: Run the e2e suite against the staged dataset**

Run: `go test -tags redump_data ./... -run 'TestE2ERoundTripRealDiscs/vampire-play|TestE2EEDCAndECCRealDiscs/vampire-play' -v`
Expected: PASS — Pack→Unpack round-trips byte-exact; data-track error count is 0; EDC/ECC samples agree.

Then confirm no regression on the other datasets and the default build:

Run: `go test -tags redump_data ./...`
Expected: PASS (other fixtures skip if absent).

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit (source only — never stage test-discs/)**

```bash
git add e2e_redump_test.go
git commit -m "test: e2e fixture row for combined-cue disc (Vampire_play)

Adds a redump_data row exercising native multi-track-per-FILE pack/unpack,
and makes the e2e helpers honor Track.FileOffset so they read the correct
range within a combined .bin.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Changelog + manual CLI verification

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add a changelog entry**

Open `CHANGELOG.md` and add an entry under a new unreleased MINOR version heading (match the existing format/most-recent-version style in the file). Content:

```markdown
### Added
- Native support for combined multi-track-per-FILE cuesheets (the layout
  produced by `chdman createcd`/`extractcd`): `pack`/`unpack`/`verify` now
  accept a single combined `.bin` with several tracks, in addition to
  Redumper's one-track-per-FILE output. No container-format change —
  combined containers record several tracks sharing one filename.
```

- [ ] **Step 2: Manual end-to-end check against the original disc**

Build and pack the real combined cue from `~/Downloads/disc2`, writing artifacts next to the cue (not `/tmp`), keeping the source scram:

```bash
go build -o miniscram .
( cd "$HOME/Downloads/disc2" && \
  "$OLDPWD/miniscram" pack Vampire_play.cue -o vp.miniscram --keep-source && \
  "$OLDPWD/miniscram" verify vp.miniscram && \
  "$OLDPWD/miniscram" inspect vp.miniscram | head -20 )
```

Expected: `pack` reports two tracks and a verified round-trip; `verify` prints "all three match"; `inspect` lists two tracks both with `filename=Vampire_play.bin`. Then clean up: `rm "$HOME/Downloads/disc2/vp.miniscram"`.

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog for combined-cuesheet support

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review notes (for the implementer)

- **Spec coverage:** §1 data model → Task 1 Step 3. §2 parser → Task 1. §3 resolver + validation → Task 2. §4 hashing → Task 3 + wiring Task 4. §5 prediction/delta untouched → verified by Task 4/5 round-trips (no code change). §6 unpack dedup + re-derive offsets → Task 4 Step 5. §7 no format change → Task 1 (`json:"-"`, TRKS codec untouched). §Testing: property test → Task 5; resolver units → Task 2; e2e row → Task 6.
- **No format change guarantee:** `FileOffset`/`IndexFrame` carry `json:"-"` and `encodeTRKSPayload`/`decodeTRKSPayload` are never edited, so the on-disk container is byte-identical to today for single-track cues and adds no new fields for combined ones.
- **Type consistency:** `hashTracks(baseDir string, tracks []Track) ([]FileHashes, error)` and `assignFileOffsets(tracks []Track)` are used with these exact signatures in pack.go (Task 4 Step 4), unpack.go (Task 4 Step 5), and e2e (Task 6 Step 3).
- **Single-track behavior preserved:** ResolveCue's `len(group)==1` branch and unpack's per-file dedup both reduce to the original whole-file logic when every FILE has one track (FileOffset 0, Size = whole file).
