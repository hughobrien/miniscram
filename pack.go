// /home/hugh/miniscram/pack.go
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

// toolVersion is reported in the manifest. Bump in lockstep with
// container or behaviour changes.
const toolVersion = "miniscram 0.1.0"

// PackOptions captures everything Pack needs. Defaults match the spec
// (Verify on, LeadinLBA = LBALeadinStart). Fields without a comment
// match the obvious thing.
type PackOptions struct {
	BinPath    string
	CuePath    string
	ScramPath  string
	OutputPath string
	LeadinLBA  int32 // 0 → use LBALeadinStart
	Verify     bool
}

// Pack produces a .miniscram container at OutputPath. It does not
// remove the source on its own — that is the caller's job in main.go,
// gated on the verification result and CLI flags.
func Pack(opts PackOptions, r Reporter) error {
	if opts.LeadinLBA == 0 {
		opts.LeadinLBA = LBALeadinStart
	}
	if r == nil {
		r = quietReporter{}
	}

	st := r.Step("running scramble-table self-test")
	if err := CheckScrambleTable(); err != nil {
		st.Fail(err)
		return err
	}
	st.Done("ok")

	// 1. parse cue
	st = r.Step("parsing " + opts.CuePath)
	cueFile, err := os.Open(opts.CuePath)
	if err != nil {
		st.Fail(err)
		return err
	}
	tracks, err := ParseCue(cueFile)
	cueFile.Close()
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%d track(s)", len(tracks))

	// 2. stat scram
	scramInfo, err := os.Stat(opts.ScramPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", opts.ScramPath, err)
	}
	scramSize := scramInfo.Size()

	// stat bin
	binInfo, err := os.Stat(opts.BinPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", opts.BinPath, err)
	}
	binSize := binInfo.Size()
	if binSize%SectorSize != 0 {
		return fmt.Errorf("bin size %d is not a multiple of %d", binSize, SectorSize)
	}
	binSectors := int32(binSize / SectorSize)

	// 3. auto-detect write offset
	st = r.Step("auto-detecting write offset")
	writeOffsetBytes, err := detectWriteOffset(opts.ScramPath, opts.LeadinLBA)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%d bytes", writeOffsetBytes)

	// 4. constant-offset check
	st = r.Step("checking constant offset")
	if err := checkConstantOffset(opts.ScramPath, scramSize, opts.LeadinLBA, writeOffsetBytes); err != nil {
		st.Fail(err)
		return err
	}
	st.Done("ok")

	// 5. hash bin and scram
	st = r.Step("hashing bin")
	binSHA, err := sha256File(opts.BinPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%s", binSHA[:12])

	st = r.Step("hashing scram")
	scramSHA, err := sha256File(opts.ScramPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%s", scramSHA[:12])

	// 6. build ε̂ and run lockstep pre-check
	st = r.Step("building ε̂ + lockstep pre-check")
	hatPath, errSectors, err := buildHatTempFile(opts, tracks, scramSize, writeOffsetBytes, binSectors)
	if err != nil {
		st.Fail(err)
		return err
	}
	defer os.Remove(hatPath)
	st.Done("%d sector(s) differ", len(errSectors))

	// 7. run xdelta3 -e
	st = r.Step("running xdelta3 -e")
	deltaPath := hatPath + ".delta"
	if err := XDelta3Encode(hatPath, opts.ScramPath, deltaPath, scramSize); err != nil {
		st.Fail(err)
		return err
	}
	defer os.Remove(deltaPath)
	deltaInfo, err := os.Stat(deltaPath)
	if err != nil {
		return err
	}
	st.Done("%d bytes", deltaInfo.Size())

	// 8. assemble manifest and write container
	m := &Manifest{
		FormatVersion:        1,
		ToolVersion:          toolVersion + " (" + runtime.Version() + ")",
		CreatedUTC:           time.Now().UTC().Format(time.RFC3339),
		ScramSize:            scramSize,
		ScramSHA256:          scramSHA,
		BinSize:              binSize,
		BinSHA256:            binSHA,
		WriteOffsetBytes:     writeOffsetBytes,
		LeadinLBA:            opts.LeadinLBA,
		Tracks:               tracks,
		BinFirstLBA:          tracks[0].FirstLBA,
		BinSectorCount:       binSectors,
		ErrorSectors:         errSectors,
		ErrorSectorCount:     len(errSectors),
		DeltaSize:            deltaInfo.Size(),
		ScramblerTableSHA256: expectedScrambleTableSHA256,
	}

	st = r.Step("writing container")
	deltaFile, err := os.Open(deltaPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	if err := WriteContainer(opts.OutputPath, m, deltaFile); err != nil {
		deltaFile.Close()
		st.Fail(err)
		return err
	}
	deltaFile.Close()
	st.Done("%s", opts.OutputPath)

	// 9. verify by round-tripping (unless --no-verify)
	if !opts.Verify {
		r.Warn("verification skipped (--no-verify)")
		return nil
	}
	st = r.Step("verifying round-trip")
	if err := verifyRoundTrip(opts.OutputPath, opts.BinPath, m); err != nil {
		st.Fail(err)
		_ = os.Remove(opts.OutputPath)
		return err
	}
	st.Done("ok")
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// detectWriteOffset finds the first scrambled-sync field in the scram
// file beyond the leadin region and returns the implied write offset
// in bytes. It also descrambles the candidate sync's MSF header and
// rejects the result if the LBA is not LBAPregapStart (-150).
func detectWriteOffset(scramPath string, leadinLBA int32) (int, error) {
	f, err := os.Open(scramPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// search a window around the expected pregap location
	expectedAt := int64(LBAPregapStart-leadinLBA) * SectorSize
	const windowBytes = 64 * 1024
	startAt := expectedAt - windowBytes
	if startAt < 0 {
		startAt = 0
	}
	if _, err := f.Seek(startAt, io.SeekStart); err != nil {
		return 0, err
	}
	buf := make([]byte, 4*windowBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return 0, err
	}
	syncIdx := -1
	for i := 0; i+SyncLen <= n; i++ {
		ok := true
		for j := 0; j < SyncLen; j++ {
			if buf[i+j] != Sync[j] {
				ok = false
				break
			}
		}
		if ok {
			syncIdx = i
			break
		}
	}
	if syncIdx < 0 {
		return 0, errors.New("no scrambled sync field found in expected window")
	}
	syncOffset := startAt + int64(syncIdx)
	writeOffset := int(syncOffset - expectedAt)
	if writeOffset%4 != 0 {
		return 0, fmt.Errorf("auto-detected write offset %d is not sample-aligned", writeOffset)
	}
	if writeOffset > 10*SectorSize || writeOffset < -10*SectorSize {
		return 0, fmt.Errorf("auto-detected write offset %d is implausibly large", writeOffset)
	}
	// descramble the candidate sync's BCD MSF header (bytes 12..14 of the sector).
	header := [4]byte{}
	if _, err := f.ReadAt(header[:], syncOffset+12); err != nil {
		return 0, err
	}
	for i := 0; i < 4; i++ {
		header[i] ^= scrambleTable[12+i]
	}
	if BCDMSFToLBA([3]byte{header[0], header[1], header[2]}) != LBAPregapStart {
		return 0, fmt.Errorf("first sync's BCD MSF header decodes to LBA != %d", LBAPregapStart)
	}
	return writeOffset, nil
}

// checkConstantOffset samples sync positions at the start, middle, and
// near-end of the data region and confirms they all share the same
// (offset mod SectorSize) value relative to leadinLBA.
func checkConstantOffset(scramPath string, scramSize int64, leadinLBA int32, writeOffsetBytes int) error {
	f, err := os.Open(scramPath)
	if err != nil {
		return err
	}
	defer f.Close()
	leadinBytes := int64(LBAPregapStart-leadinLBA) * SectorSize
	dataBytes := scramSize - leadinBytes
	if dataBytes < 4*SectorSize {
		return nil // too little data to sample
	}
	expectedMod := ((leadinBytes+int64(writeOffsetBytes))%int64(SectorSize) + int64(SectorSize)) % int64(SectorSize)
	checkAt := func(off int64) error {
		buf := make([]byte, 2*SectorSize)
		if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
			return err
		}
		for i := 0; i+SyncLen <= len(buf); i++ {
			ok := true
			for j := 0; j < SyncLen; j++ {
				if buf[i+j] != Sync[j] {
					ok = false
					break
				}
			}
			if ok {
				absolute := off + int64(i)
				mod := ((absolute-leadinBytes)%int64(SectorSize) + int64(SectorSize)) % int64(SectorSize)
				if mod != expectedMod {
					return fmt.Errorf("variable write offset detected at byte %d (mod %d vs expected %d)",
						absolute, mod, expectedMod)
				}
				return nil
			}
		}
		return fmt.Errorf("no sync field near byte %d", off)
	}
	mids := []int64{leadinBytes, leadinBytes + dataBytes/2, leadinBytes + dataBytes - 4*SectorSize}
	for _, m := range mids {
		if err := checkAt(m); err != nil {
			return err
		}
	}
	return nil
}

func buildHatTempFile(opts PackOptions, tracks []Track, scramSize int64, writeOffsetBytes int, binSectors int32) (string, []int32, error) {
	hatFile, err := os.CreateTemp("", "miniscram-hat-*")
	if err != nil {
		return "", nil, err
	}
	hatPath := hatFile.Name()
	defer func() {
		_ = hatFile.Close()
	}()
	binFile, err := os.Open(opts.BinPath)
	if err != nil {
		return "", nil, err
	}
	defer binFile.Close()
	scramFile, err := os.Open(opts.ScramPath)
	if err != nil {
		return "", nil, err
	}
	defer scramFile.Close()
	params := BuildParams{
		LeadinLBA:        opts.LeadinLBA,
		WriteOffsetBytes: writeOffsetBytes,
		ScramSize:        scramSize,
		BinFirstLBA:      tracks[0].FirstLBA,
		BinSectorCount:   binSectors,
		Tracks:           tracks,
	}
	errs, err := BuildEpsilonHat(hatFile, params, binFile, scramFile)
	if err != nil {
		os.Remove(hatPath)
		return "", nil, err
	}
	if err := hatFile.Sync(); err != nil {
		return "", nil, err
	}
	return hatPath, errs, nil
}

func verifyRoundTrip(containerPath, binPath string, want *Manifest) error {
	tmpOut, err := os.CreateTemp("", "miniscram-verify-*")
	if err != nil {
		return err
	}
	tmpOutPath := tmpOut.Name()
	tmpOut.Close()
	defer os.Remove(tmpOutPath)
	if err := Unpack(UnpackOptions{
		BinPath:       binPath,
		ContainerPath: containerPath,
		OutputPath:    tmpOutPath,
		Verify:        false, // we'll hash here ourselves
		Force:         true,
	}, quietReporter{}); err != nil {
		return err
	}
	got, err := sha256File(tmpOutPath)
	if err != nil {
		return err
	}
	if got != want.ScramSHA256 {
		return fmt.Errorf("verify: round-trip sha256 %s != recorded %s", got, want.ScramSHA256)
	}
	return nil
}
