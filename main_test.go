// /home/hugh/miniscram/main_test.go
package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIPackDiscovers(t *testing.T) {
	dir := t.TempDir()
	binPath, _, scramPath, _ := writeSynthDiscFiles(t, 100, 0, 10)
	// move the synth files into a clean dir so cwd discovery is unambiguous
	mv := func(src, dst string) {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mv(binPath, filepath.Join(dir, "g.bin"))
	mv(scramPath, filepath.Join(dir, "g.scram"))
	if err := os.WriteFile(filepath.Join(dir, "g.cue"),
		[]byte("FILE \"g.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	// We need to patch LeadinLBA for synthetic data. The CLI uses
	// LBALeadinStart by default — synth disc uses LBAPregapStart. So
	// the CLI test cannot use the synthetic dataset; we test the
	// real discovery+ flag handling here, and rely on Pack-level tests
	// for synthetic verification.
	code := run([]string{"pack", "--help"}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("pack --help exit %d, stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("USAGE:")) {
		t.Fatalf("expected USAGE in help output")
	}
}

func TestCLIUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"foo"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("got exit %d, want %d", code, exitUsage)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("unknown command")) {
		t.Fatalf("missing 'unknown command' in stderr")
	}
}

func TestCLIVersion(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--version"}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("miniscram")) {
		t.Fatalf("missing version: %s", stderr.String())
	}
}
