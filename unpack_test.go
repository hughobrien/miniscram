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

// TestUnpackRoundTripMode2SynthDisc covers TASKS.md item B4: end-to-end
// pack → unpack round-trip on a synthetic Mode 2 disc.
func TestUnpackRoundTripMode2SynthDisc(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFilesWithMode(t, 100, -48, 10, 0x02, "MODE2/2352")
	_ = binPath
	containerPath := filepath.Join(dir, "x.miniscram")
	rep := NewReporter(io.Discard, true)
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, rep); err != nil {
		t.Fatal(err)
	}
	m, _, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tracks) != 1 || m.Tracks[0].Mode != "MODE2/2352" {
		t.Fatalf("manifest tracks = %+v; want one MODE2/2352 entry", m.Tracks)
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
		t.Fatalf("Mode 2 round-trip differs (got %d bytes, want %d)", len(got), len(want))
	}
}

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

// TestUnpackVerifiesPerTrackHashes confirms that tampering ANY single
// per-track hash in the container's manifest causes Unpack to fail.
func TestUnpackVerifiesPerTrackHashes(t *testing.T) {
	for _, hashName := range []string{"track_md5", "track_sha1", "track_sha256"} {
		t.Run(hashName, func(t *testing.T) {
			_, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
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
			var target string
			switch hashName {
			case "track_md5":
				target = m.Tracks[0].Hashes.MD5
			case "track_sha1":
				target = m.Tracks[0].Hashes.SHA1
			case "track_sha256":
				target = m.Tracks[0].Hashes.SHA256
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

// TestUnpackVerifiesAllThreeOutputHashes confirms that tampering ANY
// single recorded scram hash causes Unpack to fail.
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
			m, _, _, err := ReadContainer(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			var target string
			switch hashName {
			case "scram_md5":
				target = m.Scram.Hashes.MD5
			case "scram_sha1":
				target = m.Scram.Hashes.SHA1
			case "scram_sha256":
				target = m.Scram.Hashes.SHA256
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
