//go:build redump_data

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const (
	deusExDir  = "/home/hugh/miniscram/deus-ex"
	deusExStem = "DeusEx_v1002f"
)

func TestE2EDeusEx(t *testing.T) {
	if _, err := os.Stat(filepath.Join(deusExDir, deusExStem+".scram")); err != nil {
		t.Skipf("deus ex dataset not present: %v", err)
	}
	// Use a temp dir on the same filesystem as the deus-ex dataset to
	// avoid /tmp overflow (the test produces ~900 MB intermediate files).
	tmp, err := os.MkdirTemp(deusExDir, "miniscram-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	containerPath := filepath.Join(tmp, deusExStem+".miniscram")
	rep := NewReporter(io.Discard, true)
	if err := Pack(PackOptions{
		BinPath:    filepath.Join(deusExDir, deusExStem+".bin"),
		CuePath:    filepath.Join(deusExDir, deusExStem+".cue"),
		ScramPath:  filepath.Join(deusExDir, deusExStem+".scram"),
		OutputPath: containerPath,
		Verify:     true,
	}, rep); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	m, _, err := ReadContainer(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.ErrorSectorCount != 0 {
		t.Errorf("error_sector_count = %d; submission info reports 0", m.ErrorSectorCount)
	}
	if m.DeltaSize >= 1024 {
		t.Errorf("delta is %d bytes; expected < 1024 on a clean disc with smarter builder", m.DeltaSize)
	}
	containerInfo, err := os.Stat(containerPath)
	if err != nil {
		t.Fatal(err)
	}
	if containerInfo.Size() >= 2*1024 {
		t.Errorf(".miniscram is %d bytes; expected < 2048 on a clean disc", containerInfo.Size())
	}

	// recover and byte-compare
	outPath := filepath.Join(tmp, deusExStem+".scram.recovered")
	if err := Unpack(UnpackOptions{
		BinPath:       filepath.Join(deusExDir, deusExStem+".bin"),
		ContainerPath: containerPath,
		OutputPath:    outPath,
		Verify:        true,
	}, rep); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if !filesEqual(t, outPath, filepath.Join(deusExDir, deusExStem+".scram")) {
		t.Fatal("recovered .scram differs from original")
	}
}

func TestEDCAndECCAgainstDeusEx(t *testing.T) {
	if _, err := os.Stat(filepath.Join(deusExDir, deusExStem+".bin")); err != nil {
		t.Skipf("dataset not present: %v", err)
	}
	binPath := filepath.Join(deusExDir, deusExStem+".bin")
	f, err := os.Open(binPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, lba := range []int64{0, 100, 1000, 100000} {
		var sec [SectorSize]byte
		if _, err := f.ReadAt(sec[:], lba*SectorSize); err != nil {
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
		// Zero out the ECC region so we genuinely recompute it.
		for i := 2076; i < SectorSize; i++ {
			test[i] = 0
		}
		ComputeECC(&test)
		if !bytes.Equal(test[2076:], sec[2076:]) {
			t.Errorf("LBA %d ECC differs", lba)
		}
	}
}

// filesEqual compares two files in 1-MiB chunks.
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
