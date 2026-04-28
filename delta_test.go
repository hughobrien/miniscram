// /home/hugh/miniscram/delta_test.go
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDeltaEncodeEmpty(t *testing.T) {
	scram := bytes.Repeat([]byte{0xAB}, 10000)
	hat := append([]byte{}, scram...)
	var out bytes.Buffer
	n, err := EncodeDelta(&out, bytes.NewReader(hat), bytes.NewReader(scram), int64(len(scram)))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("override count = %d; want 0", n)
	}
	want := []byte{0, 0, 0, 0}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("payload = % x; want % x", out.Bytes(), want)
	}
}

func TestDeltaEncodeSingleByte(t *testing.T) {
	scram := bytes.Repeat([]byte{0xAB}, 10000)
	hat := append([]byte{}, scram...)
	scram[1234] ^= 0xFF
	var out bytes.Buffer
	n, err := EncodeDelta(&out, bytes.NewReader(hat), bytes.NewReader(scram), int64(len(scram)))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("override count = %d; want 1", n)
	}
	r := bytes.NewReader(out.Bytes())
	var count uint32
	binary.Read(r, binary.BigEndian, &count)
	if count != 1 {
		t.Fatal("count mismatch")
	}
	var off uint64
	var ln uint32
	binary.Read(r, binary.BigEndian, &off)
	binary.Read(r, binary.BigEndian, &ln)
	if off != 1234 || ln != 1 {
		t.Fatalf("got offset=%d length=%d; want 1234, 1", off, ln)
	}
	b, _ := io.ReadAll(r)
	if len(b) != 1 || b[0] != (0xAB^0xFF) {
		t.Fatalf("payload = % x; want %02x", b, 0xAB^0xFF)
	}
}

func TestDeltaEncodeCoalescesAdjacent(t *testing.T) {
	scram := bytes.Repeat([]byte{0xAB}, 10000)
	hat := append([]byte{}, scram...)
	for i := 100; i < 200; i++ {
		scram[i] ^= 0xFF
	}
	var out bytes.Buffer
	n, err := EncodeDelta(&out, bytes.NewReader(hat), bytes.NewReader(scram), int64(len(scram)))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("override count = %d; want 1 (coalesced)", n)
	}
}

func TestDeltaEncodeKeepsSeparated(t *testing.T) {
	scram := bytes.Repeat([]byte{0xAB}, 10000)
	hat := append([]byte{}, scram...)
	scram[100] ^= 0xFF
	scram[102] ^= 0xFF
	var out bytes.Buffer
	n, err := EncodeDelta(&out, bytes.NewReader(hat), bytes.NewReader(scram), int64(len(scram)))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("override count = %d; want 2", n)
	}
}

func TestDeltaEncodeSplitsAtSectorSize(t *testing.T) {
	// A run longer than SectorSize must split into multiple overrides
	// of at most SectorSize bytes each.
	const runLen = SectorSize*2 + 100
	scram := bytes.Repeat([]byte{0x00}, runLen+1000)
	hat := append([]byte{}, scram...)
	for i := 500; i < 500+runLen; i++ {
		scram[i] = 0xFF
	}
	var out bytes.Buffer
	n, err := EncodeDelta(&out, bytes.NewReader(hat), bytes.NewReader(scram), int64(len(scram)))
	if err != nil {
		t.Fatal(err)
	}
	// runLen / SectorSize = 2 full + 100 bytes leftover = 3 records
	if n != 3 {
		t.Fatalf("override count = %d; want 3", n)
	}
}

func TestDeltaApplyRoundTrip(t *testing.T) {
	scram := make([]byte, 1<<16)
	if _, err := rand.Read(scram); err != nil {
		t.Fatal(err)
	}
	hat := make([]byte, len(scram))
	if _, err := rand.Read(hat); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	_, err := EncodeDelta(&encoded, bytes.NewReader(hat), bytes.NewReader(scram), int64(len(scram)))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	if err := os.WriteFile(path, hat, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyDelta(f, &encoded); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, scram) {
		t.Fatal("round-trip mismatch")
	}
}

func TestDeltaApplyRejectsTruncated(t *testing.T) {
	bad := []byte{0, 0, 0, 1} // count says 1, no record follows
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0}, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_RDWR, 0)
	defer f.Close()
	if err := ApplyDelta(f, bytes.NewReader(bad)); err == nil {
		t.Fatal("expected error for truncated delta")
	}
}
