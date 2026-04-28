//go:build redump_data

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const (
	deusExDir   = "/home/hugh/miniscram/deus-ex"
	deusExStem  = "DeusEx_v1002f"
	maxDeltaPct = 0.01 // 1% of scram size
)

func TestE2EDeusEx(t *testing.T) {
	if _, err := os.Stat(filepath.Join(deusExDir, deusExStem+".scram")); err != nil {
		t.Skipf("deus ex dataset not present: %v", err)
	}
	// Use a temp dir on the same filesystem as the deus-ex dataset to
	// avoid /tmp overflow (the test produces ~900 MB intermediate files).
	tmp, err := os.MkdirTemp(deusExDir, "miniscram-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	containerPath := filepath.Join(tmp, deusExStem+".miniscram")
	rep := NewReporter(io.Discard, true)
	if err := Pack(PackOptions{
		BinPath:    filepath.Join(deusExDir, deusExStem+".bin"),
		CuePath:    filepath.Join(deusExDir, deusExStem+".cue"),
		ScramPath:  filepath.Join(deusExDir, deusExStem+".scram"),
		OutputPath: containerPath,
		Verify:     true,
	}, rep); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	m, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.ErrorSectorCount != 0 {
		t.Errorf("error_sector_count = %d; submission info reports 0", m.ErrorSectorCount)
	}
	pct := float64(m.DeltaSize) / float64(m.ScramSize)
	if pct >= maxDeltaPct {
		t.Errorf("delta is %.4f%% of scram (>= 1%%); something is off in ε̂", pct*100)
	}

	// recover and byte-compare
	outPath := filepath.Join(tmp, deusExStem+".scram.recovered")
	if err := Unpack(UnpackOptions{
		BinPath:       filepath.Join(deusExDir, deusExStem+".bin"),
		ContainerPath: containerPath,
		OutputPath:    outPath,
		Verify:        true,
	}, rep); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if !filesEqual(t, outPath, filepath.Join(deusExDir, deusExStem+".scram")) {
		t.Fatal("recovered .scram differs from original")
	}
}

// filesEqual compares two files in 1-MiB chunks.
func filesEqual(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	defer fb.Close()
	bufA := make([]byte, 1<<20)
	bufB := make([]byte, 1<<20)
	for {
		nA, errA := io.ReadFull(fa, bufA)
		nB, errB := io.ReadFull(fb, bufB)
		if nA != nB {
			return false
		}
		if !bytes.Equal(bufA[:nA], bufB[:nB]) {
			return false
		}
		if errA == io.EOF || errA == io.ErrUnexpectedEOF {
			return errB == io.EOF || errB == io.ErrUnexpectedEOF
		}
		if errA != nil {
			t.Fatal(errA)
		}
	}
}
