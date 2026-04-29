package main

import "testing"

func TestBCDMSFToLBA(t *testing.T) {
	for _, c := range []struct {
		in   [3]byte
		want int32
	}{
		{[3]byte{0x00, 0x00, 0x00}, -150},
		{[3]byte{0x00, 0x02, 0x00}, 0},
		{[3]byte{0x01, 0x00, 0x00}, 75*60 - 150},
		{[3]byte{0x00, 0x02, 0x74}, 74},
	} {
		if got := BCDMSFToLBA(c.in); got != c.want {
			t.Errorf("BCDMSFToLBA(% x) = %d; want %d", c.in, got, c.want)
		}
	}
}

func TestLBAMSFRoundTrip(t *testing.T) {
	for _, l := range []int32{-150, -1, 0, 1, 100, 17850, 449849} {
		if got := BCDMSFToLBA(LBAToBCDMSF(l)); got != l {
			t.Errorf("BCDMSFToLBA(LBAToBCDMSF(%d)) = %d", l, got)
		}
	}
}

func TestScramOffset(t *testing.T) {
	for _, c := range []struct {
		lba    int32
		offset int
		want   int64
	}{
		{-150, -48, 105839952},
		{0, 0, 106192800},
		{0, 48, 106192848},
		{-45150, 0, 0},
		{-45150, -48, -48},
	} {
		if got := ScramOffset(c.lba, c.offset); got != c.want {
			t.Errorf("ScramOffset(%d, %d) = %d; want %d", c.lba, c.offset, got, c.want)
		}
	}
}
