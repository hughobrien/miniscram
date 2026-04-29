// /home/hugh/miniscram/cue.go
package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Track is a single track entry in a cuesheet, augmented with
// filesystem metadata at pack time.
type Track struct {
	Number   int    `json:"number"`
	Mode     string `json:"mode"`      // "MODE1/2352", "MODE2/2352", or "AUDIO"
	FirstLBA int32  `json:"first_lba"` // absolute LBA where this track's FILE begins (set by ResolveCue)
	Size     int64  `json:"size"`      // bytes in this track's .bin file (set by ResolveCue)
	Filename string `json:"filename"`  // basename of source .bin (set by ParseCue)
	MD5      string `json:"md5"`       // lowercase hex (set at pack time)
	SHA1     string `json:"sha1"`
	SHA256   string `json:"sha256"`
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
