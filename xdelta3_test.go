// /home/hugh/miniscram/xdelta3_test.go
package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func ensureXDelta3(t *testing.T) {
	if _, err := exec.LookPath("xdelta3"); err != nil {
		t.Skip("xdelta3 not on PATH; skipping")
	}
}

func TestXDelta3RoundTrip(t *testing.T) {
	ensureXDelta3(t)
	dir := t.TempDir()
	src := make([]byte, 1<<20)
	if _, err := rand.Read(src); err != nil {
		t.Fatal(err)
	}
	tgt := append([]byte{}, src...)
	// flip a handful of bytes
	for i := 0; i < 1000; i++ {
		tgt[i*1024] ^= 0xFF
	}
	srcPath := filepath.Join(dir, "src")
	tgtPath := filepath.Join(dir, "tgt")
	deltaPath := filepath.Join(dir, "delta")
	outPath := filepath.Join(dir, "out")
	if err := os.WriteFile(srcPath, src, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tgtPath, tgt, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := XDelta3Encode(srcPath, tgtPath, deltaPath, int64(len(src))); err != nil {
		t.Fatal(err)
	}
	if err := XDelta3Decode(srcPath, deltaPath, outPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, tgt) {
		t.Fatalf("round-trip output != target")
	}
}

func TestXDelta3MissingBinary(t *testing.T) {
	// Temporarily clear PATH and assert the error mentions xdelta3.
	oldPATH := os.Getenv("PATH")
	t.Setenv("PATH", "")
	defer t.Setenv("PATH", oldPATH)
	err := XDelta3Encode("/dev/null", "/dev/null", "/tmp/nope", 4096)
	if err == nil {
		t.Fatal("expected error with empty PATH")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("xdelta3")) {
		t.Fatalf("error %q should mention xdelta3", err)
	}
}
