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
		ToolVersion:      "miniscram 1.0.0 (go1.22)",
		CreatedUTC:       "2026-04-28T14:30:21Z",
		WriteOffsetBytes: -52,
		LeadinLBA:        -150,
		Scram: ScramInfo{
			Size: 739729728,
			Hashes: FileHashes{
				MD5:    strings.Repeat("1", 32),
				SHA1:   strings.Repeat("2", 40),
				SHA256: strings.Repeat("c", 64),
			},
		},
		Tracks: []Track{{
			Number:   1,
			Mode:     "MODE1/2352",
			FirstLBA: 0,
			Size:     235200,
			Filename: "x.bin",
			Hashes: FileHashes{
				MD5:    strings.Repeat("a", 32),
				SHA1:   strings.Repeat("b", 40),
				SHA256: strings.Repeat("d", 64),
			},
		}},
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
	var fakeHash [32]byte
	copy(fakeHash[:], bytes.Repeat([]byte{0x88}, 32))
	out, err := formatHumanInspect(m, "MSCM", 0x01, fakeHash, delta, false)
	if err != nil {
		t.Fatal(err)
	}

	wantLines := []string{
		"container:  MSCM v1",
		"  tool_version:           miniscram 1.0.0 (go1.22)",
		"  scram.hashes.sha256:    " + strings.Repeat("c", 64),
		"  write_offset_bytes:     -52",
		"  track 1: MODE1/2352  first_lba=0  size=235200  filename=x.bin",
		"    md5:    " + strings.Repeat("a", 32),
		"    sha1:   " + strings.Repeat("b", 40),
		"    sha256: " + strings.Repeat("d", 64),
		"  override_records:       0",
	}
	for _, line := range wantLines {
		if !strings.Contains(out, line) {
			t.Errorf("missing line in human output:\n  want: %q\n  full output:\n%s", line, out)
		}
	}
	// v0.2 fields must not appear
	for _, absent := range []string{"bin_sha256:", "scram_sha256:", "scrambler_table_sha256:", "delta_size:", "error_sector_count:", "bin_first_lba:"} {
		if strings.Contains(out, absent) {
			t.Errorf("v0.2 field %q should not appear in v1 output:\n%s", absent, out)
		}
	}
	if strings.Contains(out, "overrides:") {
		t.Errorf("unexpected overrides: section in human output without --full:\n%s", out)
	}
}

func TestInspectFormatHumanFullListsOverrides(t *testing.T) {
	m := sampleManifest()
	delta := buildDelta(t, []uint64{2352, 4704 + 100, 7056})
	var fakeHash [32]byte
	out, err := formatHumanInspect(m, "MSCM", 0x01, fakeHash, delta, true)
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
	var fakeHash [32]byte
	out, err := formatHumanInspect(m, "MSCM", 0x01, fakeHash, delta, true)
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
	var fakeHash [32]byte
	out, err := formatHumanInspect(m, "MSCM", 0x01, fakeHash, delta, false)
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
		"tool_version", "scram", "tracks", "delta_records",
	} {
		if _, ok := top[k]; !ok {
			t.Errorf("missing top-level key %q in JSON", k)
		}
	}
	// v0.2 fields must not appear
	for _, absent := range []string{"format_version", "bin_sha256", "scram_sha256", "bin_first_lba"} {
		if _, ok := top[absent]; ok {
			t.Errorf("v0.2 field %q should not appear in v1 JSON output", absent)
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
	// v1 JSON starts with tool_version (first field in Manifest struct).
	if !strings.HasPrefix(s, `{"tool_version":`) {
		t.Errorf("JSON does not start with tool_version: %s", s[:min(60, len(s))])
	}
	idxTV := strings.Index(s, `"tool_version"`)
	idxDR := strings.Index(s, `"delta_records"`)
	if idxTV < 0 || idxDR < 0 || idxDR < idxTV {
		t.Errorf("delta_records should come after tool_version; got idxTV=%d idxDR=%d", idxTV, idxDR)
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
		{Number: 1, Mode: "MODE1/2352", FirstLBA: 0, Size: 235200, Filename: "x.bin"},
		{Number: 2, Mode: "AUDIO", FirstLBA: 12345, Size: 47040, Filename: "y.bin"},
	}
	var fakeHash [32]byte
	out, err := formatHumanInspect(m, "MSCM", 0x01, fakeHash, []byte{0, 0, 0, 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "track 1: MODE1/2352  first_lba=0  size=235200  filename=x.bin") {
		t.Errorf("expected non-padded track 1 line:\n%s", out)
	}
	if !strings.Contains(out, "track 2: AUDIO       first_lba=12345  size=47040  filename=y.bin") {
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
		"container:  MSCM v1",
		"manifest:",
		"tool_version:",
		"scram.hashes.sha256:",
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
	if _, ok := top["scram"]; !ok {
		t.Errorf("scram missing in JSON output: %v", top)
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

func TestCLIInspectRejectsWrongVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v9.miniscram")
	// v9 header (wrong version)
	body := make([]byte, containerHeaderSize)
	copy(body, "MSCM")
	body[4] = 0x09
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", path}, &stdout, &stderr)
	if code != exitIO {
		t.Fatalf("exit %d; want %d (exitIO); stderr=%s", code, exitIO, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unsupported container version") {
		t.Errorf("missing version error in stderr:\n%s", stderr.String())
	}
}

func TestCLIInspectRejectsBadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "junk.miniscram")
	body := make([]byte, containerHeaderSize)
	copy(body, "XXXX") // bad magic
	if err := os.WriteFile(path, body, 0o644); err != nil {
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

func TestInspectShowsScramHashes(t *testing.T) {
	path := packSyntheticContainer(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", path}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"scram.hashes.md5:",
		"scram.hashes.sha1:",
		"scram.hashes.sha256:",
		// per-track hash lines
		"    md5:    ",
		"    sha1:   ",
		"    sha256: ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in inspect output", want)
		}
	}
}

func TestInspectJSONIncludesNestedHashes(t *testing.T) {
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
	scram, ok := top["scram"].(map[string]any)
	if !ok {
		t.Fatalf("scram is not an object: %T", top["scram"])
	}
	hashes, ok := scram["hashes"].(map[string]any)
	if !ok {
		t.Fatalf("scram.hashes is not an object: %T", scram["hashes"])
	}
	for _, key := range []string{"md5", "sha1", "sha256"} {
		if v, ok := hashes[key]; !ok || v == "" {
			t.Errorf("scram.hashes.%s missing or empty (got %v)", key, v)
		}
	}
}

// min is a helper for older Go compat (Go 1.21 has builtin min but earlier does not).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
