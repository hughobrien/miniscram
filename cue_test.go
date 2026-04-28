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
	// Track 1: data starting at LBA 0 (MSF 00:00:00 in cuesheet).
	// Track 2: audio starting at MSF 04:00:00 = LBA 4*60*75 = 18000.
	src := `FILE "Mixed.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
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
	if got[0].Mode != "MODE1/2352" || got[0].FirstLBA != 0 {
		t.Fatalf("track 1 = %+v", got[0])
	}
	if got[1].Mode != "AUDIO" || got[1].FirstLBA != 18000 {
		t.Fatalf("track 2 = %+v", got[1])
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
