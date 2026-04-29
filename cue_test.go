// /home/hugh/miniscram/cue_test.go
package main

import (
	"strings"
	"testing"
)

func TestCueDeusExSingleTrack(t *testing.T) {
	src := `FILE "DeusEx_v1002f.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
`
	got, err := ParseCue(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d tracks; want 1", len(got))
	}
	tr := got[0]
	if tr.Number != 1 || tr.Mode != "MODE1/2352" || tr.FirstLBA != 0 {
		t.Fatalf("got %+v; want {1 MODE1/2352 0}", tr)
	}
}

func TestCueMixedDataAudio(t *testing.T) {
	// Track 1: data in first file.
	// Track 2: audio in second file.
	// (With multi-FILE parsing, each FILE contains exactly one TRACK)
	src := `FILE "Mixed1.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
FILE "Mixed2.bin" BINARY
  TRACK 02 AUDIO
    INDEX 00 03:58:00
    INDEX 01 04:00:00
`
	got, err := ParseCue(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tracks; want 2", len(got))
	}
	if got[0].Mode != "MODE1/2352" {
		t.Fatalf("track 1 mode = %q; want MODE1/2352", got[0].Mode)
	}
	if got[1].Mode != "AUDIO" {
		t.Fatalf("track 2 mode = %q; want AUDIO", got[1].Mode)
	}
}

func TestCueMode2(t *testing.T) {
	src := `FILE "M2.bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
`
	got, err := ParseCue(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Mode != "MODE2/2352" {
		t.Fatalf("got %s; want MODE2/2352", got[0].Mode)
	}
}

func TestCueRejectsUnknownMode(t *testing.T) {
	src := `FILE "X.bin" BINARY
  TRACK 01 MODE3/2336
    INDEX 01 00:00:00
`
	_, err := ParseCue(strings.NewReader(src))
	if err == nil {
		t.Fatal("expected error for unknown mode, got nil")
	}
	if !strings.Contains(err.Error(), "MODE3/2336") {
		t.Fatalf("error %q does not mention bad token", err.Error())
	}
}

func TestCueIgnoresCommentsAndBlankLines(t *testing.T) {
	src := `REM GENRE Action

FILE "X.bin" BINARY
  TRACK 01 MODE1/2352

    INDEX 01 00:00:00
`
	got, err := ParseCue(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d tracks", len(got))
	}
}

func TestCueRequiresIndex01(t *testing.T) {
	src := `FILE "X.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 00 00:00:00
`
	_, err := ParseCue(strings.NewReader(src))
	if err == nil {
		t.Fatal("expected error for missing INDEX 01")
	}
}

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
