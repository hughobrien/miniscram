// /home/hugh/miniscram/delta_test.go
package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func encodeDeltaOf(hat, scram []byte) (int, []byte, error) {
	var out bytes.Buffer
	n, err := EncodeDelta(&out, bytes.NewReader(hat), bytes.NewReader(scram), int64(len(scram)))
	return n, out.Bytes(), err
}

func TestEncodeDelta(t *testing.T) {
	base := bytes.Repeat([]byte{0xAB}, 10000)

	t.Run("empty", func(t *testing.T) {
		n, out, err := encodeDeltaOf(base, base)
		if err != nil || n != 0 || !bytes.Equal(out, []byte{0, 0, 0, 0}) {
			t.Fatalf("err=%v n=%d payload=%x; want nil,0,4-zeros", err, n, out)
		}
	})
	t.Run("single-byte", func(t *testing.T) {
		scram := append([]byte{}, base...)
		scram[1234] ^= 0xFF
		if n, _, err := encodeDeltaOf(base, scram); err != nil || n != 1 {
			t.Fatalf("err=%v n=%d; want nil,1", err, n)
		}
	})
	t.Run("coalesces", func(t *testing.T) {
		scram := append([]byte{}, base...)
		for i := 100; i < 200; i++ {
			scram[i] ^= 0xFF
		}
		if n, _, err := encodeDeltaOf(base, scram); err != nil || n != 1 {
			t.Fatalf("err=%v n=%d; want nil,1 (coalesced)", err, n)
		}
	})
	t.Run("keeps-separated", func(t *testing.T) {
		scram := append([]byte{}, base...)
		scram[100] ^= 0xFF
		scram[102] ^= 0xFF
		if n, _, err := encodeDeltaOf(base, scram); err != nil || n != 2 {
			t.Fatalf("err=%v n=%d; want nil,2", err, n)
		}
	})
	t.Run("large-run-single-record", func(t *testing.T) {
		const runLen = SectorSize*2 + 100
		scram := bytes.Repeat([]byte{0x00}, runLen+1000)
		hat := append([]byte{}, scram...)
		for i := 500; i < 500+runLen; i++ {
			scram[i] = 0xFF
		}
		if n, _, err := encodeDeltaOf(hat, scram); err != nil || n != 1 {
			t.Fatalf("err=%v n=%d; want nil,1 (single record)", err, n)
		}
	})
}

func TestDeltaApplyRoundTrip(t *testing.T) {
	scram, hat := make([]byte, 1<<16), make([]byte, 1<<16)
	rand.Read(scram)
	rand.Read(hat)
	_, encoded, _ := encodeDeltaOf(hat, scram)
	path := filepath.Join(t.TempDir(), "out.bin")
	os.WriteFile(path, hat, 0o644)
	f, _ := os.OpenFile(path, os.O_RDWR, 0)
	if err := ApplyDelta(f, bytes.NewReader(encoded)); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if got, _ := os.ReadFile(path); !bytes.Equal(got, scram) {
		t.Fatal("round-trip mismatch")
	}
}

func TestDeltaApplyRejectsInvalid(t *testing.T) {
	nop := &nopWriterAt{}
	t.Run("truncated", func(t *testing.T) {
		if err := ApplyDelta(nop, bytes.NewReader([]byte{0, 0, 0, 1})); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("implausible-length", func(t *testing.T) {
		bad := []byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0x00, 0x00, 0x01}
		err := ApplyDelta(nop, bytes.NewReader(bad))
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("implausible length")) {
			t.Fatalf("expected implausible-length error; got %v", err)
		}
	})
}

func TestIterateDeltaRecords(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		count, err := IterateDeltaRecords([]byte{0, 0, 0, 0}, func(uint64, uint32) error { return nil })
		if err != nil || count != 0 {
			t.Fatalf("err=%v count=%d; want nil,0", err, count)
		}
	})

	t.Run("three-records", func(t *testing.T) {
		scram := bytes.Repeat([]byte{0xAB}, 10000)
		hat := append([]byte{}, scram...)
		scram[100] ^= 0xFF
		scram[500] ^= 0xFF
		scram[5000] ^= 0xFF
		_, enc, err := encodeDeltaOf(hat, scram)
		if err != nil {
			t.Fatal(err)
		}
		var got [][2]uint64
		count, err := IterateDeltaRecords(enc, func(off uint64, length uint32) error {
			got = append(got, [2]uint64{off, uint64(length)})
			return nil
		})
		if err != nil || count != 3 {
			t.Fatalf("err=%v count=%d; want nil,3", err, count)
		}
		for i, w := range [][2]uint64{{100, 1}, {500, 1}, {5000, 1}} {
			if got[i] != w {
				t.Fatalf("record %d = %v; want %v", i, got[i], w)
			}
		}
	})

	t.Run("bad-input", func(t *testing.T) {
		for _, bad := range [][]byte{
			{0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0},                              // truncated header
			{0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10, 1, 2, 3, 4, 5}, // truncated payload
			{0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},                  // zero length
			{0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0x00, 0x00, 0x01},      // length too large
		} {
			if _, err := IterateDeltaRecords(bad, func(uint64, uint32) error { return nil }); err == nil {
				t.Fatalf("expected error for %x", bad)
			}
		}
	})

	t.Run("callback-error", func(t *testing.T) {
		_, enc, _ := encodeDeltaOf([]byte{0xAA, 0xAB, 0xAB}, []byte{0xAB, 0xAB, 0xAB})
		want := errors.New("stop")
		_, err := IterateDeltaRecords(enc, func(uint64, uint32) error { return want })
		if !errors.Is(err, want) {
			t.Fatalf("got %v; want %v", err, want)
		}
	})
}

// TestDeltaEncoderDirectAppend exercises the encoder without EncodeDelta.
func TestDeltaEncoderDirectAppend(t *testing.T) {
	var buf bytes.Buffer
	enc := NewDeltaEncoder(&buf)
	enc.Append(100, []byte{0x11, 0x22})
	enc.Append(0, []byte{}) // zero-length: silently skipped
	enc.Append(5000, []byte{0x33, 0x44, 0x55})
	count, err := enc.Close()
	if err != nil || count != 2 {
		t.Fatalf("err=%v count=%d; want nil,2", err, count)
	}
	var got [][2]uint64
	IterateDeltaRecords(buf.Bytes(), func(off uint64, length uint32) error {
		got = append(got, [2]uint64{off, uint64(length)})
		return nil
	})
	for i, w := range [][2]uint64{{100, 2}, {5000, 3}} {
		if got[i] != w {
			t.Fatalf("record %d = %v; want %v", i, got[i], w)
		}
	}
}

type nopWriterAt struct{}

func (nopWriterAt) WriteAt(p []byte, off int64) (int, error) { return len(p), nil }
