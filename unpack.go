// /home/hugh/miniscram/unpack.go
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
		r = quietReporter{}
	}

	st := r.Step("running scramble-table self-test")
	if err := CheckScrambleTable(); err != nil {
		st.Fail(err)
		return err
	}
	st.Done("ok")

	if !opts.Force {
		if _, err := os.Stat(opts.OutputPath); err == nil {
			return fmt.Errorf("output %s already exists (pass -f / --force to overwrite)", opts.OutputPath)
		}
	}

	st = r.Step("reading container " + opts.ContainerPath)
	m, delta, err := ReadContainer(opts.ContainerPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("manifest %d bytes, delta %d bytes", deltaJSONSize(m), len(delta))

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

	// Single hashing pass: per-track + disc-level roll-up.
	st = r.Step("verifying bin hashes")
	rollupMD5, rollupSHA1, rollupSHA256 := md5.New(), sha1.New(), sha256.New()
	rollupWriter := io.MultiWriter(rollupMD5, rollupSHA1, rollupSHA256)
	for i, rf := range files {
		f, err := os.Open(rf.Path)
		if err != nil {
			st.Fail(err)
			return err
		}
		trackMD5, trackSHA1, trackSHA256 := md5.New(), sha1.New(), sha256.New()
		w := io.MultiWriter(trackMD5, trackSHA1, trackSHA256, rollupWriter)
		if _, err := io.Copy(w, f); err != nil {
			f.Close()
			st.Fail(err)
			return err
		}
		f.Close()
		got := FileHashes{
			MD5:    hex.EncodeToString(trackMD5.Sum(nil)),
			SHA1:   hex.EncodeToString(trackSHA1.Sum(nil)),
			SHA256: hex.EncodeToString(trackSHA256.Sum(nil)),
		}
		want := FileHashes{MD5: m.Tracks[i].MD5, SHA1: m.Tracks[i].SHA1, SHA256: m.Tracks[i].SHA256}
		if cmpErr := compareHashes(got, want); cmpErr != nil {
			err := fmt.Errorf("%w: track %d (%s): %v", errBinHashMismatch, m.Tracks[i].Number, m.Tracks[i].Filename, cmpErr)
			st.Fail(err)
			return err
		}
	}
	gotRoll := FileHashes{
		MD5:    hex.EncodeToString(rollupMD5.Sum(nil)),
		SHA1:   hex.EncodeToString(rollupSHA1.Sum(nil)),
		SHA256: hex.EncodeToString(rollupSHA256.Sum(nil)),
	}
	wantRoll := FileHashes{MD5: m.BinMD5, SHA1: m.BinSHA1, SHA256: m.BinSHA256}
	if cmpErr := compareHashes(gotRoll, wantRoll); cmpErr != nil {
		err := fmt.Errorf("%w: roll-up: %v", errBinHashMismatch, cmpErr)
		st.Fail(err)
		return err
	}
	st.Done("all tracks + roll-up match")

	// Build ε̂ to a tempfile next to the output path.
	st = r.Step("building ε̂")
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
		ScramSize:        m.ScramSize,
		BinFirstLBA:      m.BinFirstLBA,
		BinSectorCount:   m.BinSectorCount,
		Tracks:           m.Tracks,
	}
	if _, err := BuildEpsilonHat(hatFile, params, binReader, nil); err != nil {
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

	// Move ε̂ into place at OutputPath.
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
	st = r.Step("verifying output hashes")
	outHashes, err := hashFile(opts.OutputPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	wantOut := FileHashes{MD5: m.ScramMD5, SHA1: m.ScramSHA1, SHA256: m.ScramSHA256}
	if cmpErr := compareHashes(outHashes, wantOut); cmpErr != nil {
		_ = os.Remove(opts.OutputPath)
		err := fmt.Errorf("%w: %v", errOutputHashMismatch, cmpErr)
		st.Fail(err)
		return err
	}
	st.Done("all three match")
	return nil
}

// deltaJSONSize returns the marshalled length of the manifest. Used
// only for the reporter line.
func deltaJSONSize(m *Manifest) int {
	body, err := m.Marshal()
	if err != nil {
		return 0
	}
	return len(body)
}
