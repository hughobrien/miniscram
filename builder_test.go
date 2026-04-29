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

func TestGenerateLeadoutSectorLBA0(t *testing.T) {
	sec := generateLeadoutSector(0)
	// Descramble (Scramble is self-inverse) and check fields.
	Scramble(&sec)
	// Sync field unchanged.
	for i := 0; i < SyncLen; i++ {
		if sec[i] != Sync[i] {
			t.Fatalf("sync byte %d = 0x%02x; want 0x%02x", i, sec[i], Sync[i])
		}
	}
	// BCD MSF for LBA 0 is 00:02:00.
	if sec[12] != 0x00 || sec[13] != 0x02 || sec[14] != 0x00 {
		t.Fatalf("BCD MSF = %02x %02x %02x; want 00 02 00", sec[12], sec[13], sec[14])
	}
	// Mode byte must be 0x00.
	if sec[15] != 0x00 {
		t.Fatalf("mode byte = 0x%02x; want 0x00", sec[15])
	}
	// Bytes 16..2351 must be zero.
	for i := 16; i < SectorSize; i++ {
		if sec[i] != 0 {
			t.Fatalf("byte %d = 0x%02x; want 0x00", i, sec[i])
		}
	}
}

// synthDiscWithMode returns (bin, scram, params) for a small fake disc
// with a single data track in the requested mode. modeByte goes into
// each bin sector's header byte (0x01 for MODE1/2352, 0x02 for
// MODE2/2352); modeStr is the cuesheet-style mode string and goes into
// the returned BuildParams' Tracks.
//
// Disc layout: pregap-of-zero (150 sectors) + scrambled bin sectors
// (mainSectors) + leadout scrambled-zero (leadoutSectors), shifted by
// writeOffsetBytes. LeadinLBA is set to -150 (no real leadin region)
// to keep the synthetic .scram small; BuildParams allows the override.
//
// Mode 1 vs Mode 2 differ in the EDC/ECC layout of real-world sectors,
// but miniscram's predictor doesn't compute EDC/ECC — it just scrambles
// raw bytes per the ECMA-130 table. The round-trip is therefore
// mode-agnostic, and this helper exists primarily to confirm that
// pack/unpack/cue parsing all flow through correctly for MODE2/2352.
func synthDiscWithMode(t *testing.T, mainSectors int, writeOffsetBytes int, leadoutSectors int32, modeByte byte, modeStr string) ([]byte, []byte, BuildParams) {
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
		s[15] = modeByte
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
			sec = generateLeadoutSector(int32(i) + LBAPregapStart)
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
		Tracks:           []Track{{Number: 1, Mode: modeStr, FirstLBA: 0}},
	}
	return bin, scram, params
}

// synthDisc is the Mode 1 / 2352 specialization of synthDiscWithMode,
// preserved as the default for the bulk of pre-existing tests.
func synthDisc(t *testing.T, mainSectors int, writeOffsetBytes int, leadoutSectors int32) ([]byte, []byte, BuildParams) {
	t.Helper()
	return synthDiscWithMode(t, mainSectors, writeOffsetBytes, leadoutSectors, 0x01, "MODE1/2352")
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

// TestBuilderCleanRoundTripMode2 exercises the MODE2/2352 path. Mode 1
// vs Mode 2 differ in EDC/ECC layout in real-world sectors, but
// miniscram's predictor is mode-agnostic for non-AUDIO tracks (it just
// scrambles raw bytes per the ECMA-130 table). This test confirms the
// predictor handles the Mode 2 mode byte (0x02 in sector header) and
// the cue's "MODE2/2352" string without regression.
func TestBuilderCleanRoundTripMode2(t *testing.T) {
	bin, scram, params := synthDiscWithMode(t, 100, -48, 10, 0x02, "MODE2/2352")
	var hat bytes.Buffer
	errs, err := BuildEpsilonHat(&hat, params, bytes.NewReader(bin), bytes.NewReader(scram))
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("got %d error sectors, want 0", len(errs))
	}
	if !bytes.Equal(hat.Bytes(), scram) {
		t.Fatalf("ε̂ != scram for clean Mode 2 disc")
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

func TestBuildEpsilonHatAndDeltaCleanRoundTrip(t *testing.T) {
	bin, scram, params := synthDisc(t, 100, -48, 10)
	var hatBuf bytes.Buffer
	var deltaBuf bytes.Buffer
	count, errs, err := BuildEpsilonHatAndDelta(
		&hatBuf, &deltaBuf, params,
		bytes.NewReader(bin), bytes.NewReader(scram),
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || len(errs) != 0 {
		t.Fatalf("expected 0 overrides, got count=%d errs=%v", count, errs)
	}
	if int64(hatBuf.Len()) != params.ScramSize {
		t.Fatalf("ε̂ size %d != scramSize %d", hatBuf.Len(), params.ScramSize)
	}
	if !bytes.Equal(hatBuf.Bytes(), scram) {
		t.Fatal("ε̂ != scram on clean disc")
	}
	if !bytes.Equal(deltaBuf.Bytes(), []byte{0, 0, 0, 0}) {
		t.Fatalf("delta = % x; want 00 00 00 00", deltaBuf.Bytes())
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

func TestBuildEpsilonHatAndDeltaRefusesAtTooManyMismatches(t *testing.T) {
	bin, scram, params := synthDisc(t, 100, 0, 10)
	// flip every main sector (100% mismatch in bin range; ~38% across full disc)
	for i := 0; i < 100; i++ {
		scram[(150+i)*SectorSize+50] ^= 0xFF
	}
	var hat, delta bytes.Buffer
	_, _, err := BuildEpsilonHatAndDelta(&hat, &delta, params, bytes.NewReader(bin), bytes.NewReader(scram))
	if err == nil {
		t.Fatal("expected layout-mismatch error")
	}
	var lme *LayoutMismatchError
	if !errors.As(err, &lme) {
		t.Fatalf("error %v is not *LayoutMismatchError", err)
	}
}
