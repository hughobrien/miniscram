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
	return writeSynthDiscFilesWithMode(t, mainSectors, writeOffsetBytes, leadoutSectors, 0x01, "MODE1/2352")
}

// writeSynthDiscFilesWithMode is the parameterized variant for tests
// that need a specific mode (e.g. MODE2/2352 for B4's Mode 2 fixture).
func writeSynthDiscFilesWithMode(t *testing.T, mainSectors int, writeOffsetBytes int, leadoutSectors int32, modeByte byte, modeStr string) (binPath, cuePath, scramPath, dir string) {
	t.Helper()
	bin, scram, params := synthDiscWithMode(t, mainSectors, writeOffsetBytes, leadoutSectors, modeByte, modeStr)
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
	cueContent := `FILE "x.bin" BINARY
  TRACK 01 ` + modeStr + `
    INDEX 01 00:00:00
`
	if err := os.WriteFile(cuePath, []byte(cueContent), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = params // params reused via reading the .cue
	return binPath, cuePath, scramPath, dir
}

func TestPackCleanDisc(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	_ = binPath // .bin lives next to .cue; ResolveCue finds it via cue
	outPath := filepath.Join(dir, "x.miniscram")
	rep := NewReporter(io.Discard, true)
	err := Pack(PackOptions{
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
	m, _, _, err := ReadContainer(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.WriteOffsetBytes != 0 {
		t.Fatalf("write offset = %d; want 0", m.WriteOffsetBytes)
	}
	want := mustHashFile(t, scramPath)
	if m.Scram.Hashes.SHA256 != want {
		t.Fatalf("scram sha256 = %s; want %s", m.Scram.Hashes.SHA256, want)
	}
}

func TestPackDetectsNegativeWriteOffset(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, -48, 10)
	_ = binPath // .bin lives next to .cue; ResolveCue finds it via cue
	outPath := filepath.Join(dir, "x.miniscram")
	err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: outPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true))
	if err != nil {
		t.Fatal(err)
	}
	m, _, _, _ := ReadContainer(outPath)
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

func TestPackPopulatesScramHashes(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	containerPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	m, _, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{
		"Scram.Hashes.MD5":    m.Scram.Hashes.MD5,
		"Scram.Hashes.SHA1":   m.Scram.Hashes.SHA1,
		"Scram.Hashes.SHA256": m.Scram.Hashes.SHA256,
	} {
		if got == "" {
			t.Errorf("%s is empty in manifest", name)
		}
	}
	// Cross-check: recompute scram hashes via hashFile and compare.
	fresh, err := hashFile(scramPath)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.MD5 != m.Scram.Hashes.MD5 || fresh.SHA1 != m.Scram.Hashes.SHA1 || fresh.SHA256 != m.Scram.Hashes.SHA256 {
		t.Errorf("scram hashes don't match a fresh hashFile run")
	}
	_ = binPath
}

func TestPackPopulatesPerTrackHashes(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	_ = binPath // .bin lives next to .cue; ResolveCue finds it via cue
	_ = scramPath
	containerPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	m, _, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(m.Tracks))
	}
	tr := m.Tracks[0]
	if tr.Hashes.MD5 == "" || tr.Hashes.SHA1 == "" || tr.Hashes.SHA256 == "" {
		t.Errorf("track hashes empty: %+v", tr)
	}
	if tr.Size == 0 {
		t.Errorf("track size = 0")
	}
	if tr.Filename == "" {
		t.Errorf("track filename empty")
	}
}

func TestReadContainerRejectsWrongVersion(t *testing.T) {
	dir := t.TempDir()
	v9 := filepath.Join(dir, "v9.miniscram")
	header := make([]byte, containerHeaderSize)
	copy(header, "MSCM")
	header[4] = 0x09
	if err := os.WriteFile(v9, header, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := ReadContainer(v9)
	if err == nil {
		t.Fatal("expected error reading wrong-version container")
	}
	if !strings.Contains(err.Error(), "unsupported container version") {
		t.Errorf("error doesn't mention unsupported version: %v", err)
	}
}
