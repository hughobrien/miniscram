// /home/hugh/miniscram/cue.go
package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Track is a single track entry in a cuesheet.
type Track struct {
	Number   int    // 1-based track number
	Mode     string // "MODE1/2352", "MODE2/2352", or "AUDIO"
	FirstLBA int32  // LBA of INDEX 01 (the user-visible track start)
}

// IsData reports whether the track's main-channel data is scrambled.
// AUDIO tracks are not scrambled; everything else is.
func (t Track) IsData() bool { return t.Mode != "AUDIO" }

var validModes = map[string]bool{
	"MODE1/2352": true,
	"MODE2/2352": true,
	"AUDIO":      true,
}

// ParseCue extracts TRACK / MODE / INDEX 01 from a cuesheet. It is a
// deliberate subset of the cue spec — enough to drive miniscram on
// Redumper output, no more.
func ParseCue(r io.Reader) ([]Track, error) {
	scanner := bufio.NewScanner(r)
	var tracks []Track
	var cur *Track
	var hasIndex01 bool
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
			// Ignored — we don't multi-file in miniscram's scope.
		case "TRACK":
			if err := flushTrack(); err != nil {
				return nil, err
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
			cur = &Track{Number: n, Mode: mode}
			hasIndex01 = false
		case "INDEX":
			if cur == nil {
				return nil, fmt.Errorf("INDEX before TRACK: %q", line)
			}
			if len(fields) < 3 {
				return nil, fmt.Errorf("malformed INDEX line: %q", line)
			}
			if fields[1] != "01" {
				continue // ignore INDEX 00 and others; we only need INDEX 01
			}
			lba, err := parseMSF(fields[2])
			if err != nil {
				return nil, fmt.Errorf("bad MSF in %q: %v", line, err)
			}
			cur.FirstLBA = lba
			hasIndex01 = true
		default:
			// PERFORMER, TITLE, CATALOG, etc. — ignored.
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
