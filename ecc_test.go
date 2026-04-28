// /home/hugh/miniscram/ecc_test.go
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

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
