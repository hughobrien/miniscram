package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestContainerRoundtrip(t *testing.T) {
	m := &Manifest{
		ToolVersion:      "miniscram 1.0.0-test",
		CreatedUTC:       "2026-04-27T17:00:00Z",
		WriteOffsetBytes: -48,
		LeadinLBA:        -45150,
		Scram: ScramInfo{
			Size:   897527784,
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
		append([]byte("BADM"), make([]byte, containerHeaderSize-4)...),     // bad magic
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
	buf.WriteByte(0x00)
	buf.Write(make([]byte, 36)) // 32 bytes all-zero hash + 4 bytes zero length
	path := filepath.Join(t.TempDir(), "bad-hash.miniscram")
	os.WriteFile(path, buf.Bytes(), 0o644)
	if _, _, _, err := ReadContainer(path); !errors.Is(err, errScramblerHashMismatch) {
		t.Fatalf("expected errScramblerHashMismatch, got: %v", err)
	}
}

func TestContainerDeltaIsZlibFramed(t *testing.T) {
	m := &Manifest{
		ToolVersion: "miniscram-test",
		CreatedUTC:  "2026-04-28T00:00:00Z",
		Scram:       ScramInfo{Size: 0, Hashes: FileHashes{MD5: "0", SHA1: "0", SHA256: "0"}},
		Tracks: []Track{{
			Number: 1, Mode: "MODE1/2352", FirstLBA: 0, Size: 0, Filename: "t.bin",
			Hashes: FileHashes{MD5: "0", SHA1: "0", SHA256: "0"},
		}},
	}
	delta := bytes.Repeat([]byte("ABCDEFGH"), 1024)
	path := filepath.Join(t.TempDir(), "x.miniscram")
	if err := WriteContainer(path, m, bytes.NewReader(delta)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mlen := binary.BigEndian.Uint32(raw[containerHeaderSize-4 : containerHeaderSize])
	postManifest := raw[containerHeaderSize+int(mlen):]
	if len(postManifest) < 2 {
		t.Fatalf("post-manifest too short: %d bytes", len(postManifest))
	}
	if postManifest[0] != 0x78 {
		t.Fatalf("expected zlib magic 0x78 at start of post-manifest, got 0x%02x", postManifest[0])
	}
	if len(postManifest) >= len(delta) {
		t.Fatalf("compression no-op: post-manifest %d bytes vs plaintext %d bytes", len(postManifest), len(delta))
	}
}

func TestContainerRejectsPlaintextDelta(t *testing.T) {
	tableHash, err := hex.DecodeString(expectedScrambleTableSHA256)
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"tool_version":"x","created_utc":"x","write_offset_bytes":0,"leadin_lba":0,` +
		`"scram":{"size":0,"hashes":{"md5":"0","sha1":"0","sha256":"0"}},` +
		`"tracks":[{"number":1,"mode":"MODE1/2352","first_lba":0,"filename":"t.bin","size":0,` +
		`"hashes":{"md5":"0","sha1":"0","sha256":"0"}}]}`)
	var buf bytes.Buffer
	buf.WriteString(containerMagic)
	buf.WriteByte(containerVersion)
	buf.Write(tableHash)
	binary.Write(&buf, binary.BigEndian, uint32(len(manifest)))
	buf.Write(manifest)
	buf.Write([]byte{0, 0, 0, 0}) // any non-zlib bytes; zlib.NewReader fails on the magic
	path := filepath.Join(t.TempDir(), "plain.miniscram")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = ReadContainer(path)
	if err == nil {
		t.Fatalf("expected error reading plaintext-delta v1 file")
	}
	if !strings.Contains(err.Error(), "decompressing delta payload") {
		t.Fatalf("expected error to mention 'decompressing delta payload', got: %v", err)
	}
}
