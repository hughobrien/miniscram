// /home/hugh/miniscram/delta_test.go
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
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

func TestIterateDeltaRecordsEmpty(t *testing.T) {
	count, err := IterateDeltaRecords([]byte{0, 0, 0, 0}, func(off uint64, length uint32) error {
		t.Fatalf("callback invoked for empty delta")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count = %d; want 0", count)
	}
}

func TestIterateDeltaRecordsThreeRecords(t *testing.T) {
	scram := bytes.Repeat([]byte{0xAB}, 10000)
	hat := append([]byte{}, scram...)
	scram[100] ^= 0xFF
	scram[500] ^= 0xFF
	scram[5000] ^= 0xFF
	var enc bytes.Buffer
	if _, err := EncodeDelta(&enc, bytes.NewReader(hat), bytes.NewReader(scram), int64(len(scram))); err != nil {
		t.Fatal(err)
	}
	type rec struct {
		off uint64
		ln  uint32
	}
	var got []rec
	count, err := IterateDeltaRecords(enc.Bytes(), func(off uint64, length uint32) error {
		got = append(got, rec{off, length})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("count = %d; want 3", count)
	}
	want := []rec{{100, 1}, {500, 1}, {5000, 1}}
	if len(got) != len(want) {
		t.Fatalf("got %d records; want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("record %d = %+v; want %+v", i, got[i], want[i])
		}
	}
}

func TestIterateDeltaRecordsTruncatedHeader(t *testing.T) {
	bad := []byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0}
	if _, err := IterateDeltaRecords(bad, func(uint64, uint32) error { return nil }); err == nil {
		t.Fatal("expected framing error for short record header")
	}
}

func TestIterateDeltaRecordsTruncatedPayload(t *testing.T) {
	bad := []byte{
		0, 0, 0, 1,
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 10,
		1, 2, 3, 4, 5,
	}
	if _, err := IterateDeltaRecords(bad, func(uint64, uint32) error { return nil }); err == nil {
		t.Fatal("expected framing error for truncated payload")
	}
}

func TestIterateDeltaRecordsBadLength(t *testing.T) {
	bad := []byte{
		0, 0, 0, 1,
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0,
	}
	if _, err := IterateDeltaRecords(bad, func(uint64, uint32) error { return nil }); err == nil {
		t.Fatal("expected framing error for length=0")
	}
}

func TestIterateDeltaRecordsLengthTooLarge(t *testing.T) {
	// length = SectorSize+1 should be rejected (mirrors ApplyDelta's check)
	bad := []byte{
		0, 0, 0, 1,
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0x09, 0x31, // length = 2353 = SectorSize + 1, big-endian
	}
	if _, err := IterateDeltaRecords(bad, func(uint64, uint32) error { return nil }); err == nil {
		t.Fatal("expected framing error for length > SectorSize")
	}
}

func TestIterateDeltaRecordsCallbackError(t *testing.T) {
	scram := []byte{0xAB, 0xAB, 0xAB}
	hat := []byte{0xAA, 0xAB, 0xAB}
	var enc bytes.Buffer
	if _, err := EncodeDelta(&enc, bytes.NewReader(hat), bytes.NewReader(scram), 3); err != nil {
		t.Fatal(err)
	}
	want := errors.New("stop")
	_, err := IterateDeltaRecords(enc.Bytes(), func(uint64, uint32) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("got err=%v; want %v", err, want)
	}
}
