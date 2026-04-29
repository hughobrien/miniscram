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
	Scramble(&sec)
	for i := 0; i < SyncLen; i++ {
		if sec[i] != Sync[i] {
			t.Fatalf("sync byte %d = 0x%02x; want 0x%02x", i, sec[i], Sync[i])
		}
	}
	if sec[12] != 0x00 || sec[13] != 0x02 || sec[14] != 0x00 {
		t.Fatalf("BCD MSF = %02x %02x %02x; want 00 02 00", sec[12], sec[13], sec[14])
	}
	if sec[15] != 0x00 {
		t.Fatalf("mode byte = 0x%02x; want 0x00", sec[15])
	}
}

// synthDiscRaw builds a minimal disc and constructs BuildParams directly.
// Builder unit tests need BuildParams; other tests should use synthDisc(t, SynthOpts{}).
func synthDiscRaw(t *testing.T, mainSectors int, writeOffsetBytes int, leadoutSectors int32, modeByte byte, modeStr string) ([]byte, []byte, BuildParams) {
	t.Helper()
	disc := synthDisc(t, SynthOpts{
		MainSectors: mainSectors, WriteOffset: writeOffsetBytes,
		LeadoutSectors: leadoutSectors, Mode: modeStr, ModeByte: modeByte,
	})
	params := BuildParams{
		LeadinLBA: disc.LeadinLBA, WriteOffsetBytes: writeOffsetBytes,
		ScramSize: int64(len(disc.Scram)), BinFirstLBA: 0,
		BinSectorCount: int32(mainSectors),
		Tracks:         []Track{{Number: 1, Mode: modeStr, FirstLBA: 0}},
	}
	return disc.Bin, disc.Scram, params
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
	v := lba + 150
	m := v / (60 * 75)
	v -= m * 60 * 75
	s := v / 75
	f := v - s*75
	enc := func(n int32) byte { return byte(n/10*16 + n%10) }
	return enc(m), enc(s), enc(f)
}

func TestBuilderCleanRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name        string
		modeByte    byte
		modeStr     string
		writeOffset int
	}{
		{"mode1-neg-offset", 0x01, "MODE1/2352", -48},
		{"mode1-pos-offset", 0x01, "MODE1/2352", 48},
		{"mode2-neg-offset", 0x02, "MODE2/2352", -48},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, scram, params := synthDiscRaw(t, 100, tc.writeOffset, 10, tc.modeByte, tc.modeStr)
			var hat bytes.Buffer
			var deltaBuf bytes.Buffer
			enc := NewDeltaEncoder(&deltaBuf)
			errs, mismatched, err := BuildEpsilonHat(&hat, params, bytes.NewReader(bin), bytes.NewReader(scram), enc.Append)
			if err != nil {
				t.Fatal(err)
			}
			count, _ := enc.Close()
			if len(errs) != 0 || mismatched != 0 || count != 0 {
				t.Fatalf("got %d error sectors / %d mismatched / %d delta records, want 0", len(errs), mismatched, count)
			}
			if int64(hat.Len()) != params.ScramSize {
				t.Fatalf("ε̂ size %d != scramSize %d", hat.Len(), params.ScramSize)
			}
			if !bytes.Equal(hat.Bytes(), scram) {
				t.Fatal("ε̂ != scram")
			}
		})
	}
}

func TestBuilderDetectsErrorSector(t *testing.T) {
	bin, scram, params := synthDiscRaw(t, 100, 0, 10, 0x01, "MODE1/2352")
	scram[(150+2)*SectorSize+200] ^= 0xFF
	var hat bytes.Buffer
	errs, mismatched, err := BuildEpsilonHat(&hat, params, bytes.NewReader(bin), bytes.NewReader(scram), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 1 || errs[0] != 2 {
		t.Fatalf("got error sectors %v, want [2]", errs)
	}
	if mismatched != 1 {
		t.Fatalf("got mismatched %d, want 1", mismatched)
	}
}

func TestBuilderRefusesAtTooManyMismatches(t *testing.T) {
	bin, scram, params := synthDiscRaw(t, 100, 0, 10, 0x01, "MODE1/2352")
	for i := 0; i < 100; i++ {
		scram[(150+i)*SectorSize+50] ^= 0xFF
	}
	errLBAs, mismatched, err := BuildEpsilonHat(io.Discard, params, bytes.NewReader(bin), bytes.NewReader(scram), nil)
	if err != nil {
		t.Fatal("BuildEpsilonHat itself should not error")
	}
	totalDisc := TotalLBAs(params.ScramSize, params.WriteOffsetBytes)
	err = CheckLayoutMismatch(errLBAs, mismatched, totalDisc)
	if err == nil {
		t.Fatal("expected layout-mismatch error")
	}
	var lme *LayoutMismatchError
	if !errors.As(err, &lme) {
		t.Fatalf("error %v is not *LayoutMismatchError", err)
	}
}
