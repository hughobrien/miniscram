// /home/hugh/miniscram/ecc.go
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ECMA-130 Annex A: Reed-Solomon Product Code over GF(2^8).
// Field polynomial: x^8 + x^4 + x^3 + x^2 + 1 = 0x11D.
// Primitive element α = 2.

var (
	gfExp [256]byte // gfExp[i] = α^i mod field polynomial
	gfLog [256]byte // gfLog[α^i] = i; gfLog[0] is undefined and stays zero
)

const expectedGFTablesSHA256 = "3f28238c7a4c03869377e7c31e50479e5c895dc44971b3dc8865789bbb9c33bc"

func init() {
	buildGFTables()
	if err := checkGFTables(); err != nil {
		panic(err)
	}
	gfInv3 = gfExp[(255-int(gfLog[3]))%255]
}

func buildGFTables() {
	x := byte(1)
	for i := 0; i < 255; i++ {
		gfExp[i] = x
		gfLog[x] = byte(i)
		hi := x & 0x80
		x <<= 1
		if hi != 0 {
			x ^= 0x1D
		}
	}
	gfExp[255] = gfExp[0]
}

func checkGFTables() error {
	buf := make([]byte, 512)
	copy(buf[:256], gfExp[:])
	copy(buf[256:], gfLog[:])
	sum := sha256.Sum256(buf)
	got := hex.EncodeToString(sum[:])
	if got != expectedGFTablesSHA256 {
		return fmt.Errorf("gfExp||gfLog sha256 mismatch: got %s want %s",
			got, expectedGFTablesSHA256)
	}
	return nil
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[(int(gfLog[a])+int(gfLog[b]))%255]
}

// gfInv3 is 1/3 in GF(256), computed once at init() and used by the
// RSPC parity solver.
var gfInv3 byte

// ComputeECC fills bytes 2076..2351 of sec with the P+Q parity per
// ECMA-130 §14.5/14.6 + Annex A.
func ComputeECC(sec *[SectorSize]byte) {
	var msb, lsb [1170]byte
	for n := 0; n < 1170; n++ {
		lsb[n] = sec[2*n+12]
		msb[n] = sec[2*n+13]
	}
	rspcParity(&msb)
	rspcParity(&lsb)
	for n := 1032; n < 1170; n++ {
		sec[2*n+12] = lsb[n]
		sec[2*n+13] = msb[n]
	}
}

// rspcParity fills positions 1032..1169 of stream with P+Q parity
// computed over positions 0..1031.
func rspcParity(stream *[1170]byte) {
	// P-vectors: 43 columns, each (26,24) RS codeword.
	for np := 0; np < 43; np++ {
		var s0, s1 byte
		for i := 0; i < 24; i++ {
			v := stream[43*i+np]
			s0 ^= v
			s1 ^= gfMul(gfExp[(25-i)%255], v)
		}
		v24 := gfMul(s0^s1, gfInv3)
		v25 := s0 ^ v24
		stream[1032+np] = v24
		stream[1075+np] = v25
	}
	// Q-vectors: 26 diagonals, each (45,43) RS codeword. Indices use
	// (44·MQ + 43·NQ) mod 1118.
	for nq := 0; nq < 26; nq++ {
		var s0, s1 byte
		for mq := 0; mq < 43; mq++ {
			v := stream[(44*mq+43*nq)%1118]
			s0 ^= v
			s1 ^= gfMul(gfExp[(44-mq)%255], v)
		}
		v43 := gfMul(s0^s1, gfInv3)
		v44 := s0 ^ v43
		stream[1118+nq] = v43
		stream[1144+nq] = v44
	}
}
