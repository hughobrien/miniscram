// /home/hugh/miniscram/manifest_test.go
package main

import (
	"bytes"
	"encoding/binary"
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
			Hashes: FileHashes{
				MD5:    "abc",
				SHA1:   "def",
				SHA256: "ghi",
			},
		},
		Tracks: []Track{{
			Number:   1,
			Mode:     "MODE1/2352",
			FirstLBA: 0,
			Size:     791104608,
			Filename: "x.bin",
			Hashes:   FileHashes{MD5: "11", SHA1: "22", SHA256: "33"},
		}},
	}
	delta := []byte("DELTA-PAYLOAD-FAKE-VCDIFF-BYTES")
	dir := t.TempDir()
	path := filepath.Join(dir, "x.miniscram")
	if err := WriteContainer(path, m, bytes.NewReader(delta)); err != nil {
		t.Fatal(err)
	}
	gotM, _, gotDelta, err := ReadContainer(path)
	if err != nil {
		t.Fatal(err)
	}
	// Every top-level field must round-trip.
	if gotM.ToolVersion != m.ToolVersion ||
		gotM.CreatedUTC != m.CreatedUTC ||
		gotM.WriteOffsetBytes != m.WriteOffsetBytes ||
		gotM.LeadinLBA != m.LeadinLBA {
		t.Fatalf("manifest scalar fields mismatch: got %+v want %+v", gotM, m)
	}
	if !reflect.DeepEqual(gotM.Scram, m.Scram) {
		t.Fatalf("scram block mismatch: got %+v want %+v", gotM.Scram, m.Scram)
	}
	if !reflect.DeepEqual(gotM.Tracks, m.Tracks) {
		t.Fatalf("tracks mismatch: got %+v want %+v", gotM.Tracks, m.Tracks)
	}
	if !bytes.Equal(gotDelta, delta) {
		t.Fatalf("delta bytes mismatch")
	}
}

func TestContainerRejectsScramblerHashMismatch(t *testing.T) {
	// Write a v1 container header with all-zero scrambler hash (mismatch).
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-hash.miniscram")
	var buf bytes.Buffer
	buf.WriteString("MSCM")
	buf.WriteByte(0x01)
	buf.Write(make([]byte, 32)) // all-zero hash — will mismatch
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, 0)
	buf.Write(b)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := ReadContainer(path)
	if !errors.Is(err, errScramblerHashMismatch) {
		t.Fatalf("expected errScramblerHashMismatch, got: %v", err)
	}
}

func TestContainerRejectsBadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.miniscram")
	body := make([]byte, containerHeaderSize)
	copy(body, "BADM") // wrong magic (not "MSCM")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadContainer(path); err == nil {
		t.Fatal("expected error for bad magic, got nil")
	}
}

func TestContainerRejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v9.miniscram")
	// magic 'MSCM' + version 9 + zero-filled remainder
	body := make([]byte, containerHeaderSize)
	copy(body, "MSCM")
	body[4] = 0x09
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := ReadContainer(path)
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
}
