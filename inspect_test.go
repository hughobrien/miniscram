// /home/hugh/miniscram/inspect_test.go
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

func sampleManifest() *Manifest {
	return &Manifest{
		FormatVersion:        2,
		ToolVersion:          "miniscram 0.2.0 (go1.22)",
		CreatedUTC:           "2026-04-28T14:30:21Z",
		ScramSize:            739729728,
		ScramSHA256:          strings.Repeat("c", 64),
		BinSize:              739729728,
		BinSHA256:            strings.Repeat("a", 64),
		WriteOffsetBytes:     -52,
		LeadinLBA:            -150,
		Tracks:               []Track{{Number: 1, Mode: "MODE1/2352", FirstLBA: 0}},
		BinFirstLBA:          0,
		BinSectorCount:       314546,
		ErrorSectorCount:     0,
		DeltaSize:            312,
		ScramblerTableSHA256: strings.Repeat("8", 64),
	}
}

// buildDelta encodes a delta with N synthetic 1-byte overrides at the
// given byte offsets. Returns a byte slice in wire format.
func buildDelta(t *testing.T, offsets []uint64) []byte {
	t.Helper()
	var buf bytes.Buffer
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(offsets)))
	buf.Write(hdr)
	for _, off := range offsets {
		rec := make([]byte, 12)
		binary.BigEndian.PutUint64(rec[:8], off)
		binary.BigEndian.PutUint32(rec[8:], 1)
		buf.Write(rec)
		buf.WriteByte(0xFF)
	}
	return buf.Bytes()
}

func TestInspectFormatHumanCleanDelta(t *testing.T) {
	m := sampleManifest()
	delta := []byte{0, 0, 0, 0}
	out := formatHumanInspect(m, "MSCM", 0x02, delta, false)

	wantLines := []string{
		"container:  MSCM v2",
		"  tool_version:           miniscram 0.2.0 (go1.22)",
		"  bin_sha256:             " + strings.Repeat("a", 64),
		"  scram_sha256:           " + strings.Repeat("c", 64),
		"  scrambler_table_sha256: " + strings.Repeat("8", 64),
		"  write_offset_bytes:     -52",
		"  bin_first_lba:          0",
		"  delta_size:             312",
		"  error_sector_count:     0",
		"  track 1: MODE1/2352  first_lba=0",
		"  override_records:       0",
	}
	for _, line := range wantLines {
		if !strings.Contains(out, line) {
			t.Errorf("missing line in human output:\n  want: %q\n  full output:\n%s", line, out)
		}
	}
	if strings.Contains(out, "overrides:") {
		t.Errorf("unexpected overrides: section in human output without --full:\n%s", out)
	}
}

func TestInspectFormatHumanFullListsOverrides(t *testing.T) {
	m := sampleManifest()
	delta := buildDelta(t, []uint64{2352, 4704 + 100, 7056})
	out := formatHumanInspect(m, "MSCM", 0x02, delta, true)
	if !strings.Contains(out, "overrides:\n") {
		t.Errorf("expected overrides: section with --full and 3 records:\n%s", out)
	}
	wantLines := []string{
		"byte_offset=2352",
		"byte_offset=4804",
		"byte_offset=7056",
		"lba=1",
		"lba=2",
		"lba=3",
	}
	for _, line := range wantLines {
		if !strings.Contains(out, line) {
			t.Errorf("missing %q in --full output:\n%s", line, out)
		}
	}
}

func TestInspectFormatHumanFullEmptyHidesSection(t *testing.T) {
	m := sampleManifest()
	delta := []byte{0, 0, 0, 0}
	out := formatHumanInspect(m, "MSCM", 0x02, delta, true)
	if strings.Contains(out, "overrides:") {
		t.Errorf("expected no overrides: section when --full and 0 records:\n%s", out)
	}
}

func TestInspectFormatHumanReportsDeltaError(t *testing.T) {
	m := sampleManifest()
	delta := []byte{0, 0, 0, 1}
	out := formatHumanInspect(m, "MSCM", 0x02, delta, false)
	if !strings.Contains(out, "delta_error:") {
		t.Errorf("expected delta_error: line for truncated delta:\n%s", out)
	}
}

func TestInspectFormatJSONStructure(t *testing.T) {
	m := sampleManifest()
	delta := buildDelta(t, []uint64{2352, 7056})
	body, err := formatJSONInspect(m, delta)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("JSON did not parse: %v\n%s", err, body)
	}
	for _, k := range []string{
		"format_version", "tool_version", "bin_sha256", "scram_sha256",
		"tracks", "bin_first_lba", "delta_records",
	} {
		if _, ok := top[k]; !ok {
			t.Errorf("missing top-level key %q in JSON", k)
		}
	}
	records, ok := top["delta_records"].([]any)
	if !ok {
		t.Fatalf("delta_records is not an array: %T", top["delta_records"])
	}
	if len(records) != 2 {
		t.Fatalf("delta_records length = %d; want 2", len(records))
	}
	first, _ := records[0].(map[string]any)
	for _, k := range []string{"byte_offset", "length", "lba"} {
		if _, ok := first[k]; !ok {
			t.Errorf("missing record key %q", k)
		}
	}
}

func TestInspectFormatJSONEmptyRecordsIsArray(t *testing.T) {
	m := sampleManifest()
	body, err := formatJSONInspect(m, []byte{0, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"delta_records":[]`)) {
		t.Errorf("expected delta_records:[] in JSON; got %s", body)
	}
}

func TestInspectFormatJSONFieldOrder(t *testing.T) {
	m := sampleManifest()
	body, err := formatJSONInspect(m, []byte{0, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.HasPrefix(s, `{"format_version":`) {
		t.Errorf("JSON does not start with format_version: %s", s[:60])
	}
	idxFV := strings.Index(s, `"format_version"`)
	idxDR := strings.Index(s, `"delta_records"`)
	if idxFV < 0 || idxDR < 0 || idxDR < idxFV {
		t.Errorf("delta_records should come after format_version; got idxFV=%d idxDR=%d", idxFV, idxDR)
	}
}

func TestInspectFormatJSONReturnsErrorOnBadDelta(t *testing.T) {
	m := sampleManifest()
	bad := []byte{0, 0, 0, 1}
	if _, err := formatJSONInspect(m, bad); err == nil {
		t.Errorf("expected error for truncated delta in JSON path")
	}
}

func TestInspectFormatHumanTrackPadding(t *testing.T) {
	m := sampleManifest()
	m.Tracks = []Track{
		{Number: 1, Mode: "MODE1/2352", FirstLBA: 0},
		{Number: 2, Mode: "AUDIO", FirstLBA: 12345},
	}
	out := formatHumanInspect(m, "MSCM", 0x02, []byte{0, 0, 0, 0}, false)
	if !strings.Contains(out, "track 1: MODE1/2352  first_lba=0") {
		t.Errorf("expected non-padded track 1 line:\n%s", out)
	}
	if !strings.Contains(out, "track 2: AUDIO       first_lba=12345") {
		t.Errorf("expected padded AUDIO line:\n%s", out)
	}
}
