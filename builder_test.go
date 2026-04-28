// /home/hugh/miniscram/builder_test.go
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

func TestGenerateMode1ZeroSectorLBAZero(t *testing.T) {
	const wantSHA = "b2c91211b98919e43eb75d5d1eba18821c607badf31e60af4d166883a96cd68f"
	sec := generateMode1ZeroSector(0)
	sum := sha256.Sum256(sec[:])
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		t.Fatalf("generateMode1ZeroSector(0) sha256 = %s; want %s", got, wantSHA)
	}
}

// synthDisc returns (bin, scram, params) for a small fake disc:
//   - 100 Mode 1 data sectors with valid sync + BCD MSF header,
//     starting at LBA 0 (bin).
//   - scram = leadin zeros (45000 sectors) + pregap-of-zero (150
//     sectors) + scrambled bin sectors (100 sectors) + leadout
//     scrambled-zero (10 sectors), shifted by writeOffsetBytes.
//   - writeOffsetBytes is configurable for testing both signs.
//
// Using full 45000-sector leadin would dominate the test; instead we
// use a custom LeadinLBA = -150 (no leadin region) so the synthetic
// .scram has only pregap + main + leadout. The builder must handle
// this case correctly because BuildParams allows overriding LeadinLBA.
func synthDisc(t *testing.T, mainSectors int, writeOffsetBytes int, leadoutSectors int32) ([]byte, []byte, BuildParams) {
	t.Helper()
	const leadinLBA int32 = LBAPregapStart // -150; no real leadin region
	binSize := mainSectors * SectorSize
	bin := make([]byte, binSize)
	for i := 0; i < mainSectors; i++ {
		s := bin[i*SectorSize : (i+1)*SectorSize]
		copy(s[:SyncLen], Sync[:])
		// header: BCD m, BCD s, BCD f, mode
		lba := int32(i)
		m, sec, f := lbaToBCDMSF(lba)
		s[12] = m
		s[13] = sec
		s[14] = f
		s[15] = 0x01 // mode 1
		// fill user data with deterministic noise
		for j := 16; j < SectorSize; j++ {
			s[j] = byte(j * (i + 1))
		}
	}
	// build .scram from bin: pregap zeros + scrambled bin + leadout zeros, then shift.
	pregap := 150
	totalSectors := int32(pregap+mainSectors) + leadoutSectors
	scram := make([]byte, int64(totalSectors)*int64(SectorSize)+int64(writeOffsetBytes))
	for i := int32(0); i < totalSectors; i++ {
		var sec [SectorSize]byte
		switch {
		case i < int32(pregap):
			sec = generateMode1ZeroSector(int32(i) + LBAPregapStart)
		case i < int32(pregap+mainSectors):
			binIdx := int(i) - pregap
			copy(sec[:], bin[binIdx*SectorSize:(binIdx+1)*SectorSize])
			Scramble(&sec)
		default:
			sec = generateMode1ZeroSector(int32(i) + LBAPregapStart)
		}
		dst := int64(i)*int64(SectorSize) + int64(writeOffsetBytes)
		// when offset is negative, the first sector's leading bytes are clipped.
		writeAt(scram, dst, sec[:])
	}
	params := BuildParams{
		LeadinLBA:        leadinLBA,
		WriteOffsetBytes: writeOffsetBytes,
		ScramSize:        int64(len(scram)),
		BinFirstLBA:      0,
		BinSectorCount:   int32(mainSectors),
		Tracks:           []Track{{Number: 1, Mode: "MODE1/2352", FirstLBA: 0}},
	}
	return bin, scram, params
}

// writeAt copies src into dst[at:], clipping if at < 0 or at+len > len(dst).
func writeAt(dst []byte, at int64, src []byte) {
	if at >= int64(len(dst)) {
		return
	}
	srcStart := int64(0)
	if at < 0 {
		srcStart = -at
		at = 0
	}
	if srcStart >= int64(len(src)) {
		return
	}
	n := int64(len(src)) - srcStart
	if at+n > int64(len(dst)) {
		n = int64(len(dst)) - at
	}
	copy(dst[at:at+n], src[srcStart:srcStart+n])
}

func lbaToBCDMSF(lba int32) (byte, byte, byte) {
	v := lba + 150 // post-pregap offset
	m := v / (60 * 75)
	v -= m * 60 * 75
	s := v / 75
	f := v - s*75
	enc := func(n int32) byte { return byte(n/10*16 + n%10) }
	return enc(m), enc(s), enc(f)
}

func TestBuilderCleanRoundTrip(t *testing.T) {
	bin, scram, params := synthDisc(t, 100, -48, 10)
	var hat bytes.Buffer
	errs, err := BuildEpsilonHat(&hat, params, bytes.NewReader(bin), bytes.NewReader(scram))
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("got %d error sectors, want 0", len(errs))
	}
	if int64(hat.Len()) != params.ScramSize {
		t.Fatalf("ε̂ size %d != scramSize %d", hat.Len(), params.ScramSize)
	}
	if !bytes.Equal(hat.Bytes(), scram) {
		t.Fatalf("ε̂ != scram for clean disc")
	}
}

func TestBuilderDetectsErrorSector(t *testing.T) {
	bin, scram, params := synthDisc(t, 100, 0, 10)
	// flip a byte inside the third main sector of .scram (LBA 2)
	scram[(150+2)*SectorSize+200] ^= 0xFF
	var hat bytes.Buffer
	errs, err := BuildEpsilonHat(&hat, params, bytes.NewReader(bin), bytes.NewReader(scram))
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 1 || errs[0] != 2 {
		t.Fatalf("got error sectors %v, want [2]", errs)
	}
	if int64(hat.Len()) != params.ScramSize {
		t.Fatalf("ε̂ size mismatch")
	}
}

func TestBuilderRefusesAtTooManyMismatches(t *testing.T) {
	bin, scram, params := synthDisc(t, 100, 0, 10)
	// flip every main sector (100% mismatch)
	for i := 0; i < 100; i++ {
		scram[(150+i)*SectorSize+50] ^= 0xFF
	}
	_, err := BuildEpsilonHat(io.Discard, params, bytes.NewReader(bin), bytes.NewReader(scram))
	if err == nil {
		t.Fatal("expected layout-mismatch error")
	}
	var lme *LayoutMismatchError
	if !errors.As(err, &lme) {
		t.Fatalf("error %v is not *LayoutMismatchError", err)
	}
}

func TestBuilderCleanRoundTripPositiveOffset(t *testing.T) {
	bin, scram, params := synthDisc(t, 100, 48, 10)
	var hat bytes.Buffer
	errs, err := BuildEpsilonHat(&hat, params, bytes.NewReader(bin), bytes.NewReader(scram))
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("got %d error sectors, want 0", len(errs))
	}
	if int64(hat.Len()) != params.ScramSize {
		t.Fatalf("ε̂ size %d != scramSize %d", hat.Len(), params.ScramSize)
	}
	if !bytes.Equal(hat.Bytes(), scram) {
		t.Fatal("ε̂ != scram for clean disc with positive write offset")
	}
}
