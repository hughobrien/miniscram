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

// packForVerify packs a synthetic disc and returns the container path,
// dir, and parsed manifest. Reused by verify tests and cli_test.go.
func packForVerify(t *testing.T) (containerPath, dir string, m *Manifest) {
	t.Helper()
	disc := synthDisc(t, SynthOpts{MainSectors: 100, LeadoutSectors: 10, WriteOffset: -48})
	dir = t.TempDir()
	_, scramPath, cuePath := writeFixture(t, dir, disc)
	containerPath = filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	mm, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	return containerPath, dir, mm
}

func assertNoVerifyTempfile(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "miniscram-verify-") {
			t.Errorf("tempfile not cleaned up: %s", e.Name())
		}
	}
}

func TestVerifySynthDiscOK(t *testing.T) {
	containerPath, dir, _ := packForVerify(t)
	if err := Verify(VerifyOptions{ContainerPath: containerPath}, NewReporter(io.Discard, true)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	assertNoVerifyTempfile(t, dir)
}

func TestVerifyDetectsScramHashMismatch(t *testing.T) {
	containerPath, dir, m := packForVerify(t)
	tamperContainerHash(t, containerPath, m.Scram.Hashes.SHA256)
	err := Verify(VerifyOptions{ContainerPath: containerPath}, NewReporter(io.Discard, true))
	if !errors.Is(err, errOutputHashMismatch) {
		t.Fatalf("expected errOutputHashMismatch, got %v", err)
	}
	assertNoVerifyTempfile(t, dir)
}

func TestVerifyDetectsTruncatedContainer(t *testing.T) {
	containerPath, _, _ := packForVerify(t)
	os.WriteFile(containerPath, []byte("TRUNCATE"), 0o644)
	var buf bytes.Buffer
	err := Verify(VerifyOptions{ContainerPath: containerPath}, NewReporter(&buf, false))
	if err == nil {
		t.Fatal("expected error on truncated container")
	}
	if !strings.Contains(buf.String(), "FAIL") {
		t.Errorf("expected FAIL in reporter output; got: %q", buf.String())
	}
}

func TestCLIVerifyUsageErrors(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"verify", "a", "b", "c"}, io.Discard, &stderr); code != exitUsage {
		t.Fatalf("3-positional exit %d, want %d", code, exitUsage)
	}
	stderr.Reset()
	if code := run([]string{"verify", "/no/such/container.miniscram"}, io.Discard, &stderr); code != exitIO {
		t.Fatalf("missing file exit %d, want %d", code, exitIO)
	}
}
