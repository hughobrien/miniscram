// /home/hugh/miniscram/inspect_test.go
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleManifest() *Manifest {
	return &Manifest{
		FormatVersion:        4,
		ToolVersion:          "miniscram 0.2.0 (go1.22)",
		CreatedUTC:           "2026-04-28T14:30:21Z",
		ScramSize:            739729728,
		ScramMD5:             strings.Repeat("1", 32),
		ScramSHA1:            strings.Repeat("2", 40),
		ScramSHA256:          strings.Repeat("c", 64),
		BinSize:              739729728,
		BinMD5:               strings.Repeat("3", 32),
		BinSHA1:              strings.Repeat("4", 40),
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
	out, err := formatHumanInspect(m, "MSCM", 0x03, delta, false)
	if err != nil {
		t.Fatal(err)
	}

	wantLines := []string{
		"container:  MSCM v3",
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
	out, err := formatHumanInspect(m, "MSCM", 0x03, delta, true)
	if err != nil {
		t.Fatal(err)
	}
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
	out, err := formatHumanInspect(m, "MSCM", 0x03, delta, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "overrides:") {
		t.Errorf("expected no overrides: section when --full and 0 records:\n%s", out)
	}
}

func TestInspectFormatHumanReportsDeltaError(t *testing.T) {
	m := sampleManifest()
	delta := []byte{0, 0, 0, 1} // count=1, no record follows → framing error
	out, err := formatHumanInspect(m, "MSCM", 0x03, delta, false)
	if err == nil {
		t.Fatal("expected framing error from formatHumanInspect")
	}
	if strings.Contains(out, "delta_error:") {
		t.Errorf("delta_error: marker should not appear in output (it routes via returned error):\n%s", out)
	}
	// Partial output through the delta: section header should still be present.
	if !strings.Contains(out, "delta:\n") {
		t.Errorf("expected partial output through delta: section:\n%s", out)
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
	out, err := formatHumanInspect(m, "MSCM", 0x03, []byte{0, 0, 0, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "track 1: MODE1/2352  first_lba=0") {
		t.Errorf("expected non-padded track 1 line:\n%s", out)
	}
	if !strings.Contains(out, "track 2: AUDIO       first_lba=12345") {
		t.Errorf("expected padded AUDIO line:\n%s", out)
	}
}

// packSyntheticContainer builds a real .miniscram on disk via Pack, so
// CLI tests can hit the actual ReadContainer code path.
func packSyntheticContainer(t *testing.T) string {
	t.Helper()
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	_ = binPath // .bin lives next to .cue; ResolveCue finds it via cue
	out := filepath.Join(dir, "x.miniscram")
	rep := NewReporter(io.Discard, true)
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath,
		OutputPath: out, LeadinLBA: LBAPregapStart, Verify: true,
	}, rep); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCLIInspectHumanOutput(t *testing.T) {
	path := packSyntheticContainer(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", path}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"container:  MSCM v4",
		"manifest:",
		"tool_version:",
		"bin_sha256:",
		"scram_sha256:",
		"tracks:",
		"track 1: MODE1/2352",
		"delta:",
		"override_records:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestCLIInspectJSONOutput(t *testing.T) {
	path := packSyntheticContainer(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", "--json", path}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	var top map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &top); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout.String())
	}
	if _, ok := top["delta_records"]; !ok {
		t.Errorf("delta_records missing in JSON output: %v", top)
	}
	if _, ok := top["bin_sha256"]; !ok {
		t.Errorf("bin_sha256 missing in JSON output: %v", top)
	}
}

func TestCLIInspectFullFlag(t *testing.T) {
	path := packSyntheticContainer(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", "--full", path}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "override_records:       0") {
		t.Errorf("expected override_records: 0 line:\n%s", out)
	}
	if strings.Contains(out, "overrides:\n") {
		t.Errorf("unexpected overrides: section with 0 records:\n%s", out)
	}
}

func TestCLIInspectRejectsV3(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v3.miniscram")
	body := []byte("MSCM\x03\x00\x00\x00\x00")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", path}, &stdout, &stderr)
	if code != exitIO {
		t.Fatalf("exit %d; want %d (exitIO); stderr=%s", code, exitIO, stderr.String())
	}
	if !strings.Contains(stderr.String(), "v0.3") {
		t.Errorf("missing v0.3 migration error in stderr:\n%s", stderr.String())
	}
}

func TestCLIInspectRejectsBadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "junk.miniscram")
	if err := os.WriteFile(path, []byte("XXXX\x02\x00\x00\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", path}, &stdout, &stderr)
	if code != exitIO {
		t.Fatalf("exit %d; want %d", code, exitIO)
	}
	if !strings.Contains(stderr.String(), "not a miniscram container") {
		t.Errorf("missing bad-magic error in stderr:\n%s", stderr.String())
	}
}

func TestCLIInspectUsageErrors(t *testing.T) {
	t.Run("zero positionals", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"inspect"}, &stdout, &stderr)
		if code != exitUsage {
			t.Fatalf("exit %d; want %d", code, exitUsage)
		}
		if !strings.Contains(stderr.String(), "expected exactly one container path") {
			t.Errorf("missing usage error message in stderr:\n%s", stderr.String())
		}
	})
	t.Run("two positionals", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"inspect", "a", "b"}, &stdout, &stderr)
		if code != exitUsage {
			t.Fatalf("exit %d; want %d", code, exitUsage)
		}
		if !strings.Contains(stderr.String(), "expected exactly one container path") {
			t.Errorf("missing usage error message in stderr:\n%s", stderr.String())
		}
	})
}

func TestCLIInspectHelp(t *testing.T) {
	t.Run("inspect --help", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"inspect", "--help"}, &stdout, &stderr)
		if code != exitOK {
			t.Fatalf("exit %d; stderr=%s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "miniscram inspect") {
			t.Errorf("inspect --help did not print help:\n%s", stderr.String())
		}
	})
	t.Run("help inspect", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"help", "inspect"}, &stdout, &stderr)
		if code != exitOK {
			t.Fatalf("exit %d; stderr=%s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "miniscram inspect") {
			t.Errorf("help inspect did not print help:\n%s", stderr.String())
		}
	})
	t.Run("top-level help lists inspect", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"help"}, &stdout, &stderr)
		if code != exitOK {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(stderr.String(), "inspect") {
			t.Errorf("top-level help missing inspect command:\n%s", stderr.String())
		}
	})
}

func TestCLIInspectFullWithOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "with-overrides.miniscram")
	m := sampleManifest()
	m.BinFirstLBA = 0
	delta := buildDelta(t, []uint64{2352, 4704, 7056})
	if err := WriteContainer(path, m, bytes.NewReader(delta)); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", "--full", path}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "overrides:\n") {
		t.Errorf("expected overrides: section with --full and 3 records:\n%s", out)
	}
	if !strings.Contains(out, "override_records:       3") {
		t.Errorf("expected override_records: 3:\n%s", out)
	}
	for _, want := range []string{"byte_offset=2352", "byte_offset=4704", "byte_offset=7056", "lba=1", "lba=2", "lba=3"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in --full output:\n%s", want, out)
		}
	}
}

func TestInspectShowsAllSixHashes(t *testing.T) {
	path := packSyntheticContainer(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", path}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"bin_md5:",
		"bin_sha1:",
		"bin_sha256:",
		"scram_md5:",
		"scram_sha1:",
		"scram_sha256:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in inspect output", want)
		}
	}
}

func TestInspectJSONIncludesAllSixHashes(t *testing.T) {
	path := packSyntheticContainer(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", "--json", path}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	var top map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &top); err != nil {
		t.Fatalf("JSON parse: %v", err)
	}
	for _, key := range []string{
		"bin_md5", "bin_sha1", "bin_sha256",
		"scram_md5", "scram_sha1", "scram_sha256",
	} {
		if v, ok := top[key]; !ok || v == "" {
			t.Errorf("JSON output missing or empty key %q (got %v)", key, v)
		}
	}
}
