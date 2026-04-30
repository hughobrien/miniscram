package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestContainerRoundtrip(t *testing.T) {
	m := &Manifest{
		ToolVersion:      "miniscram 1.0.0-test",
		CreatedUnix:      1714435200,
		WriteOffsetBytes: -48,
		LeadinLBA:        -45150,
		Scram: ScramInfo{
			Size: 897527784,
			Hashes: FileHashes{
				MD5:    strings.Repeat("a", 32),
				SHA1:   strings.Repeat("b", 40),
				SHA256: strings.Repeat("c", 64),
			},
		},
		Tracks: []Track{{
			Number: 1, Mode: "MODE1/2352", FirstLBA: 0, Size: 791104608, Filename: "x.bin",
			Hashes: FileHashes{
				MD5:    strings.Repeat("1", 32),
				SHA1:   strings.Repeat("2", 40),
				SHA256: strings.Repeat("3", 64),
			},
		}},
	}
	delta := []byte("DELTA-PAYLOAD-FAKE-VCDIFF-BYTES")
	path := filepath.Join(t.TempDir(), "x.miniscram")
	if err := WriteContainer(path, m, bytes.NewReader(delta)); err != nil {
		t.Fatal(err)
	}
	gotM, gotDelta, err := ReadContainer(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotM.ToolVersion != m.ToolVersion || gotM.CreatedUnix != m.CreatedUnix ||
		gotM.WriteOffsetBytes != m.WriteOffsetBytes || gotM.LeadinLBA != m.LeadinLBA {
		t.Fatalf("manifest scalars mismatch: got %+v want %+v", gotM, m)
	}
	if !reflect.DeepEqual(gotM.Scram, m.Scram) || !reflect.DeepEqual(gotM.Tracks, m.Tracks) {
		t.Fatalf("scram/tracks mismatch")
	}
	if !bytes.Equal(gotDelta, delta) {
		t.Fatalf("delta bytes mismatch")
	}
}

func TestContainerRejectsInvalid(t *testing.T) {
	// bad-magic and unknown-version: just check error is non-nil.
	for _, body := range [][]byte{
		{'B', 'A', 'D', 'M', 0x00}, // bad magic
		{'M', 'S', 'C', 'M', 0x09}, // unknown version
	} {
		path := filepath.Join(t.TempDir(), "bad.miniscram")
		os.WriteFile(path, body, 0o644)
		if _, _, err := ReadContainer(path); err == nil {
			t.Errorf("expected error for header %x...", body[:5])
		}
	}
}

func TestContainerDeltaIsZlibFramed(t *testing.T) {
	m := &Manifest{
		ToolVersion: "miniscram-test",
		CreatedUnix: 1714435200,
		Scram: ScramInfo{Size: 0, Hashes: FileHashes{
			MD5:    strings.Repeat("0", 32),
			SHA1:   strings.Repeat("0", 40),
			SHA256: strings.Repeat("0", 64),
		}},
		Tracks: []Track{{
			Number: 1, Mode: "MODE1/2352", FirstLBA: 0, Size: 0, Filename: "t.bin",
			Hashes: FileHashes{
				MD5:    strings.Repeat("0", 32),
				SHA1:   strings.Repeat("0", 40),
				SHA256: strings.Repeat("0", 64),
			},
		}},
	}
	delta := bytes.Repeat([]byte("ABCDEFGH"), 1024)
	path := filepath.Join(t.TempDir(), "x.miniscram")
	if err := WriteContainer(path, m, bytes.NewReader(delta)); err != nil {
		t.Fatal(err)
	}
	// Walk chunks; find DLTA; check its payload starts with 0x78.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(raw[fileHeaderSize:])
	for {
		tag, payload, err := readChunk(r)
		if err != nil {
			t.Fatalf("walking chunks: %v", err)
		}
		if tag == dltaTag {
			if len(payload) < 2 || payload[0] != 0x78 {
				t.Fatalf("DLTA payload should start with zlib magic 0x78, got 0x%02x", payload[0])
			}
			if len(payload) >= len(delta) {
				t.Fatalf("compression no-op: DLTA payload %d bytes vs plaintext %d bytes", len(payload), len(delta))
			}
			return
		}
	}
}

// validV2Container builds a minimal valid v2 container in memory and
// returns its bytes. Used as a base by corruption tests, which mutate
// specific bytes/chunks and assert the reader rejects.
func validV2Container(t *testing.T) []byte {
	t.Helper()
	m := &Manifest{
		ToolVersion: "miniscram-test",
		CreatedUnix: 1,
		Scram:       ScramInfo{Size: 0, Hashes: FileHashes{MD5: strings.Repeat("0", 32), SHA1: strings.Repeat("0", 40), SHA256: strings.Repeat("0", 64)}},
		Tracks: []Track{{
			Number: 1, Mode: "AUDIO", FirstLBA: 0, Size: 0, Filename: "t.bin",
			Hashes: FileHashes{MD5: strings.Repeat("0", 32), SHA1: strings.Repeat("0", 40), SHA256: strings.Repeat("0", 64)},
		}},
	}
	path := filepath.Join(t.TempDir(), "valid.miniscram")
	if err := WriteContainer(path, m, bytes.NewReader([]byte{0, 0, 0, 0})); err != nil {
		t.Fatalf("writing valid container: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// chunkRange locates the [start, end) byte range of the named chunk
// inside a container blob (post-header). Fails the test fatally if the
// chunk isn't present.
func chunkRange(t *testing.T, raw []byte, want [4]byte) (int, int) {
	t.Helper()
	off := fileHeaderSize
	for off < len(raw) {
		var tag [4]byte
		copy(tag[:], raw[off:off+4])
		length := int(binary.BigEndian.Uint32(raw[off+4 : off+8]))
		end := off + 8 + length + 4 // type + length + payload + CRC
		if tag == want {
			return off, end
		}
		off = end
	}
	t.Fatalf("chunkRange: chunk %q not found in container", want)
	return 0, 0 // unreachable
}

// writeRaw writes raw bytes to a tempfile and returns the path.
func writeRaw(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "corrupt.miniscram")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestContainerRejectsCorruption(t *testing.T) {
	t.Run("bad-magic", func(t *testing.T) {
		raw := validV2Container(t)
		copy(raw[:4], []byte("BADM"))
		_, _, err := ReadContainer(writeRaw(t, raw))
		if err == nil || !strings.Contains(err.Error(), "bad magic") {
			t.Fatalf("expected bad-magic error, got %v", err)
		}
	})

	for _, ver := range []byte{0x01, 0x03, 0x09} {
		t.Run(fmt.Sprintf("wrong-version-0x%02x", ver), func(t *testing.T) {
			raw := validV2Container(t)
			raw[4] = ver
			_, _, err := ReadContainer(writeRaw(t, raw))
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("0x%02x", ver)) {
				t.Fatalf("expected version-0x%02x error, got %v", ver, err)
			}
			if !strings.Contains(err.Error(), "v2") {
				t.Errorf("expected error to mention 'v2', got %v", err)
			}
		})
	}

	t.Run("truncated-mid-chunk", func(t *testing.T) {
		raw := validV2Container(t)
		mfstStart, mfstEnd := chunkRange(t, raw, mfstTag)
		_, _, err := ReadContainer(writeRaw(t, raw[:mfstStart+(mfstEnd-mfstStart)/2]))
		if err == nil {
			t.Fatal("expected error reading truncated MFST")
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("expected io.ErrUnexpectedEOF, got %v", err)
		}
	})

	t.Run("bad-crc", func(t *testing.T) {
		raw := validV2Container(t)
		_, mfstEnd := chunkRange(t, raw, mfstTag)
		raw[mfstEnd-1] ^= 0xFF // flip last byte of MFST's CRC
		_, _, err := ReadContainer(writeRaw(t, raw))
		if err == nil || !strings.Contains(err.Error(), "crc mismatch") {
			t.Fatalf("expected crc-mismatch error, got %v", err)
		}
	})

	t.Run("length-cap-exceeded", func(t *testing.T) {
		raw := validV2Container(t)
		_, mfstEnd := chunkRange(t, raw, mfstTag)
		// Inject a forged FAKE chunk after MFST with length > 16 MiB.
		var fake bytes.Buffer
		fake.Write([]byte{'F', 'A', 'K', 'E'})
		binary.Write(&fake, binary.BigEndian, uint32(16<<20+1))
		// No payload bytes — readChunk should reject before reading them.
		corrupt := append(append([]byte{}, raw[:mfstEnd]...), fake.Bytes()...)
		corrupt = append(corrupt, raw[mfstEnd:]...)
		_, _, err := ReadContainer(writeRaw(t, corrupt))
		if err == nil || !strings.Contains(err.Error(), "16 MiB") {
			t.Fatalf("expected 16-MiB-cap error, got %v", err)
		}
	})

	t.Run("unknown-critical-chunk", func(t *testing.T) {
		raw := validV2Container(t)
		_, mfstEnd := chunkRange(t, raw, mfstTag)
		// Inject a valid XXXX chunk (with correct CRC) after MFST.
		var bogus bytes.Buffer
		writeChunk(&bogus, fourcc("XXXX"), []byte("payload"))
		corrupt := append(append([]byte{}, raw[:mfstEnd]...), bogus.Bytes()...)
		corrupt = append(corrupt, raw[mfstEnd:]...)
		_, _, err := ReadContainer(writeRaw(t, corrupt))
		if err == nil || !strings.Contains(err.Error(), "unsupported critical chunk") {
			t.Fatalf("expected unsupported-critical-chunk error, got %v", err)
		}
	})

	t.Run("unknown-ancillary-chunk-accepted", func(t *testing.T) {
		raw := validV2Container(t)
		_, mfstEnd := chunkRange(t, raw, mfstTag)
		var bogus bytes.Buffer
		writeChunk(&bogus, fourcc("xfut"), []byte("future-data"))
		corrupt := append(append([]byte{}, raw[:mfstEnd]...), bogus.Bytes()...)
		corrupt = append(corrupt, raw[mfstEnd:]...)
		if _, _, err := ReadContainer(writeRaw(t, corrupt)); err != nil {
			t.Fatalf("ancillary chunk should be skipped, got %v", err)
		}
	})

	for _, missing := range []struct {
		name string
		tag  [4]byte
	}{
		{"missing-mfst", mfstTag},
		{"missing-trks", trksTag},
		{"missing-hash", hashTag},
		{"missing-dlta", dltaTag},
	} {
		t.Run(missing.name, func(t *testing.T) {
			raw := validV2Container(t)
			start, end := chunkRange(t, raw, missing.tag)
			corrupt := append(append([]byte{}, raw[:start]...), raw[end:]...)
			_, _, err := ReadContainer(writeRaw(t, corrupt))
			if err == nil || !strings.Contains(err.Error(), "missing required chunk") {
				t.Fatalf("expected missing-required-chunk error, got %v", err)
			}
			if !strings.Contains(err.Error(), string(missing.tag[:])) {
				t.Errorf("expected error to name %q, got %v", missing.tag, err)
			}
		})
	}

	for _, dup := range []struct {
		name string
		tag  [4]byte
	}{
		{"duplicate-mfst", mfstTag},
		{"duplicate-trks", trksTag},
		{"duplicate-hash", hashTag},
		{"duplicate-dlta", dltaTag},
	} {
		t.Run(dup.name, func(t *testing.T) {
			raw := validV2Container(t)
			start, end := chunkRange(t, raw, dup.tag)
			chunkBytes := append([]byte{}, raw[start:end]...)
			// Insert a second copy of the chunk right after the first.
			corrupt := append(append([]byte{}, raw[:end]...), chunkBytes...)
			corrupt = append(corrupt, raw[end:]...)
			_, _, err := ReadContainer(writeRaw(t, corrupt))
			if err == nil || !strings.Contains(err.Error(), "duplicate chunk") {
				t.Fatalf("expected duplicate-chunk error, got %v", err)
			}
		})
	}

	t.Run("hash-after-dlta-accepted", func(t *testing.T) {
		raw := validV2Container(t)
		hashStart, hashEnd := chunkRange(t, raw, hashTag)
		dltaStart, dltaEnd := chunkRange(t, raw, dltaTag)
		// Verify the test fixture's order is HASH-then-DLTA before swap.
		if hashEnd > dltaStart {
			t.Fatalf("test fixture order changed; expected HASH < DLTA")
		}
		hash := append([]byte{}, raw[hashStart:hashEnd]...)
		dlta := append([]byte{}, raw[dltaStart:dltaEnd]...)
		// Reassemble: prefix (header + MFST + TRKS) || DLTA || HASH || any tail.
		corrupt := append([]byte{}, raw[:hashStart]...)
		corrupt = append(corrupt, dlta...)
		corrupt = append(corrupt, hash...)
		corrupt = append(corrupt, raw[dltaEnd:]...)
		if _, _, err := ReadContainer(writeRaw(t, corrupt)); err != nil {
			t.Fatalf("HASH-after-DLTA should be accepted (chunks may appear in any order after MFST), got %v", err)
		}
	})

	t.Run("mfst-not-first", func(t *testing.T) {
		raw := validV2Container(t)
		mfstStart, mfstEnd := chunkRange(t, raw, mfstTag)
		trksStart, trksEnd := chunkRange(t, raw, trksTag)
		// Swap MFST and TRKS so TRKS appears first.
		mfst := append([]byte{}, raw[mfstStart:mfstEnd]...)
		trks := append([]byte{}, raw[trksStart:trksEnd]...)
		corrupt := append([]byte{}, raw[:mfstStart]...)
		corrupt = append(corrupt, trks...)
		corrupt = append(corrupt, mfst...)
		corrupt = append(corrupt, raw[trksEnd:]...)
		_, _, err := ReadContainer(writeRaw(t, corrupt))
		if err == nil || !strings.Contains(err.Error(), "MFST must be the first chunk") {
			t.Fatalf("expected MFST-not-first error, got %v", err)
		}
	})
}
