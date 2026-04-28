// /home/hugh/miniscram/builder.go
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// BuildParams holds everything BuildEpsilonHat needs to know about the
// disc layout. Note LeadinLBA is parameterised so unit tests can use a
// truncated layout (no real leadin) while real Redumper input uses
// LBALeadinStart = -45150.
type BuildParams struct {
	LeadinLBA        int32
	WriteOffsetBytes int
	ScramSize        int64
	BinFirstLBA      int32
	BinSectorCount   int32
	Tracks           []Track
}

// LayoutMismatchError indicates the lockstep pre-check found enough
// mismatches to prove that the caller's parameters don't actually
// describe the .scram on disk.
type LayoutMismatchError struct {
	BinSectors    int32
	ErrorSectors  []int32 // capped at 10 for the message
	MismatchRatio float64
}

func (e *LayoutMismatchError) Error() string {
	return fmt.Sprintf("layout mismatch: %d/%d sectors differ (%.1f%%); first mismatched LBAs: %v",
		len(e.ErrorSectors), e.BinSectors, e.MismatchRatio*100, e.ErrorSectors)
}

const layoutMismatchAbortRatio = 0.05

// generateMode1ZeroSector returns the scrambled bytes of a Mode 1
// sector with all-zero user data and a BCD MSF header for the given
// LBA. This is the standard pregap and leadout content for CD-ROMs
// mastered with Mode 1 zero sectors in those regions.
func generateMode1ZeroSector(lba int32) [SectorSize]byte {
	var sec [SectorSize]byte
	copy(sec[:SyncLen], Sync[:])
	msf := LBAToBCDMSF(lba)
	sec[12] = msf[0]
	sec[13] = msf[1]
	sec[14] = msf[2]
	sec[15] = 0x01
	edc := ComputeEDC(sec[:2064])
	sec[2064] = edc[0]
	sec[2065] = edc[1]
	sec[2066] = edc[2]
	sec[2067] = edc[3]
	// bytes 2068..2075 already zero (intermediate)
	ComputeECC(&sec)
	Scramble(&sec)
	return sec
}

// generateLeadoutSector returns the scrambled bytes of a Mode 0 zero
// sector for the given LBA: sync + BCD MSF header + mode 0x00 + 2336
// zeros, all scrambled. This matches the convention used by Redumper-
// dumped Deus Ex (and likely most commercial CD-ROMs) for the leadout
// region — the disc's "this is the end" filler.
//
// Use for LBAs at or after the bin's last sector. For pregap (LBAs in
// [-150, BinFirstLBA)) use generateMode1ZeroSector instead.
func generateLeadoutSector(lba int32) [SectorSize]byte {
	var sec [SectorSize]byte
	copy(sec[:SyncLen], Sync[:])
	msf := LBAToBCDMSF(lba)
	sec[12] = msf[0]
	sec[13] = msf[1]
	sec[14] = msf[2]
	sec[15] = 0x00 // Mode 0 (per ECMA-130 §14.2)
	// Bytes 16..2351 stay zero. No EDC, no ECC.
	Scramble(&sec)
	return sec
}

// trackModeAt returns the mode of the track whose first LBA is <= lba.
// Returns "" for LBAs that precede the first track (leadin/pregap).
// Callers should only consult this for LBAs known to fall inside the
// .bin coverage range — leadin/pregap/leadout sectors are emitted as
// zeros or scrambled-zeros directly, never via trackModeAt.
func trackModeAt(tracks []Track, lba int32) string {
	mode := ""
	for _, tr := range tracks {
		if tr.FirstLBA <= lba {
			mode = tr.Mode
		} else {
			break
		}
	}
	return mode
}

// scramFileOffset returns the byte offset within the .scram file for a
// given LBA. Uses p.LeadinLBA as the base (rather than the hardcoded
// LBALeadinStart) so that unit tests with truncated disc layouts work
// correctly.
func scramFileOffset(lba, leadinLBA int32, writeOffsetBytes int) int64 {
	return int64(lba-leadinLBA)*int64(SectorSize) + int64(writeOffsetBytes)
}

// BuildEpsilonHat writes the reconstructed scrambled image to out. If
// scram is non-nil, sectors covered by .bin are compared against it in
// lockstep and the list of mismatched LBAs is returned. The caller is
// responsible for closing the io.Reader handles; out must be a Writer
// that can absorb ScramSize bytes (typically a *os.File — random
// access is not required).
//
// Implementation note: bin must be a stream-readable source delivering
// (BinSectorCount × SectorSize) bytes in order. scram, if provided,
// must also be sequentially readable from byte 0 of the .scram file.
func BuildEpsilonHat(out io.Writer, p BuildParams, bin io.Reader, scram io.Reader) ([]int32, error) {
	if p.ScramSize <= 0 {
		return nil, errors.New("ScramSize must be positive")
	}
	totalLBAs := TotalLBAs(p.ScramSize, p.WriteOffsetBytes)
	endLBA := p.LeadinLBA + totalLBAs

	// Position in scram (read cursor). When scram != nil we read it in
	// lockstep with our writes.
	var scramCursor int64
	advanceScram := func(to int64) error {
		if scram == nil || to <= scramCursor {
			return nil
		}
		_, err := io.CopyN(io.Discard, scram, to-scramCursor)
		if err != nil {
			return fmt.Errorf("seeking scram to %d: %w", to, err)
		}
		scramCursor = to
		return nil
	}

	// Apply leading shift (positive offset prepends zeros to ε̂).
	written := int64(0)
	if p.WriteOffsetBytes > 0 {
		zeros := make([]byte, p.WriteOffsetBytes)
		if _, err := out.Write(zeros); err != nil {
			return nil, err
		}
		written = int64(p.WriteOffsetBytes)
	}
	skipFirst := 0
	if p.WriteOffsetBytes < 0 {
		skipFirst = -p.WriteOffsetBytes
	}

	binBuf := make([]byte, SectorSize)
	scramBuf := make([]byte, SectorSize)
	var errSectors []int32

	for lba := p.LeadinLBA; lba < endLBA; lba++ {
		var sec [SectorSize]byte
		switch {
		case lba < LBAPregapStart:
			// leadin: zeros (Redumper convention; drives don't read here)
		case lba < p.BinFirstLBA:
			sec = generateMode1ZeroSector(lba)
		case lba < p.BinFirstLBA+p.BinSectorCount:
			if _, err := io.ReadFull(bin, binBuf); err != nil {
				return nil, fmt.Errorf("reading bin LBA %d: %w", lba, err)
			}
			copy(sec[:], binBuf)
			if trackModeAt(p.Tracks, lba) != "AUDIO" {
				Scramble(&sec)
			}
		default:
			sec = generateLeadoutSector(lba)
		}

		// Apply skipFirst on the very first sector if needed.
		secBytes := sec[:]
		if skipFirst > 0 {
			secBytes = secBytes[skipFirst:]
			skipFirst = 0
		}
		// Don't write past ScramSize.
		remain := p.ScramSize - written
		if int64(len(secBytes)) > remain {
			secBytes = secBytes[:remain]
		}
		if _, err := out.Write(secBytes); err != nil {
			return nil, err
		}
		written += int64(len(secBytes))

		// Lockstep pre-check (only for full bin-covered sectors).
		// Use p.LeadinLBA as the scram-file base so truncated test
		// layouts (LeadinLBA = -150) work as well as real ones (-45150).
		secOffset := scramFileOffset(lba, p.LeadinLBA, p.WriteOffsetBytes)
		if scram != nil &&
			secOffset >= 0 && secOffset+SectorSize <= p.ScramSize {
			if err := advanceScram(secOffset); err != nil {
				return nil, err
			}
			if _, err := io.ReadFull(scram, scramBuf); err != nil {
				return nil, fmt.Errorf("reading scram LBA %d: %w", lba, err)
			}
			// ReadFull advanced the underlying reader by SectorSize but
			// does not touch scramCursor; resync.
			scramCursor = secOffset + SectorSize
			if !bytes.Equal(sec[:], scramBuf) {
				errSectors = append(errSectors, lba)
			}
		}
		if written >= p.ScramSize {
			break
		}
	}

	if endLBA > p.LeadinLBA {
		totalDisc := int32(endLBA - p.LeadinLBA)
		ratio := float64(len(errSectors)) / float64(totalDisc)
		if ratio > layoutMismatchAbortRatio {
			head := errSectors
			if len(head) > 10 {
				head = head[:10]
			}
			return errSectors, &LayoutMismatchError{
				BinSectors:    totalDisc,
				ErrorSectors:  head,
				MismatchRatio: ratio,
			}
		}
	}
	return errSectors, nil
}

// BuildEpsilonHatAndDelta walks bin and scram in lockstep, writing the
// reconstructed scrambled image to epsilonHat and the structured
// delta to deltaOut. scram and deltaOut must both be nil or both
// non-nil.
//
// Returns the number of override records written and the LBAs they
// cover (capped at errorSectorsListCap). Aborts with
// LayoutMismatchError if more than 5% of disc sectors mismatch.
func BuildEpsilonHatAndDelta(
	epsilonHat io.Writer,
	deltaOut io.Writer,
	p BuildParams,
	bin io.Reader,
	scram io.Reader,
) (int, []int32, error) {
	if (scram == nil) != (deltaOut == nil) {
		return 0, nil, errors.New("BuildEpsilonHatAndDelta: scram and deltaOut must both be nil or both non-nil")
	}
	if scram == nil {
		// Build-only mode: no delta, no comparison.
		_, err := BuildEpsilonHat(epsilonHat, p, bin, nil)
		return 0, nil, err
	}

	if p.ScramSize <= 0 {
		return 0, nil, errors.New("ScramSize must be positive")
	}
	totalLBAs := TotalLBAs(p.ScramSize, p.WriteOffsetBytes)
	endLBA := p.LeadinLBA + totalLBAs

	// Delta encoder state.
	var body []byte
	count := 0
	var run []byte
	var runStart int64

	flush := func(off int64, r []byte) {
		i := 0
		for i < len(r) {
			n := len(r) - i
			if n > SectorSize {
				n = SectorSize
			}
			var hdr [12]byte
			binary.BigEndian.PutUint64(hdr[:8], uint64(off+int64(i)))
			binary.BigEndian.PutUint32(hdr[8:], uint32(n))
			body = append(body, hdr[:]...)
			body = append(body, r[i:i+n]...)
			count++
			i += n
		}
	}

	// Apply leading shift.
	if p.WriteOffsetBytes > 0 {
		zeros := make([]byte, p.WriteOffsetBytes)
		if _, err := epsilonHat.Write(zeros); err != nil {
			return 0, nil, err
		}
	}
	skipFirst := 0
	if p.WriteOffsetBytes < 0 {
		skipFirst = -p.WriteOffsetBytes
	}
	written := int64(0)
	if p.WriteOffsetBytes > 0 {
		written = int64(p.WriteOffsetBytes)
	}

	binBuf := make([]byte, SectorSize)
	scramBuf := make([]byte, SectorSize)
	var errLBAs []int32
	var scramCur int64

	advanceScramTo := func(target int64) error {
		if target <= scramCur {
			return nil
		}
		if _, err := io.CopyN(io.Discard, scram, target-scramCur); err != nil {
			return fmt.Errorf("advancing scram to %d: %w", target, err)
		}
		scramCur = target
		return nil
	}

	for lba := p.LeadinLBA; lba < endLBA; lba++ {
		var sec [SectorSize]byte
		switch {
		case lba < LBAPregapStart:
			// leadin: zeros
		case lba < p.BinFirstLBA:
			sec = generateMode1ZeroSector(lba)
		case lba < p.BinFirstLBA+p.BinSectorCount:
			if _, err := io.ReadFull(bin, binBuf); err != nil {
				return 0, nil, fmt.Errorf("reading bin LBA %d: %w", lba, err)
			}
			copy(sec[:], binBuf)
			if trackModeAt(p.Tracks, lba) != "AUDIO" {
				Scramble(&sec)
			}
		default:
			sec = generateLeadoutSector(lba)
		}

		secBytes := sec[:]
		if skipFirst > 0 {
			secBytes = secBytes[skipFirst:]
			skipFirst = 0
		}
		remain := p.ScramSize - written
		if int64(len(secBytes)) > remain {
			secBytes = secBytes[:remain]
		}
		hatStart := written
		if _, err := epsilonHat.Write(secBytes); err != nil {
			return 0, nil, err
		}
		written += int64(len(secBytes))

		// Compare against scram for this byte range.
		if err := advanceScramTo(hatStart); err != nil {
			return 0, nil, err
		}
		if _, err := io.ReadFull(scram, scramBuf[:len(secBytes)]); err != nil {
			return 0, nil, fmt.Errorf("reading scram at %d: %w", hatStart, err)
		}
		scramCur = hatStart + int64(len(secBytes))

		sectorMismatch := false
		for i := 0; i < len(secBytes); i++ {
			if secBytes[i] != scramBuf[i] {
				if len(run) == 0 {
					runStart = hatStart + int64(i)
				}
				run = append(run, scramBuf[i])
				sectorMismatch = true
			} else if len(run) > 0 {
				flush(runStart, run)
				run = run[:0]
			}
		}
		if sectorMismatch && len(errLBAs) < errorSectorsListCap {
			errLBAs = append(errLBAs, lba)
		}
		if written >= p.ScramSize {
			break
		}
	}
	if len(run) > 0 {
		flush(runStart, run)
	}

	// Mismatch ratio check (5% of total disc sectors).
	if endLBA > p.LeadinLBA {
		totalDisc := int32(endLBA - p.LeadinLBA)
		ratio := float64(len(errLBAs)) / float64(totalDisc)
		if ratio > layoutMismatchAbortRatio {
			head := errLBAs
			if len(head) > 10 {
				head = head[:10]
			}
			return 0, errLBAs, &LayoutMismatchError{
				BinSectors:    totalDisc,
				ErrorSectors:  head,
				MismatchRatio: ratio,
			}
		}
	}

	// Write count + body to deltaOut.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(count))
	if _, err := deltaOut.Write(hdr[:]); err != nil {
		return 0, nil, err
	}
	if _, err := deltaOut.Write(body); err != nil {
		return 0, nil, err
	}
	return count, errLBAs, nil
}
