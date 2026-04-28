// /home/hugh/miniscram/manifest_test.go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestContainerRoundtrip(t *testing.T) {
	m := &Manifest{
		FormatVersion:        1,
		ToolVersion:          "miniscram 0.0.1-test",
		CreatedUTC:           "2026-04-27T17:00:00Z",
		ScramSize:            897527784,
		ScramSHA256:          "abc",
		BinSize:              791104608,
		BinSHA256:            "def",
		WriteOffsetBytes:     -48,
		LeadinLBA:            -45150,
		Tracks:               []Track{{Number: 1, Mode: "MODE1/2352", FirstLBA: 0}},
		BinFirstLBA:          0,
		BinSectorCount:       336354,
		ErrorSectors:         []int32{},
		ErrorSectorCount:     0,
		DeltaSize:            42,
		ScramblerTableSHA256: expectedScrambleTableSHA256,
	}
	delta := []byte("DELTA-PAYLOAD-FAKE-VCDIFF-BYTES")
	dir := t.TempDir()
	path := filepath.Join(dir, "x.miniscram")
	if err := WriteContainer(path, m, bytes.NewReader(delta)); err != nil {
		t.Fatal(err)
	}
	gotM, gotDelta, err := ReadContainer(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotM.ScramSize != m.ScramSize || gotM.WriteOffsetBytes != m.WriteOffsetBytes ||
		gotM.BinSectorCount != m.BinSectorCount {
		t.Fatalf("manifest round-trip mismatch: %+v vs %+v", gotM, m)
	}
	if !bytes.Equal(gotDelta, delta) {
		t.Fatalf("delta bytes mismatch")
	}
}

func TestContainerRejectsBadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.miniscram")
	if err := os.WriteFile(path, []byte("BADMAGICXYZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadContainer(path); err == nil {
		t.Fatal("expected error for bad magic, got nil")
	}
}

func TestContainerRejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v9.miniscram")
	// magic 'MSCM' + version 9 + zero-length manifest
	body := []byte{'M', 'S', 'C', 'M', 0x09, 0, 0, 0, 0}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadContainer(path)
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
}

func TestErrorSectorsCappedInJSON(t *testing.T) {
	m := &Manifest{ErrorSectorCount: 50000}
	for i := int32(0); i < 50000; i++ {
		m.ErrorSectors = append(m.ErrorSectors, i)
	}
	data, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("\"error_sectors\":[")) {
		t.Fatalf("error_sectors should be omitted when count > 10000")
	}
	if !bytes.Contains(data, []byte("\"error_sector_count\":50000")) {
		t.Fatalf("error_sector_count missing from output")
	}
}
