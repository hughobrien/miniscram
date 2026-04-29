// /home/hugh/miniscram/ecma130_test.go
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// =====================================================================
// Scrambler tests
// =====================================================================

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

func TestScrambleTableSpotChecks(t *testing.T) {
	// First post-sync byte is shift & 0xFF after one byte of LFSR
	// output. With seed 0x0001 the very first value taken is 0x01.
	cases := []struct {
		idx  int
		want byte
	}{
		{12, 0x01},
		{13, 0x80},
		{1000, 0x7C}, // mid-table spot-check
		{2351, 0x99}, // last byte
	}
	for _, c := range cases {
		if scrambleTable[c.idx] != c.want {
			t.Errorf("scrambleTable[%d] = 0x%02x; want 0x%02x", c.idx, scrambleTable[c.idx], c.want)
		}
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

// =====================================================================
// EDC tests
// =====================================================================

func TestEDCTableSHA256(t *testing.T) {
	const want = "0875e2687d8e984a77f00950f541c66c781febb0bc4e2444058bad275a4163d7"
	buf := make([]byte, 256*4)
	for i, v := range edcTable {
		binary.LittleEndian.PutUint32(buf[i*4:], v)
	}
	sum := sha256.Sum256(buf)
	got := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("edcTable sha256 = %s; want %s", got, want)
	}
}

func TestEDCLBAZeroMode1Zero(t *testing.T) {
	// Build the LBA-0 Mode 1 zero sector's first 2064 bytes:
	// sync (12) + BCD MSF 00:02:00 + mode 1 + 2048 zeros.
	buf := make([]byte, 2064)
	buf[0] = 0x00
	for i := 1; i <= 10; i++ {
		buf[i] = 0xFF
	}
	buf[11] = 0x00
	buf[12] = 0x00 // BCD M
	buf[13] = 0x02 // BCD S
	buf[14] = 0x00 // BCD F
	buf[15] = 0x01 // mode 1
	// bytes 16..2063 already zero
	got := ComputeEDC(buf)
	want := [4]byte{0xc5, 0x13, 0x68, 0x2b}
	if got != want {
		t.Fatalf("ComputeEDC = %x; want %x", got, want)
	}
}

func TestEDCKnownDeterministicSector(t *testing.T) {
	// Hand-crafted sector with deterministic non-zero user data.
	buf := make([]byte, 2064)
	buf[0] = 0x00
	for i := 1; i <= 10; i++ {
		buf[i] = 0xFF
	}
	buf[11] = 0x00
	buf[12] = 0x12
	buf[13] = 0x34
	buf[14] = 0x56
	buf[15] = 0x01
	for i := 16; i < 2064; i++ {
		buf[i] = byte(i & 0xFF)
	}
	got := ComputeEDC(buf)
	// Pinned reference — recompute and confirm before pinning if you don't
	// trust the value below.
	want := [4]byte{0xee, 0x9c, 0x2a, 0x0e}
	if got != want {
		t.Fatalf("ComputeEDC = %x; want %x", got, want)
	}
}

// =====================================================================
// ECC tests
// =====================================================================

func TestGFTableInvariants(t *testing.T) {
	for i := 1; i < 256; i++ {
		if gfExp[gfLog[i]] != byte(i) {
			t.Fatalf("gfExp[gfLog[%d]] = %d; want %d", i, gfExp[gfLog[i]], i)
		}
	}
}

func TestGFTableSHA256(t *testing.T) {
	const want = "3f28238c7a4c03869377e7c31e50479e5c895dc44971b3dc8865789bbb9c33bc"
	buf := make([]byte, 512)
	copy(buf[:256], gfExp[:])
	copy(buf[256:], gfLog[:])
	sum := sha256.Sum256(buf)
	got := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("gfExp||gfLog sha256 = %s; want %s", got, want)
	}
}

func makeLBAZeroMode1Sector(t *testing.T) [SectorSize]byte {
	t.Helper()
	var sec [SectorSize]byte
	sec[0] = 0x00
	for i := 1; i <= 10; i++ {
		sec[i] = 0xFF
	}
	sec[11] = 0x00
	sec[12] = 0x00
	sec[13] = 0x02
	sec[14] = 0x00
	sec[15] = 0x01
	// bytes 16..2063 zero (user data)
	edc := ComputeEDC(sec[:2064])
	sec[2064], sec[2065], sec[2066], sec[2067] = edc[0], edc[1], edc[2], edc[3]
	// bytes 2068..2075 zero (intermediate)
	ComputeECC(&sec)
	return sec
}

func TestECCLBAZeroMode1Zero(t *testing.T) {
	const wantECC = "619e335e55204ce597079fd54f8df335793665dc098a41cb0c59f36185705394"
	const wantFull = "b4f18ab66709c9b3fdef2721cc323e031b6728f3ca6c57b7c435c96189222250"
	sec := makeLBAZeroMode1Sector(t)
	eccSum := sha256.Sum256(sec[2076:])
	if got := hex.EncodeToString(eccSum[:]); got != wantECC {
		t.Errorf("ECC[2076:2352] sha256 = %s; want %s", got, wantECC)
	}
	fullSum := sha256.Sum256(sec[:])
	if got := hex.EncodeToString(fullSum[:]); got != wantFull {
		t.Errorf("full sector sha256 = %s; want %s", got, wantFull)
	}
}
