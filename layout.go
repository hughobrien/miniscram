// /home/hugh/miniscram/layout.go
package main

import "fmt"

const (
	SectorSize         = 2352
	SyncLen            = 12
	LBALeadinStart     = -45150
	LBAPregapStart     = -150
	MSFFramesPerSecond = 75
)

var Sync = [SyncLen]byte{
	0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00,
}

func bcdDecode(b byte) int { return int(b>>4)*10 + int(b&0x0F) }

// BCDMSFToLBA converts a 3-byte BCD MSF triple (as found in the
// header of a CD-ROM data sector) into an LBA per ECMA-130 / Redbook:
//
//	LBA = ((m * 60) + s) * 75 + f - 150
//
// Inputs are expected to be valid BCD bytes (each nibble in 0x0..0x9);
// passing non-BCD bytes will produce nonsense output but won't panic.
// The 2-second pre-pregap means MSF 00:02:00 corresponds to LBA 0,
// and MSF 00:00:00 corresponds to LBA -150.
func BCDMSFToLBA(bcdMSF [3]byte) int32 {
	m := bcdDecode(bcdMSF[0])
	s := bcdDecode(bcdMSF[1])
	f := bcdDecode(bcdMSF[2])
	return int32(MSFFramesPerSecond*(60*m+s) + f - 150)
}

// LBAToBCDMSF is the inverse of BCDMSFToLBA. Given an LBA, returns
// the 3-byte BCD MSF triple stored in the header field of the
// corresponding Mode 1 sector. LBA -150 yields {0x00, 0x00, 0x00}.
//
// Caller must ensure lba is in [-150, 99*60*75 - 150) — the absolute-
// time addressing range per ECMA-130 §14.2. Out-of-range inputs
// silently produce nonsense BCD bytes (no panic).
func LBAToBCDMSF(lba int32) [3]byte {
	v := lba + 150
	m := v / (60 * MSFFramesPerSecond)
	v -= m * 60 * MSFFramesPerSecond
	s := v / MSFFramesPerSecond
	f := v - s*MSFFramesPerSecond
	enc := func(n int32) byte { return byte(n/10*16 + n%10) }
	return [3]byte{enc(m), enc(s), enc(f)}
}

// ScramOffset returns the byte offset within a Redumper .scram file
// for a given LBA, given the disc's write offset in bytes
// (samples × 4). May be negative for LBAs that fall before the file
// start when the write offset is negative.
func ScramOffset(lba int32, writeOffsetBytes int) int64 {
	return int64(lba-LBALeadinStart)*int64(SectorSize) + int64(writeOffsetBytes)
}

// TotalLBAs returns the number of full+partial LBA-sized records the
// .scram file represents, given its size and write offset.
//
// Precondition: scramSize > |writeOffsetBytes|. Callers (see
// pack.go) are responsible for validating this upstream — a violation
// indicates a programming error, not a malformed input file, so this
// function panics rather than returning an error.
func TotalLBAs(scramSize int64, writeOffsetBytes int) int32 {
	v := scramSize - int64(writeOffsetBytes) + int64(SectorSize) - 1
	if v < 0 {
		panic(fmt.Sprintf("TotalLBAs: precondition violated (scramSize=%d, writeOffsetBytes=%d)",
			scramSize, writeOffsetBytes))
	}
	return int32(v / int64(SectorSize))
}
