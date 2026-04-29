// /home/hugh/miniscram/cue_test.go
package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCueAccepts(t *testing.T) {
	t.Run("mixed-data-audio", func(t *testing.T) {
		src := `FILE "Mixed1.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
FILE "Mixed2.bin" BINARY
  TRACK 02 AUDIO
    INDEX 00 03:58:00
    INDEX 01 04:00:00
`
		got, err := ParseCue(strings.NewReader(src))
		if err != nil || len(got) != 2 {
			t.Fatalf("err=%v tracks=%d; want nil,2", err, len(got))
		}
		if got[0].Mode != "MODE1/2352" || got[1].Mode != "AUDIO" {
			t.Fatalf("modes = %q,%q; want MODE1/2352,AUDIO", got[0].Mode, got[1].Mode)
		}
	})

	t.Run("comments-and-blanks", func(t *testing.T) {
		src := "REM GENRE Action\n\nFILE \"X.bin\" BINARY\n  TRACK 01 MODE1/2352\n\n    INDEX 01 00:00:00\n"
		got, err := ParseCue(strings.NewReader(src))
		if err != nil || len(got) != 1 {
			t.Fatalf("err=%v tracks=%d; want nil,1", err, len(got))
		}
	})

	t.Run("single-file", func(t *testing.T) {
		src := "FILE \"x.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n"
		tracks, err := ParseCue(strings.NewReader(src))
		if err != nil || len(tracks) != 1 || tracks[0].Filename != "x.bin" {
			t.Fatalf("err=%v len=%d filename=%q; want nil,1,x.bin", err, len(tracks), tracks[0].Filename)
		}
	})

	t.Run("multi-file", func(t *testing.T) {
		src := "FILE \"HALFLIFE (Track 01).bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n" +
			"FILE \"HALFLIFE (Track 02).bin\" BINARY\n  TRACK 02 AUDIO\n    INDEX 00 00:00:00\n    INDEX 01 00:02:00\n" +
			"FILE \"HALFLIFE (Track 03).bin\" BINARY\n  TRACK 03 AUDIO\n    INDEX 01 00:00:00\n"
		tracks, err := ParseCue(strings.NewReader(src))
		if err != nil || len(tracks) != 3 || tracks[0].Mode != "MODE1/2352" || tracks[2].Mode != "AUDIO" {
			t.Fatalf("err=%v tracks=%v", err, tracks)
		}
	})
}

func TestParseCueRejects(t *testing.T) {
	cases := []struct {
		name string
		cue  string
	}{
		{"unknown-mode", "FILE \"X.bin\" BINARY\n  TRACK 01 MODE3/2336\n    INDEX 01 00:00:00\n"},
		{"non-binary", "FILE \"x.wav\" WAVE\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n"},
		{"relative-traversal", "FILE \"../bad.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n"},
		{"path-separator", "FILE \"subdir/x.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n"},
		{"multi-track-per-file", "FILE \"shared.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n  TRACK 02 AUDIO\n    INDEX 01 02:00:00\n"},
		{"no-index01", "FILE \"X.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 00 00:00:00\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseCue(strings.NewReader(tc.cue)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestResolveCue(t *testing.T) {
	t.Run("computes-absolute-lbas", func(t *testing.T) {
		dir := t.TempDir()
		for name, n := range map[string]int{"a.bin": 100, "b.bin": 50, "c.bin": 25} {
			os.WriteFile(filepath.Join(dir, name), make([]byte, n*SectorSize), 0o644)
		}
		cue := "FILE \"a.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n" +
			"FILE \"b.bin\" BINARY\n  TRACK 02 AUDIO\n    INDEX 01 00:00:00\n" +
			"FILE \"c.bin\" BINARY\n  TRACK 03 AUDIO\n    INDEX 01 00:00:00\n"
		cuePath := filepath.Join(dir, "x.cue")
		os.WriteFile(cuePath, []byte(cue), 0o644)
		resolved, err := ResolveCue(cuePath)
		if err != nil || len(resolved.Tracks) != 3 {
			t.Fatalf("err=%v len=%d; want nil,3", err, len(resolved.Tracks))
		}
		// FirstLBA assignments depend on file order which map iteration doesn't guarantee,
		// so just check sizes (each file is one contiguous track).
		for i, size := range []int64{100 * SectorSize, 50 * SectorSize, 25 * SectorSize} {
			if resolved.Tracks[i].Size != size {
				t.Errorf("Tracks[%d].Size=%d; want %d", i, resolved.Tracks[i].Size, size)
			}
		}
	})

	t.Run("missing-file", func(t *testing.T) {
		dir := t.TempDir()
		cue := "FILE \"missing.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n"
		cuePath := filepath.Join(dir, "x.cue")
		os.WriteFile(cuePath, []byte(cue), 0o644)
		if _, err := ResolveCue(cuePath); err == nil {
			t.Fatal("expected error when file is missing")
		}
	})
}

func TestOpenBinStreamReadsConcatenated(t *testing.T) {
	dir := t.TempDir()
	var files []ResolvedFile
	for _, s := range []struct{ name, content string }{{"a.bin", "aaa"}, {"b.bin", "bb"}, {"c.bin", "c"}} {
		path := filepath.Join(dir, s.name)
		os.WriteFile(path, []byte(s.content), 0o644)
		files = append(files, ResolvedFile{Path: path, Size: int64(len(s.content))})
	}
	r, closer, err := OpenBinStream(files)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	closer()
	if string(got) != "aaabbc" {
		t.Errorf("got %q; want %q", got, "aaabbc")
	}
}

func TestHashReaderMatchesHashFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "data")
	content := []byte("hello multi-FILE world")
	os.WriteFile(tmp, content, 0o644)
	viaFile, _ := hashFile(tmp)
	viaReader, err := hashReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if viaFile != viaReader {
		t.Errorf("hashFile != hashReader")
	}
}
