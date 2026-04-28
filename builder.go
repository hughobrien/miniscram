// /home/hugh/miniscram/builder.go
package main

import (
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

// trackModeAt returns the mode of the track containing the given LBA.
// Returns "" when no track covers it (e.g., leadin/leadout).
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
			// leadin: zeros
		case lba < p.BinFirstLBA:
			// pregap: scrambled zero == scramble table
			copy(sec[:], scrambleTable[:])
		case lba < p.BinFirstLBA+p.BinSectorCount:
			if _, err := io.ReadFull(bin, binBuf); err != nil {
				return nil, fmt.Errorf("reading bin LBA %d: %w", lba, err)
			}
			copy(sec[:], binBuf)
			if trackModeAt(p.Tracks, lba) != "AUDIO" {
				Scramble(&sec)
			}
		default:
			// leadout: scrambled zero
			copy(sec[:], scrambleTable[:])
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
			lba >= p.BinFirstLBA && lba < p.BinFirstLBA+p.BinSectorCount &&
			secOffset >= 0 && secOffset+SectorSize <= p.ScramSize {
			if err := advanceScram(secOffset); err != nil {
				return nil, err
			}
			if _, err := io.ReadFull(scram, scramBuf); err != nil {
				return nil, fmt.Errorf("reading scram LBA %d: %w", lba, err)
			}
			scramCursor = secOffset + SectorSize
			if !bytesEqual(sec[:], scramBuf) {
				errSectors = append(errSectors, lba)
			}
		}
		if written >= p.ScramSize {
			break
		}
	}

	if p.BinSectorCount > 0 {
		ratio := float64(len(errSectors)) / float64(p.BinSectorCount)
		if ratio > layoutMismatchAbortRatio {
			head := errSectors
			if len(head) > 10 {
				head = head[:10]
			}
			return errSectors, &LayoutMismatchError{
				BinSectors:    p.BinSectorCount,
				ErrorSectors:  head,
				MismatchRatio: ratio,
			}
		}
	}
	return errSectors, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
