// /home/hugh/miniscram/unpack.go
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// UnpackOptions holds inputs for Unpack.
type UnpackOptions struct {
	BinPath       string
	ContainerPath string
	OutputPath    string
	Verify        bool
	Force         bool
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

	// 1. verify bin sha256
	st = r.Step("verifying bin sha256")
	binSHA, err := sha256File(opts.BinPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	if binSHA != m.BinSHA256 {
		err := fmt.Errorf("bin sha256 mismatch: got %s, manifest expects %s", binSHA, m.BinSHA256)
		st.Fail(err)
		return err
	}
	st.Done("matches")

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

	// 3. write delta to a temp file (xdelta3 -d needs a real file)
	deltaFile, err := os.CreateTemp("", "miniscram-unpack-delta-*")
	if err != nil {
		return err
	}
	deltaPath := deltaFile.Name()
	defer os.Remove(deltaPath)
	if _, err := deltaFile.Write(delta); err != nil {
		deltaFile.Close()
		return err
	}
	if err := deltaFile.Close(); err != nil {
		return err
	}

	// 4. run xdelta3 -d
	st = r.Step("running xdelta3 -d")
	if err := XDelta3Decode(hatPath, deltaPath, opts.OutputPath); err != nil {
		st.Fail(err)
		return err
	}
	st.Done("wrote %s", opts.OutputPath)

	// 5. verify output sha256
	if !opts.Verify {
		r.Warn("verification skipped (--no-verify)")
		return nil
	}
	st = r.Step("verifying output sha256")
	outSHA, err := sha256File(opts.OutputPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	if outSHA != m.ScramSHA256 {
		_ = os.Remove(opts.OutputPath)
		err := fmt.Errorf("output sha256 %s != manifest %s", outSHA, m.ScramSHA256)
		st.Fail(err)
		return err
	}
	st.Done("matches")
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

// ensure-the-import: bytes is sometimes pulled by future edits.
var _ = bytes.Equal
var _ io.Writer = io.Discard
