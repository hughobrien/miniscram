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
	ContainerPath         string
	OutputPath            string
	Verify                bool
	Force                 bool
	SuppressVerifyWarning bool // skip the "verification skipped" Warn; for callers that perform their own verification (e.g. Verify)
}

// Unpack reproduces the original .scram from the container's track files + delta.
func Unpack(opts UnpackOptions, r Reporter) error {
	if r == nil {
		r = quietReporter{w: io.Discard}
	}

	if err := runStep(r, "running scramble-table self-test", func() (string, error) {
		if err := CheckScrambleTable(); err != nil {
			return "", err
		}
		return "ok", nil
	}); err != nil {
		return err
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

	// Resolve track files relative to the container's directory.
	containerDir := filepath.Dir(opts.ContainerPath)
	files := make([]ResolvedFile, len(m.Tracks))
	for i, tr := range m.Tracks {
		path := filepath.Join(containerDir, tr.Filename)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("track %d (%s): %w", tr.Number, tr.Filename, err)
		}
		if info.Size() != tr.Size {
			return fmt.Errorf("%w: track %d (%s) size on disk %d != manifest %d",
				errBinHashMismatch, tr.Number, tr.Filename, info.Size(), tr.Size)
		}
		files[i] = ResolvedFile{Path: path, Size: tr.Size}
	}

	st = r.Step("verifying bin hashes")
	perTrack, err := hashTrackFiles(files)
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
	st.Done("ok")

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

	// Verify recovered scram hashes (unless skipped).
	if !opts.Verify {
		if !opts.SuppressVerifyWarning {
			r.Warn("verification skipped (--no-verify)")
		}
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
	st.Done("ok")

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
		ContainerPath:         opts.ContainerPath,
		OutputPath:            tmpPath,
		Verify:                false,
		Force:                 true,
		SuppressVerifyWarning: true,
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
