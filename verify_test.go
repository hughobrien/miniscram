package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packForVerify packs a synthetic disc and returns the bin path,
// container path, dir, and parsed manifest. Reused by every verify
// test that needs a known-good baseline container.
func packForVerify(t *testing.T) (binPath, containerPath, dir string, m *Manifest) {
	t.Helper()
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, -48, 10)
	containerPath = filepath.Join(dir, "x.miniscram")
	rep := NewReporter(io.Discard, true)
	if err := Pack(PackOptions{
		BinPath: binPath, CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, rep); err != nil {
		t.Fatal(err)
	}
	mm, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	return binPath, containerPath, dir, mm
}

func assertNoVerifyTempfile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "miniscram-verify-") {
			t.Errorf("tempfile not cleaned up: %s", e.Name())
		}
	}
}

func TestVerifySynthDiscOK(t *testing.T) {
	binPath, containerPath, dir, _ := packForVerify(t)
	if err := Verify(VerifyOptions{
		BinPath: binPath, ContainerPath: containerPath,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	assertNoVerifyTempfile(t, dir)
}

func TestVerifyDetectsScramSHA256Mismatch(t *testing.T) {
	binPath, containerPath, dir, m := packForVerify(t)

	// Locate the recorded scram_sha256 string inside the container's
	// JSON manifest and flip one bit. The recovered scram still hashes
	// to the original (correct) value, but the manifest now disagrees.
	data, err := os.ReadFile(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(data, []byte(m.ScramSHA256))
	if idx < 0 {
		t.Fatal("scram_sha256 string not present in container")
	}
	data[idx] ^= 1
	if err := os.WriteFile(containerPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	err = Verify(VerifyOptions{
		BinPath: binPath, ContainerPath: containerPath,
	}, NewReporter(io.Discard, true))
	if !errors.Is(err, errOutputSHA256Mismatch) {
		t.Fatalf("expected errOutputSHA256Mismatch, got %v", err)
	}
	assertNoVerifyTempfile(t, dir)
}

func TestVerifyDetectsWrongBin(t *testing.T) {
	_, containerPath, dir, _ := packForVerify(t)
	wrongBin := filepath.Join(dir, "wrong.bin")
	if err := os.WriteFile(wrongBin, []byte("not the right bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Verify(VerifyOptions{
		BinPath: wrongBin, ContainerPath: containerPath,
	}, NewReporter(io.Discard, true))
	if !errors.Is(err, errBinSHA256Mismatch) {
		t.Fatalf("expected errBinSHA256Mismatch, got %v", err)
	}
	assertNoVerifyTempfile(t, dir)
}
