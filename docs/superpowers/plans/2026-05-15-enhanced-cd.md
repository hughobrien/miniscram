# Enhanced CD multi-session pack — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make miniscram pack two-session Enhanced CDs (CD-Extra: audio session 1 + data session 2), surface pack failures in the GUI, and add a synthetic regression test covering the path.

**Architecture:** `ParseCue` learns to read `REM SESSION NN`. `Pack` adds a per-session-boundary gap-detection step that scans the scram for the next session's first scrambled sync; the detected sector offset becomes that session's `Track.FirstLBA` shift. `BuildEpsilonHat` gains a `regionAt` classifier and a `SessionGap` data structure so the same builder works for both single- and multi-session discs. The audio-leading pregap (LBAs -150..0 when track 1 is `AUDIO`) is emitted as silent audio (zeros) rather than scrambled Mode 1 zero. Unpack mirrors the same `SessionGaps` deterministically from the manifest's per-track `Session` + `FirstLBA` fields, so no new manifest field is needed beyond `Track.Session`.

**Tech Stack:** Go 1.23, single `package main` in repo root. GUI is a separate Go module under `tools/miniscram-gui/` (Gio UI, requires `CC=/usr/bin/clang CGO_ENABLED=1` locally).

**Spec:** `docs/superpowers/specs/2026-05-15-enhanced-cd-design.md`.

---

## File map

CLI / packer (root package `main`):

- **Modify:** `cue.go` — add `Track.Session`; parse `REM SESSION NN`.
- **Modify:** `cue_test.go` — REM-SESSION tests.
- **Modify:** `builder.go` — `SessionGap` type, `BuildParams.SessionGaps`, `regionAt` classifier, switch the main loop onto it, audio-leading pregap fix, `derivedSessionGaps` helper.
- **Modify:** `builder_test.go` — table tests for `regionAt` and `derivedSessionGaps`.
- **Modify:** `pack.go` — `detectSessionGap`, error sentinels `ErrSessionGapOutOfRange` and `ErrSessionFirstTrackNotData`, wire into `Pack` flow.
- **Modify:** `pack_test.go` — Enhanced-CD pack test (red then green).
- **Modify:** `unpack.go` — pass `SessionGaps` (via `derivedSessionGaps`) into `BuildParams`.
- **Modify:** `fixtures_test.go` — `SynthEnhancedCDOpts`, `synthEnhancedCD` helper.

GUI (`tools/miniscram-gui/`):

- **Modify:** `queue_widget.go` — render `it.Reason` for `qFailed` rows.

No new files. All changes live next to the code they touch.

---

### Task 1: GUI — render fail reason on failed queue rows

**Files:**
- Modify: `tools/miniscram-gui/queue_widget.go`

- [ ] **Step 1.1: Replace the hardcoded "fail" label with the captured reason**

In `tools/miniscram-gui/queue_widget.go`, find `queueRowSuffix` (around line 266). Inside the `switch it.State` block, replace the `case qFailed:` branch:

```go
		case qFailed:
			label = "fail"
			col = bad
```

with:

```go
		case qFailed:
			label = it.Reason
			if label == "" {
				label = "fail"
			}
			col = bad
```

- [ ] **Step 1.2: Build verify**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go build -o /tmp/miniscram-gui-verify .
```

Expected: build succeeds.

- [ ] **Step 1.3: Run GUI tests**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test ./...
```

Expected: all tests pass. The runner+queue tests already populate `Reason` from `state.LastLine`; this change just renders it.

- [ ] **Step 1.4: Commit**

```bash
git add tools/miniscram-gui/queue_widget.go
git commit -m "gui: show captured error reason on failed queue rows (#44)"
```

---

### Task 2: Add `Track.Session` field

**Files:**
- Modify: `cue.go`

- [ ] **Step 2.1: Add the field**

In `cue.go`, find the `Track` struct (around line 36). Add `Session` as the second field:

```go
type Track struct {
	Number   int        `json:"number"`
	Session  int        `json:"session,omitempty"`
	Mode     string     `json:"mode"`
	FirstLBA int32      `json:"first_lba"`
	Filename string     `json:"filename"`
	Size     int64      `json:"size"`
	Hashes   FileHashes `json:"hashes"`
}
```

`omitempty` keeps single-session manifests byte-identical.

- [ ] **Step 2.2: Run full test suite to confirm nothing broke**

```bash
go test ./...
```

Expected: pass. No usage of the new field yet.

- [ ] **Step 2.3: Commit**

```bash
git add cue.go
git commit -m "cue: add Track.Session field (default 0 / omitempty)"
```

---

### Task 3: `ParseCue` learns `REM SESSION NN` (TDD)

**Files:**
- Modify: `cue_test.go`
- Modify: `cue.go`

- [ ] **Step 3.1: Write the failing happy-path test**

Append to `cue_test.go` (after the existing `TestParseCueAccepts` cases or in a new top-level function):

```go
func TestParseCueRemSession(t *testing.T) {
	t.Run("stamps-subsequent-tracks", func(t *testing.T) {
		src := `FILE "a (Track 1).bin" BINARY
  TRACK 01 AUDIO
    INDEX 01 00:00:00
FILE "a (Track 2).bin" BINARY
  TRACK 02 AUDIO
    INDEX 01 00:00:00
REM SESSION 02
FILE "a (Track 3).bin" BINARY
  TRACK 03 MODE1/2352
    INDEX 01 00:00:00
`
		got, err := ParseCue(strings.NewReader(src))
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if len(got) != 3 {
			t.Fatalf("len=%d, want 3", len(got))
		}
		if got[0].Session != 1 || got[1].Session != 1 {
			t.Fatalf("session1 tracks got Session=%d,%d; want 1,1", got[0].Session, got[1].Session)
		}
		if got[2].Session != 2 {
			t.Fatalf("session2 track got Session=%d; want 2", got[2].Session)
		}
	})

	t.Run("case-insensitive", func(t *testing.T) {
		src := `FILE "a (Track 1).bin" BINARY
  TRACK 01 AUDIO
    INDEX 01 00:00:00
rem session 2
FILE "a (Track 2).bin" BINARY
  TRACK 02 MODE1/2352
    INDEX 01 00:00:00
`
		got, err := ParseCue(strings.NewReader(src))
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if got[1].Session != 2 {
			t.Fatalf("got Session=%d; want 2", got[1].Session)
		}
	})

	t.Run("rejects-non-monotonic", func(t *testing.T) {
		src := `FILE "a (Track 1).bin" BINARY
  TRACK 01 AUDIO
    INDEX 01 00:00:00
REM SESSION 02
FILE "a (Track 2).bin" BINARY
  TRACK 02 MODE1/2352
    INDEX 01 00:00:00
REM SESSION 02
FILE "a (Track 3).bin" BINARY
  TRACK 03 MODE1/2352
    INDEX 01 00:00:00
`
		_, err := ParseCue(strings.NewReader(src))
		if err == nil {
			t.Fatalf("expected error for non-monotonic REM SESSION")
		}
		if !strings.Contains(err.Error(), "SESSION") {
			t.Fatalf("error doesn't mention SESSION: %v", err)
		}
	})
}
```

- [ ] **Step 3.2: Run, expect failure**

```bash
go test -run TestParseCueRemSession ./...
```

Expected: FAIL — current parser ignores REM lines and never sets `Session`.

- [ ] **Step 3.3: Implement REM SESSION parsing**

In `cue.go`, find the line-loop preamble (around line 96):

```go
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "REM ") || line == "REM" {
			continue
		}
```

Add a `currentSession` variable (default 1) immediately before the `for` (next to `var currentFile string`):

```go
	var currentFile string // basename of the most recent FILE line
	var fileTrackCount int // number of TRACKs seen in currentFile (must end at 0 or 1)
	currentSession := 1    // bumped by REM SESSION NN; stamped on every TRACK
```

Replace the REM-skip block with a dispatcher that detects `REM SESSION NN`:

```go
		if line == "" {
			continue
		}
		if line == "REM" || strings.HasPrefix(line, "REM ") {
			rem := strings.TrimSpace(strings.TrimPrefix(line, "REM"))
			fields := strings.Fields(rem)
			if len(fields) >= 2 && strings.EqualFold(fields[0], "SESSION") {
				n, err := strconv.Atoi(fields[1])
				if err != nil || n < 1 {
					return nil, fmt.Errorf("bad REM SESSION number %q", fields[1])
				}
				if n <= currentSession {
					return nil, fmt.Errorf("REM SESSION %d is not greater than current session %d", n, currentSession)
				}
				// Flush any in-progress track before the session bump so the
				// stamp applies only to the next TRACK onward.
				if err := flushTrack(); err != nil {
					return nil, err
				}
				cur = nil
				hasIndex01 = false
				currentSession = n
			}
			continue
		}
```

Then in the `case "TRACK":` branch, where `cur = &Track{...}` is constructed (around line 154), add the session stamp:

```go
				cur = &Track{Number: n, Mode: mode, Filename: currentFile, Session: currentSession}
```

- [ ] **Step 3.4: Run, expect pass**

```bash
go test -run TestParseCueRemSession ./...
```

Expected: PASS.

- [ ] **Step 3.5: Run full test suite — no regressions**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 3.6: Commit**

```bash
git add cue.go cue_test.go
git commit -m "cue: parse REM SESSION NN and stamp tracks with their session"
```

---

### Task 4: Add `synthEnhancedCD` fixture helper

**Files:**
- Modify: `fixtures_test.go`

This fixture is what every later task tests against. It models a real Redumper enhanced-CD dump: audio session 1 + data session 2 with an inter-session gap baked into the scram.

- [ ] **Step 4.1: Append the helper at the bottom of `fixtures_test.go`**

```go
// SynthEnhancedCDOpts configures synthEnhancedCD.
type SynthEnhancedCDOpts struct {
	AudioTracks       int   // number of audio tracks in session 1 (default 3)
	AudioSectorsEach  int32 // sectors per audio track (default 200)
	DataSectors       int32 // sectors in the session-2 data track (default 500)
	GapLeadoutSectors int32 // sec.1 lead-out (default 6750, redumper minimum)
	GapLeadinSectors  int32 // sec.2 lead-in  (default 4500)
	GapPregapSectors  int32 // sec.2 pregap   (default 150)
	TrailingLeadout   int32 // post-disc leadout sectors (default 5)
	WriteOffset       int   // bytes (default 0)
}

// SynthEnhancedCD is the result of synthEnhancedCD.
type SynthEnhancedCD struct {
	AudioBins            [][]byte
	DataBin              []byte
	Scram                []byte
	Cue                  string
	LeadinLBA            int32
	ExpectedDataFirstLBA int32 // post-fix value; matches the actual scram layout
	GapSectors           int32 // total inter-session gap
}

// synthEnhancedCD builds an in-memory bin/scram/cue triple modelling
// a CD-Extra disc: N audio tracks (session 1) + REM SESSION 02 +
// 1 data track (session 2). The scram has the inter-session gap
// baked in (Mode 0 leadout + zero leadin + Mode 1 pregap), so a
// correct packer must detect that gap from the scram alone.
func synthEnhancedCD(t *testing.T, opts SynthEnhancedCDOpts) SynthEnhancedCD {
	t.Helper()

	if opts.AudioTracks == 0 {
		opts.AudioTracks = 3
	}
	if opts.AudioSectorsEach == 0 {
		opts.AudioSectorsEach = 200
	}
	if opts.DataSectors == 0 {
		opts.DataSectors = 500
	}
	if opts.GapLeadoutSectors == 0 {
		opts.GapLeadoutSectors = 6750
	}
	if opts.GapLeadinSectors == 0 {
		opts.GapLeadinSectors = 4500
	}
	if opts.GapPregapSectors == 0 {
		opts.GapPregapSectors = 150
	}
	if opts.TrailingLeadout == 0 {
		opts.TrailingLeadout = 5
	}

	const (
		leadinLBA int32 = LBALeadinStart
		pregap          = 150
	)
	leadinSectors := int32(LBAPregapStart - LBALeadinStart) // 45000

	// Audio bins: deterministic, non-zero, and chosen so no 12-byte
	// run accidentally matches the scrambled-sync pattern. The Sync
	// pattern's first byte is 0x00 and its 2..11 bytes are 0xFF;
	// our pattern only produces 0xFF when (j*3 + a*17) % 256 == 0xFF,
	// which can't appear 10 bytes in a row.
	audioBins := make([][]byte, opts.AudioTracks)
	totalAudioSectors := int32(0)
	for a := range audioBins {
		ab := make([]byte, int(opts.AudioSectorsEach)*SectorSize)
		for j := range ab {
			ab[j] = byte(j*3 + (a+1)*17)
		}
		audioBins[a] = ab
		totalAudioSectors += opts.AudioSectorsEach
	}

	// Data bin: descrambled Mode 1 sectors at LBAs starting at the
	// data track's actual FirstLBA on disc. The bin holds the
	// descrambled bytes; the scram below scrambles them.
	dataFirstLBA := totalAudioSectors + opts.GapLeadoutSectors + opts.GapLeadinSectors + opts.GapPregapSectors
	dataBin := make([]byte, int(opts.DataSectors)*SectorSize)
	for i := int32(0); i < opts.DataSectors; i++ {
		s := dataBin[int(i)*SectorSize : (int(i)+1)*SectorSize]
		copy(s[:SyncLen], Sync[:])
		lba := dataFirstLBA + i
		msf := LBAToBCDMSF(lba)
		s[12], s[13], s[14], s[15] = msf[0], msf[1], msf[2], 0x01
		for j := 16; j < SectorSize; j++ {
			s[j] = byte(j * int(i+1))
		}
	}

	// Scram layout in disc order.
	totalScramSectors := leadinSectors + pregap + totalAudioSectors +
		opts.GapLeadoutSectors + opts.GapLeadinSectors + opts.GapPregapSectors +
		opts.DataSectors + opts.TrailingLeadout
	scramLen := int64(totalScramSectors)*int64(SectorSize) + int64(opts.WriteOffset)
	if scramLen < 0 {
		scramLen = 0
	}
	scram := make([]byte, scramLen)

	writeSec := func(idx int32, sec [SectorSize]byte) {
		pos := int64(idx)*int64(SectorSize) + int64(opts.WriteOffset)
		writeAt(scram, pos, sec[:])
	}

	idx := int32(0)
	// Disc lead-in: zeros (already zeroed).
	idx += leadinSectors
	// Track-1 pregap: silent audio (zeros) because track 1 is audio.
	idx += pregap
	// Audio tracks: PCM as-is, no scrambling.
	for a := range audioBins {
		for j := int32(0); j < opts.AudioSectorsEach; j++ {
			var sec [SectorSize]byte
			copy(sec[:], audioBins[a][int(j)*SectorSize:(int(j)+1)*SectorSize])
			writeSec(idx, sec)
			idx++
		}
	}
	// Session-1 lead-out: Mode 0 scrambled zero, increasing LBA.
	for j := int32(0); j < opts.GapLeadoutSectors; j++ {
		writeSec(idx, generateLeadoutSector(leadinLBA+idx))
		idx++
	}
	// Session-2 lead-in: zeros.
	idx += opts.GapLeadinSectors
	// Session-2 pregap: Mode 1 scrambled zero.
	for j := int32(0); j < opts.GapPregapSectors; j++ {
		writeSec(idx, generateMode1ZeroSector(leadinLBA+idx))
		idx++
	}
	// Data track: Scramble() applied to the bin sectors.
	for j := int32(0); j < opts.DataSectors; j++ {
		var sec [SectorSize]byte
		copy(sec[:], dataBin[int(j)*SectorSize:(int(j)+1)*SectorSize])
		Scramble(&sec)
		writeSec(idx, sec)
		idx++
	}
	// Trailing lead-out: Mode 0 scrambled zero.
	for j := int32(0); j < opts.TrailingLeadout; j++ {
		writeSec(idx, generateLeadoutSector(leadinLBA+idx))
		idx++
	}

	// Cue.
	var cue strings.Builder
	for a := 0; a < opts.AudioTracks; a++ {
		fmt.Fprintf(&cue, "FILE \"x (Track %d).bin\" BINARY\n  TRACK %02d AUDIO\n    INDEX 01 00:00:00\n",
			a+1, a+1)
	}
	cue.WriteString("REM SESSION 02\n")
	fmt.Fprintf(&cue, "FILE \"x (Track %d).bin\" BINARY\n  TRACK %02d MODE1/2352\n    INDEX 01 00:00:00\n",
		opts.AudioTracks+1, opts.AudioTracks+1)

	return SynthEnhancedCD{
		AudioBins:            audioBins,
		DataBin:              dataBin,
		Scram:                scram,
		Cue:                  cue.String(),
		LeadinLBA:            leadinLBA,
		ExpectedDataFirstLBA: dataFirstLBA,
		GapSectors:           opts.GapLeadoutSectors + opts.GapLeadinSectors + opts.GapPregapSectors,
	}
}

// writeEnhancedCDFixture writes the enhanced-CD bin/scram/cue files
// into dir. Returns the cue path.
func writeEnhancedCDFixture(t *testing.T, dir string, d SynthEnhancedCD) string {
	t.Helper()
	for a, ab := range d.AudioBins {
		p := filepath.Join(dir, fmt.Sprintf("x (Track %d).bin", a+1))
		if err := os.WriteFile(p, ab, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dataPath := filepath.Join(dir, fmt.Sprintf("x (Track %d).bin", len(d.AudioBins)+1))
	if err := os.WriteFile(dataPath, d.DataBin, 0o644); err != nil {
		t.Fatal(err)
	}
	scramPath := filepath.Join(dir, "x.scram")
	if err := os.WriteFile(scramPath, d.Scram, 0o644); err != nil {
		t.Fatal(err)
	}
	cuePath := filepath.Join(dir, "x.cue")
	if err := os.WriteFile(cuePath, []byte(d.Cue), 0o644); err != nil {
		t.Fatal(err)
	}
	return cuePath
}
```

If `writeAt` is not yet visible from `fixtures_test.go`'s package scope, it is — `synthDisc` uses it via the same file. No new imports needed beyond `fmt`, `strings`, `os`, `path/filepath`, `testing` (all already imported).

- [ ] **Step 4.2: Compile-check by running the test package**

```bash
go test -run NoSuchTest ./...
```

Expected: compiles, "no tests to run" (or runs unrelated tests). Just ensure no build error.

- [ ] **Step 4.3: Commit**

```bash
git add fixtures_test.go
git commit -m "test: synthEnhancedCD helper modelling a CD-Extra disc"
```

---

### Task 5: Red regression test — Enhanced CD pack must currently fail

**Files:**
- Modify: `pack_test.go`

- [ ] **Step 5.1: Add the failing pack test**

Append to `pack_test.go`:

```go
func TestPackEnhancedCDPlaceholder(t *testing.T) {
	// Pre-multi-session, packing an Enhanced CD trips the 5% layout
	// abort because ResolveCue places the session-2 data track at
	// LBA = cumulative_audio_sectors, but on the actual scram it
	// sits ~11400 sectors later (session lead-out + lead-in + pregap).
	// Once Task 11 lands, this test flips to a successful round-trip.
	dir := t.TempDir()
	disc := synthEnhancedCD(t, SynthEnhancedCDOpts{})
	cuePath := writeEnhancedCDFixture(t, dir, disc)

	out := filepath.Join(dir, "x.miniscram")
	err := Pack(PackOptions{
		CuePath:    cuePath,
		ScramPath:  filepath.Join(dir, "x.scram"),
		OutputPath: out,
	}, nil)

	var lme *LayoutMismatchError
	if !errors.As(err, &lme) {
		t.Fatalf("expected *LayoutMismatchError, got %T: %v", err, err)
	}
	if lme.MismatchRatio <= layoutMismatchAbortRatio {
		t.Fatalf("ratio %.4f should exceed abort threshold %.2f",
			lme.MismatchRatio, layoutMismatchAbortRatio)
	}
}
```

Ensure imports include `errors` and `filepath` (`pack_test.go` likely already imports both; if not, `go test` will tell you).

- [ ] **Step 5.2: Run, expect pass**

```bash
go test -run TestPackEnhancedCDPlaceholder ./...
```

Expected: PASS. The test asserts the CURRENT (broken) behaviour, so it succeeds on main. This is the regression target.

- [ ] **Step 5.3: Commit**

```bash
git add pack_test.go
git commit -m "test: lock in current Enhanced-CD pack failure as a regression target"
```

---

### Task 6: `SessionGap` type and `regionAt` classifier (TDD)

**Files:**
- Modify: `builder.go`
- Modify: `builder_test.go`

- [ ] **Step 6.1: Write the failing regionAt table test**

Append to `builder_test.go`:

```go
func TestRegionAt(t *testing.T) {
	// Two-session disc: audio LBAs 0..199, gap 200..11599, data 11600..11649.
	tracks := []Track{
		{Number: 1, Session: 1, Mode: "AUDIO", FirstLBA: 0, Size: 100 * int64(SectorSize)},
		{Number: 2, Session: 1, Mode: "AUDIO", FirstLBA: 100, Size: 100 * int64(SectorSize)},
		{Number: 3, Session: 2, Mode: "MODE1/2352", FirstLBA: 11600, Size: 50 * int64(SectorSize)},
	}
	gaps := []SessionGap{
		{StartLBA: 200, LeadoutSectors: 6750, LeadinSectors: 4500, PregapSectors: 150},
	}
	cases := []struct {
		name string
		lba  int32
		want region
	}{
		{"leadin", -45150, regionLeadin},
		{"pregap-last", -1, regionPregap},
		{"audio-track-1-start", 0, regionBin},
		{"audio-track-2-end", 199, regionBin},
		{"gap-leadout-start", 200, regionGapLeadout},
		{"gap-leadout-end", 6949, regionGapLeadout},
		{"gap-leadin-start", 6950, regionGapLeadin},
		{"gap-leadin-end", 11449, regionGapLeadin},
		{"gap-pregap-start", 11450, regionGapPregap},
		{"gap-pregap-end", 11599, regionGapPregap},
		{"data-start", 11600, regionBin},
		{"data-end", 11649, regionBin},
		{"trailing-leadout", 11650, regionLeadout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := regionAt(c.lba, tracks, gaps)
			if got != c.want {
				t.Fatalf("regionAt(%d) = %v; want %v", c.lba, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 6.2: Run, expect failure (type/func not defined)**

```bash
go test -run TestRegionAt ./...
```

Expected: FAIL — `region`, `regionAt`, `SessionGap` all undefined.

- [ ] **Step 6.3: Add the `SessionGap` type and `region` enum**

In `builder.go`, immediately before `BuildParams` (around line 15), add:

```go
// SessionGap describes the inter-session gap between session N and
// session N+1 on a multi-session disc. The three sub-regions appear
// in disc order: session-N lead-out, session-(N+1) lead-in, session-
// (N+1) pregap. Sectors sum to the total detected gap.
type SessionGap struct {
	StartLBA       int32 // first LBA of LeadoutSectors
	LeadoutSectors int32 // session-N lead-out (Mode 0 scrambled zero)
	LeadinSectors  int32 // session-(N+1) lead-in (zeros)
	PregapSectors  int32 // session-(N+1) pregap (Mode 1 scrambled zero)
}

// region classifies an LBA into one of the seven structural regions
// BuildEpsilonHat emits content for.
type region int

const (
	regionLeadin region = iota
	regionPregap
	regionBin
	regionGapLeadout
	regionGapLeadin
	regionGapPregap
	regionLeadout
)
```

Add the new field to `BuildParams`:

```go
type BuildParams struct {
	LeadinLBA        int32
	WriteOffsetBytes int
	ScramSize        int64
	BinFirstLBA      int32
	BinSectorCount   int32
	Tracks           []Track
	SessionGaps      []SessionGap // empty for single-session discs
}
```

- [ ] **Step 6.4: Add the `regionAt` helper**

Append to `builder.go` (e.g. just after `trackModeAt`):

```go
// regionAt classifies an LBA into one of the seven structural
// regions BuildEpsilonHat emits content for. Inter-session gaps
// take precedence over bin regions: a gap LBA may sit inside the
// closed interval [tracks[0].FirstLBA, lastTrack.FirstLBA+lastSize),
// and the gap classification wins.
func regionAt(lba int32, tracks []Track, gaps []SessionGap) region {
	if lba < LBAPregapStart {
		return regionLeadin
	}
	for _, g := range gaps {
		end := g.StartLBA + g.LeadoutSectors + g.LeadinSectors + g.PregapSectors
		if lba < g.StartLBA || lba >= end {
			continue
		}
		switch {
		case lba < g.StartLBA+g.LeadoutSectors:
			return regionGapLeadout
		case lba < g.StartLBA+g.LeadoutSectors+g.LeadinSectors:
			return regionGapLeadin
		default:
			return regionGapPregap
		}
	}
	if len(tracks) > 0 && lba < tracks[0].FirstLBA {
		return regionPregap
	}
	for _, t := range tracks {
		sectors := int32(t.Size / int64(SectorSize))
		if lba >= t.FirstLBA && lba < t.FirstLBA+sectors {
			return regionBin
		}
	}
	return regionLeadout
}
```

- [ ] **Step 6.5: Run, expect pass**

```bash
go test -run TestRegionAt ./...
```

Expected: PASS.

- [ ] **Step 6.6: Run full test suite**

```bash
go test ./...
```

Expected: PASS (the new field is unused in BuildEpsilonHat yet, so existing behaviour unchanged).

- [ ] **Step 6.7: Commit**

```bash
git add builder.go builder_test.go
git commit -m "builder: add SessionGap type and regionAt classifier"
```

---

### Task 7: Switch `BuildEpsilonHat` onto `regionAt`; fix audio-leading pregap

**Files:**
- Modify: `builder.go`

The current `switch` in `BuildEpsilonHat` (around line 165) classifies inline. Replacing it with `regionAt` keeps behaviour identical for single-session discs and adds correct emission for gap regions. Same edit drops the audio-leading pregap fix.

- [ ] **Step 7.1: Replace the inline switch with `regionAt` branching**

In `builder.go`, find the loop body (around line 163):

```go
	for lba := p.LeadinLBA; lba < endLBA; lba++ {
		var sec [SectorSize]byte
		switch {
		case lba < LBAPregapStart:
			// leadin: zeros
		case lba < p.BinFirstLBA:
			sec = generateMode1ZeroSector(lba)
		case lba < p.BinFirstLBA+p.BinSectorCount:
			if _, err := io.ReadFull(bin, binBuf); err != nil {
				return nil, 0, 0, fmt.Errorf("reading bin LBA %d: %w", lba, err)
			}
			copy(sec[:], binBuf)
			if trackModeAt(p.Tracks, lba) != "AUDIO" {
				// Mirror redumper's Scrambler::descramble() decision
				// (cd/cd_scrambler.ixx:23-61). Scramble bin only when it
				// holds the descrambled form ("pass" sectors). For "fail"
				// sectors, .bin == .scram for the LBA — passing through
				// preserves the original disc bytes without an override.
				if classifyBinSector(sec[:], lba) {
					Scramble(&sec)
				} else {
					passThroughs++
				}
			}
		default:
			sec = generateLeadoutSector(lba)
		}
```

Replace with:

```go
	for lba := p.LeadinLBA; lba < endLBA; lba++ {
		var sec [SectorSize]byte
		switch regionAt(lba, p.Tracks, p.SessionGaps) {
		case regionLeadin:
			// zeros
		case regionPregap:
			// Audio-leading discs have silent audio (PCM zeros) in
			// the track-1 pregap, not scrambled Mode 1 zero. Single-
			// session data discs keep the historical Mode 1 emission.
			if len(p.Tracks) > 0 && p.Tracks[0].Mode == "AUDIO" {
				// zeros
			} else {
				sec = generateMode1ZeroSector(lba)
			}
		case regionBin:
			if _, err := io.ReadFull(bin, binBuf); err != nil {
				return nil, 0, 0, fmt.Errorf("reading bin LBA %d: %w", lba, err)
			}
			copy(sec[:], binBuf)
			if trackModeAt(p.Tracks, lba) != "AUDIO" {
				// Mirror redumper's Scrambler::descramble() decision
				// (cd/cd_scrambler.ixx:23-61). Scramble bin only when it
				// holds the descrambled form ("pass" sectors). For "fail"
				// sectors, .bin == .scram for the LBA — passing through
				// preserves the original disc bytes without an override.
				if classifyBinSector(sec[:], lba) {
					Scramble(&sec)
				} else {
					passThroughs++
				}
			}
		case regionGapLeadout:
			sec = generateLeadoutSector(lba)
		case regionGapLeadin:
			// zeros
		case regionGapPregap:
			sec = generateMode1ZeroSector(lba)
		case regionLeadout:
			sec = generateLeadoutSector(lba)
		}
```

- [ ] **Step 7.2: Run full test suite — should still pass on existing fixtures**

```bash
go test ./...
```

Expected: PASS. The new switch behaves identically to the old one for single-session discs with a data first track. The Enhanced-CD test from Task 5 still fails for the same reason it did before (FirstLBA is still wrong), which is what we want — it'll flip in Task 11.

Heads-up: if any existing fixture has track 1 = AUDIO with non-zero scram bytes in the pregap, Task 7 will newly flag those as overrides. None of the in-tree synthetic fixtures use audio-leading layouts (only `synthEnhancedCD` does, and it has zero pregap bytes), so this should be safe. Investigate if any test starts failing.

- [ ] **Step 7.3: Commit**

```bash
git add builder.go
git commit -m "builder: route emission via regionAt; emit silent audio in audio-leading pregap"
```

---

### Task 8: `derivedSessionGaps` helper

**Files:**
- Modify: `builder.go`
- Modify: `builder_test.go`

- [ ] **Step 8.1: Write the failing test**

Append to `builder_test.go`:

```go
func TestDerivedSessionGaps(t *testing.T) {
	t.Run("single-session-no-gaps", func(t *testing.T) {
		tracks := []Track{
			{Session: 1, FirstLBA: 0, Size: 100 * int64(SectorSize)},
			{Session: 1, FirstLBA: 100, Size: 200 * int64(SectorSize)},
		}
		got := derivedSessionGaps(tracks)
		if len(got) != 0 {
			t.Fatalf("expected 0 gaps, got %v", got)
		}
	})

	t.Run("two-session-standard-minima", func(t *testing.T) {
		// Audio: tracks 1..2 covering LBAs 0..199. Gap of 11400 sectors.
		// Data: track 3 at LBA 11600.
		tracks := []Track{
			{Session: 1, FirstLBA: 0, Size: 100 * int64(SectorSize)},
			{Session: 1, FirstLBA: 100, Size: 100 * int64(SectorSize)},
			{Session: 2, FirstLBA: 11600, Size: 50 * int64(SectorSize)},
		}
		got := derivedSessionGaps(tracks)
		if len(got) != 1 {
			t.Fatalf("expected 1 gap, got %d", len(got))
		}
		g := got[0]
		if g.StartLBA != 200 {
			t.Errorf("StartLBA=%d; want 200", g.StartLBA)
		}
		if g.PregapSectors != 150 {
			t.Errorf("PregapSectors=%d; want 150", g.PregapSectors)
		}
		if g.LeadinSectors != 4500 {
			t.Errorf("LeadinSectors=%d; want 4500", g.LeadinSectors)
		}
		if g.LeadoutSectors != 11400-4500-150 {
			t.Errorf("LeadoutSectors=%d; want %d", g.LeadoutSectors, 11400-4500-150)
		}
	})

	t.Run("two-session-extra-slack-goes-to-leadout", func(t *testing.T) {
		// Total gap = 13000 sectors (1600 over the minimum). Lead-in
		// and pregap stay at the minima; lead-out absorbs the slack.
		tracks := []Track{
			{Session: 1, FirstLBA: 0, Size: 200 * int64(SectorSize)},
			{Session: 2, FirstLBA: 13200, Size: 50 * int64(SectorSize)},
		}
		got := derivedSessionGaps(tracks)
		if len(got) != 1 {
			t.Fatalf("expected 1 gap, got %d", len(got))
		}
		g := got[0]
		if g.PregapSectors != 150 {
			t.Errorf("PregapSectors=%d; want 150", g.PregapSectors)
		}
		if g.LeadinSectors != 4500 {
			t.Errorf("LeadinSectors=%d; want 4500", g.LeadinSectors)
		}
		if g.LeadoutSectors != 13000-4500-150 {
			t.Errorf("LeadoutSectors=%d; want %d", g.LeadoutSectors, 13000-4500-150)
		}
	})
}
```

- [ ] **Step 8.2: Run, expect failure (undefined func)**

```bash
go test -run TestDerivedSessionGaps ./...
```

Expected: FAIL — `derivedSessionGaps` undefined.

- [ ] **Step 8.3: Implement `derivedSessionGaps`**

Append to `builder.go`:

```go
// Canonical sub-region sizes for a CD-Extra inter-session gap.
// Redumper's printCUE uses these as the standard minima:
// CD_LEADOUT_MIN_SIZE = 6750, CD_LEADIN_MIN_SIZE = 4500,
// CD_PREGAP_SIZE = 150.
const (
	sessionGapPregapSectors = 150
	sessionGapLeadinSectors = 4500
)

// derivedSessionGaps reconstructs SessionGap entries from a tracks
// slice whose FirstLBAs have already been adjusted for inter-session
// gaps (i.e. as written into the manifest by Pack). The total gap
// between sessions is the LBA jump between the last track of session
// N and the first track of session N+1; we split it using the
// canonical pregap/leadin minima and put any slack into the leadout.
//
// Returns one SessionGap per session boundary, in order. Empty for
// single-session discs.
func derivedSessionGaps(tracks []Track) []SessionGap {
	if len(tracks) < 2 {
		return nil
	}
	var gaps []SessionGap
	for i := 1; i < len(tracks); i++ {
		prev, cur := tracks[i-1], tracks[i]
		if cur.Session <= prev.Session {
			continue
		}
		prevEnd := prev.FirstLBA + int32(prev.Size/int64(SectorSize))
		total := cur.FirstLBA - prevEnd
		if total <= 0 {
			continue
		}
		leadout := total - sessionGapLeadinSectors - sessionGapPregapSectors
		if leadout < 0 {
			leadout = 0
		}
		gaps = append(gaps, SessionGap{
			StartLBA:       prevEnd,
			LeadoutSectors: leadout,
			LeadinSectors:  sessionGapLeadinSectors,
			PregapSectors:  sessionGapPregapSectors,
		})
	}
	return gaps
}
```

- [ ] **Step 8.4: Run, expect pass**

```bash
go test -run TestDerivedSessionGaps ./...
```

Expected: PASS.

- [ ] **Step 8.5: Commit**

```bash
git add builder.go builder_test.go
git commit -m "builder: derivedSessionGaps reconstructs SessionGap from per-track LBAs"
```

---

### Task 9: Pack — detect each gap from the scram, populate `SessionGaps`, gate on bounds

**Files:**
- Modify: `pack.go`
- Modify: `pack_test.go`

- [ ] **Step 9.1: Add error sentinels + `detectSessionGap`**

In `pack.go`, find the error sentinels block (around line 29):

```go
var (
	errVerifyMismatch = errors.New("round-trip verification failed")
)
```

Replace with:

```go
var (
	errVerifyMismatch = errors.New("round-trip verification failed")

	// ErrSessionGapOutOfRange means the inter-session gap detected
	// from the scram fell outside the plausible window. Either a
	// scram-corruption symptom or a disc whose lead-out exceeds our
	// upper bound — surface to the user with the detected value.
	ErrSessionGapOutOfRange = errors.New("session gap size out of expected range")

	// ErrSessionFirstTrackNotData means the first track of session 2
	// (or later) is AUDIO. Detection relies on locking onto a
	// scrambled sync and audio has none; this case is unsupported.
	ErrSessionFirstTrackNotData = errors.New("first track of a non-leading session must be DATA")
)

// Inter-session gap bounds. The lower bound is redumper's sum-of-
// minima (CD_LEADOUT_MIN_SIZE + CD_LEADIN_MIN_SIZE + CD_PREGAP_SIZE
// = 6750 + 4500 + 150). The upper bound is a generous backstop
// against detection drift.
const (
	sessionGapMinSectors = 11400
	sessionGapMaxSectors = 30000
)
```

Append to `pack.go`, near the existing `validateSyncCandidate` (which already lives near `detectWriteOffset`):

```go
// detectSessionGap scans scram from the byte position implied by
// naiveLBA forward, looking for the first scrambled sync whose
// decoded MSF agrees exactly with its byte position under the
// already-known writeOffsetBytes. Returns the gap as sectors past
// naiveLBA. Fails with a wrapped error if no such sync is found,
// or with ErrSessionGapOutOfRange if the detected gap is outside
// [sessionGapMinSectors, sessionGapMaxSectors].
func detectSessionGap(scramPath string, scramSize int64, leadinLBA int32, writeOffsetBytes int, naiveLBA int32) (int32, error) {
	f, err := os.Open(scramPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	startByte := int64(naiveLBA-leadinLBA)*int64(SectorSize) + int64(writeOffsetBytes)
	if startByte < 0 {
		startByte = 0
	}

	const chunkSize = 128 * 1024
	chunk := make([]byte, chunkSize)
	carry := make([]byte, 0, SyncLen-1)
	pos := startByte
	for pos < scramSize {
		readLen := int64(chunkSize)
		if pos+readLen > scramSize {
			readLen = scramSize - pos
		}
		n, err := f.ReadAt(chunk[:readLen], pos)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if n == 0 {
			break
		}
		var search []byte
		var carryLen int64
		if len(carry) > 0 {
			search = append(carry, chunk[:n]...)
			carryLen = int64(len(carry))
		} else {
			search = chunk[:n]
		}
		searchPos := 0
		for {
			idx := bytes.Index(search[searchPos:], Sync[:])
			if idx < 0 {
				break
			}
			idx += searchPos
			syncOff := pos - carryLen + int64(idx)
			if lba, ok := validateGapSync(f, syncOff, leadinLBA, writeOffsetBytes, scramSize); ok {
				gap := lba - naiveLBA
				if gap < sessionGapMinSectors || gap > sessionGapMaxSectors {
					return 0, fmt.Errorf("%w: detected %d sectors past LBA %d (expected [%d, %d])",
						ErrSessionGapOutOfRange, gap, naiveLBA, sessionGapMinSectors, sessionGapMaxSectors)
				}
				return gap, nil
			}
			searchPos = idx + 1
		}
		tailStart := n - (SyncLen - 1)
		if tailStart < 0 {
			tailStart = 0
		}
		carry = append(carry[:0], chunk[tailStart:n]...)
		pos += int64(n)
	}
	return 0, fmt.Errorf("no scrambled sync found past naive LBA %d", naiveLBA)
}

// validateGapSync is a stricter cousin of validateSyncCandidate: it
// uses the already-known disc-wide writeOffsetBytes, so the BCD MSF
// at this sync must decode to *exactly* the LBA implied by the byte
// position (no ±2 sector slop). Returns the decoded LBA on success.
func validateGapSync(f io.ReaderAt, syncOff int64, leadinLBA int32, writeOffsetBytes int, scramSize int64) (int32, bool) {
	if syncOff+int64(SyncLen)+3 > scramSize {
		return 0, false
	}
	var header [3]byte
	if _, err := f.ReadAt(header[:], syncOff+int64(SyncLen)); err != nil {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		header[i] ^= scrambleTable[SyncLen+i]
	}
	isBCD := func(b byte) bool { return (b>>4) <= 9 && (b&0x0F) <= 9 }
	if !isBCD(header[0]) || !isBCD(header[1]) || !isBCD(header[2]) {
		return 0, false
	}
	decodedLBA := BCDMSFToLBA(header)
	if decodedLBA < leadinLBA || decodedLBA > 500_000 {
		return 0, false
	}
	expectedSyncOff := int64(decodedLBA-leadinLBA)*int64(SectorSize) + int64(writeOffsetBytes)
	if syncOff != expectedSyncOff {
		return 0, false
	}
	return decodedLBA, true
}
```

- [ ] **Step 9.2: Wire detection into `Pack`'s flow**

In `pack.go`, find the block immediately after the `checkConstantOffset` step (around line 91, after `st.Done("")`). Insert a new step that detects each session gap and adjusts FirstLBAs:

```go
	// 4b. detect inter-session gaps (if any) and adjust track FirstLBAs.
	if hasMultipleSessions(tracks) {
		st = r.Step("detecting session gaps")
		if err := applySessionGaps(tracks, opts.ScramPath, scramSize, opts.LeadinLBA, writeOffsetBytes); err != nil {
			st.Fail(err)
			return err
		}
		st.Done("%d session boundary(s)", countSessionBoundaries(tracks))
	}
```

Then add these three helpers at the bottom of `pack.go` (after `buildHatAndDelta`):

```go
func hasMultipleSessions(tracks []Track) bool {
	for _, t := range tracks {
		if t.Session > 1 {
			return true
		}
	}
	return false
}

func countSessionBoundaries(tracks []Track) int {
	n := 0
	for i := 1; i < len(tracks); i++ {
		if tracks[i].Session > tracks[i-1].Session {
			n++
		}
	}
	return n
}

// applySessionGaps detects the gap before each session-boundary
// track and shifts that track's FirstLBA (and every later track's)
// by the detected gap size. Tracks are mutated in place.
//
// Requires the session-N+1 first track to be DATA (its scrambled
// sync is the lock target). Returns ErrSessionFirstTrackNotData
// otherwise.
func applySessionGaps(tracks []Track, scramPath string, scramSize int64, leadinLBA int32, writeOffsetBytes int) error {
	for i := 1; i < len(tracks); i++ {
		if tracks[i].Session <= tracks[i-1].Session {
			continue
		}
		if tracks[i].Mode == "AUDIO" {
			return fmt.Errorf("%w (track %d, session %d)", ErrSessionFirstTrackNotData, tracks[i].Number, tracks[i].Session)
		}
		gap, err := detectSessionGap(scramPath, scramSize, leadinLBA, writeOffsetBytes, tracks[i].FirstLBA)
		if err != nil {
			return err
		}
		for j := i; j < len(tracks); j++ {
			tracks[j].FirstLBA += gap
		}
	}
	return nil
}
```

`bytes` and `io` are already imported by `pack.go`. `scrambleTable` is package-scope. No new imports needed.

- [ ] **Step 9.3: Wire `SessionGaps` into `buildHatAndDelta`**

Still in `pack.go`, find `buildHatAndDelta` (around line 468) and the `BuildParams` construction (around line 501):

```go
	params := BuildParams{
		LeadinLBA:        opts.LeadinLBA,
		WriteOffsetBytes: writeOffsetBytes,
		ScramSize:        scramSize,
		BinFirstLBA:      tracks[0].FirstLBA,
		BinSectorCount:   binSectors,
		Tracks:           tracks,
	}
```

Add `SessionGaps`:

```go
	params := BuildParams{
		LeadinLBA:        opts.LeadinLBA,
		WriteOffsetBytes: writeOffsetBytes,
		ScramSize:        scramSize,
		BinFirstLBA:      tracks[0].FirstLBA,
		BinSectorCount:   binSectors,
		Tracks:           tracks,
		SessionGaps:      derivedSessionGaps(tracks),
	}
```

- [ ] **Step 9.4: Add a guard test for the audio-first-session-2 case**

Append to `pack_test.go`:

```go
func TestPackEnhancedCDRejectsAudioSecondSession(t *testing.T) {
	dir := t.TempDir()
	disc := synthEnhancedCD(t, SynthEnhancedCDOpts{})
	// Build a cue where the post-REM-SESSION-02 track is AUDIO.
	disc.Cue = ""
	for a := 0; a < 2; a++ {
		disc.Cue += fmt.Sprintf("FILE \"x (Track %d).bin\" BINARY\n  TRACK %02d AUDIO\n    INDEX 01 00:00:00\n",
			a+1, a+1)
	}
	disc.Cue += "REM SESSION 02\n"
	disc.Cue += "FILE \"x (Track 3).bin\" BINARY\n  TRACK 03 AUDIO\n    INDEX 01 00:00:00\n"
	// Re-truncate audio bins to match (drop the unused 3rd) and
	// re-emit fixture files.
	disc.AudioBins = disc.AudioBins[:2]
	disc.DataBin = disc.AudioBins[0][:1024*int(SectorSize)] // dummy 1024-sector "track 3"
	if err := os.WriteFile(filepath.Join(dir, "x.cue"), []byte(disc.Cue), 0o644); err != nil {
		t.Fatal(err)
	}
	for a, ab := range disc.AudioBins {
		p := filepath.Join(dir, fmt.Sprintf("x (Track %d).bin", a+1))
		if err := os.WriteFile(p, ab, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "x (Track 3).bin"), disc.DataBin, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.scram"), disc.Scram, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "x.miniscram")
	err := Pack(PackOptions{
		CuePath:    filepath.Join(dir, "x.cue"),
		ScramPath:  filepath.Join(dir, "x.scram"),
		OutputPath: out,
	}, nil)
	if !errors.Is(err, ErrSessionFirstTrackNotData) {
		t.Fatalf("expected ErrSessionFirstTrackNotData, got %v", err)
	}
}
```

- [ ] **Step 9.5: Run targeted tests**

```bash
go test -run "TestPackEnhancedCDRejectsAudioSecondSession" ./...
```

Expected: PASS.

```bash
go test -run TestPackEnhancedCDPlaceholder ./...
```

Expected: FAIL — the placeholder test asserted `*LayoutMismatchError`, but Pack now succeeds (or at least proceeds past layout-check). That's the signal to flip the test in Task 11.

- [ ] **Step 9.6: Commit**

```bash
git add pack.go pack_test.go
git commit -m "pack: detect inter-session gap from scram and adjust track FirstLBAs"
```

---

### Task 10: Wire `SessionGaps` into unpack

**Files:**
- Modify: `unpack.go`

- [ ] **Step 10.1: Find unpack's `BuildParams` construction**

```bash
grep -n "BuildParams{" unpack.go
```

Expected: one match where Unpack assembles the params for round-trip generation.

- [ ] **Step 10.2: Add `SessionGaps`**

Mirror the Pack-side change. The exact `BuildParams{...}` literal in `unpack.go` will look similar to:

```go
	params := BuildParams{
		LeadinLBA:        m.LeadinLBA,
		WriteOffsetBytes: m.WriteOffsetBytes,
		ScramSize:        m.Scram.Size,
		BinFirstLBA:      m.BinFirstLBA(),
		BinSectorCount:   m.BinSectorCount(),
		Tracks:           m.Tracks,
	}
```

Add `SessionGaps`:

```go
	params := BuildParams{
		LeadinLBA:        m.LeadinLBA,
		WriteOffsetBytes: m.WriteOffsetBytes,
		ScramSize:        m.Scram.Size,
		BinFirstLBA:      m.BinFirstLBA(),
		BinSectorCount:   m.BinSectorCount(),
		Tracks:           m.Tracks,
		SessionGaps:      derivedSessionGaps(m.Tracks),
	}
```

- [ ] **Step 10.3: Run full test suite**

```bash
go test ./...
```

Expected: PASS for all single-session tests. The Enhanced-CD placeholder test is still red from Task 9.5 — leave it for Task 11.

- [ ] **Step 10.4: Commit**

```bash
git add unpack.go
git commit -m "unpack: wire SessionGaps from manifest into BuildParams"
```

---

### Task 11: Flip the placeholder test to a successful round-trip

**Files:**
- Modify: `pack_test.go`

- [ ] **Step 11.1: Replace the placeholder body with the round-trip assertions**

In `pack_test.go`, find `TestPackEnhancedCDPlaceholder` (added in Task 5). Rename and replace:

```go
func TestPackEnhancedCDRoundTrip(t *testing.T) {
	dir := t.TempDir()
	disc := synthEnhancedCD(t, SynthEnhancedCDOpts{})
	cuePath := writeEnhancedCDFixture(t, dir, disc)
	scramPath := filepath.Join(dir, "x.scram")
	containerPath := filepath.Join(dir, "x.miniscram")

	// Pack succeeds: detection finds the gap, builder emits matching
	// content for the gap region, layout-mismatch ratio stays at 0.
	if err := Pack(PackOptions{
		CuePath:    cuePath,
		ScramPath:  scramPath,
		OutputPath: containerPath,
	}, nil); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	// Round-trip the container into a temp .scram and byte-compare.
	roundtripPath := filepath.Join(dir, "x.scram.roundtrip")
	if err := Unpack(UnpackOptions{
		ContainerPath: containerPath,
		OutputPath:    roundtripPath,
	}, nil); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	original, err := os.ReadFile(scramPath)
	if err != nil {
		t.Fatal(err)
	}
	roundtripped, err := os.ReadFile(roundtripPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, roundtripped) {
		t.Fatalf("round-trip scram differs from original (len %d vs %d)",
			len(original), len(roundtripped))
	}

	// Container should be small: bin + delta with a tiny delta because
	// the synth scram matches our prediction sector-for-sector. Pin a
	// generous upper bound so the test catches a regression where the
	// delta blows up but isn't brittle against minor format changes.
	info, err := os.Stat(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	const maxContainerKB = 64
	if info.Size() > maxContainerKB*1024 {
		t.Fatalf("container size %d bytes exceeds %d KB upper bound",
			info.Size(), maxContainerKB)
	}
}
```

If the `Unpack` API in `unpack.go` differs (different option struct name or signature), inspect with `grep -n "func Unpack" unpack.go` and adapt the call. The intent — round-trip the container into a temp file and byte-compare — is what matters.

- [ ] **Step 11.2: Run the new test**

```bash
go test -run TestPackEnhancedCDRoundTrip ./...
```

Expected: PASS. If it fails:
- A non-zero delta means some sector isn't being predicted correctly. Inspect with `go test -run TestPackEnhancedCDRoundTrip -v` and check whether the failing region is the audio-leading pregap (Task 7), the session gap (Task 7), or the data track (Task 9 LBA shift). Re-read the failing test's mismatch report.
- A `LayoutMismatchError` means the FirstLBA shift didn't take effect — re-verify that `applySessionGaps` mutates `tracks` in place AND that `buildHatAndDelta` receives the mutated slice (it does, because `tracks` is passed by reference into the slice).

- [ ] **Step 11.3: Run the FULL suite as a final regression check**

```bash
go test ./...
```

Expected: PASS. Also run the GUI test package:

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test ./...
```

Expected: PASS.

- [ ] **Step 11.4: Commit**

```bash
git add pack_test.go
git commit -m "test: Enhanced-CD round-trips byte-exact; placeholder flips to green"
```

---

### Task 12: Open the PR

- [ ] **Step 12.1: Push the branch**

```bash
git push -u origin feat/enhanced-cd-design
```

- [ ] **Step 12.2: Open the PR**

```bash
gh pr create --title "Enhanced CD (multi-session) pack support + GUI fail-reason fix (#44)" --body "$(cat <<'EOF'
## Summary

- Fixes #44. Diagnoses the failure shown in the issue (bare "fail" in the queue) as **multi-session ignorance**: `ParseCue` skipped `REM SESSION NN`, `ResolveCue` placed the session-2 data track ~11400 sectors short of its real position in the scram, every data sector mismatched, layout-mismatch abort tripped.
- GUI: failed queue rows now render the captured error reason instead of a bare "fail" tag.
- Cue: `REM SESSION NN` is parsed and stamped on the subsequent track via a new `Track.Session` field (omitempty, byte-identical manifests for single-session discs).
- Pack: detects each inter-session gap from the scram by locking onto the next session's first scrambled sync; bound-checks against [11400, 30000] sectors. Audio-leading session 2 returns a typed `ErrSessionFirstTrackNotData`.
- Builder: new `regionAt` classifier handles the seven structural regions (leadin / pregap / bin / gap-leadout / gap-leadin / gap-pregap / leadout). Audio-leading discs now emit silent audio in the track-1 pregap rather than scrambled Mode 1 zero.
- Synthetic `synthEnhancedCD` fixture exercises the whole path.

Design: `docs/superpowers/specs/2026-05-15-enhanced-cd-design.md`.
Plan: `docs/superpowers/plans/2026-05-15-enhanced-cd.md`.

## Test plan

- [ ] `go test ./...` passes on linux/amd64.
- [ ] `cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test ./...` passes.
- [ ] Manual: load a packed (any) container in the GUI, force a fail (e.g. delete the scram), confirm the queue row shows the error string rather than bare "fail".
- [ ] Manual (if a real Enhanced CD dump is available): pack it end-to-end, confirm byte-exact round-trip.
EOF
)"
```

---

## Self-review

**Spec coverage** (against `docs/superpowers/specs/2026-05-15-enhanced-cd-design.md`):
- §1 GUI fail reason → Task 1.
- §2 `synthEnhancedCD` + red test → Tasks 4–5, flipped in Task 11.
- §3.a `Track.Session`, `REM SESSION` parsing → Tasks 2–3.
- §3.b Gap detection, bounds check, `ErrSessionFirstTrackNotData` rejection → Task 9.
- §3.c `SessionGap`, `regionAt`, audio-leading pregap fix → Tasks 6–7.
- §3 unpack reconstructs `SessionGaps` via `derivedSessionGaps` → Tasks 8 and 10.
- Test coverage additions (cue REM SESSION, regionAt, derivedSessionGaps, audio-first-session-2 reject, round-trip) → Tasks 3, 6, 8, 9, 11.

**Placeholder scan:** All steps contain full code or exact commands. No "TODO", "TBD", "implement later". The single "if it fails" paragraph in Task 11 step 2 is debugging guidance, not a placeholder for missing implementation.

**Type consistency:**
- `SessionGap` field names (`StartLBA`, `LeadoutSectors`, `LeadinSectors`, `PregapSectors`) — used identically in Tasks 6, 8, 9.
- `region` enum constants (`regionLeadin`, etc.) — used identically in Tasks 6, 7.
- `Track.Session` — added in Task 2, used in Tasks 3, 6, 8, 9.
- `derivedSessionGaps(tracks)` — defined in Task 8, called in Tasks 9 (via Pack-side BuildParams) and 10 (Unpack-side).
- `ErrSessionGapOutOfRange`, `ErrSessionFirstTrackNotData` — defined in Task 9, asserted in Task 9.
- `Pack` / `Unpack` option structs in Task 11 follow whatever shape the repo already has — the plan flags this and tells the executor to adapt if the signature differs.
