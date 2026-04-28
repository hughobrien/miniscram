// /home/hugh/miniscram/edc.go
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// edcPoly is the reflected (LSB-first table form) of the ECMA-130
// §14.3 polynomial P(x) = (x^16 + x^15 + x^2 + 1) · (x^16 + x^2 + x + 1).
// In conventional form P(x) = 0x18001801B (33 bits); the 32-bit
// reflection is 0xD8018001. Verified empirically against Deus Ex
// sector 100 and against a known LBA-0 Mode 1 zero sector.
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
