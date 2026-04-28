// /home/hugh/miniscram/layout_test.go
package main

import "testing"

func TestBCDMSFToLBA(t *testing.T) {
	cases := []struct {
		name string
		in   [3]byte
		want int32
	}{
		{"pregap start", [3]byte{0x00, 0x00, 0x00}, -150},
		{"LBA 0", [3]byte{0x00, 0x02, 0x00}, 0},
		{"one minute in", [3]byte{0x01, 0x00, 0x00}, 75*60 - 150},
		{"frame 74 of LBA 0", [3]byte{0x00, 0x02, 0x74}, 74},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BCDMSFToLBA(c.in)
			if got != c.want {
				t.Fatalf("BCDMSFToLBA(% x) = %d; want %d", c.in, got, c.want)
			}
		})
	}
}

func TestScramOffset(t *testing.T) {
	cases := []struct {
		lba    int32
		offset int
		want   int64
	}{
		{-150, -48, 105839952},
		{0, 0, 106192800},
		{0, 48, 106192848},
		{-45150, 0, 0},
		{-45150, -48, -48},
	}
	for _, c := range cases {
		got := ScramOffset(c.lba, c.offset)
		if got != c.want {
			t.Errorf("ScramOffset(%d, %d) = %d; want %d", c.lba, c.offset, got, c.want)
		}
	}
}
