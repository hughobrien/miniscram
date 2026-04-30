package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestChunkRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	tag := fourcc("TEST")
	payload := []byte("hello world")
	if err := writeChunk(&buf, tag, payload); err != nil {
		t.Fatal(err)
	}
	gotTag, gotPayload, err := readChunk(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if gotTag != tag {
		t.Errorf("tag: got %v, want %v", gotTag, tag)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("payload: got %q, want %q", gotPayload, payload)
	}
}

func TestChunkEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := writeChunk(&buf, fourcc("EMPT"), nil); err != nil {
		t.Fatal(err)
	}
	tag, payload, err := readChunk(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if tag != fourcc("EMPT") || len(payload) != 0 {
		t.Fatalf("got tag=%v len=%d", tag, len(payload))
	}
}

func TestChunkCleanEOF(t *testing.T) {
	_, _, err := readChunk(bytes.NewReader(nil))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF on empty reader, got %v", err)
	}
}

func TestChunkTruncatedHeader(t *testing.T) {
	_, _, err := readChunk(bytes.NewReader([]byte{'M', 'F'}))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF, got %v", err)
	}
}

func TestChunkTruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{'M', 'F', 'S', 'T'})
	binary.Write(&buf, binary.BigEndian, uint32(100))
	buf.Write(make([]byte, 50)) // only half the payload
	_, _, err := readChunk(&buf)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF, got %v", err)
	}
}

func TestChunkBadCRC(t *testing.T) {
	var buf bytes.Buffer
	if err := writeChunk(&buf, fourcc("TEST"), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	raw[len(raw)-1] ^= 0xFF // corrupt last byte of CRC
	_, _, err := readChunk(bytes.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "crc mismatch") {
		t.Fatalf("expected crc mismatch error, got %v", err)
	}
}

func TestChunkLengthCapNonDLTA(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{'F', 'A', 'K', 'E'})
	binary.Write(&buf, binary.BigEndian, uint32(16<<20+1))
	_, _, err := readChunk(&buf)
	if err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("expected length-cap error, got %v", err)
	}
}

func TestChunkLengthCapDLTAExempt(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{'D', 'L', 'T', 'A'})
	binary.Write(&buf, binary.BigEndian, uint32(16<<20+1))
	_, _, err := readChunk(&buf)
	// DLTA bypasses the cap, so the failure mode is "ran out of bytes"
	if err == nil || strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("DLTA should bypass length cap, got %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF reading DLTA payload, got %v", err)
	}
}

func TestMFSTRoundTrip(t *testing.T) {
	in := &Manifest{
		ToolVersion:      "miniscram 1.0.0-test",
		CreatedUnix:      1714435200,
		WriteOffsetBytes: -48,
		LeadinLBA:        -45150,
		Scram:            ScramInfo{Size: 897527784},
	}
	payload := encodeMFSTPayload(in)
	out, err := decodeMFSTPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if out.ToolVersion != in.ToolVersion ||
		out.CreatedUnix != in.CreatedUnix ||
		out.WriteOffsetBytes != in.WriteOffsetBytes ||
		out.LeadinLBA != in.LeadinLBA ||
		out.Scram.Size != in.Scram.Size {
		t.Fatalf("round-trip mismatch:\nin:  %+v\nout: %+v", in, out)
	}
}

func TestMFSTRejectsTruncated(t *testing.T) {
	full := encodeMFSTPayload(&Manifest{
		ToolVersion: "miniscram", CreatedUnix: 1, WriteOffsetBytes: 0,
		LeadinLBA: 0, Scram: ScramInfo{Size: 0},
	})
	for i := 0; i < len(full); i++ {
		_, err := decodeMFSTPayload(full[:i])
		if err == nil {
			t.Fatalf("decoding truncated MFST (len=%d) should fail", i)
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("len=%d: expected ErrUnexpectedEOF, got %v", i, err)
		}
	}
}

func TestTRKSRoundTrip(t *testing.T) {
	in := []Track{
		{Number: 1, Mode: "MODE1/2352", FirstLBA: 0, Size: 791104608, Filename: "x.bin"},
		{Number: 2, Mode: "AUDIO", FirstLBA: 336420, Size: 47040, Filename: "x.bin"},
	}
	payload := encodeTRKSPayload(in)
	out, err := decodeTRKSPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("count: got %d want %d", len(out), len(in))
	}
	for i := range in {
		// Hashes intentionally not compared — populated by HASH codec.
		got, want := out[i], in[i]
		if got.Number != want.Number || got.Mode != want.Mode ||
			got.FirstLBA != want.FirstLBA || got.Size != want.Size ||
			got.Filename != want.Filename {
			t.Errorf("track %d:\ngot:  %+v\nwant: %+v", i, got, want)
		}
	}
}

func TestTRKSRejectsTruncated(t *testing.T) {
	full := encodeTRKSPayload([]Track{{Number: 1, Mode: "AUDIO", FirstLBA: 0, Size: 0, Filename: "t.bin"}})
	for i := 0; i < len(full); i++ {
		_, err := decodeTRKSPayload(full[:i])
		if err == nil {
			t.Errorf("decoding truncated TRKS (len=%d) should fail", i)
		}
	}
}

func TestHASHRoundTrip(t *testing.T) {
	in := &Manifest{
		Scram: ScramInfo{Hashes: FileHashes{
			MD5:    "0123456789abcdef0123456789abcdef",
			SHA1:   "0123456789abcdef0123456789abcdef01234567",
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
		Tracks: []Track{
			{Number: 1, Hashes: FileHashes{
				MD5:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				SHA1:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			}},
		},
	}
	payload := encodeHASHPayload(in)
	out := &Manifest{Tracks: []Track{{Number: 1}}}
	if err := decodeHASHPayload(payload, out); err != nil {
		t.Fatal(err)
	}
	if out.Scram.Hashes != in.Scram.Hashes {
		t.Errorf("scram hashes mismatch:\ngot:  %+v\nwant: %+v", out.Scram.Hashes, in.Scram.Hashes)
	}
	if out.Tracks[0].Hashes != in.Tracks[0].Hashes {
		t.Errorf("track[0] hashes mismatch")
	}
}

func TestHASHRejectsUnknownAlgo(t *testing.T) {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 1)
	b = append(b, 0)                  // target=0
	b = append(b, 'X', 'X', 'X', 'X') // bogus algo
	b = append(b, 16)                 // claims 16-byte digest
	b = append(b, make([]byte, 16)...)
	err := decodeHASHPayload(b, &Manifest{})
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("expected unknown-algo error, got %v", err)
	}
}

func TestHASHRejectsBadDigestLen(t *testing.T) {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 1)
	b = append(b, 0)
	b = append(b, 'M', 'D', '5', ' ')
	b = append(b, 99) // wrong length for MD5 (16)
	b = append(b, make([]byte, 99)...)
	err := decodeHASHPayload(b, &Manifest{})
	if err == nil || !strings.Contains(err.Error(), "digest length") {
		t.Fatalf("expected digest-length error, got %v", err)
	}
}

func TestHASHRejectsBadTarget(t *testing.T) {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 1)
	b = append(b, 5) // target=5 with 0 tracks
	b = append(b, 'M', 'D', '5', ' ')
	b = append(b, 16)
	b = append(b, make([]byte, 16)...)
	err := decodeHASHPayload(b, &Manifest{})
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("expected target out-of-range error, got %v", err)
	}
}

func TestHASHRejectsTruncated(t *testing.T) {
	// Build a valid HASH payload (1 record), then walk every prefix.
	full := encodeHASHPayload(&Manifest{
		Scram: ScramInfo{Hashes: FileHashes{
			MD5:    "0123456789abcdef0123456789abcdef",
			SHA1:   "0123456789abcdef0123456789abcdef01234567",
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
	})
	for i := 0; i < len(full); i++ {
		err := decodeHASHPayload(full[:i], &Manifest{})
		if err == nil {
			t.Errorf("decoding truncated HASH (len=%d) should fail", i)
		}
	}
}
