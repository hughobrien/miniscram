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
