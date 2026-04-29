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

// packAndUnpackSetup packs a clean disc and returns the container path + dir.
func packAndUnpackSetup(t *testing.T) (containerPath, dir string) {
	t.Helper()
	disc := synthDisc(t, SynthOpts{MainSectors: 100, LeadoutSectors: 10})
	dir = t.TempDir()
	_, scramPath, cuePath := writeFixture(t, dir, disc)
	containerPath = filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	return
}

func TestUnpackRefusesOverwrite(t *testing.T) {
	containerPath, dir := packAndUnpackSetup(t)
	outPath := filepath.Join(dir, "exists.scram")
	os.WriteFile(outPath, []byte("hi"), 0o644)
	err := Unpack(UnpackOptions{ContainerPath: containerPath, OutputPath: outPath, Verify: true}, NewReporter(io.Discard, true))
	if err == nil {
		t.Fatal("expected error refusing to overwrite")
	}
}

func TestUnpackRejectsTrackFileSizeMismatch(t *testing.T) {
	containerPath, dir := packAndUnpackSetup(t)
	binPath := filepath.Join(dir, "x.bin")
	info, _ := os.Stat(binPath)
	os.Truncate(binPath, info.Size()-int64(SectorSize))
	err := Unpack(UnpackOptions{
		ContainerPath: containerPath,
		OutputPath:    filepath.Join(dir, "x.scram.recovered"),
		Verify:        true,
	}, NewReporter(io.Discard, true))
	if !errors.Is(err, errBinHashMismatch) {
		t.Fatalf("expected errBinHashMismatch on truncated track, got %v", err)
	}
}

// tamperContainerHash corrupts the first occurrence of target in the container file.
func tamperContainerHash(t *testing.T, containerPath, target string) {
	t.Helper()
	data, _ := os.ReadFile(containerPath)
	idx := bytes.Index(data, []byte(target))
	if idx < 0 {
		t.Fatalf("hash %q not in container", target)
	}
	data[idx] ^= 1
	os.WriteFile(containerPath, data, 0o644)
}

func TestUnpackVerifiesHashes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		getHash func(*Manifest) string
		wantErr error
		isScram bool // true → scram hash; false → track hash
	}{
		{"track-md5", func(m *Manifest) string { return m.Tracks[0].Hashes.MD5 }, errBinHashMismatch, false},
		{"track-sha1", func(m *Manifest) string { return m.Tracks[0].Hashes.SHA1 }, errBinHashMismatch, false},
		{"track-sha256", func(m *Manifest) string { return m.Tracks[0].Hashes.SHA256 }, errBinHashMismatch, false},
		{"scram-md5", func(m *Manifest) string { return m.Scram.Hashes.MD5 }, errOutputHashMismatch, true},
		{"scram-sha1", func(m *Manifest) string { return m.Scram.Hashes.SHA1 }, errOutputHashMismatch, true},
		{"scram-sha256", func(m *Manifest) string { return m.Scram.Hashes.SHA256 }, errOutputHashMismatch, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			containerPath, dir := packAndUnpackSetup(t)
			m, _, _, _ := ReadContainer(containerPath)
			tamperContainerHash(t, containerPath, tc.getHash(m))
			err := Unpack(UnpackOptions{
				ContainerPath: containerPath,
				OutputPath:    filepath.Join(dir, "out.scram"),
				Verify:        true,
			}, NewReporter(io.Discard, true))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}
