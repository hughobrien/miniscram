package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packSyntheticContainer packs a clean synthetic disc and returns the
// container path. Reused by CLI tests that need a real container.
func packSyntheticContainer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	disc := synthDisc(t, SynthOpts{MainSectors: 100, LeadoutSectors: 10})
	_, scramPath, cuePath := writeFixture(t, dir, disc)
	out := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: out, LeadinLBA: LBAPregapStart,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHashFile(t *testing.T) {
	for _, tc := range []struct {
		content    []byte
		wantMD5    string
		wantSHA1   string
		wantSHA256 string
	}{
		{nil,
			"d41d8cd98f00b204e9800998ecf8427e",
			"da39a3ee5e6b4b0d3255bfef95601890afd80709",
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{[]byte("abc"),
			"900150983cd24fb0d6963f7d28e17f72",
			"a9993e364706816aba3e25717850c26c9cd0d89d",
			"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	} {
		tmp := filepath.Join(t.TempDir(), "f")
		os.WriteFile(tmp, tc.content, 0o644)
		got, err := hashFile(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if got.MD5 != tc.wantMD5 || got.SHA1 != tc.wantSHA1 || got.SHA256 != tc.wantSHA256 {
			t.Errorf("hashFile(%q) = %+v; want %s/%s/%s", tc.content, got, tc.wantMD5, tc.wantSHA1, tc.wantSHA256)
		}
	}
	if _, err := hashFile("/nonexistent/path/here"); err == nil {
		t.Fatal("expected error opening nonexistent file")
	}
}

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

func TestPackEnhancedCDRejectsAudioSecondSession(t *testing.T) {
	dir := t.TempDir()
	disc := synthEnhancedCD(t, SynthEnhancedCDOpts{})
	// Build a cue where the post-REM-SESSION-02 track is AUDIO.
	disc.Cue = ""
	for a := 0; a < 2; a++ {
		disc.Cue += fmt.Sprintf("FILE \"x (Track %d).bin\" BINARY\n  TRACK %02d MODE1/2352\n    INDEX 01 00:00:00\n",
			a+1, a+1)
	}
	disc.Cue += "REM SESSION 02\n"
	disc.Cue += "FILE \"x (Track 3).bin\" BINARY\n  TRACK 03 AUDIO\n    INDEX 01 00:00:00\n"
	// Re-truncate audio bins to match (drop the unused 3rd) and
	// re-emit fixture files. The session-1 bins are declared MODE1/2352
	// in the cue; ResolveCue only stats file size, so the byte content
	// does not need to be valid sector data for this error-path test.
	disc.AudioBins = disc.AudioBins[:2]
	disc.DataBin = make([]byte, 1024*int(SectorSize)) // dummy 1024-sector "track 3"
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

// TestPackRejectsAudioOnlyCue confirms Pack short-circuits before any
// scram I/O when every TRACK in the cue is AUDIO. ScramPath points to
// a nonexistent file on purpose: if the early-reject ever regresses,
// os.Stat would surface a different error and this test would still
// fail (just with the wrong message), making the regression obvious.
func TestPackRejectsAudioOnlyCue(t *testing.T) {
	dir := t.TempDir()
	cue := "FILE \"x (Track 1).bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n" +
		"FILE \"x (Track 2).bin\" BINARY\n  TRACK 02 AUDIO\n    INDEX 01 00:00:00\n"
	if err := os.WriteFile(filepath.Join(dir, "x.cue"), []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	// ResolveCue stats each FILE; one-sector audio bins are enough.
	for _, name := range []string{"x (Track 1).bin", "x (Track 2).bin"} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, SectorSize), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := Pack(PackOptions{
		CuePath:    filepath.Join(dir, "x.cue"),
		ScramPath:  filepath.Join(dir, "does-not-exist.scram"),
		OutputPath: filepath.Join(dir, "x.miniscram"),
	}, nil)
	if !errors.Is(err, ErrAudioOnlyDisc) {
		t.Fatalf("expected ErrAudioOnlyDisc, got %v", err)
	}
}

// capturingReporter records all calls for assertion. Implements the
// full Reporter interface; Step returns a no-op StepHandle that
// records Fail-with-error calls for completeness.
type capturingReporter struct {
	info, warn []string
	unused     []struct {
		Path string
		Size int64
	}
	fails []error
}

func (c *capturingReporter) Step(label string) StepHandle {
	return &capturingStep{c: c}
}
func (c *capturingReporter) Info(format string, args ...any) {
	c.info = append(c.info, fmt.Sprintf(format, args...))
}
func (c *capturingReporter) Warn(format string, args ...any) {
	c.warn = append(c.warn, fmt.Sprintf(format, args...))
}
func (c *capturingReporter) UnusedScram(path string, size int64) {
	c.unused = append(c.unused, struct {
		Path string
		Size int64
	}{path, size})
}

type capturingStep struct{ c *capturingReporter }

func (s *capturingStep) Done(string, ...any) {}
func (s *capturingStep) Fail(err error)      { s.c.fails = append(s.c.fails, err) }

func TestPackEmitsUnusedScramEvent(t *testing.T) {
	dir := t.TempDir()
	cue := "FILE \"x (Track 1).bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n"
	if err := os.WriteFile(filepath.Join(dir, "x.cue"), []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x (Track 1).bin"), make([]byte, SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	const scramPayload = "junk-scram-content"
	scramPath := filepath.Join(dir, "x.scram")
	if err := os.WriteFile(scramPath, []byte(scramPayload), 0o644); err != nil {
		t.Fatal(err)
	}
	cap := &capturingReporter{}
	err := Pack(PackOptions{
		CuePath:    filepath.Join(dir, "x.cue"),
		ScramPath:  scramPath,
		OutputPath: filepath.Join(dir, "x.miniscram"),
	}, cap)
	if !errors.Is(err, ErrAudioOnlyDisc) {
		t.Fatalf("expected ErrAudioOnlyDisc, got %v", err)
	}
	if len(cap.unused) != 1 {
		t.Fatalf("got %d UnusedScram calls, want 1", len(cap.unused))
	}
	if cap.unused[0].Path != scramPath {
		t.Errorf("path = %q, want %q", cap.unused[0].Path, scramPath)
	}
	if cap.unused[0].Size != int64(len(scramPayload)) {
		t.Errorf("size = %d, want %d", cap.unused[0].Size, int64(len(scramPayload)))
	}
}

func TestPackUnusedScramStatFailureIsSwallowed(t *testing.T) {
	dir := t.TempDir()
	cue := "FILE \"x (Track 1).bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n"
	if err := os.WriteFile(filepath.Join(dir, "x.cue"), []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x (Track 1).bin"), make([]byte, SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	cap := &capturingReporter{}
	err := Pack(PackOptions{
		CuePath:    filepath.Join(dir, "x.cue"),
		ScramPath:  filepath.Join(dir, "does-not-exist.scram"),
		OutputPath: filepath.Join(dir, "x.miniscram"),
	}, cap)
	if !errors.Is(err, ErrAudioOnlyDisc) {
		t.Fatalf("expected ErrAudioOnlyDisc, got %v", err)
	}
	if len(cap.unused) != 0 {
		t.Errorf("got %d UnusedScram calls, want 0 (scram missing)", len(cap.unused))
	}
}

func TestCompareHashes(t *testing.T) {
	base := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	if err := compareHashes(base, base); err != nil {
		t.Fatalf("all-match: %v", err)
	}
	for _, tc := range []struct {
		got  FileHashes
		want string
	}{
		{FileHashes{MD5: "xxx", SHA1: "bbb", SHA256: "ccc"}, "md5"},
		{FileHashes{MD5: "aaa", SHA1: "yyy", SHA256: "ccc"}, "sha1"},
		{FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "zzz"}, "sha256"},
	} {
		err := compareHashes(tc.got, base)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("compareHashes: err=%v, want %q in message", err, tc.want)
		}
	}
}
