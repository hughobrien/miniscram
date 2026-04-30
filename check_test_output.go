package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Minimal test to see actual reporter output
func TestShowReporterOutput(t *testing.T) {
	const (
		mainSectors    = 100
		leadoutSectors = 10
		failOffset     = 50
	)
	dir := t.TempDir()
	disc := synthDiscWithFailSector(t, mainSectors, leadoutSectors, failOffset)
	_, scramPath, cuePath := writeFixture(t, dir, disc)
	outPath := filepath.Join(dir, "x.miniscram")

	var repBuf strings.Builder
	rep := NewReporter(&repBuf, false)

	if err := Pack(PackOptions{
		CuePath:    cuePath,
		ScramPath:  scramPath,
		OutputPath: outPath,
		LeadinLBA:  LBAPregapStart,
		Verify:     true,
	}, rep); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	repOut := repBuf.String()
	fmt.Printf("=== REPORTER OUTPUT START ===\n%s\n=== REPORTER OUTPUT END ===\n", repOut)
	fmt.Printf("Contains '0 pass-through(s)': %v\n", strings.Contains(repOut, "0 pass-through(s)"))
	fmt.Printf("Contains '10 pass-through(s)': %v\n", strings.Contains(repOut, "10 pass-through(s)"))
	fmt.Printf("Contains '1 pass-through(s)': %v\n", strings.Contains(repOut, "1 pass-through(s)"))
}
