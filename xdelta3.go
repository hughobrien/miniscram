// /home/hugh/miniscram/xdelta3.go
package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
)

// XDelta3Encode runs `xdelta3 -e -9 -B <window> -f -s <source> <target> <delta>`.
// The -f flag forces overwrite of an existing delta path. The window
// is the source window size in bytes; pass at least the source size
// so xdelta3 can find matches across the whole input.
func XDelta3Encode(source, target, delta string, sourceWindow int64) error {
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
