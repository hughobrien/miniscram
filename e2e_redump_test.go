//go:build redump_data

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// realDiscFixture configures a single dataset's e2e expectations.
// Add a new entry to realDiscFixtures (below) when a new dataset is
// available. Each sub-test skips independently when its files aren't
// present, so adding a row never causes failures on machines without
// that dataset.
type realDiscFixture struct {
	Name                    string  // sub-test name, e.g. "deus-ex"
	Dir                     string  // absolute path to the dataset directory
	Stem                    string  // filename stem (no extension)
	ExpectedDataTrackErrors int32   // data-track ECC/EDC error count — the Redumper "errors count" metric. Stable signature for protection class.
	MaxDeltaBytes           int64   // assert len(delta payload) < this
	MaxContainerBytes       int64   // assert os.Stat(container).Size() < this
	EDCSampleLBAs           []int64 // LBAs to sample in TestE2EEDCAndECCRealDiscs (must be Mode 1, unprotected)
}

// realDiscFixtures is the authoritative dataset list. Keep entries
// sorted alphabetically by Name.
var realDiscFixtures = []realDiscFixture{
	{
		Name:                    "deus-ex",
		Dir:                     "test-discs/deus-ex",
		Stem:                    "DeusEx_v1002f",
		ExpectedDataTrackErrors: 0,
		MaxDeltaBytes:           1024,
		MaxContainerBytes:       2048,
		EDCSampleLBAs:           []int64{0, 100, 1000, 100000},
	},
	{
		Name: "freelancer",
		Dir:  "test-discs/freelancer",
		Stem: "FL_v1",
		// SafeDisc 2.70.030; per redump.org submission, 588 deliberately
		// corrupted sectors. Round-trip byte-equality plus this exact
		// count proves miniscram captures the protection losslessly.
		ExpectedDataTrackErrors: 588,
		MaxDeltaBytes:           15 * 1024 * 1024,
		MaxContainerBytes:       15 * 1024 * 1024,
		// SafeDisc protection clusters near end-of-disc; LBAs in the
		// first 100k are well clear of it.
		EDCSampleLBAs: []int64{0, 100, 1000, 100000},
	},
	{
		Name: "half-life",
		Dir:  "test-discs/half-life",
		Stem: "HALFLIFE",
		// Clean retail disc, 1 Mode 1 data track + 27 audio tracks.
		// Multi-FILE cue (one .bin per track). 0 ECC/EDC errors per
		// redump.org. Real-world dump has ~5 MB delta from lead-in
		// noise + boundary sectors (same shape as Freelancer's lead-in
		// contribution, minus the SafeDisc protection sectors).
		ExpectedDataTrackErrors: 0,
		MaxDeltaBytes:           15 * 1024 * 1024,
		MaxContainerBytes:       15 * 1024 * 1024,
		// Track 01 spans LBAs 0..135010 (Mode 1); sample within that range.
		EDCSampleLBAs: []int64{0, 100, 1000, 100000},
	},
}

// fixturePresent reports whether the .cue and .scram for a fixture
// exist on disk. The .bin files referenced by the cue are not checked
// here (multi-FILE cues have no top-level .bin); ResolveCue will fail
// fast at pack time if any FILE entry is missing.
func fixturePresent(f realDiscFixture) bool {
	for _, ext := range []string{".cue", ".scram"} {
		if _, err := os.Stat(filepath.Join(f.Dir, f.Stem+ext)); err != nil {
			return false
		}
	}
	return true
}

// TestE2ERoundTripRealDiscs runs Pack → ReadContainer → Unpack against
// each configured fixture, asserts per-fixture bounds, and confirms
// the recovered .scram is byte-equal to the original.
func TestE2ERoundTripRealDiscs(t *testing.T) {
	for _, f := range realDiscFixtures {
		f := f // capture for the closure
		t.Run(f.Name, func(t *testing.T) {
			if !fixturePresent(f) {
				t.Skipf("dataset not present at %s", f.Dir)
			}
			cuePath := filepath.Join(f.Dir, f.Stem+".cue")
			scramPath := filepath.Join(f.Dir, f.Stem+".scram")

			// Use a temp dir on the same filesystem as the dataset to
			// avoid /tmp overflow (the test produces ~scram-sized
			// recovered files — hundreds of MB).
			tmp, err := os.MkdirTemp(f.Dir, "miniscram-e2e-*")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(tmp) })

			// Container goes in the dataset directory (next to the
			// per-track .bin files, so Unpack/Verify can find them by
			// the manifest's Track.Filename in the container's dir).
			// Cleaned up explicitly below.
			containerPath := filepath.Join(f.Dir, f.Stem+".miniscram")
			t.Cleanup(func() { os.Remove(containerPath) })
			rep := NewReporter(io.Discard, true)

			if err := Pack(PackOptions{
				CuePath:    cuePath,
				ScramPath:  scramPath,
				OutputPath: containerPath,
				Verify:     true,
			}, rep); err != nil {
				t.Fatalf("Pack: %v", err)
			}

			m, delta, err := ReadContainer(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			// Exact assertion on data-track ECC/EDC error count. This
			// matches Redumper's "errors count" definition and is a
			// stable signature for the protection class. Walks per-track
			// .bin files via the manifest so it works for both single-
			// and multi-FILE cues.
			gotDataErrs := countDataTrackErrors(t, f.Dir, m.Tracks)
			if int32(gotDataErrs) != f.ExpectedDataTrackErrors {
				t.Errorf("data-track error count = %d; expected %d (Redumper-style metric)",
					gotDataErrs, f.ExpectedDataTrackErrors)
			}
			// Note: delta size (the delta-override byte count) varies
			// per dump because it includes lead-in noise; do not assert
			// on it here. Byte-equal round-trip below covers preservation.
			if int64(len(delta)) >= f.MaxDeltaBytes {
				t.Errorf("delta is %d bytes; expected < %d", len(delta), f.MaxDeltaBytes)
			}
			containerInfo, err := os.Stat(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			if containerInfo.Size() >= f.MaxContainerBytes {
				t.Errorf(".miniscram is %d bytes; expected < %d", containerInfo.Size(), f.MaxContainerBytes)
			}

			outPath := filepath.Join(tmp, f.Stem+".scram.recovered")
			if err := Unpack(UnpackOptions{
				ContainerPath: containerPath,
				OutputPath:    outPath,
				Verify:        true,
			}, rep); err != nil {
				t.Fatalf("Unpack: %v", err)
			}
			if !filesEqual(t, outPath, scramPath) {
				t.Fatal("recovered .scram differs from original")
			}
		})
	}
}

// TestE2EEDCAndECCRealDiscs verifies that miniscram's ComputeEDC /
// ComputeECC agree with the EDC/ECC stored in real Redumper bins. This
// is a sanity check on the bin format itself — failures here mean
// either the dataset is corrupt or EDC/ECC computation is broken, not
// that miniscram's pack/unpack flow is wrong.
//
// Reads from the first data track's file (resolved from the cue), so
// it works for both single-FILE and multi-FILE Redumper output.
func TestE2EEDCAndECCRealDiscs(t *testing.T) {
	for _, f := range realDiscFixtures {
		f := f
		t.Run(f.Name, func(t *testing.T) {
			if !fixturePresent(f) {
				t.Skipf("dataset not present at %s", f.Dir)
			}
			cuePath := filepath.Join(f.Dir, f.Stem+".cue")
			resolved, err := ResolveCue(cuePath)
			if err != nil {
				t.Fatal(err)
			}
			// Find the first data track and read its file directly.
			var dataTrackPath string
			for i, tr := range resolved.Tracks {
				if tr.IsData() {
					dataTrackPath = resolved.Files[i].Path
					break
				}
			}
			if dataTrackPath == "" {
				t.Fatal("no data track found in cue")
			}
			file, err := os.Open(dataTrackPath)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			for _, lba := range f.EDCSampleLBAs {
				var sec [SectorSize]byte
				if _, err := file.ReadAt(sec[:], lba*SectorSize); err != nil {
					t.Fatalf("reading sector %d: %v", lba, err)
				}
				// EDC over [0:2064] should equal stored bytes [2064:2068].
				gotEDC := ComputeEDC(sec[:2064])
				var wantEDC [4]byte
				copy(wantEDC[:], sec[2064:2068])
				if gotEDC != wantEDC {
					t.Errorf("LBA %d EDC: got %x; stored %x", lba, gotEDC, wantEDC)
				}
				// ECC over [12:2076] should equal stored bytes [2076:2352].
				var test [SectorSize]byte = sec
				for i := 2076; i < SectorSize; i++ {
					test[i] = 0
				}
				ComputeECC(&test)
				if !bytes.Equal(test[2076:], sec[2076:]) {
					t.Errorf("LBA %d ECC differs", lba)
				}
			}
		})
	}
}

// filesEqual compares two files in 1-MiB chunks. Test helper, kept
// in this file because no other test file needs it.
func filesEqual(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	defer fb.Close()
	bufA := make([]byte, 1<<20)
	bufB := make([]byte, 1<<20)
	for {
		nA, errA := io.ReadFull(fa, bufA)
		nB, errB := io.ReadFull(fb, bufB)
		if nA != nB {
			return false
		}
		if !bytes.Equal(bufA[:nA], bufB[:nB]) {
			return false
		}
		if errA == io.EOF || errA == io.ErrUnexpectedEOF {
			return errB == io.EOF || errB == io.ErrUnexpectedEOF
		}
		if errA != nil {
			t.Fatal(errA)
		}
	}
}

// countDataTrackErrors walks every data-track .bin file referenced by
// the manifest and counts sectors whose stored EDC bytes [2064:2068]
// don't match a freshly computed EDC over [0:2064]. This is the
// data-track ECC/EDC error count — the same metric Redumper reports
// as "errors count" in its submission templates and on redump.org.
//
// Audio tracks are skipped (they have no EDC/ECC structure).
//
// For SafeDisc-protected discs this number is a class signature
// (e.g., SafeDisc 2.70 typically yields ~588 deliberately corrupted
// sectors). For clean discs it's 0.
//
// Distinct from the delta payload size, which reflects every sector
// requiring a delta override (data-track errors plus lead-in noise
// plus boundary sectors). That count varies per dump; this one
// doesn't.
func countDataTrackErrors(t *testing.T, fixtureDir string, tracks []Track) int {
	t.Helper()
	count := 0
	var sec [SectorSize]byte
	for _, tr := range tracks {
		if !tr.IsData() {
			continue
		}
		path := filepath.Join(fixtureDir, tr.Filename)
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		nSectors := tr.Size / int64(SectorSize)
		for i := int64(0); i < nSectors; i++ {
			offset := i * int64(SectorSize)
			if _, err := f.ReadAt(sec[:], offset); err != nil {
				f.Close()
				t.Fatalf("reading sector %d of %s: %v", i, tr.Filename, err)
			}
			gotEDC := ComputeEDC(sec[:2064])
			var wantEDC [4]byte
			copy(wantEDC[:], sec[2064:2068])
			if gotEDC != wantEDC {
				count++
			}
		}
		f.Close()
	}
	return count
}

func boolToWantStr(b bool) string {
	if b {
		return ">0"
	}
	return "0"
}

func protectionLabel(protected bool) string {
	if protected {
		return "protected"
	}
	return "clean"
}
