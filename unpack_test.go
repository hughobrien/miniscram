// /home/hugh/miniscram/unpack_test.go
package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestUnpackRoundTripSynthDisc(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, -48, 10)
	_ = binPath
	containerPath := filepath.Join(dir, "x.miniscram")
	rep := NewReporter(io.Discard, true)
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, rep); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "x.scram.recovered")
	if err := Unpack(UnpackOptions{
		ContainerPath: containerPath,
		OutputPath:    outPath,
		Verify:        true,
	}, rep); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(scramPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip differs (got %d bytes, want %d)", len(got), len(want))
	}
}

func TestUnpackRefusesOverwrite(t *testing.T) {
	_, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	containerPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "exists.scram")
	if err := os.WriteFile(outPath, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Unpack(UnpackOptions{
		ContainerPath: containerPath,
		OutputPath:    outPath,
		Verify:        true, Force: false,
	}, NewReporter(io.Discard, true))
	if err == nil {
		t.Fatal("expected error refusing to overwrite")
	}
}

func TestUnpackRejectsTrackFileSizeMismatch(t *testing.T) {
	_, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	containerPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	// Truncate the .bin file by one sector.
	binPathInDir := filepath.Join(dir, "x.bin")
	info, err := os.Stat(binPathInDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(binPathInDir, info.Size()-int64(SectorSize)); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "x.scram.recovered")
	err = Unpack(UnpackOptions{
		ContainerPath: containerPath,
		OutputPath:    outPath,
		Verify:        true,
	}, NewReporter(io.Discard, true))
	if !errors.Is(err, errBinHashMismatch) {
		t.Fatalf("expected errBinHashMismatch on truncated track, got %v", err)
	}
}

// TestUnpackVerifiesAllThreeBinHashes confirms the strict any-of-three
// policy: tampering ANY single recorded bin hash in the container's
// manifest causes Unpack to fail with errBinHashMismatch.
func TestUnpackVerifiesAllThreeBinHashes(t *testing.T) {
	for _, hashName := range []string{"bin_md5", "bin_sha1", "bin_sha256"} {
		t.Run(hashName, func(t *testing.T) {
			_, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
			containerPath := filepath.Join(dir, "x.miniscram")
			if err := Pack(PackOptions{
				CuePath: cuePath, ScramPath: scramPath,
				OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
			}, NewReporter(io.Discard, true)); err != nil {
				t.Fatal(err)
			}
			m, _, err := ReadContainer(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			var target string
			switch hashName {
			case "bin_md5":
				target = m.BinMD5
			case "bin_sha1":
				target = m.BinSHA1
			case "bin_sha256":
				target = m.BinSHA256
			}
			data, err := os.ReadFile(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			idx := bytes.Index(data, []byte(target))
			if idx < 0 {
				t.Fatalf("hash %q not in container", hashName)
			}
			data[idx] ^= 1
			if err := os.WriteFile(containerPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
			outPath := filepath.Join(dir, "out.scram")
			err = Unpack(UnpackOptions{
				ContainerPath: containerPath,
				OutputPath:    outPath,
				Verify:        true,
			}, NewReporter(io.Discard, true))
			if !errors.Is(err, errBinHashMismatch) {
				t.Fatalf("expected errBinHashMismatch tampering %s, got %v", hashName, err)
			}
		})
	}
}

// TestUnpackVerifiesAllThreeOutputHashes confirms the strict any-of-three
// policy: tampering ANY single recorded scram hash in the container's
// manifest causes Unpack to fail with errOutputHashMismatch.
func TestUnpackVerifiesAllThreeOutputHashes(t *testing.T) {
	for _, hashName := range []string{"scram_md5", "scram_sha1", "scram_sha256"} {
		t.Run(hashName, func(t *testing.T) {
			_, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
			containerPath := filepath.Join(dir, "x.miniscram")
			if err := Pack(PackOptions{
				CuePath: cuePath, ScramPath: scramPath,
				OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
			}, NewReporter(io.Discard, true)); err != nil {
				t.Fatal(err)
			}
			m, _, err := ReadContainer(containerPath)
			if err != nil {
				t.Fatal(err)
			}
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
				t.Fatalf("hash %q not in container", hashName)
			}
			data[idx] ^= 1
			if err := os.WriteFile(containerPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
			outPath := filepath.Join(dir, "out.scram")
			err = Unpack(UnpackOptions{
				ContainerPath: containerPath,
				OutputPath:    outPath,
				Verify:        true,
			}, NewReporter(io.Discard, true))
			if !errors.Is(err, errOutputHashMismatch) {
				t.Fatalf("expected errOutputHashMismatch tampering %s, got %v", hashName, err)
			}
		})
	}
}
