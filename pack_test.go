// /home/hugh/miniscram/pack_test.go
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestHashFile_EmptyFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(tmp, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hashFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	const (
		emptyMD5    = "d41d8cd98f00b204e9800998ecf8427e"
		emptySHA1   = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
		emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	)
	if got.MD5 != emptyMD5 {
		t.Errorf("MD5 = %s; want %s", got.MD5, emptyMD5)
	}
	if got.SHA1 != emptySHA1 {
		t.Errorf("SHA1 = %s; want %s", got.SHA1, emptySHA1)
	}
	if got.SHA256 != emptySHA256 {
		t.Errorf("SHA256 = %s; want %s", got.SHA256, emptySHA256)
	}
}

func TestHashFile_NonemptyFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "abc")
	if err := os.WriteFile(tmp, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hashFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	// Reference values from FIPS-180 / RFC 1321 test vectors for "abc".
	if got.MD5 != "900150983cd24fb0d6963f7d28e17f72" {
		t.Errorf("MD5 = %s", got.MD5)
	}
	if got.SHA1 != "a9993e364706816aba3e25717850c26c9cd0d89d" {
		t.Errorf("SHA1 = %s", got.SHA1)
	}
	if got.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("SHA256 = %s", got.SHA256)
	}
}

func TestHashFile_OpenError(t *testing.T) {
	_, err := hashFile("/nonexistent/path/here")
	if err == nil {
		t.Fatal("expected error opening nonexistent file")
	}
}

func TestCompareHashes_AllMatch(t *testing.T) {
	h := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	if err := compareHashes(h, h); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCompareHashes_MD5Mismatch(t *testing.T) {
	got := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	want := FileHashes{MD5: "xxx", SHA1: "bbb", SHA256: "ccc"}
	err := compareHashes(got, want)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "md5") {
		t.Errorf("error message missing 'md5': %v", err)
	}
}

func TestCompareHashes_SHA1Mismatch(t *testing.T) {
	got := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	want := FileHashes{MD5: "aaa", SHA1: "yyy", SHA256: "ccc"}
	err := compareHashes(got, want)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "sha1") {
		t.Errorf("error message missing 'sha1': %v", err)
	}
}

func TestCompareHashes_SHA256Mismatch(t *testing.T) {
	got := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	want := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "zzz"}
	err := compareHashes(got, want)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("error message missing 'sha256': %v", err)
	}
}

func TestCompareHashes_AllMismatch(t *testing.T) {
	got := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	want := FileHashes{MD5: "xxx", SHA1: "yyy", SHA256: "zzz"}
	err := compareHashes(got, want)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	msg := err.Error()
	for _, want := range []string{"md5", "sha1", "sha256"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %v", want, err)
		}
	}
}
