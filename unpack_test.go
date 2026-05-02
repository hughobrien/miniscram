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
