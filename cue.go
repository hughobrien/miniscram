// /home/hugh/miniscram/cue.go
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Track is a single track entry in a cuesheet, augmented with
// filesystem metadata at pack time.
type Track struct {
	Number   int        `json:"number"`
	Mode     string     `json:"mode"`
	FirstLBA int32      `json:"first_lba"`
	Filename string     `json:"filename"`
	Size     int64      `json:"size"`
	Hashes   FileHashes `json:"hashes"`
}

// IsData reports whether the track's main-channel data is scrambled.
// AUDIO tracks are not scrambled; everything else is.
func (t Track) IsData() bool { return t.Mode != "AUDIO" }

var validModes = map[string]bool{
	"MODE1/2352": true,
	"MODE2/2352": true,
	"AUDIO":      true,
}

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
	var currentFile string // basename of the most recent FILE line
	var fileTrackCount int // number of TRACKs seen in currentFile (must end at 0 or 1)
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

// parseMSF turns "mm:ss:ff" (decimal, not BCD) into an LBA.
// Cuesheet INDEX values use MSF notation where 00:00:00 represents LBA 0.
// Example: "00:00:00" → 0; "04:00:00" → 18000 (4*60*75).
func parseMSF(s string) (int32, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("MSF must be mm:ss:ff, got %q", s)
	}
	m, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	sec, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	f, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, err
	}
	return int32(m*60*MSFFramesPerSecond + sec*MSFFramesPerSecond + f), nil
}

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
