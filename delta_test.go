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

func TestDeltaEncodeLargeRunSingleRecord(t *testing.T) {
	// A run longer than SectorSize is now emitted as a single record
	// (no per-sector splitting). The DeltaEncoder lifts the cap.
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
	// One contiguous run → exactly 1 record regardless of length.
	if n != 1 {
		t.Fatalf("override count = %d; want 1", n)
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
	// length = MaxDeltaRecordLength+1 should be rejected (sanity ceiling).
	// MaxDeltaRecordLength = 0x40000000; +1 = 0x40000001
	bad := []byte{
		0, 0, 0, 1,
		0, 0, 0, 0, 0, 0, 0, 0,
		0x40, 0x00, 0x00, 0x01, // 1<<30 + 1 = 0x40000001
	}
	if _, err := IterateDeltaRecords(bad, func(uint64, uint32) error { return nil }); err == nil {
		t.Fatal("expected framing error for length > MaxDeltaRecordLength")
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

// TestApplyDeltaRejectsImplausibleLength feeds ApplyDelta a hand-crafted
// record whose length exceeds MaxDeltaRecordLength and confirms it
// rejects rather than allocating the implausible buffer.
func TestApplyDeltaRejectsImplausibleLength(t *testing.T) {
	bad := []byte{
		0, 0, 0, 1,
		0, 0, 0, 0, 0, 0, 0, 0,
		0x40, 0x00, 0x00, 0x01, // 1<<30 + 1 = 0x40000001
	}
	out := &nopWriterAt{}
	err := ApplyDelta(out, bytes.NewReader(bad))
	if err == nil {
		t.Fatal("expected error for implausible length")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("implausible length")) {
		t.Fatalf("error should mention implausible length; got: %v", err)
	}
}

// TestDeltaEncoderDirectAppend exercises NewDeltaEncoder + Append +
// Close without going through EncodeDelta. Locks in the encoder's
// independent contract (Task 5 will drive Append directly from the
// builder's mismatch callback, not via EncodeDelta).
func TestDeltaEncoderDirectAppend(t *testing.T) {
	var buf bytes.Buffer
	enc := NewDeltaEncoder(&buf)
	enc.Append(100, []byte{0x11, 0x22})
	enc.Append(0, []byte{}) // zero-length: should be silently skipped
	enc.Append(5000, []byte{0x33, 0x44, 0x55})
	count, err := enc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count = %d; want 2 (zero-length Append should not increment)", count)
	}
	type rec struct {
		off    uint64
		length uint32
	}
	var got []rec
	if _, err := IterateDeltaRecords(buf.Bytes(), func(off uint64, length uint32) error {
		got = append(got, rec{off, length})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []rec{{100, 2}, {5000, 3}}
	if len(got) != len(want) {
		t.Fatalf("records = %v; want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("record %d = %+v; want %+v", i, got[i], want[i])
		}
	}
}

// nopWriterAt is a sink that satisfies io.WriterAt without keeping the
// data — used by tests that only care about whether ApplyDelta returns
// an error before reaching the WriteAt call.
type nopWriterAt struct{}

func (nopWriterAt) WriteAt(p []byte, off int64) (int, error) { return len(p), nil }
