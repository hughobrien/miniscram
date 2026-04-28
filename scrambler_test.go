// /home/hugh/miniscram/scrambler_test.go
package main

import (
	"crypto/rand"
	"testing"
)

func TestScrambleTableSHA256(t *testing.T) {
	if err := CheckScrambleTable(); err != nil {
		t.Fatal(err)
	}
}

func TestScrambleTableSyncBytesZero(t *testing.T) {
	for i := 0; i < SyncLen; i++ {
		if scrambleTable[i] != 0 {
			t.Fatalf("scrambleTable[%d] = 0x%02x; want 0", i, scrambleTable[i])
		}
	}
}

func TestScrambleTableFirstBytes(t *testing.T) {
	// First post-sync byte is shift & 0xFF after one byte of LFSR
	// output. With seed 0x0001 the very first value taken is 0x01.
	if scrambleTable[12] != 0x01 {
		t.Fatalf("scrambleTable[12] = 0x%02x; want 0x01", scrambleTable[12])
	}
	if scrambleTable[13] != 0x80 {
		t.Fatalf("scrambleTable[13] = 0x%02x; want 0x80", scrambleTable[13])
	}
}

func TestScrambleSelfInverse(t *testing.T) {
	for trial := 0; trial < 1000; trial++ {
		var orig [SectorSize]byte
		if _, err := rand.Read(orig[:]); err != nil {
			t.Fatal(err)
		}
		var s [SectorSize]byte = orig
		Scramble(&s)
		Scramble(&s)
		if s != orig {
			t.Fatalf("trial %d: Scramble∘Scramble != identity", trial)
		}
	}
}

func TestScrambleLeavesSyncUntouched(t *testing.T) {
	var s [SectorSize]byte
	copy(s[:], Sync[:])
	Scramble(&s)
	for i := 0; i < SyncLen; i++ {
		if s[i] != Sync[i] {
			t.Fatalf("Scramble changed sync byte %d: got 0x%02x want 0x%02x",
				i, s[i], Sync[i])
		}
	}
}
