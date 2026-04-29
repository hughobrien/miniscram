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

func TestScrambleTableProperties(t *testing.T) {
	// Sync bytes must be zero.
	for i := 0; i < SyncLen; i++ {
		if scrambleTable[i] != 0 {
			t.Fatalf("scrambleTable[%d] = 0x%02x; want 0", i, scrambleTable[i])
		}
	}
	// Spot-checks derived from LFSR with seed 0x0001.
	for _, c := range []struct {
		idx  int
		want byte
	}{{12, 0x01}, {13, 0x80}, {1000, 0x7C}, {2351, 0x99}} {
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
			t.Fatalf("Scramble changed sync byte %d: got 0x%02x want 0x%02x", i, s[i], Sync[i])
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
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("edcTable sha256 = %s; want %s", got, want)
	}
}

func TestEDCKnownSectors(t *testing.T) {
	makeBuf := func(msf [4]byte, fill func([]byte)) []byte {
		b := make([]byte, 2064)
		b[1], b[2], b[3], b[4], b[5], b[6], b[7], b[8], b[9], b[10] = 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF
		b[12], b[13], b[14], b[15] = msf[0], msf[1], msf[2], msf[3]
		if fill != nil {
			fill(b[16:])
		}
		return b
	}
	if got := ComputeEDC(makeBuf([4]byte{0x00, 0x02, 0x00, 0x01}, nil)); got != [4]byte{0xc5, 0x13, 0x68, 0x2b} {
		t.Errorf("LBA-0 zero ComputeEDC = %x; want c513682b", got)
	}
	if got := ComputeEDC(makeBuf([4]byte{0x12, 0x34, 0x56, 0x01}, func(p []byte) {
		for i := range p {
			p[i] = byte((i + 16) & 0xFF)
		}
	})); got != [4]byte{0xee, 0x9c, 0x2a, 0x0e} {
		t.Errorf("deterministic ComputeEDC = %x; want ee9c2a0e", got)
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
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("gfExp||gfLog sha256 = %s; want %s", got, want)
	}
}

func TestECCLBAZeroMode1Zero(t *testing.T) {
	const wantECC = "619e335e55204ce597079fd54f8df335793665dc098a41cb0c59f36185705394"
	const wantFull = "b4f18ab66709c9b3fdef2721cc323e031b6728f3ca6c57b7c435c96189222250"
	var sec [SectorSize]byte
	sec[0] = 0x00
	for i := 1; i <= 10; i++ {
		sec[i] = 0xFF
	}
	sec[13] = 0x02
	sec[15] = 0x01
	edc := ComputeEDC(sec[:2064])
	sec[2064], sec[2065], sec[2066], sec[2067] = edc[0], edc[1], edc[2], edc[3]
	ComputeECC(&sec)
	eccSum := sha256.Sum256(sec[2076:])
	if got := hex.EncodeToString(eccSum[:]); got != wantECC {
		t.Errorf("ECC[2076:2352] sha256 = %s; want %s", got, wantECC)
	}
	fullSum := sha256.Sum256(sec[:])
	if got := hex.EncodeToString(fullSum[:]); got != wantFull {
		t.Errorf("full sector sha256 = %s; want %s", got, wantFull)
	}
}
