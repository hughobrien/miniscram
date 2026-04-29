// /home/hugh/miniscram/pack.go
package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// toolVersion is reported in the manifest. Bump in lockstep with
// container or behaviour changes.
const toolVersion = "miniscram 1.0.0"

// Sentinel errors so the CLI can map error classes to exit codes
// without resorting to substring matching on the message.
var (
	errVerifyMismatch = errors.New("round-trip verification failed")
)

// PackOptions captures everything Pack needs. Defaults match the spec
// (Verify on, LeadinLBA = LBALeadinStart). Fields without a comment
// match the obvious thing.
type PackOptions struct {
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

	// 1. resolve cue (parse + stat + cumulative LBAs).
	st = r.Step("resolving cue " + opts.CuePath)
	resolved, err := ResolveCue(opts.CuePath)
	if err != nil {
		st.Fail(err)
		return err
	}
	tracks := resolved.Tracks
	binSize := int64(0)
	for _, f := range resolved.Files {
		binSize += f.Size
	}
	binSectors := int32(binSize / int64(SectorSize))
	st.Done("%d track(s), %d bytes total", len(tracks), binSize)

	// 2. stat scram.
	scramInfo, err := os.Stat(opts.ScramPath)
	if err != nil {
		return err
	}
	scramSize := scramInfo.Size()

	// 3. detect write offset.
	st = r.Step("detecting write offset")
	writeOffsetBytes, err := detectWriteOffset(opts.ScramPath, opts.LeadinLBA)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%d bytes", writeOffsetBytes)

	// 4. constant-offset check.
	st = r.Step("checking constant offset")
	if err := checkConstantOffset(opts.ScramPath, scramSize, opts.LeadinLBA); err != nil {
		st.Fail(err)
		return err
	}
	st.Done("ok")

	// 5. single hashing pass over track files.
	st = r.Step("hashing tracks")
	perTrack, err := hashTrackFiles(resolved.Files)
	if err != nil {
		st.Fail(err)
		return err
	}
	for i := range tracks {
		tracks[i].Hashes = perTrack[i]
	}
	st.Done("%d track(s) hashed", len(tracks))

	st = r.Step("hashing scram")
	scramHashes, err := hashFile(opts.ScramPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%s", scramHashes.SHA256[:12])

	// 6. build ε̂ + delta in one pass over the multi-bin stream.
	st = r.Step("building ε̂ + delta")
	hatPath, deltaPath, errSectors, deltaSize, err := buildHatAndDelta(opts, resolved.Files, tracks, scramSize, writeOffsetBytes, binSectors)
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
	if err := os.Remove(hatPath); err == nil {
		hatRemoved = true
	}
	st.Done("%d override(s), delta %d bytes", len(errSectors), deltaSize)

	// 7. assemble manifest and write container.
	m := &Manifest{
		ToolVersion:      toolVersion + " (" + runtime.Version() + ")",
		CreatedUTC:       time.Now().UTC().Format(time.RFC3339),
		WriteOffsetBytes: writeOffsetBytes,
		LeadinLBA:        opts.LeadinLBA,
		Scram: ScramInfo{
			Size:   scramSize,
			Hashes: scramHashes,
		},
		Tracks: tracks,
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

	// 8. verify by round-tripping (unless --no-verify).
	if !opts.Verify {
		r.Warn("verification skipped (--no-verify)")
		return nil
	}
	st = r.Step("verifying round-trip")
	if err := verifyRoundTrip(opts.OutputPath, resolved.Files, m); err != nil {
		st.Fail(err)
		_ = os.Remove(opts.OutputPath)
		return err
	}
	st.Done("ok")
	return nil
}

// FileHashes is the {md5, sha1, sha256} triple miniscram records per
// entity. Marshalled as a nested JSON object.
type FileHashes struct {
	MD5    string `json:"md5"`
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
}

// hashReader streams r through MD5, SHA-1, and SHA-256 in a single
// pass and returns all three as lowercase hex.
func hashReader(r io.Reader) (FileHashes, error) {
	m, s1, s256 := md5.New(), sha1.New(), sha256.New()
	w := io.MultiWriter(m, s1, s256)
	if _, err := io.Copy(w, r); err != nil {
		return FileHashes{}, err
	}
	return FileHashes{
		MD5:    hex.EncodeToString(m.Sum(nil)),
		SHA1:   hex.EncodeToString(s1.Sum(nil)),
		SHA256: hex.EncodeToString(s256.Sum(nil)),
	}, nil
}

// hashFile is a thin wrapper around hashReader that opens path.
func hashFile(path string) (FileHashes, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileHashes{}, err
	}
	defer f.Close()
	return hashReader(f)
}

// hashTrackFiles streams every file once, returning per-file
// MD5/SHA-1/SHA-256 hashes in input order. Used by Pack (to populate
// the manifest) and by Unpack (to verify against the manifest's
// recorded values). Files are read sequentially; failure on any file
// aborts. Returns one FileHashes per input file, in order.
func hashTrackFiles(files []ResolvedFile) ([]FileHashes, error) {
	perFile := make([]FileHashes, len(files))
	for i, rf := range files {
		f, err := os.Open(rf.Path)
		if err != nil {
			return nil, err
		}
		m, s1, s256 := md5.New(), sha1.New(), sha256.New()
		w := io.MultiWriter(m, s1, s256)
		_, copyErr := io.Copy(w, f)
		f.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		perFile[i] = FileHashes{
			MD5:    hex.EncodeToString(m.Sum(nil)),
			SHA1:   hex.EncodeToString(s1.Sum(nil)),
			SHA256: hex.EncodeToString(s256.Sum(nil)),
		}
	}
	return perFile, nil
}

// compareHashes returns nil iff all three hashes match. Otherwise it
// returns a plain (un-sentinel-wrapped) error whose message describes
// each hash's status. Callers wrap with their own sentinel via
// fmt.Errorf("%w: %v", sentinel, err) to attach the appropriate exit
// code semantics.
func compareHashes(got, want FileHashes) error {
	var diffs []string
	if got.MD5 != want.MD5 {
		diffs = append(diffs, fmt.Sprintf("md5 got %s, manifest expects %s", got.MD5, want.MD5))
	}
	if got.SHA1 != want.SHA1 {
		diffs = append(diffs, fmt.Sprintf("sha1 got %s, manifest expects %s", got.SHA1, want.SHA1))
	}
	if got.SHA256 != want.SHA256 {
		diffs = append(diffs, fmt.Sprintf("sha256 got %s, manifest expects %s", got.SHA256, want.SHA256))
	}
	if len(diffs) == 0 {
		return nil
	}
	return errors.New(strings.Join(diffs, "; "))
}

// validateSyncCandidate reads the 3-byte MSF header at syncOff+SyncLen
// and returns the implied write offset if all checks pass:
//   - BCD-valid header bytes (each nibble ≤ 9 after descrambling)
//   - decoded LBA in [leadinLBA, 500_000]
//   - implied write offset sample-aligned (multiple of 4)
//   - implied write offset within ±2 sectors
//
// Used by both detectWriteOffset (first valid candidate wins) and
// checkConstantOffset (samples N candidates and asserts equality).
//
// Returns ok=false on any failure (bad read, BCD mismatch, etc.).
func validateSyncCandidate(f io.ReaderAt, syncOff int64, leadinLBA int32, scramSize int64) (int, bool) {
	if syncOff+int64(SyncLen)+3 > scramSize {
		return 0, false
	}
	var header [3]byte
	if _, err := f.ReadAt(header[:], syncOff+int64(SyncLen)); err != nil {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		header[i] ^= scrambleTable[SyncLen+i]
	}
	isBCD := func(b byte) bool { return (b>>4) <= 9 && (b&0x0F) <= 9 }
	if !isBCD(header[0]) || !isBCD(header[1]) || !isBCD(header[2]) {
		return 0, false
	}
	decodedLBA := BCDMSFToLBA(header)
	if decodedLBA < leadinLBA || decodedLBA > 500_000 {
		return 0, false
	}
	expectedAt := int64(decodedLBA-leadinLBA) * int64(SectorSize)
	writeOffset := int(syncOff - expectedAt)
	if writeOffset%4 != 0 {
		return 0, false
	}
	const writeOffsetLimit = 2 * SectorSize
	if writeOffset > writeOffsetLimit || writeOffset < -writeOffsetLimit {
		return 0, false
	}
	return writeOffset, true
}

// detectWriteOffset scans the scram file for sync-field candidates,
// descrambles each candidate's MSF header, and returns the implied
// write offset of the first candidate whose header is valid BCD and
// whose implied write offset is sample-aligned and within ±2 sectors.
//
// Real-world Redumper output of copy-protected discs (e.g. SafeDisc)
// can contain bytes in the leadin region that happen to match the
// 12-byte sync pattern but are not aligned to a sector boundary or
// have deliberately corrupted MSF headers. The original implementation
// took the first sync unconditionally, which produced a nonsense write
// offset for these discs. Iterating until a header decodes cleanly
// skips past those false positives without affecting the synthetic
// test fixtures (whose first sync is always at the start of a real
// sector with valid BCD).
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
		var carryLen int64
		if len(carry) > 0 {
			search = append(carry, chunk[:n]...)
			carryLen = int64(len(carry))
		} else {
			search = chunk[:n]
		}
		// Walk every sync occurrence in this buffer; first plausible
		// candidate wins.
		searchPos := 0
		for {
			idx := bytes.Index(search[searchPos:], Sync[:])
			if idx < 0 {
				break
			}
			idx += searchPos
			syncOffset := pos - carryLen + int64(idx)
			if wo, ok := validateSyncCandidate(f, syncOffset, leadinLBA, info.Size()); ok {
				return wo, nil
			}
			searchPos = idx + 1
		}
		// Save the tail in case Sync straddles the boundary.
		tailStart := n - (SyncLen - 1)
		if tailStart < 0 {
			tailStart = 0
		}
		carry = append(carry[:0], chunk[tailStart:n]...)
		pos += int64(n)
	}
	return 0, fmt.Errorf("no plausible scrambled sync field found in first %d bytes of scram", limit)
}

// checkConstantOffset samples sync positions across the scram file and
// confirms they all imply the same write offset. A drift here would
// indicate a variable-offset disc, which miniscram doesn't support.
//
// Only "real" sync candidates count: a candidate must have valid BCD
// MSF bytes after descrambling, decode to a plausible LBA, and imply
// a write offset within ±2 sectors. This filters out coincidental Sync
// byte sequences in audio regions (where there are no real scrambled
// syncs, just PCM samples that may happen to match the sync pattern
// AND have BCD-valid bytes following). Without this filter, multi-
// track discs with audio (e.g. HL1) report spurious "variable write
// offset" errors from audio coincidences.
//
// If fewer than 2 valid syncs are found across the sample anchors, the
// check is skipped (constancy can't be verified with a single sample;
// detectWriteOffset already verified the disc has a valid write offset
// at the start).
func checkConstantOffset(scramPath string, scramSize int64, leadinLBA int32) error {
	f, err := os.Open(scramPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// findValidSyncFrom searches forward from startAt for the first
	// sync that passes validateSyncCandidate. Returns (-1, nil) if none.
	findValidSyncFrom := func(startAt int64) (int, bool, error) {
		const chunkSize = 128 * 1024
		pos := startAt
		for pos < scramSize {
			readLen := int64(chunkSize)
			if pos+readLen > scramSize {
				readLen = scramSize - pos
			}
			buf := make([]byte, readLen)
			if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
				return 0, false, err
			}
			searchPos := 0
			for {
				idx := bytes.Index(buf[searchPos:], Sync[:])
				if idx < 0 {
					break
				}
				idx += searchPos
				syncOff := pos + int64(idx)
				if off, ok := validateSyncCandidate(f, syncOff, leadinLBA, scramSize); ok {
					return off, true, nil
				}
				searchPos = idx + 1
			}
			pos += readLen - int64(SyncLen) + 1
			if readLen < int64(SyncLen) {
				break
			}
		}
		return 0, false, nil
	}

	if scramSize < 8*SectorSize {
		return nil // file too small to bother
	}
	starts := []int64{0, scramSize / 2, scramSize * 3 / 4}
	var offsets []int
	for _, s := range starts {
		off, ok, err := findValidSyncFrom(s)
		if err != nil {
			return err
		}
		if !ok {
			continue // no valid sync from this anchor (e.g. anchor lands in audio)
		}
		offsets = append(offsets, off)
	}
	if len(offsets) < 2 {
		return nil // can't verify constancy with a single sample
	}
	for i := 1; i < len(offsets); i++ {
		if offsets[i] != offsets[0] {
			return fmt.Errorf("variable write offset detected (sample %d implies offset %d, sample 0 implies %d)",
				i, offsets[i], offsets[0])
		}
	}
	return nil
}

// buildHatAndDelta produces the ε̂ temp file and the delta temp file
// in one pass. Returns paths to both plus the override LBA list and
// the delta payload size in bytes.
func buildHatAndDelta(opts PackOptions, files []ResolvedFile, tracks []Track, scramSize int64, writeOffsetBytes int, binSectors int32) (string, string, []int32, int64, error) {
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

	binReader, closeBin, err := OpenBinStream(files)
	if err != nil {
		hatFile.Close()
		deltaFile.Close()
		os.Remove(hatPath)
		os.Remove(deltaPath)
		return "", "", nil, 0, err
	}
	defer closeBin()
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
	_, errs, err := BuildEpsilonHatAndDelta(hatFile, deltaFile, params, binReader, scramFile)
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

func verifyRoundTrip(containerPath string, files []ResolvedFile, want *Manifest) error {
	// Recovered .scram is the same size as the original — keep it next
	// to the container output so we don't fill /tmp.
	tmpOut, err := os.CreateTemp(filepath.Dir(containerPath), "miniscram-verify-*")
	if err != nil {
		return err
	}
	tmpOutPath := tmpOut.Name()
	tmpOut.Close()
	defer os.Remove(tmpOutPath)

	// Build ε̂ from the multi-bin stream into the tempfile.
	hatFile, err := os.OpenFile(tmpOutPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	binReader, closeBin, err := OpenBinStream(files)
	if err != nil {
		hatFile.Close()
		return err
	}
	params := BuildParams{
		LeadinLBA:        want.LeadinLBA,
		WriteOffsetBytes: want.WriteOffsetBytes,
		ScramSize:        want.Scram.Size,
		BinFirstLBA:      want.BinFirstLBA(),
		BinSectorCount:   want.BinSectorCount(),
		Tracks:           want.Tracks,
	}
	if _, err := BuildEpsilonHat(hatFile, params, binReader, nil); err != nil {
		closeBin()
		hatFile.Close()
		return err
	}
	closeBin()
	if err := hatFile.Sync(); err != nil {
		hatFile.Close()
		return err
	}
	hatFile.Close()

	// Apply delta from the container.
	_, _, deltaBytes, err := ReadContainer(containerPath)
	if err != nil {
		return err
	}
	outFile, err := os.OpenFile(tmpOutPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := ApplyDelta(outFile, bytes.NewReader(deltaBytes)); err != nil {
		outFile.Close()
		return err
	}
	if err := outFile.Sync(); err != nil {
		outFile.Close()
		return err
	}
	outFile.Close()

	// Hash the result and compare against the manifest's recorded scram hashes.
	got, err := hashFile(tmpOutPath)
	if err != nil {
		return err
	}
	wantHashes := want.Scram.Hashes
	if err := compareHashes(got, wantHashes); err != nil {
		return fmt.Errorf("%w: round-trip hash mismatch: %v", errVerifyMismatch, err)
	}
	return nil
}
