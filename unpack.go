// /home/hugh/miniscram/unpack.go
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
	BinPath               string
	ContainerPath         string
	OutputPath            string
	Verify                bool
	Force                 bool
	SuppressVerifyWarning bool // skip the "verification skipped" Warn; for callers that perform their own verification (e.g. Verify)
}

// Unpack reproduces the original .scram from <bin> + <container>.
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

	// 1. verify bin hashes
	st = r.Step("verifying bin hashes")
	binHashes, err := hashFile(opts.BinPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	wantBin := FileHashes{MD5: m.BinMD5, SHA1: m.BinSHA1, SHA256: m.BinSHA256}
	if err := compareHashes(binHashes, wantBin); err != nil {
		err := fmt.Errorf("%w: %v", errBinHashMismatch, err)
		st.Fail(err)
		return err
	}
	st.Done("all three match")

	// 2. rebuild ε̂. ε̂ is the same size as the recovered .scram (often
	// hundreds of MB), so put it next to the output rather than /tmp.
	st = r.Step("building ε̂")
	hatFile, err := os.CreateTemp(filepath.Dir(opts.OutputPath), "miniscram-unpack-hat-*")
	if err != nil {
		st.Fail(err)
		return err
	}
	hatPath := hatFile.Name()
	defer os.Remove(hatPath)
	binFile, err := os.Open(opts.BinPath)
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
	if _, err := BuildEpsilonHat(hatFile, params, binFile, nil); err != nil {
		binFile.Close()
		hatFile.Close()
		st.Fail(err)
		return err
	}
	binFile.Close()
	if err := hatFile.Sync(); err != nil {
		hatFile.Close()
		st.Fail(err)
		return err
	}
	hatFile.Close()
	st.Done("ok")

	// 3. ε̂ is now at hatPath. Rename to opts.OutputPath (or copy on cross-fs failure).
	if err := os.Rename(hatPath, opts.OutputPath); err != nil {
		hatF, oerr := os.Open(hatPath)
		if oerr != nil {
			st.Fail(oerr)
			return oerr
		}
		outF, oerr := os.Create(opts.OutputPath)
		if oerr != nil {
			hatF.Close()
			st.Fail(oerr)
			return oerr
		}
		_, cerr := io.Copy(outF, hatF)
		hatF.Close()
		outF.Close()
		os.Remove(hatPath)
		if cerr != nil {
			st.Fail(cerr)
			return cerr
		}
	}
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

	// 4. verify output hashes
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
	if err := compareHashes(outHashes, wantOut); err != nil {
		_ = os.Remove(opts.OutputPath)
		err := fmt.Errorf("%w: %v", errOutputHashMismatch, err)
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
