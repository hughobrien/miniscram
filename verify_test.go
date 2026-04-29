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
// dir, and parsed manifest. Reused by every verify test that needs a
// known-good baseline container.
func packForVerify(t *testing.T) (containerPath, dir string, m *Manifest) {
	t.Helper()
	_, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, -48, 10)
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
	containerPath, dir, _ := packForVerify(t)
	if err := Verify(VerifyOptions{
		ContainerPath: containerPath,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	assertNoVerifyTempfile(t, dir)
}

func TestVerifyDetectsScramHashMismatch(t *testing.T) {
	containerPath, dir, m := packForVerify(t)

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
		ContainerPath: containerPath,
	}, NewReporter(io.Discard, true))
	if !errors.Is(err, errOutputHashMismatch) {
		t.Fatalf("expected errOutputHashMismatch, got %v", err)
	}
	assertNoVerifyTempfile(t, dir)
}

func TestCLIVerifyOK(t *testing.T) {
	containerPath, _, _ := packForVerify(t)
	var stderr bytes.Buffer
	code := run([]string{"verify", containerPath}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
}

func TestCLIVerifyExitCodes(t *testing.T) {
	containerPath, _, m := packForVerify(t)

	// Tampered scram_sha256 → exit 3.
	data, err := os.ReadFile(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(data, []byte(m.ScramSHA256))
	if idx < 0 {
		t.Fatal("scram_sha256 not present in container")
	}
	data[idx] ^= 1
	if err := os.WriteFile(containerPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := run([]string{"verify", containerPath}, io.Discard, &stderr)
	if code != exitVerifyFail {
		t.Fatalf("tampered exit %d, want %d; stderr=%s", code, exitVerifyFail, stderr.String())
	}
}

func TestCLIVerifyOnePositional(t *testing.T) {
	containerPath, _, _ := packForVerify(t)

	t.Run("one-arg-container", func(t *testing.T) {
		var stderr bytes.Buffer
		code := run([]string{"verify", containerPath}, io.Discard, &stderr)
		if code != exitOK {
			t.Fatalf("exit %d; stderr=%s", code, stderr.String())
		}
	})
}

func TestCLIVerifyHelp(t *testing.T) {
	// verify --help
	var stderr bytes.Buffer
	code := run([]string{"verify", "--help"}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("verify --help exit %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("USAGE:")) {
		t.Fatalf("verify --help missing USAGE; stderr=%s", stderr.String())
	}

	// help verify
	stderr.Reset()
	code = run([]string{"help", "verify"}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("help verify exit %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("USAGE:")) {
		t.Fatalf("help verify missing USAGE; stderr=%s", stderr.String())
	}

	// top-level help mentions verify
	stderr.Reset()
	code = run([]string{"--help"}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("top --help exit %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("verify")) {
		t.Fatalf("top help doesn't mention verify; stderr=%s", stderr.String())
	}
}

func TestVerifyDetectsTruncatedContainer(t *testing.T) {
	containerPath, _, _ := packForVerify(t)

	// Truncate the container to 8 bytes — too short to be a valid container.
	if err := os.WriteFile(containerPath, []byte("TRUNCATE"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	rep := NewReporter(&buf, false)
	err := Verify(VerifyOptions{
		ContainerPath: containerPath,
	}, rep)
	if err == nil {
		t.Fatal("expected error on truncated container, got nil")
	}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
	if !strings.Contains(buf.String(), "FAIL") {
		t.Errorf("expected reporter to surface failure; got: %q", buf.String())
	}
}

func TestCLIVerifyUsageErrors(t *testing.T) {
	// 3 positionals → usage error
	var stderr bytes.Buffer
	code := run([]string{"verify", "a", "b", "c"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("3-positional exit %d, want %d", code, exitUsage)
	}
	// unknown flag
	stderr.Reset()
	code = run([]string{"verify", "--no-such-flag"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("bad flag exit %d, want %d", code, exitUsage)
	}
	// missing input file → I/O error (single positional, file doesn't exist)
	stderr.Reset()
	code = run([]string{"verify", "/no/such/container.miniscram"}, io.Discard, &stderr)
	if code != exitIO {
		t.Fatalf("missing file exit %d, want %d (exitIO)", code, exitIO)
	}
}

// TestVerifyDetectsScramHashMismatchAllThree confirms the strict
// any-of-three policy: tampering ANY single recorded scram hash in
// the container's manifest causes Verify to fail with errOutputHashMismatch,
// not just sha256 mismatches.
func TestVerifyDetectsScramHashMismatchAllThree(t *testing.T) {
	for _, hashName := range []string{"scram_md5", "scram_sha1", "scram_sha256"} {
		t.Run(hashName, func(t *testing.T) {
			containerPath, _, m := packForVerify(t)

			// Identify which manifest hex string to tamper.
			var target string
			switch hashName {
			case "scram_md5":
				target = m.ScramMD5
			case "scram_sha1":
				target = m.ScramSHA1
			case "scram_sha256":
				target = m.ScramSHA256
			}

			data, err := os.ReadFile(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			idx := bytes.Index(data, []byte(target))
			if idx < 0 {
				t.Fatalf("hash %q (%s) not found in container", hashName, target)
			}
			data[idx] ^= 1
			if err := os.WriteFile(containerPath, data, 0o644); err != nil {
				t.Fatal(err)
			}

			err = Verify(VerifyOptions{
				ContainerPath: containerPath,
			}, NewReporter(io.Discard, true))
			if !errors.Is(err, errOutputHashMismatch) {
				t.Fatalf("expected errOutputHashMismatch tampering %s, got %v", hashName, err)
			}
		})
	}
}
