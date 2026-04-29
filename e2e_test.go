// /home/hugh/miniscram/e2e_test.go
package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type e2eRow struct {
	name string
	opts SynthOpts
}

func TestE2EMatrix(t *testing.T) {
	rows := []e2eRow{
		{"clean", SynthOpts{MainSectors: 100, LeadoutSectors: 10}},
		{"negative-offset", SynthOpts{MainSectors: 100, LeadoutSectors: 10, WriteOffset: -48}},
		{"positive-offset", SynthOpts{MainSectors: 100, LeadoutSectors: 10, WriteOffset: 48}},
		{"mode2", SynthOpts{MainSectors: 100, LeadoutSectors: 10, Mode: "MODE2/2352", ModeByte: 0x02}},
		{"with-errors", SynthOpts{MainSectors: 100, LeadoutSectors: 10, InjectErrors: []int{12, 47, 63}}},
		{"data-plus-audio", SynthOpts{MainSectors: 100, LeadoutSectors: 10, AudioTracks: 1}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			runE2E(t, row.opts)
		})
	}
}

func runE2E(t *testing.T, opts SynthOpts) {
	t.Helper()
	dir := t.TempDir()
	disc := synthDisc(t, opts)
	_, scramPath, cuePath := writeFixture(t, dir, disc)
	outPath := filepath.Join(dir, "x.miniscram")

	rep := NewReporter(io.Discard, true)

	// Pack (Pack itself does not delete .scram; that's runPack's job).
	if err := Pack(PackOptions{
		CuePath:    cuePath,
		ScramPath:  scramPath,
		OutputPath: outPath,
		LeadinLBA:  LBAPregapStart,
		Verify:     true,
	}, rep); err != nil {
		t.Fatalf("Pack: %v", err)
	}

	// Inspect smoke: manifest must parse and write offset must match.
	m, _, _, err := ReadContainer(outPath)
	if err != nil {
		t.Fatalf("ReadContainer: %v", err)
	}
	if m.WriteOffsetBytes != opts.WriteOffset {
		t.Fatalf("write offset: got %d want %d", m.WriteOffsetBytes, opts.WriteOffset)
	}

	// Verify.
	if err := Verify(VerifyOptions{ContainerPath: outPath}, rep); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Unpack into a fresh path.
	recovered := filepath.Join(dir, "x.recovered.scram")
	if err := Unpack(UnpackOptions{
		ContainerPath: outPath,
		OutputPath:    recovered,
		Verify:        true,
		Force:         true,
	}, rep); err != nil {
		t.Fatalf("Unpack: %v", err)
	}

	recoveredBytes, err := os.ReadFile(recovered)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(recoveredBytes, disc.Scram) {
		t.Fatalf("byte mismatch: recovered scram differs from original (got %d bytes, want %d)", len(recoveredBytes), len(disc.Scram))
	}
}
