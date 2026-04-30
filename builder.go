package main

import (
	"errors"
	"fmt"
	"io"
)

const errorSectorsListCap = 10000

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

// BuildEpsilonHat writes the reconstructed scrambled image to out.
//
// If scram is non-nil, every byte written is compared against scram in
// lockstep. onMismatch (if non-nil) is invoked for each contiguous
// run of mismatching bytes, with the file offset of the run start and
// the *scram* bytes (so the caller can encode delta records).
//
// Returns the list of LBAs that contained at least one mismatch
// (capped at errorSectorsListCap) and the count of mismatched sectors
// (uncapped). The caller decides what to do with this — see
// CheckLayoutMismatch.
//
// scram == nil implies onMismatch is ignored. The function does not
// emit anything on its own beyond writing to out; callers that need
// a delta payload supply onMismatch via a DeltaEncoder.
func BuildEpsilonHat(
	out io.Writer,
	p BuildParams,
	bin io.Reader,
	scram io.Reader,
	onMismatch func(off int64, scramRun []byte),
) ([]int32, int, int, error) {
	if p.ScramSize <= 0 {
		return nil, 0, 0, errors.New("ScramSize must be positive")
	}
	totalLBAs := TotalLBAs(p.ScramSize, p.WriteOffsetBytes)
	endLBA := p.LeadinLBA + totalLBAs

	var written int64
	if p.WriteOffsetBytes > 0 {
		zeros := make([]byte, p.WriteOffsetBytes)
		if _, err := out.Write(zeros); err != nil {
			return nil, 0, 0, err
		}
		written = int64(p.WriteOffsetBytes)
	}
	skipFirst := 0
	if p.WriteOffsetBytes < 0 {
		skipFirst = -p.WriteOffsetBytes
	}

	binBuf := make([]byte, SectorSize)
	scramBuf := make([]byte, SectorSize)
	var errLBAs []int32
	var mismatchedSectors int
	var passThroughs int
	var scramCur int64

	advanceScram := func(target int64) error {
		if scram == nil || target <= scramCur {
			return nil
		}
		if _, err := io.CopyN(io.Discard, scram, target-scramCur); err != nil {
			return fmt.Errorf("advancing scram to %d: %w", target, err)
		}
		scramCur = target
		return nil
	}

	var run []byte
	var runStart int64

	for lba := p.LeadinLBA; lba < endLBA; lba++ {
		var sec [SectorSize]byte
		switch {
		case lba < LBAPregapStart:
			// leadin: zeros
		case lba < p.BinFirstLBA:
			sec = generateMode1ZeroSector(lba)
		case lba < p.BinFirstLBA+p.BinSectorCount:
			if _, err := io.ReadFull(bin, binBuf); err != nil {
				return nil, 0, 0, fmt.Errorf("reading bin LBA %d: %w", lba, err)
			}
			copy(sec[:], binBuf)
			if trackModeAt(p.Tracks, lba) != "AUDIO" {
				// Mirror redumper's Scrambler::descramble() decision
				// (cd/cd_scrambler.ixx:23-61). Scramble bin only when it
				// holds the descrambled form ("pass" sectors). For "fail"
				// sectors, .bin == .scram for the LBA — passing through
				// preserves the original disc bytes without an override.
				if classifyBinSector(sec[:], lba) {
					Scramble(&sec)
				} else {
					passThroughs++
				}
			}
		default:
			sec = generateLeadoutSector(lba)
		}

		secBytes := sec[:]
		// skipFirst can exceed one sector (offsets up to ±4704 = 2 sectors).
		// Drain whole sectors here: bin's io.ReadFull above advances in
		// lockstep with lba, scram is read lazily via advanceScram, and no
		// run is open yet because we're still in the leadin-drain phase.
		if skipFirst >= len(sec) {
			skipFirst -= len(sec)
			continue
		}
		if skipFirst > 0 {
			secBytes = secBytes[skipFirst:]
			skipFirst = 0
		}
		remain := p.ScramSize - written
		if int64(len(secBytes)) > remain {
			secBytes = secBytes[:remain]
		}
		hatStart := written
		if _, err := out.Write(secBytes); err != nil {
			return nil, 0, 0, err
		}
		written += int64(len(secBytes))

		if scram != nil {
			if err := advanceScram(hatStart); err != nil {
				return nil, 0, 0, err
			}
			if _, err := io.ReadFull(scram, scramBuf[:len(secBytes)]); err != nil {
				return nil, 0, 0, fmt.Errorf("reading scram at %d: %w", hatStart, err)
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
					if onMismatch != nil {
						onMismatch(runStart, run)
					}
					run = run[:0]
				}
			}
			if sectorMismatch {
				mismatchedSectors++
				if len(errLBAs) < errorSectorsListCap {
					errLBAs = append(errLBAs, lba)
				}
			}
		}
		if written >= p.ScramSize {
			break
		}
	}
	if scram != nil && len(run) > 0 && onMismatch != nil {
		onMismatch(runStart, run)
	}

	return errLBAs, mismatchedSectors, passThroughs, nil
}

// CheckLayoutMismatch returns *LayoutMismatchError when the mismatch
// ratio exceeds layoutMismatchAbortRatio. Callers that have a scram to
// compare against (Pack) run this; callers that don't (Unpack) skip it.
func CheckLayoutMismatch(errLBAs []int32, mismatchedSectors int, totalDiscSectors int32) error {
	if totalDiscSectors <= 0 {
		return nil
	}
	ratio := float64(mismatchedSectors) / float64(totalDiscSectors)
	if ratio <= layoutMismatchAbortRatio {
		return nil
	}
	head := errLBAs
	if len(head) > 10 {
		head = head[:10]
	}
	return &LayoutMismatchError{
		BinSectors:    totalDiscSectors,
		ErrorSectors:  head,
		MismatchRatio: ratio,
	}
}
