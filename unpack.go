package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Sentinel errors. See pack.go for the rationale.
var (
	errBinHashMismatch    = errors.New("bin hash mismatch")
	errOutputHashMismatch = errors.New("output hash mismatch")
)

// UnpackOptions holds inputs for Unpack.
type UnpackOptions struct {
	ContainerPath string
	OutputPath    string
	Verify        bool
	Force         bool
	// BinPath optionally points unpack at a single bin to source from,
	// overriding filename-based resolution. Empty = auto-resolve.
	BinPath string
}

// Unpack reproduces the original .scram from the container's track files + delta.
func Unpack(opts UnpackOptions, r Reporter) error {
	if r == nil {
		r = quietReporter{w: io.Discard}
	}

	if !opts.Force {
		if _, err := os.Stat(opts.OutputPath); err == nil {
			return fmt.Errorf("output %s already exists (pass -f / --force to overwrite)", opts.OutputPath)
		}
	}

	st := r.Step("reading container " + opts.ContainerPath)
	m, delta, err := ReadContainer(opts.ContainerPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("delta %d bytes", len(delta))

	st = r.Step("resolving bin files")
	containerDir := filepath.Dir(opts.ContainerPath)
	baseDir, files, err := resolveBinSource(containerDir, opts.BinPath, m.Tracks)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%d file(s), %d track(s)", len(files), len(m.Tracks))

	st = r.Step("verifying bin hashes")
	perTrack, err := hashTracks(baseDir, m.Tracks)
	if err != nil {
		st.Fail(err)
		return err
	}
	for i, got := range perTrack {
		want := m.Tracks[i].Hashes
		if cmpErr := compareHashes(got, want); cmpErr != nil {
			err := fmt.Errorf("%w: track %d (%s): %v", errBinHashMismatch, m.Tracks[i].Number, m.Tracks[i].Filename, cmpErr)
			st.Fail(err)
			return err
		}
	}
	st.Done("all tracks match")

	// Build the scram prediction (ε̂ in Hauenstein's notation) to a
	// tempfile next to the output path.
	st = r.Step("building scram prediction")
	hatFile, err := os.CreateTemp(filepath.Dir(opts.OutputPath), "miniscram-unpack-hat-*")
	if err != nil {
		st.Fail(err)
		return err
	}
	hatPath := hatFile.Name()
	defer os.Remove(hatPath)
	binReader, closeBin, err := OpenBinStream(files)
	if err != nil {
		hatFile.Close()
		st.Fail(err)
		return err
	}
	params := BuildParams{
		LeadinLBA:        m.LeadinLBA,
		WriteOffsetBytes: m.WriteOffsetBytes,
		ScramSize:        m.Scram.Size,
		BinFirstLBA:      m.BinFirstLBA(),
		BinSectorCount:   m.BinSectorCount(),
		Tracks:           m.Tracks,
		SessionGaps:      derivedSessionGaps(m.Tracks),
	}
	if _, _, _, err := BuildEpsilonHat(hatFile, params, binReader, nil, nil); err != nil {
		closeBin()
		hatFile.Close()
		st.Fail(err)
		return err
	}
	closeBin()
	if err := hatFile.Sync(); err != nil {
		hatFile.Close()
		st.Fail(err)
		return err
	}
	hatFile.Close()
	st.Done("%d sector(s)", TotalLBAs(m.Scram.Size, m.WriteOffsetBytes))

	// Move the scram prediction file into place at OutputPath.
	if err := os.Rename(hatPath, opts.OutputPath); err != nil {
		hatF, oerr := os.Open(hatPath)
		if oerr != nil {
			return oerr
		}
		outF, oerr := os.Create(opts.OutputPath)
		if oerr != nil {
			hatF.Close()
			return oerr
		}
		_, cerr := io.Copy(outF, hatF)
		hatF.Close()
		outF.Close()
		os.Remove(hatPath)
		if cerr != nil {
			return cerr
		}
	}

	// Apply delta in-place.
	st = r.Step("applying delta")
	outFile, err := os.OpenFile(opts.OutputPath, os.O_RDWR, 0)
	if err != nil {
		st.Fail(err)
		return err
	}
	if err := ApplyDelta(outFile, bytes.NewReader(delta)); err != nil {
		outFile.Close()
		st.Fail(err)
		return err
	}
	if err := outFile.Sync(); err != nil {
		outFile.Close()
		st.Fail(err)
		return err
	}
	outFile.Close()
	st.Done("%d byte(s) of delta applied", len(delta))

	// Verify recovered scram hashes (unless caller opts out — the Verify
	// subcommand does this to avoid double-hashing the rebuilt scram).
	if !opts.Verify {
		return nil
	}
	wantOut := m.Scram.Hashes
	return runStep(r, "verifying output hashes", func() (string, error) {
		outHashes, err := hashFile(opts.OutputPath)
		if err != nil {
			return "", err
		}
		if cmpErr := compareHashes(outHashes, wantOut); cmpErr != nil {
			_ = os.Remove(opts.OutputPath)
			return "", fmt.Errorf("%w: %v", errOutputHashMismatch, cmpErr)
		}
		return "all three match", nil
	})
}

// resolveBinSource maps the manifest's tracks onto bin bytes available on
// disk, by content rather than by the exact filenames the container
// recorded. It rewrites each track's Filename/FileOffset to point at the
// resolved source and returns the baseDir to hash from plus the deduped
// files to stream, in track order.
//
// Precedence:
//  1. binPath != "" — that single file is the byte source (explicit override).
//  2. every recorded Filename exists in dir — use them as-is (the Redumper
//     layout; identical to the prior filename-based behavior).
//  3. exactly one *.bin in dir — that single file is the byte source.
//
// Otherwise an error describing what was found.
//
// A single-file source (cases 1 and 3) lays tracks out by cumulative size:
// track k spans [Σ sizes[0..k-1], +sizes[k]). The size precheck here is
// cheap; the caller still hash-verifies every track's range, so a wrong or
// shifted source fails loudly rather than producing a bad scram.
func resolveBinSource(dir, binPath string, tracks []Track) (string, []ResolvedFile, error) {
	var total int64
	for i := range tracks {
		total += tracks[i].Size
	}

	// Map every track onto one combined file by cumulative offset.
	useSingle := func(path string) (string, []ResolvedFile, error) {
		info, err := os.Stat(path)
		if err != nil {
			return "", nil, err
		}
		if info.Size() != total {
			return "", nil, fmt.Errorf("%w: bin %s is %d bytes, manifest expects %d",
				errBinHashMismatch, path, info.Size(), total)
		}
		base := filepath.Base(path)
		var off int64
		for i := range tracks {
			tracks[i].Filename = base
			tracks[i].FileOffset = off
			off += tracks[i].Size
		}
		return filepath.Dir(path), []ResolvedFile{{Path: path, Size: info.Size()}}, nil
	}

	// 1. Explicit --bin overrides everything.
	if binPath != "" {
		return useSingle(binPath)
	}

	// 2. Named files all present → use them as recorded.
	allPresent := true
	for i := range tracks {
		if _, err := os.Stat(filepath.Join(dir, tracks[i].Filename)); err != nil {
			allPresent = false
			break
		}
	}
	if allPresent {
		assignFileOffsets(tracks)
		fileTotals := map[string]int64{}
		for _, tr := range tracks {
			fileTotals[tr.Filename] += tr.Size
		}
		var files []ResolvedFile
		seen := map[string]bool{}
		for _, tr := range tracks {
			if seen[tr.Filename] {
				continue
			}
			seen[tr.Filename] = true
			path := filepath.Join(dir, tr.Filename)
			info, err := os.Stat(path)
			if err != nil {
				return "", nil, fmt.Errorf("track %d (%s): %w", tr.Number, tr.Filename, err)
			}
			if info.Size() != fileTotals[tr.Filename] {
				return "", nil, fmt.Errorf("%w: %s size on disk %d != manifest total %d",
					errBinHashMismatch, tr.Filename, info.Size(), fileTotals[tr.Filename])
			}
			files = append(files, ResolvedFile{Path: path, Size: info.Size()})
		}
		return dir, files, nil
	}

	// 3. Exactly one *.bin in dir → use it.
	bins, _ := filepath.Glob(filepath.Join(dir, "*.bin"))
	if len(bins) == 1 {
		return useSingle(bins[0])
	}
	if len(bins) == 0 {
		return "", nil, fmt.Errorf("cannot find bin data: the manifest's track files are absent from %s and no .bin is present; pass --bin <path>", dir)
	}
	return "", nil, fmt.Errorf("cannot resolve bin data: the manifest's track files are absent from %s and %d .bin files are present; pass --bin <path> to choose", dir, len(bins))
}

// VerifyOptions holds inputs for Verify.
type VerifyOptions struct {
	ContainerPath string
}

// Verify performs a non-destructive integrity check: rebuild the
// recovered .scram into a temp file, hash it, compare against
// manifest scram hashes, then delete the temp file. Returns
// errBinHashMismatch on track hash mismatch (via Unpack),
// errOutputHashMismatch on scram hash mismatch, or any I/O error
// encountered along the way.
func Verify(opts VerifyOptions, r Reporter) error {
	if r == nil {
		r = quietReporter{w: io.Discard}
	}

	// Read the manifest up front so we have scram hashes for the final
	// compare. ReadContainer is called again inside Unpack but the
	// manifest is small (KiB) and re-parsing is negligible.
	st := r.Step("reading manifest")
	m, _, err := ReadContainer(opts.ContainerPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%d track(s), %d byte scram", len(m.Tracks), m.Scram.Size)

	// Allocate a tempfile next to the container. The rebuild produces
	// a scram-sized file (often hundreds of MB); the container's
	// filesystem already accommodated similar artifacts at pack time.
	tmp, err := os.CreateTemp(filepath.Dir(opts.ContainerPath), "miniscram-verify-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	// Reuse the unpack path: scramble-test, ReadContainer, bin hash
	// check, BuildEpsilonHat, ApplyDelta. Verify=false skips Unpack's
	// own final hash; Force=true allows writing into the tempfile we
	// just created.
	if err := Unpack(UnpackOptions{
		ContainerPath: opts.ContainerPath,
		OutputPath:    tmpPath,
		Verify:        false,
		Force:         true,
	}, r); err != nil {
		return err
	}

	wantHashes := m.Scram.Hashes
	return runStep(r, "verifying scram hashes", func() (string, error) {
		got, err := hashFile(tmpPath)
		if err != nil {
			return "", err
		}
		if cmpErr := compareHashes(got, wantHashes); cmpErr != nil {
			return "", fmt.Errorf("%w: %v", errOutputHashMismatch, cmpErr)
		}
		return "all three match", nil
	})
}
