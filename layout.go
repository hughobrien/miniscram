// /home/hugh/miniscram/layout.go
package main

import "fmt"

const (
	SectorSize     = 2352
	SyncLen        = 12
	LBALeadinStart = -45150
	LBAPregapStart = -150
	MSFFramesPerSecond = 75
)

var Sync = [SyncLen]byte{
	0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00,
}

func bcdDecode(b byte) int { return int(b>>4)*10 + int(b&0x0F) }

// BCDMSFToLBA converts a 3-byte BCD MSF triple read from a sector header
// into an LBA. Per ECMA-130 / Redbook the conversion is:
//
//	LBA = ((m*60) + s) * 75 + f - 150
//
// Frames in the lead-in (m >= 0xA0 BCD = 160 decimal) wrap into the
// negative range; this implementation matches redumper's MSF_to_LBA.
func BCDMSFToLBA(bcdMSF [3]byte) int32 {
	m := bcdDecode(bcdMSF[0])
	s := bcdDecode(bcdMSF[1])
	f := bcdDecode(bcdMSF[2])
	const minutesWrap = 160
	const lbaLimit = minutesWrap * 60 * MSFFramesPerSecond
	lba := int32(MSFFramesPerSecond*(60*m+s) + f - 150)
	if m >= minutesWrap {
		lba -= int32(lbaLimit)
	}
	return lba
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
func TotalLBAs(scramSize int64, writeOffsetBytes int) int32 {
	v := scramSize - int64(writeOffsetBytes) + int64(SectorSize) - 1
	if v < 0 {
		panic(fmt.Sprintf("TotalLBAs: negative numerator (%d)", v))
	}
	return int32(v / int64(SectorSize))
}
