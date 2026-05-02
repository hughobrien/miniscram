package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packSyntheticContainer packs a clean synthetic disc and returns the
// container path. Reused by CLI tests that need a real container.
func packSyntheticContainer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	disc := synthDisc(t, SynthOpts{MainSectors: 100, LeadoutSectors: 10})
	_, scramPath, cuePath := writeFixture(t, dir, disc)
	out := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: out, LeadinLBA: LBAPregapStart,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHashFile(t *testing.T) {
	for _, tc := range []struct {
		content    []byte
		wantMD5    string
		wantSHA1   string
		wantSHA256 string
	}{
		{nil,
			"d41d8cd98f00b204e9800998ecf8427e",
			"da39a3ee5e6b4b0d3255bfef95601890afd80709",
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{[]byte("abc"),
			"900150983cd24fb0d6963f7d28e17f72",
			"a9993e364706816aba3e25717850c26c9cd0d89d",
			"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	} {
		tmp := filepath.Join(t.TempDir(), "f")
		os.WriteFile(tmp, tc.content, 0o644)
		got, err := hashFile(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if got.MD5 != tc.wantMD5 || got.SHA1 != tc.wantSHA1 || got.SHA256 != tc.wantSHA256 {
			t.Errorf("hashFile(%q) = %+v; want %s/%s/%s", tc.content, got, tc.wantMD5, tc.wantSHA1, tc.wantSHA256)
		}
	}
	if _, err := hashFile("/nonexistent/path/here"); err == nil {
		t.Fatal("expected error opening nonexistent file")
	}
}

func TestCompareHashes(t *testing.T) {
	base := FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "ccc"}
	if err := compareHashes(base, base); err != nil {
		t.Fatalf("all-match: %v", err)
	}
	for _, tc := range []struct {
		got  FileHashes
		want string
	}{
		{FileHashes{MD5: "xxx", SHA1: "bbb", SHA256: "ccc"}, "md5"},
		{FileHashes{MD5: "aaa", SHA1: "yyy", SHA256: "ccc"}, "sha1"},
		{FileHashes{MD5: "aaa", SHA1: "bbb", SHA256: "zzz"}, "sha256"},
	} {
		err := compareHashes(tc.got, base)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("compareHashes: err=%v, want %q in message", err, tc.want)
		}
	}
}
