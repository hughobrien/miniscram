// /home/hugh/miniscram/manifest_test.go
package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestContainerRoundtrip(t *testing.T) {
	m := &Manifest{
		ToolVersion:      "miniscram 1.0.0-test",
		CreatedUTC:       "2026-04-27T17:00:00Z",
		WriteOffsetBytes: -48,
		LeadinLBA:        -45150,
		Scram: ScramInfo{
			Size: 897527784,
			Hashes: FileHashes{MD5: "abc", SHA1: "def", SHA256: "ghi"},
		},
		Tracks: []Track{{
			Number: 1, Mode: "MODE1/2352", FirstLBA: 0, Size: 791104608, Filename: "x.bin",
			Hashes: FileHashes{MD5: "11", SHA1: "22", SHA256: "33"},
		}},
	}
	delta := []byte("DELTA-PAYLOAD-FAKE-VCDIFF-BYTES")
	path := filepath.Join(t.TempDir(), "x.miniscram")
	if err := WriteContainer(path, m, bytes.NewReader(delta)); err != nil {
		t.Fatal(err)
	}
	gotM, _, gotDelta, err := ReadContainer(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotM.ToolVersion != m.ToolVersion || gotM.CreatedUTC != m.CreatedUTC ||
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
		append([]byte("BADM"), make([]byte, containerHeaderSize-4)...), // bad magic
		append([]byte("MSCM\x09"), make([]byte, containerHeaderSize-5)...), // unknown version
	} {
		path := filepath.Join(t.TempDir(), "bad.miniscram")
		os.WriteFile(path, body, 0o644)
		if _, _, _, err := ReadContainer(path); err == nil {
			t.Errorf("expected error for header %x...", body[:5])
		}
	}

	// scrambler-hash-mismatch: expect specific sentinel.
	var buf bytes.Buffer
	buf.WriteString("MSCM")
	buf.WriteByte(0x01)
	buf.Write(make([]byte, 36)) // 32 bytes all-zero hash + 4 bytes zero length
	path := filepath.Join(t.TempDir(), "bad-hash.miniscram")
	os.WriteFile(path, buf.Bytes(), 0o644)
	if _, _, _, err := ReadContainer(path); !errors.Is(err, errScramblerHashMismatch) {
		t.Fatalf("expected errScramblerHashMismatch, got: %v", err)
	}
}
