// /home/hugh/miniscram/ecma130.go
//
// Implements the CD-ROM coding layers from Standard ECMA-130 (2nd
// Edition, June 1996, "Data interchange on read-only 120 mm optical
// data disks (CD-ROM)"). The PDF is in the repo root at
// ECMA-130_2nd_edition_june_1996.pdf and clause numbers below refer
// to it.
//
// miniscram is licensed under GPL-3.0; see ./LICENSE. Some routines
// here are adapted from redumper (https://github.com/superg/redumper,
// also GPL-3.0); attribution is noted at each lift point.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// =====================================================================
// ECMA-130 clause 15 + Annex B: Scrambling
// =====================================================================
//
// The keystream-table builder below is a Go port of redumper's
// Scrambler::_TABLE lambda (cd/cd_scrambler.ixx), kept near-verbatim
// because it is the canonical, well-trusted implementation that the
// CD-preservation community already relies on. Redumper is GPL-3.0;
// miniscram matches that license.
//
// The construction itself is ECMA-130 Annex B: a 15-bit LFSR with
// feedback polynomial x^15 + x + 1, pre-set to 0x0001. Bytes 0..11 of
// the table are zero (the sync field, clause 14.1, is not scrambled);
// bytes 12..2351 are the LFSR keystream and are XORed into each sector
// by Scramble().

// expectedScrambleTableSHA256 pins the keystream produced by the Annex
// B LFSR. CheckScrambleTable verifies it at init so any change that
// alters the table's output panics on startup.
const expectedScrambleTableSHA256 = "5b91ebf010f0238d0c371d14c90722c8b1b7141c9f5b71dea05fe529bf15fd38"

// scrambleTable is the 2352-byte XOR mask defined by ECMA-130 clause 15
// + Annex B. Built once at startup and pinned via the SHA-256 above.
var scrambleTable = buildScrambleTable()

// buildScrambleTable is a near-verbatim Go port of redumper's
// Scrambler::_TABLE lambda (cd/cd_scrambler.ixx). The two inline
// comments inside the loop are reproduced from redumper as-is — they
// quote the ECMA-130 Annex B spec text.
func buildScrambleTable() *[SectorSize]byte {
	var table [SectorSize]byte

	// ECMA-130

	shiftRegister := uint16(0x0001)

	for i := SyncLen; i < SectorSize; i++ {
		table[i] = byte(shiftRegister)

		for b := 0; b < 8; b++ {
			// each bit in the input stream of the scrambler is added modulo 2 to the least significant bit of a maximum length register
			carry := (shiftRegister & 1) ^ ((shiftRegister >> 1) & 1)
			// the 15-bit register is of the parallel block synchronized type, and fed back according to polynomial x15 + x + 1
			shiftRegister = (carry<<15 | shiftRegister) >> 1
		}
	}

	return &table
}

// Scramble XORs bytes 12..2351 of sector with the Annex B keystream
// (clause 15). The XOR is self-inverse: calling Scramble twice on the
// same sector returns the original bytes. The 12-byte sync field
// (clause 14.1: 00 FF×10 00) is left untouched.
func Scramble(sector *[SectorSize]byte) {
	for i := SyncLen; i < SectorSize; i++ {
		sector[i] ^= scrambleTable[i]
	}
}

// CheckScrambleTable verifies the generated table matches the pinned
// SHA-256. Called from init() so any change to buildScrambleTable that
// alters its output panics at startup.
func CheckScrambleTable() error {
	sum := sha256.Sum256(scrambleTable[:])
	got := hex.EncodeToString(sum[:])
	if got != expectedScrambleTableSHA256 {
		return fmt.Errorf("scramble table sha256 mismatch: got %s want %s",
			got, expectedScrambleTableSHA256)
	}
	return nil
}

func init() {
	if err := CheckScrambleTable(); err != nil {
		panic(err)
	}
}

// =====================================================================
// ECMA-130 clause 14.3: EDC field (CRC over a Mode-1 sector prefix)
// =====================================================================
//
// Clause 14.3 says: "The EDC field shall consist of 4 bytes recorded in
// positions 2 064 to 2 067. The error detection code shall be a 32-bit
// CRC applied on bytes 0 to 2 063. The least significant bit of a data
// byte is used first. The EDC codeword must be divisible by the check
// polynomial: P(x) = (x^16 + x^15 + x^2 + 1)·(x^16 + x^2 + x + 1). The
// least significant parity bit (x^0) is stored in the most significant
// bit position of byte 2 067." We implement this with a standard
// reflected (LSB-first) byte-table CRC.

// edcPoly is the reflection of the clause 14.3 polynomial.
// (x^16 + x^15 + x^2 + 1)·(x^16 + x^2 + x + 1) expands to the 33-bit
// value 0x1_8001_801B; the 32-bit reflection used by the LSB-first
// table form is 0xD801_8001. Verified empirically against Redumper-
// dumped sectors (Deus Ex sector 100) and a known-good LBA-0 zero
// sector.
const edcPoly = uint32(0xD8018001)

// edcTable is built from edcPoly at init() time.
var edcTable [256]uint32

// expectedEDCTableSHA256 pins the table contents.
const expectedEDCTableSHA256 = "0875e2687d8e984a77f00950f541c66c781febb0bc4e2444058bad275a4163d7"

func init() {
	buildEDCTable()
	if err := checkEDCTable(); err != nil {
		panic(err)
	}
}

func buildEDCTable() {
	for i := 0; i < 256; i++ {
		c := uint32(i)
		for j := 0; j < 8; j++ {
			mask := uint32(0)
			if c&1 != 0 {
				mask = edcPoly
			}
			c = (c >> 1) ^ mask
		}
		edcTable[i] = c
	}
}

func checkEDCTable() error {
	buf := make([]byte, 256*4)
	for i, v := range edcTable {
		binary.LittleEndian.PutUint32(buf[i*4:], v)
	}
	sum := sha256.Sum256(buf)
	got := hex.EncodeToString(sum[:])
	if got != expectedEDCTableSHA256 {
		return fmt.Errorf("edcTable sha256 mismatch: got %s want %s",
			got, expectedEDCTableSHA256)
	}
	return nil
}

// ComputeEDC returns the 4-byte EDC for a Mode 1 sector.
// Input:  bytes 0..2063 of the unscrambled sector.
// Output: bytes intended for offset 2064..2067 (little-endian).
func ComputeEDC(secPrefix []byte) [4]byte {
	var crc uint32
	for _, b := range secPrefix {
		crc = (crc >> 8) ^ edcTable[byte(crc)^b]
	}
	var out [4]byte
	binary.LittleEndian.PutUint32(out[:], crc)
	return out
}

// =====================================================================
// ECMA-130 clauses 14.5–14.6 + Annex A: ECC (Reed-Solomon Product Code)
// =====================================================================
//
// Clauses 14.5–14.6 specify P-Parity (172 bytes at positions 2076..2247,
// computed over bytes 12..2075) and Q-Parity (104 bytes at positions
// 2248..2351, computed over bytes 12..2247) "as specified in annex A."
//
// Annex A.2 ("Input") groups the 1170 input bytes into 1170 16-bit
// words S(n) = MSB[B(2n+13)] · LSB[B(2n+12)] and applies the same code
// twice — once over the MSB stream, once over the LSB stream.
//
// Annex A.3 ("Encoding") defines the field as GF(2^8) generated by the
// primitive polynomial P(x) = x^8 + x^4 + x^3 + x^2 + 1, with primitive
// element α = (00000010) = 2. The 43 P-vectors are (26,24) Reed-Solomon
// codewords laid out as columns S(43·M_p + N_p); the 26 Q-vectors are
// (45,43) Reed-Solomon codewords laid out as diagonals
// S((44·M_q + 43·N_q) mod 1118). The two parity bytes per vector are
// the unique values that make H_P · V_P = 0 (resp. H_Q · V_Q = 0); we
// solve those linear equations directly via syndrome inversion.

// gfExp / gfLog are GF(2^8) lookup tables for the Annex A field
// (primitive polynomial 0x11D, α = 2). gfExp[i] = α^i; gfLog is its
// inverse, with gfLog[0] left at zero (undefined).
var (
	gfExp [256]byte
	gfLog [256]byte
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

// gfInv3 is 1/3 in GF(256). It falls out of the parity solver: H_P (and
// H_Q) have rows [1, 1, ..., 1] and [α^k, α^(k-1), ..., α^1, 1], so the
// two unknown parity bytes (v_{n-2}, v_{n-1}) satisfy a 2×2 system whose
// inverse contains 1/3. Computed once at init() from gfExp/gfLog.
var gfInv3 byte

// ComputeECC fills bytes 2076..2351 of sec with the P-Parity (clause
// 14.5) and Q-Parity (clause 14.6) bytes computed per Annex A.
//
// Per Annex A.2, the input is treated as 1170 16-bit words S(n); RSPC
// is applied independently to the MSB stream and the LSB stream. We
// deinterleave bytes 12..2351 into msb[] and lsb[], compute parity over
// each, then interleave the resulting parity bytes (positions 1032..
// 1169 of each stream) back into the output slots 2076..2351 (per
// Annex A.4: "The LSB of word 1 032 is recorded in byte 2 076, the MSB
// of word 1 169 in byte 2 351 of the Sector.").
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

// rspcParity fills positions 1032..1169 of stream with the P- and
// Q-parity bytes defined by Annex A.3, computed over positions 0..1031.
//
// P-vectors (Annex A.3, first matrix): 43 columns indexed by N_p; the
//
//	N_p-th vector is V_P = [S(43·M_p + N_p)] for M_p = 0..25, the last
//	two of which are the parity bytes that satisfy H_P · V_P = 0.
//
// Q-vectors (Annex A.3, second matrix): 26 diagonals indexed by N_q;
//
//	the N_q-th vector is V_Q = [S((44·M_q + 43·N_q) mod 1118)] for
//	M_q = 0..42, plus two parity bytes at index (43·26 + N_q) and
//	(44·26 + N_q), satisfying H_Q · V_Q = 0.
//
// For each codeword we compute two syndromes (s0 = Σ vᵢ, s1 = Σ α^kᵢ·vᵢ)
// over the data positions, then solve the 2×2 system H · (v_{n-2},
// v_{n-1})ᵀ = (s0, s1)ᵀ. The solution is v_{n-2} = (s0 ⊕ s1)/3,
// v_{n-1} = s0 ⊕ v_{n-2}, where the /3 is gfInv3.
func rspcParity(stream *[1170]byte) {
	// P-vectors: 43 columns, each (26,24) RS codeword over GF(256).
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
	// Q-vectors: 26 diagonals, each (45,43) RS codeword.
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
