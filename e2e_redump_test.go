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
	Name              string  // sub-test name, e.g. "deus-ex"
	Dir               string  // absolute path to the dataset directory
	Stem              string  // filename stem (no extension)
	ExpectedErrors    int     // assert manifest.ErrorSectorCount == this
	MaxDeltaBytes     int64   // assert manifest.DeltaSize < this
	MaxContainerBytes int64   // assert os.Stat(container).Size() < this
	EDCSampleLBAs     []int64 // LBAs to sample in TestE2EEDCAndECCRealDiscs (must be Mode 1, unprotected)
}

// realDiscFixtures is the authoritative dataset list. Keep entries
// sorted alphabetically by Name. HL1 (multi-track + audio) is
// intentionally absent — its Redumper output uses one .bin per track,
// which miniscram's cue.go currently ignores. Add HL1 here once
// multi-FILE .cue support lands.
var realDiscFixtures = []realDiscFixture{
	{
		Name:              "deus-ex",
		Dir:               "/home/hugh/miniscram/deus-ex",
		Stem:              "DeusEx_v1002f",
		ExpectedErrors:    0,
		MaxDeltaBytes:     1024,
		MaxContainerBytes: 2048,
		EDCSampleLBAs:     []int64{0, 100, 1000, 100000},
	},
	{
		Name: "freelancer",
		Dir:  "/home/hugh/miniscram/freelancer",
		Stem: "FL_v1",
		// SafeDisc 2.70.030; per redump.org submission, 588 deliberately
		// corrupted sectors. Round-trip byte-equality plus this exact
		// count proves miniscram captures the protection losslessly.
		ExpectedErrors:    588,
		MaxDeltaBytes:     5 * 1024 * 1024,
		MaxContainerBytes: 5 * 1024 * 1024,
		// SafeDisc protection clusters near end-of-disc; LBAs in the
		// first 100k are well clear of it.
		EDCSampleLBAs: []int64{0, 100, 1000, 100000},
	},
}

// fixturePresent reports whether all three Redumper output files for a
// fixture exist on disk. Used to gate every sub-test with a single
// check rather than letting Pack fail with a confusing message later.
func fixturePresent(f realDiscFixture) bool {
	for _, ext := range []string{".bin", ".cue", ".scram"} {
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
			binPath := filepath.Join(f.Dir, f.Stem+".bin")
			cuePath := filepath.Join(f.Dir, f.Stem+".cue")
			scramPath := filepath.Join(f.Dir, f.Stem+".scram")

			// Use a temp dir on the same filesystem as the dataset to
			// avoid /tmp overflow (the test produces ~scram-sized
			// intermediate files — hundreds of MB).
			tmp, err := os.MkdirTemp(f.Dir, "miniscram-e2e-*")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(tmp) })

			containerPath := filepath.Join(tmp, f.Stem+".miniscram")
			rep := NewReporter(io.Discard, true)

			if err := Pack(PackOptions{
				BinPath:    binPath,
				CuePath:    cuePath,
				ScramPath:  scramPath,
				OutputPath: containerPath,
				Verify:     true,
			}, rep); err != nil {
				t.Fatalf("Pack: %v", err)
			}

			m, _, err := ReadContainer(containerPath)
			if err != nil {
				t.Fatal(err)
			}
			if m.ErrorSectorCount != f.ExpectedErrors {
				t.Errorf("error_sector_count = %d; expected %d", m.ErrorSectorCount, f.ExpectedErrors)
			}
			if m.DeltaSize >= f.MaxDeltaBytes {
				t.Errorf("delta is %d bytes; expected < %d", m.DeltaSize, f.MaxDeltaBytes)
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
				BinPath:       binPath,
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
func TestE2EEDCAndECCRealDiscs(t *testing.T) {
	for _, f := range realDiscFixtures {
		f := f
		t.Run(f.Name, func(t *testing.T) {
			binPath := filepath.Join(f.Dir, f.Stem+".bin")
			if _, err := os.Stat(binPath); err != nil {
				t.Skipf("dataset not present at %s", f.Dir)
			}
			file, err := os.Open(binPath)
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
