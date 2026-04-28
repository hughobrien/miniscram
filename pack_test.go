// /home/hugh/miniscram/pack_test.go
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// writeSynthDiscFiles writes synthDisc-produced bytes into a temp dir
// and returns the file paths.
func writeSynthDiscFiles(t *testing.T, mainSectors int, writeOffsetBytes int, leadoutSectors int32) (binPath, cuePath, scramPath, dir string) {
	t.Helper()
	bin, scram, params := synthDisc(t, mainSectors, writeOffsetBytes, leadoutSectors)
	dir = t.TempDir()
	binPath = filepath.Join(dir, "x.bin")
	scramPath = filepath.Join(dir, "x.scram")
	cuePath = filepath.Join(dir, "x.cue")
	if err := os.WriteFile(binPath, bin, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scramPath, scram, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cuePath, []byte(`FILE "x.bin" BINARY
  TRACK 01 MODE1/2352
    INDEX 01 00:00:00
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = params // params reused via reading the .cue
	return binPath, cuePath, scramPath, dir
}

func TestPackCleanDisc(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	// synthDisc above uses LeadinLBA = -150 (no leadin). Real Pack uses
	// LBALeadinStart = -45150, so we override via PackOptions.LeadinLBA.
	outPath := filepath.Join(dir, "x.miniscram")
	rep := NewReporter(io.Discard, true)
	err := Pack(PackOptions{
		BinPath:    binPath,
		CuePath:    cuePath,
		ScramPath:  scramPath,
		OutputPath: outPath,
		LeadinLBA:  LBAPregapStart,
		Verify:     true,
	}, rep)
	if err != nil {
		t.Fatal(err)
	}

	// confirm container parses and manifest looks right
	m, _, err := ReadContainer(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.WriteOffsetBytes != 0 {
		t.Fatalf("write offset = %d; want 0", m.WriteOffsetBytes)
	}
	if m.ErrorSectorCount != 0 {
		t.Fatalf("error count = %d; want 0", m.ErrorSectorCount)
	}
	want := mustHashFile(t, scramPath)
	if m.ScramSHA256 != want {
		t.Fatalf("scram sha256 = %s; want %s", m.ScramSHA256, want)
	}
}

func TestPackDetectsNegativeWriteOffset(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, -48, 10)
	outPath := filepath.Join(dir, "x.miniscram")
	err := Pack(PackOptions{
		BinPath: binPath, CuePath: cuePath, ScramPath: scramPath,
		OutputPath: outPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true))
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ := ReadContainer(outPath)
	if m.WriteOffsetBytes != -48 {
		t.Fatalf("write offset = %d; want -48", m.WriteOffsetBytes)
	}
}

func mustHashFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ensure the test file in this package can reach bytes
var _ = bytes.Equal
