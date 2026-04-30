package main

import (
	"bytes"
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
