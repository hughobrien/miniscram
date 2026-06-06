package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
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
		OutputPath: containerPath, LeadinLBA: LBAPregapStart,
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

// tamperContainerHash flips one byte in the raw digest matching the
// hex-encoded target, then re-frames the HASH chunk with a fresh CRC
// so the chunk layer accepts it and the hash-mismatch check runs.
func tamperContainerHash(t *testing.T, containerPath, hexTarget string) {
	t.Helper()
	rawTarget, err := hex.DecodeString(hexTarget)
	if err != nil {
		t.Fatalf("decoding hex target %q: %v", hexTarget, err)
	}
	data, err := os.ReadFile(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	hashStart, payloadOff, payloadLen, ok := findHASHChunk(data)
	if !ok {
		t.Fatal("HASH chunk not found in container")
	}
	payload := data[payloadOff : payloadOff+payloadLen]
	idx := bytes.Index(payload, rawTarget)
	if idx < 0 {
		t.Fatalf("hash %q (raw) not in HASH chunk", hexTarget)
	}
	payload[idx] ^= 1
	// Recompute CRC over (tag || payload) so the chunk layer accepts it.
	h := crc32.New(crc32Table)
	h.Write(data[hashStart : hashStart+4])
	h.Write(payload)
	binary.BigEndian.PutUint32(data[payloadOff+payloadLen:payloadOff+payloadLen+4], h.Sum32())
	os.WriteFile(containerPath, data, 0o644)
}

// findHASHChunk locates the HASH chunk in a v2 container.
// Returns (chunkStart, payloadStart, payloadLen, ok). chunkStart is the
// position of the 4-byte tag; payloadStart is just past tag+length;
// payloadLen is the parsed length.
func findHASHChunk(data []byte) (chunkStart, payloadOff, payloadLen int, ok bool) {
	if len(data) < fileHeaderSize {
		return 0, 0, 0, false
	}
	pos := fileHeaderSize
	for pos+8 <= len(data) {
		tag := data[pos : pos+4]
		length := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		payloadStart := pos + 8
		if payloadStart+length+4 > len(data) {
			return 0, 0, 0, false
		}
		if string(tag) == "HASH" {
			return pos, payloadStart, length, true
		}
		pos = payloadStart + length + 4 // skip payload + CRC
	}
	return 0, 0, 0, false
}

func TestUnpackMissingBinSurfacesStepError(t *testing.T) {
	containerPath, dir := packAndUnpackSetup(t)

	// Move only the container to a fresh dir; leave the bin(s) behind.
	freshDir := t.TempDir()
	newContainerPath := filepath.Join(freshDir, "x.miniscram")
	if err := os.Rename(containerPath, newContainerPath); err != nil {
		t.Fatal(err)
	}
	_ = dir // bins remain in the original dir, absent from freshDir

	err := Unpack(UnpackOptions{
		ContainerPath: newContainerPath,
		OutputPath:    filepath.Join(freshDir, "x.scram.recovered"),
		Verify:        false,
	}, NewReporter(io.Discard, true))
	if err == nil {
		t.Fatal("expected error for missing bin file, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no such file") {
		t.Errorf("expected 'no such file' in error, got: %v", msg)
	}
	if !strings.Contains(msg, "track 1") {
		t.Errorf("expected track number in error, got: %v", msg)
	}
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
			m, _, _ := ReadContainer(containerPath)
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

// helper: concatenate a disc's data bin followed by all its audio bins.
func combinedBinBytes(disc SynthDisc) []byte {
	out := append([]byte{}, disc.Bin...)
	for _, ab := range disc.AudioBins {
		out = append(out, ab...)
	}
	return out
}

func TestResolveBinSourceNamedFiles(t *testing.T) {
	dir := t.TempDir()
	// Two distinct per-track files (the Redumper layout).
	if err := os.WriteFile(filepath.Join(dir, "t1.bin"), make([]byte, 4*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t2.bin"), make([]byte, 3*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	tracks := []Track{
		{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize},
		{Number: 2, Filename: "t2.bin", Size: 3 * SectorSize},
	}
	baseDir, files, err := resolveBinSource(dir, "", tracks)
	if err != nil {
		t.Fatalf("resolveBinSource: %v", err)
	}
	if baseDir != dir {
		t.Errorf("baseDir = %q, want %q", baseDir, dir)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	if tracks[0].FileOffset != 0 || tracks[1].FileOffset != 0 {
		t.Errorf("named-file tracks should keep FileOffset 0; got %d, %d", tracks[0].FileOffset, tracks[1].FileOffset)
	}
	if tracks[0].Filename != "t1.bin" || tracks[1].Filename != "t2.bin" {
		t.Errorf("named-file filenames must be unchanged; got %q, %q", tracks[0].Filename, tracks[1].Filename)
	}
}

func TestResolveBinSourceSingleBinAutoDetect(t *testing.T) {
	dir := t.TempDir()
	// One combined bin; the recorded per-track filenames are NOT present.
	if err := os.WriteFile(filepath.Join(dir, "whatever.bin"), make([]byte, 7*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	tracks := []Track{
		{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize},
		{Number: 2, Filename: "t2.bin", Size: 3 * SectorSize},
	}
	baseDir, files, err := resolveBinSource(dir, "", tracks)
	if err != nil {
		t.Fatalf("resolveBinSource: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0].Path) != "whatever.bin" {
		t.Fatalf("expected single source whatever.bin; got %+v", files)
	}
	if baseDir != dir {
		t.Errorf("baseDir = %q, want %q", baseDir, dir)
	}
	// Tracks rewritten to point at the single bin, mapped by cumulative offset.
	if tracks[0].Filename != "whatever.bin" || tracks[1].Filename != "whatever.bin" {
		t.Errorf("tracks should be rewritten to whatever.bin; got %q, %q", tracks[0].Filename, tracks[1].Filename)
	}
	if tracks[0].FileOffset != 0 || tracks[1].FileOffset != 4*SectorSize {
		t.Errorf("offsets = %d, %d; want 0, %d", tracks[0].FileOffset, tracks[1].FileOffset, 4*SectorSize)
	}
}

func TestResolveBinSourceExplicitBinPath(t *testing.T) {
	dir := t.TempDir()
	binDir := t.TempDir() // somewhere other than the container dir
	binPath := filepath.Join(binDir, "combined.bin")
	if err := os.WriteFile(binPath, make([]byte, 7*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	// Named files DO exist in dir, but --bin must override them.
	if err := os.WriteFile(filepath.Join(dir, "t1.bin"), make([]byte, 4*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t2.bin"), make([]byte, 3*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	tracks := []Track{
		{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize},
		{Number: 2, Filename: "t2.bin", Size: 3 * SectorSize},
	}
	baseDir, files, err := resolveBinSource(dir, binPath, tracks)
	if err != nil {
		t.Fatalf("resolveBinSource: %v", err)
	}
	if len(files) != 1 || files[0].Path != binPath {
		t.Fatalf("expected single source %s; got %+v", binPath, files)
	}
	if baseDir != binDir {
		t.Errorf("baseDir = %q, want %q", baseDir, binDir)
	}
	if tracks[1].FileOffset != 4*SectorSize {
		t.Errorf("track 2 offset = %d, want %d", tracks[1].FileOffset, 4*SectorSize)
	}
}

func TestResolveBinSourceSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	// Single bin present, but one sector too short for the manifest total.
	if err := os.WriteFile(filepath.Join(dir, "wrong.bin"), make([]byte, 6*SectorSize), 0o644); err != nil {
		t.Fatal(err)
	}
	tracks := []Track{
		{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize},
		{Number: 2, Filename: "t2.bin", Size: 3 * SectorSize},
	}
	_, _, err := resolveBinSource(dir, "", tracks)
	if err == nil {
		t.Fatal("expected error on size mismatch")
	}
	if !errors.Is(err, errBinHashMismatch) {
		t.Errorf("error = %v; want wrapped errBinHashMismatch", err)
	}
}

func TestResolveBinSourceAmbiguousAndMissing(t *testing.T) {
	t.Run("multiple-bins-none-named", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.bin"), make([]byte, SectorSize), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "b.bin"), make([]byte, SectorSize), 0o644); err != nil {
			t.Fatal(err)
		}
		tracks := []Track{{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize}}
		if _, _, err := resolveBinSource(dir, "", tracks); err == nil {
			t.Fatal("expected error with multiple .bin candidates and no named files")
		}
	})
	t.Run("nothing-present", func(t *testing.T) {
		dir := t.TempDir()
		tracks := []Track{{Number: 1, Filename: "t1.bin", Size: 4 * SectorSize}}
		if _, _, err := resolveBinSource(dir, "", tracks); err == nil {
			t.Fatal("expected error when no named files and no .bin present")
		}
	})
}
