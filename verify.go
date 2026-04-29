package main

import (
	"fmt"
	"os"
	"path/filepath"
)

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
		r = quietReporter{}
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

	st = r.Step("verifying scram hashes")
	got, err := hashFile(tmpPath)
	if err != nil {
		st.Fail(err)
		return err
	}
	wantHashes := FileHashes{MD5: m.ScramMD5, SHA1: m.ScramSHA1, SHA256: m.ScramSHA256}
	if cmpErr := compareHashes(got, wantHashes); cmpErr != nil {
		err := fmt.Errorf("%w: %v", errOutputHashMismatch, cmpErr)
		st.Fail(err)
		return err
	}
	st.Done("all three match")
	return nil
}
