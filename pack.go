// /home/hugh/miniscram/pack.go
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// toolVersion is reported in the manifest. Bump in lockstep with
// container or behaviour changes.
const toolVersion = "miniscram 0.1.0"

// Sentinel errors so the CLI can map error classes to exit codes
// without resorting to substring matching on the message.
var (
	errVerifyMismatch = errors.New("round-trip verification failed")
)

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
	if err := checkConstantOffset(opts.ScramPath, scramSize); err != nil {
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

	// 6. build ε̂ + delta in one pass
	st = r.Step("building ε̂ + delta")
	hatPath, deltaPath, errSectors, deltaSize, err := buildHatAndDelta(opts, tracks, scramSize, writeOffsetBytes, binSectors)
	if err != nil {
		st.Fail(err)
		return err
	}
	defer os.Remove(deltaPath)
	hatRemoved := false
	defer func() {
		if !hatRemoved {
			_ = os.Remove(hatPath)
		}
	}()
	// Free ε̂ now; verify will rebuild it.
	if err := os.Remove(hatPath); err == nil {
		hatRemoved = true
	}
	st.Done("%d override(s), delta %d bytes", len(errSectors), deltaSize)

	// 7. assemble manifest and write container
	m := &Manifest{
		FormatVersion:        2,
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
		DeltaSize:            deltaSize,
		ScramblerTableSHA256: expectedScrambleTableSHA256,
	}

	st = r.Step("writing container")
	deltaF, err := os.Open(deltaPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	if err := WriteContainer(opts.OutputPath, m, deltaF); err != nil {
		deltaF.Close()
		st.Fail(err)
		return err
	}
	deltaF.Close()
	st.Done("%s", opts.OutputPath)

	// 8. verify by round-tripping (unless --no-verify)
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

// detectWriteOffset finds the first scrambled-sync field anywhere in
// the scram file, descrambles its BCD MSF header to recover the LBA
// that sector represents, and computes the implied write offset.
//
// This works for both real Redumper input (where the first sync lands
// at LBA -150 because Mode 1 zero pregap sectors keep their sync field
// after scrambling) and the synthetic test fixture (where the first
// sync lands at LBA 0 because synthDisc emits raw scrambleTable bytes
// for pregap, which has zero in the sync slots).
func detectWriteOffset(scramPath string, leadinLBA int32) (int, error) {
	f, err := os.Open(scramPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	const (
		chunkSize = 128 * 1024
		maxScan   = 200 * 1024 * 1024
	)
	limit := info.Size()
	if limit > maxScan {
		limit = maxScan
	}

	syncOffset := int64(-1)
	chunk := make([]byte, chunkSize)
	carry := make([]byte, 0, SyncLen-1)
	pos := int64(0)
	for pos < limit {
		readLen := int64(chunkSize)
		if pos+readLen > limit {
			readLen = limit - pos
		}
		n, err := f.ReadAt(chunk[:readLen], pos)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if n == 0 {
			break
		}
		// Stitch the carry from the previous chunk to catch syncs
		// straddling a chunk boundary.
		var search []byte
		if len(carry) > 0 {
			search = append(carry, chunk[:n]...)
		} else {
			search = chunk[:n]
		}
		idx := bytes.Index(search, Sync[:])
		if idx >= 0 {
			syncOffset = pos - int64(len(carry)) + int64(idx)
			break
		}
		// Save the tail in case Sync straddles the boundary.
		tailStart := n - (SyncLen - 1)
		if tailStart < 0 {
			tailStart = 0
		}
		carry = append(carry[:0], chunk[tailStart:n]...)
		pos += int64(n)
	}
	if syncOffset < 0 {
		return 0, fmt.Errorf("no scrambled sync field found in first %d bytes of scram", limit)
	}
	// Descramble bytes 12..14 of the candidate sector to read the BCD MSF.
	var header [3]byte
	if _, err := f.ReadAt(header[:], syncOffset+12); err != nil {
		return 0, fmt.Errorf("reading header at sync %d: %w", syncOffset, err)
	}
	for i := 0; i < 3; i++ {
		header[i] ^= scrambleTable[12+i]
	}
	decodedLBA := BCDMSFToLBA(header)
	if decodedLBA < leadinLBA || decodedLBA > 500_000 {
		return 0, fmt.Errorf("first sync's MSF header decodes to implausible LBA %d", decodedLBA)
	}
	expectedAt := int64(decodedLBA-leadinLBA) * SectorSize
	writeOffset := int(syncOffset - expectedAt)
	if writeOffset%4 != 0 {
		return 0, fmt.Errorf("auto-detected write offset %d is not sample-aligned", writeOffset)
	}
	// Real-world write offsets are typically within ±1 sector. ±2
	// sectors leaves headroom for unusual but valid discs.
	const writeOffsetLimit = 2 * SectorSize
	if writeOffset > writeOffsetLimit || writeOffset < -writeOffsetLimit {
		return 0, fmt.Errorf("auto-detected write offset %d is implausibly large (>%d bytes)", writeOffset, writeOffsetLimit)
	}
	return writeOffset, nil
}

// checkConstantOffset samples sync positions near the start, middle,
// and end of the scram file and confirms they all share the same
// (offset % SectorSize) value. A drift here would indicate a
// variable-offset disc, which miniscram doesn't support.
func checkConstantOffset(scramPath string, scramSize int64) error {
	f, err := os.Open(scramPath)
	if err != nil {
		return err
	}
	defer f.Close()
	// findSyncFrom searches forward from startAt (up to the end of the
	// file) for the first sync field. It reads in 128 KB chunks to
	// avoid loading the whole file.
	findSyncFrom := func(startAt int64) (int64, error) {
		const chunkSize = 128 * 1024
		pos := startAt
		for pos < scramSize {
			readLen := int64(chunkSize)
			if pos+readLen > scramSize {
				readLen = scramSize - pos
			}
			buf := make([]byte, readLen)
			if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
				return 0, err
			}
			idx := bytes.Index(buf, Sync[:])
			if idx >= 0 {
				return pos + int64(idx), nil
			}
			// advance by chunkSize - SyncLen+1 to not straddle a boundary
			pos += readLen - int64(SyncLen) + 1
			if readLen < int64(SyncLen) {
				break
			}
		}
		return 0, fmt.Errorf("no sync field found from offset %d", startAt)
	}
	if scramSize < 8*SectorSize {
		return nil // file too small to bother
	}
	// Collect at most 3 sync positions spread across the file. We start
	// the search at 0, scramSize/2, and scramSize*3/4 so that we always
	// find syncs even when a large pregap occupies the first half of
	// the file.
	starts := []int64{0, scramSize / 2, scramSize * 3 / 4}
	var mods []int64
	for _, s := range starts {
		off, err := findSyncFrom(s)
		if err != nil {
			return err
		}
		mods = append(mods, off%int64(SectorSize))
	}
	for i := 1; i < len(mods); i++ {
		if mods[i] != mods[0] {
			return fmt.Errorf("variable write offset detected (sync mod %d at sample %d differs from %d at sample 0)",
				mods[i], i, mods[0])
		}
	}
	return nil
}

// buildHatAndDelta produces the ε̂ temp file and the delta temp file
// in one pass. Returns paths to both plus the override LBA list and
// the delta payload size in bytes.
func buildHatAndDelta(opts PackOptions, tracks []Track, scramSize int64, writeOffsetBytes int, binSectors int32) (string, string, []int32, int64, error) {
	hatFile, err := os.CreateTemp(filepath.Dir(opts.OutputPath), "miniscram-hat-*")
	if err != nil {
		return "", "", nil, 0, err
	}
	hatPath := hatFile.Name()
	deltaFile, err := os.CreateTemp(filepath.Dir(opts.OutputPath), "miniscram-delta-*")
	if err != nil {
		hatFile.Close()
		os.Remove(hatPath)
		return "", "", nil, 0, err
	}
	deltaPath := deltaFile.Name()

	binFile, err := os.Open(opts.BinPath)
	if err != nil {
		hatFile.Close()
		deltaFile.Close()
		os.Remove(hatPath)
		os.Remove(deltaPath)
		return "", "", nil, 0, err
	}
	defer binFile.Close()
	scramFile, err := os.Open(opts.ScramPath)
	if err != nil {
		hatFile.Close()
		deltaFile.Close()
		os.Remove(hatPath)
		os.Remove(deltaPath)
		return "", "", nil, 0, err
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
	_, errs, err := BuildEpsilonHatAndDelta(hatFile, deltaFile, params, binFile, scramFile)
	hatFile.Sync()
	deltaFile.Sync()
	hatFile.Close()
	deltaFile.Close()
	if err != nil {
		os.Remove(hatPath)
		os.Remove(deltaPath)
		return "", "", nil, 0, err
	}
	deltaInfo, err := os.Stat(deltaPath)
	if err != nil {
		os.Remove(hatPath)
		os.Remove(deltaPath)
		return "", "", nil, 0, err
	}
	return hatPath, deltaPath, errs, deltaInfo.Size(), nil
}

func verifyRoundTrip(containerPath, binPath string, want *Manifest) error {
	// Recovered .scram is the same size as the original — keep it next
	// to the container output so we don't fill /tmp.
	tmpOut, err := os.CreateTemp(filepath.Dir(containerPath), "miniscram-verify-*")
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
		return fmt.Errorf("%w: round-trip sha256 %s != recorded %s", errVerifyMismatch, got, want.ScramSHA256)
	}
	return nil
}
