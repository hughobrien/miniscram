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

func TestCLIVerifyOK(t *testing.T) {
	binPath, containerPath, _, _ := packForVerify(t)
	var stderr bytes.Buffer
	code := run([]string{"verify", binPath, containerPath}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
}

func TestCLIVerifyExitCodes(t *testing.T) {
	binPath, containerPath, dir, m := packForVerify(t)

	// Wrong bin → exit 5.
	wrongBin := filepath.Join(dir, "wrong.bin")
	if err := os.WriteFile(wrongBin, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := run([]string{"verify", wrongBin, containerPath}, io.Discard, &stderr)
	if code != exitWrongBin {
		t.Fatalf("wrong-bin exit %d, want %d; stderr=%s", code, exitWrongBin, stderr.String())
	}

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
	stderr.Reset()
	code = run([]string{"verify", binPath, containerPath}, io.Discard, &stderr)
	if code != exitVerifyFail {
		t.Fatalf("tampered exit %d, want %d; stderr=%s", code, exitVerifyFail, stderr.String())
	}
}

func TestCLIVerifyDiscovery(t *testing.T) {
	binPath, containerPath, dir, _ := packForVerify(t)

	t.Run("zero-arg-cwd", func(t *testing.T) {
		cwd, _ := os.Getwd()
		t.Cleanup(func() { os.Chdir(cwd) })
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		code := run([]string{"verify"}, io.Discard, &stderr)
		if code != exitOK {
			t.Fatalf("exit %d; stderr=%s", code, stderr.String())
		}
	})

	t.Run("one-arg-stem", func(t *testing.T) {
		stem := strings.TrimSuffix(containerPath, ".miniscram")
		var stderr bytes.Buffer
		code := run([]string{"verify", stem}, io.Discard, &stderr)
		if code != exitOK {
			t.Fatalf("exit %d; stderr=%s", code, stderr.String())
		}
	})

	t.Run("two-arg-explicit", func(t *testing.T) {
		var stderr bytes.Buffer
		code := run([]string{"verify", binPath, containerPath}, io.Discard, &stderr)
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

func TestCLIVerifyUsageErrors(t *testing.T) {
	// 3 positionals
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
	// missing input files (caught at resolveUnpackInputs)
	stderr.Reset()
	code = run([]string{"verify", "/no/such/bin", "/no/such/container.miniscram"}, io.Discard, &stderr)
	// 2 positionals don't trigger discovery, so the resolver returns
	// the explicit pair without checking existence; the I/O failure
	// surfaces from ReadContainer/Unpack and routes to exitIO.
	if code != exitIO {
		t.Fatalf("missing files exit %d, want %d (exitIO)", code, exitIO)
	}
	// missing input via stem (DiscoverUnpackFromArg checks existence)
	stderr.Reset()
	code = run([]string{"verify", "/no/such/stem"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("missing-stem exit %d, want %d", code, exitUsage)
	}
}
