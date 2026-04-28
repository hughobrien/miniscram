// /home/hugh/miniscram/xdelta3.go
package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
)

// xdelta3SourceWindowCap caps the -B flag passed to xdelta3 -e. xdelta3
// allocates a buffer this large in memory; CD images can be ~900 MB
// which is too much. 256 MB is comfortable for the typical case where
// our ε̂ is sector-aligned with .scram (matches are always nearby).
const xdelta3SourceWindowCap = 256 * 1024 * 1024

// XDelta3Encode runs `xdelta3 -e -9 -B <window> -f -s <source> <target> <delta>`.
// The -f flag forces overwrite of an existing delta path. sourceWindow
// is the requested source window size in bytes; the actual value passed
// to xdelta3 is min(sourceWindow, xdelta3SourceWindowCap) to avoid
// out-of-memory kills on multi-hundred-MB sources.
func XDelta3Encode(source, target, delta string, sourceWindow int64) error {
	if sourceWindow > xdelta3SourceWindowCap {
		sourceWindow = xdelta3SourceWindowCap
	}
	args := []string{
		"-e", "-9", "-f",
		"-B", strconv.FormatInt(sourceWindow, 10),
		"-s", source,
		target,
		delta,
	}
	return runXDelta3(args)
}

// XDelta3Decode runs `xdelta3 -d -f -s <source> <delta> <output>`.
func XDelta3Decode(source, delta, output string) error {
	args := []string{"-d", "-f", "-s", source, delta, output}
	return runXDelta3(args)
}

func runXDelta3(args []string) error {
	if _, err := exec.LookPath("xdelta3"); err != nil {
		return fmt.Errorf("xdelta3 not found on PATH (try 'apt install xdelta3' or 'brew install xdelta'): %w", err)
	}
	cmd := exec.Command("xdelta3", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("xdelta3 %v failed: %w (stderr: %s)", args, err, stderr.String())
	}
	return nil
}
