# miniscram inspect Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `miniscram inspect <container>` — a read-only pretty-printer for `.miniscram` files with `--full` (per-record override listing) and `--json` (machine-consumable manifest+records) modes.

**Architecture:** One new public helper (`IterateDeltaRecords` in `delta.go`), one new file (`inspect.go`) with pure formatting functions plus a `runInspect` CLI entry point, a small refactor to thread `stdout` through `main.run()`, and additions to `help.go`. No new external dependencies; reuses `ReadContainer`, `Manifest.Marshal()`, and the existing wire format.

**Tech Stack:** Go 1.x stdlib only (`encoding/json`, `encoding/binary`, `flag`, `fmt`, `io`).

**Spec:** `docs/superpowers/specs/2026-04-27-miniscram-inspect-design.md`

---

## File Structure

| File | Action | Responsibility |
| --- | --- | --- |
| `delta.go` | modify | Add `IterateDeltaRecords` walker (offset+length, no payload bytes). |
| `delta_test.go` | modify | Unit tests for the iterator (count, framing errors). |
| `inspect.go` | create | Pure formatting (`formatHumanInspect`, `formatJSONInspect`) + `runInspect` CLI entry point. |
| `inspect_test.go` | create | Tests for formatting and CLI behavior; uses existing `writeSynthDiscFiles` helper. |
| `main.go` | modify | Thread `stdout` through `run()`; dispatch `inspect`. |
| `main_test.go` | modify | Pass `stdout` buffer to `run()` in existing tests. |
| `help.go` | modify | Add `inspect` to `topHelpText`; add `inspectHelpText` and `printInspectHelp`. |

The split keeps inspect's pretty-printing logic isolated from CLI plumbing. `IterateDeltaRecords` lives in `delta.go` because it's the third consumer of the wire format alongside `EncodeDelta` and `ApplyDelta` and belongs with them.

---

## Task 1: `IterateDeltaRecords` walker

**Goal:** A pure helper in `delta.go` that walks override records in a delta byte slice, calling a callback for each record's offset and length without copying payload bytes.

**Files:**
- Modify: `/home/hugh/miniscram/delta.go` (append after `ApplyDelta`)
- Modify: `/home/hugh/miniscram/delta_test.go` (append new tests)

**Acceptance Criteria:**
- [ ] `IterateDeltaRecords(delta, fn)` returns the count from the wire-format header and walks every record in order, calling `fn(off, length)` per record.
- [ ] Returns `(0, nil)` for a 4-byte delta containing only a zero count.
- [ ] Returns a framing error with descriptive message when: header is short, payload is truncated, or `length == 0` or `length > SectorSize`.
- [ ] `fn` returning a non-nil error halts iteration and that error propagates back.
- [ ] Cannot panic on adversarial input (all bounds checks explicit).

**Verify:** `go test ./... -run TestIterateDeltaRecords -v` → all pass.

**Steps:**

- [ ] **Step 1: Write failing tests in `delta_test.go`**

Append to `/home/hugh/miniscram/delta_test.go`:

```go
func TestIterateDeltaRecordsEmpty(t *testing.T) {
	count, err := IterateDeltaRecords([]byte{0, 0, 0, 0}, func(off uint64, length uint32) error {
		t.Fatalf("callback invoked for empty delta")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count = %d; want 0", count)
	}
}

func TestIterateDeltaRecordsThreeRecords(t *testing.T) {
	// Build a delta with three records via EncodeDelta and walk it.
	scram := bytes.Repeat([]byte{0xAB}, 10000)
	hat := append([]byte{}, scram...)
	scram[100] ^= 0xFF
	scram[500] ^= 0xFF
	scram[5000] ^= 0xFF
	var enc bytes.Buffer
	if _, err := EncodeDelta(&enc, bytes.NewReader(hat), bytes.NewReader(scram), int64(len(scram))); err != nil {
		t.Fatal(err)
	}
	type rec struct {
		off uint64
		ln  uint32
	}
	var got []rec
	count, err := IterateDeltaRecords(enc.Bytes(), func(off uint64, length uint32) error {
		got = append(got, rec{off, length})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("count = %d; want 3", count)
	}
	want := []rec{{100, 1}, {500, 1}, {5000, 1}}
	if len(got) != len(want) {
		t.Fatalf("got %d records; want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("record %d = %+v; want %+v", i, got[i], want[i])
		}
	}
}

func TestIterateDeltaRecordsTruncatedHeader(t *testing.T) {
	// count says 1, header is short (only 8 of 12 bytes present)
	bad := []byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0}
	if _, err := IterateDeltaRecords(bad, func(uint64, uint32) error { return nil }); err == nil {
		t.Fatal("expected framing error for short record header")
	}
}

func TestIterateDeltaRecordsTruncatedPayload(t *testing.T) {
	// count=1, valid 12-byte header (offset=0, length=10), but only 5 bytes of payload
	bad := []byte{
		0, 0, 0, 1,
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 10,
		1, 2, 3, 4, 5,
	}
	if _, err := IterateDeltaRecords(bad, func(uint64, uint32) error { return nil }); err == nil {
		t.Fatal("expected framing error for truncated payload")
	}
}

func TestIterateDeltaRecordsBadLength(t *testing.T) {
	// length = 0 should be rejected (mirrors ApplyDelta's check)
	bad := []byte{
		0, 0, 0, 1,
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0,
	}
	if _, err := IterateDeltaRecords(bad, func(uint64, uint32) error { return nil }); err == nil {
		t.Fatal("expected framing error for length=0")
	}
}

func TestIterateDeltaRecordsCallbackError(t *testing.T) {
	// build a 1-record delta and have the callback return an error
	scram := []byte{0xAB, 0xAB, 0xAB}
	hat := []byte{0xAA, 0xAB, 0xAB}
	var enc bytes.Buffer
	if _, err := EncodeDelta(&enc, bytes.NewReader(hat), bytes.NewReader(scram), 3); err != nil {
		t.Fatal(err)
	}
	want := errors.New("stop")
	_, err := IterateDeltaRecords(enc.Bytes(), func(uint64, uint32) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("got err=%v; want %v", err, want)
	}
}
```

Also add `"errors"` to the import block of `delta_test.go`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestIterateDeltaRecords -v`
Expected: FAIL with "undefined: IterateDeltaRecords".

- [ ] **Step 3: Implement `IterateDeltaRecords` in `delta.go`**

Append to `/home/hugh/miniscram/delta.go` (after `ApplyDelta`):

```go
// IterateDeltaRecords walks the override records in delta, calling fn
// for each record's byte offset and length. fn is not given the
// payload bytes (they're skipped over in delta). Returns the count
// from the wire-format header and any framing or callback error.
//
// This is the read-only counterpart to EncodeDelta's writer; consumers
// like inspect/verify use it to enumerate records without materializing
// payloads.
func IterateDeltaRecords(delta []byte, fn func(off uint64, length uint32) error) (uint32, error) {
	if len(delta) < 4 {
		return 0, fmt.Errorf("delta too short for override count (%d bytes)", len(delta))
	}
	count := binary.BigEndian.Uint32(delta[:4])
	pos := 4
	for i := uint32(0); i < count; i++ {
		if pos+12 > len(delta) {
			return i, fmt.Errorf("override %d header truncated at offset %d", i, pos)
		}
		off := binary.BigEndian.Uint64(delta[pos : pos+8])
		length := binary.BigEndian.Uint32(delta[pos+8 : pos+12])
		pos += 12
		if length == 0 || length > SectorSize {
			return i, fmt.Errorf("override %d has implausible length %d", i, length)
		}
		if pos+int(length) > len(delta) {
			return i, fmt.Errorf("override %d payload truncated (need %d bytes at offset %d, have %d)",
				i, length, pos, len(delta)-pos)
		}
		if err := fn(off, length); err != nil {
			return i, err
		}
		pos += int(length)
	}
	return count, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run TestIterateDeltaRecords -v`
Expected: PASS for all six tests.

Also run the full delta test set to confirm no regression:

Run: `go test ./... -run TestDelta -v`
Expected: all existing delta tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add delta.go delta_test.go
git commit -m "$(cat <<'EOF'
delta: add IterateDeltaRecords walker

Read-only walk over override records yielding offset+length without
copying payloads. Used by inspect (and future verify/fsck) to
enumerate the contents of a container's delta payload.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Inspect formatting (pure functions)

**Goal:** Pure functions that produce the human and JSON inspection output from a `Manifest`, container framing metadata, and the raw delta bytes. Testable without any CLI involvement.

**Files:**
- Create: `/home/hugh/miniscram/inspect.go`
- Create: `/home/hugh/miniscram/inspect_test.go`

**Acceptance Criteria:**
- [ ] `formatHumanInspect(m, magic, version, delta, full) string` produces output matching the spec's human format: `container:` line, `manifest:` block, `tracks:` block, `delta:` block, optional `overrides:` block when `full` is true and at least one override exists.
- [ ] `formatJSONInspect(m, delta) ([]byte, error)` returns the marshaled manifest with a top-level `delta_records` array spliced in (offset, length, lba per record).
- [ ] Hashes are emitted in full (64 hex chars).
- [ ] `lba = byte_offset / 2352 + bin_first_lba` is computed correctly per record.
- [ ] On framing error from `IterateDeltaRecords`, both formatters surface the error (return error from JSON, append to a designated error string field for human — see implementation).
- [ ] Both functions are deterministic (same inputs → same outputs).

**Verify:** `go test ./... -run TestInspectFormat -v` → all pass.

**Steps:**

- [ ] **Step 1: Write `inspect.go` formatting skeleton (no CLI yet)**

Create `/home/hugh/miniscram/inspect.go`:

```go
// /home/hugh/miniscram/inspect.go
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// formatHumanInspect produces the default `miniscram inspect` text
// output. magic and version come from the raw container header (not
// the manifest's redundant format_version). delta is the full delta
// payload as returned by ReadContainer. If full is true and there is
// at least one override record, an `overrides:` block is appended.
//
// On a framing error walking the delta, the error is appended on its
// own line under the delta: section; partial output before the failure
// is preserved. (This matches inspect's "narrow scope" — surface the
// error, don't try to fsck.)
func formatHumanInspect(m *Manifest, magic string, version byte, delta []byte, full bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "container:  %s v%d\n", magic, version)
	b.WriteString("manifest:\n")
	fmt.Fprintf(&b, "  tool_version:           %s\n", m.ToolVersion)
	fmt.Fprintf(&b, "  created_utc:            %s\n", m.CreatedUTC)
	fmt.Fprintf(&b, "  bin_size:               %d\n", m.BinSize)
	fmt.Fprintf(&b, "  bin_sha256:             %s\n", m.BinSHA256)
	fmt.Fprintf(&b, "  scram_size:             %d\n", m.ScramSize)
	fmt.Fprintf(&b, "  scram_sha256:           %s\n", m.ScramSHA256)
	fmt.Fprintf(&b, "  write_offset_bytes:     %d\n", m.WriteOffsetBytes)
	fmt.Fprintf(&b, "  leadin_lba:             %d\n", m.LeadinLBA)
	fmt.Fprintf(&b, "  bin_first_lba:          %d\n", m.BinFirstLBA)
	fmt.Fprintf(&b, "  bin_sector_count:       %d\n", m.BinSectorCount)
	fmt.Fprintf(&b, "  delta_size:             %d\n", m.DeltaSize)
	fmt.Fprintf(&b, "  error_sector_count:     %d\n", m.ErrorSectorCount)
	fmt.Fprintf(&b, "  scrambler_table_sha256: %s\n", m.ScramblerTableSHA256)

	b.WriteString("tracks:\n")
	maxMode := 0
	for _, t := range m.Tracks {
		if len(t.Mode) > maxMode {
			maxMode = len(t.Mode)
		}
	}
	for _, t := range m.Tracks {
		fmt.Fprintf(&b, "  track %d: %-*s  first_lba=%d\n", t.Number, maxMode, t.Mode, t.FirstLBA)
	}

	type rec struct {
		off    uint64
		length uint32
	}
	var records []rec
	count, iterErr := IterateDeltaRecords(delta, func(off uint64, length uint32) error {
		records = append(records, rec{off, length})
		return nil
	})
	b.WriteString("delta:\n")
	fmt.Fprintf(&b, "  override_records:       %d\n", count)
	if iterErr != nil {
		fmt.Fprintf(&b, "  delta_error:            %s\n", iterErr)
	}

	if full && len(records) > 0 {
		// Sort by offset for deterministic output (records are emitted in
		// position order by EncodeDelta, but sorting makes the contract
		// explicit and stable against future encoder reorderings).
		sort.Slice(records, func(i, j int) bool { return records[i].off < records[j].off })
		b.WriteString("overrides:\n")
		for _, r := range records {
			lba := int64(r.off)/int64(SectorSize) + int64(m.BinFirstLBA)
			fmt.Fprintf(&b, "  byte_offset=%-12d length=%-5d lba=%d\n", r.off, r.length, lba)
		}
	}
	return b.String()
}

// formatJSONInspect emits the manifest JSON verbatim plus a top-level
// `delta_records` array of {byte_offset, length, lba} objects. Always
// includes all records (no cap).
func formatJSONInspect(m *Manifest, delta []byte) ([]byte, error) {
	manifestBody, err := m.Marshal()
	if err != nil {
		return nil, err
	}
	// Re-decode into a generic map so we can splice delta_records as a
	// top-level field while preserving Marshal()'s field ordering and
	// any future fields we don't have to know about here.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(manifestBody, &top); err != nil {
		return nil, fmt.Errorf("re-decoding manifest: %w", err)
	}

	type recordOut struct {
		ByteOffset uint64 `json:"byte_offset"`
		Length     uint32 `json:"length"`
		LBA        int64  `json:"lba"`
	}
	var records []recordOut
	if _, err := IterateDeltaRecords(delta, func(off uint64, length uint32) error {
		lba := int64(off)/int64(SectorSize) + int64(m.BinFirstLBA)
		records = append(records, recordOut{ByteOffset: off, Length: length, LBA: lba})
		return nil
	}); err != nil {
		return nil, err
	}
	if records == nil {
		records = []recordOut{} // emit `[]`, not `null`
	}
	recordsBody, err := json.Marshal(records)
	if err != nil {
		return nil, err
	}
	top["delta_records"] = recordsBody

	// Re-marshal in a stable order: manifest fields in their original
	// order, then delta_records last.
	keys := stableInspectFieldOrder(manifestBody)
	keys = append(keys, "delta_records")
	var out strings.Builder
	out.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		out.Write(kb)
		out.WriteByte(':')
		out.Write(top[k])
	}
	out.WriteByte('}')
	return []byte(out.String()), nil
}

// stableInspectFieldOrder returns the top-level JSON keys of body in
// the order they appear in body. Used to keep formatJSONInspect's
// output ordering matched to Manifest's struct declaration order.
func stableInspectFieldOrder(body []byte) []string {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	// expect '{'
	if _, err := dec.Token(); err != nil {
		return nil
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		k, ok := tok.(string)
		if !ok {
			return keys
		}
		keys = append(keys, k)
		// skip the value
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return keys
		}
	}
	return keys
}
```

- [ ] **Step 2: Write tests in `inspect_test.go`**

Create `/home/hugh/miniscram/inspect_test.go`:

```go
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
	// LBA = byte_offset/2352 + bin_first_lba(=0)
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
	// truncated: count=1 but no record header present
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
	// format_version is the first manifest field; delta_records must come last.
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
	bad := []byte{0, 0, 0, 1} // count=1, no record
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
	// modes should be left-padded so first_lba columns align; longest mode
	// here is "MODE1/2352" (10 chars), so AUDIO line has 5 trailing spaces.
	if !strings.Contains(out, "track 1: MODE1/2352  first_lba=0") {
		t.Errorf("expected non-padded track 1 line:\n%s", out)
	}
	if !strings.Contains(out, "track 2: AUDIO       first_lba=12345") {
		t.Errorf("expected padded AUDIO line:\n%s", out)
	}
}
```

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./... -run TestInspectFormat -v`
Expected: all 9 tests PASS.

If any fail, fix the formatting or test until all pass — typical issues are off-by-one in column padding or different float/int representation in JSON.

- [ ] **Step 4: Run the whole test suite to confirm no regression**

Run: `go test ./...`
Expected: all existing tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add inspect.go inspect_test.go
git commit -m "$(cat <<'EOF'
inspect: pure formatting functions for human + JSON output

Adds formatHumanInspect and formatJSONInspect — testable in
isolation, no CLI involvement yet. JSON output preserves manifest
field order and appends a delta_records array; human output emits
one field per line with full-length hashes.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: CLI wiring, help text, and end-to-end tests

**Goal:** Wire `inspect` into the CLI: `runInspect` entry point, dispatch from `main.run()`, help text, and end-to-end tests that exercise `run([]string{"inspect", ...})` with captured stdout.

**Files:**
- Modify: `/home/hugh/miniscram/main.go`
- Modify: `/home/hugh/miniscram/main_test.go`
- Modify: `/home/hugh/miniscram/help.go`
- Modify: `/home/hugh/miniscram/inspect.go` (append `runInspect`)
- Modify: `/home/hugh/miniscram/inspect_test.go` (append CLI tests)

**Acceptance Criteria:**
- [ ] `miniscram inspect <container>` writes the human format to stdout, exits 0.
- [ ] `miniscram inspect --json <container>` writes valid JSON to stdout, exits 0.
- [ ] `miniscram inspect --full <container>` includes the `overrides:` section when records exist.
- [ ] `miniscram inspect` with zero or two-plus positionals exits 1.
- [ ] `miniscram inspect <bad-magic-file>` exits 4 with the existing "not a miniscram container" error on stderr.
- [ ] `miniscram inspect <v1-container>` exits 4 with the existing v0.2 migration error on stderr.
- [ ] `miniscram help inspect` and `miniscram inspect --help` print inspect help, exit 0.
- [ ] `miniscram help` (top-level) lists `inspect` as a command.

**Verify:** `go test ./...` → all pass, including new `TestCLIInspect*` tests.

**Steps:**

- [ ] **Step 1: Thread stdout through `run()` in `main.go`**

In `/home/hugh/miniscram/main.go`, change the `main` function and `run` signature:

```go
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printTopHelp(stderr)
		return exitUsage
	}
	switch args[0] {
	case "pack":
		return runPack(args[1:], stderr)
	case "unpack":
		return runUnpack(args[1:], stderr)
	case "inspect":
		return runInspect(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		if len(args) >= 2 {
			switch args[1] {
			case "pack":
				printPackHelp(stderr)
				return exitOK
			case "unpack":
				printUnpackHelp(stderr)
				return exitOK
			case "inspect":
				printInspectHelp(stderr)
				return exitOK
			}
		}
		printTopHelp(stderr)
		return exitOK
	case "--version":
		fmt.Fprintln(stderr, toolVersion)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printTopHelp(stderr)
		return exitUsage
	}
}
```

`runPack` and `runUnpack` are unchanged. Only `run()`'s signature and the new `inspect` case are different. Note that `pack`/`unpack` don't need stdout — their progress reporters legitimately write to stderr, and that's existing behavior we're preserving.

- [ ] **Step 2: Update `main_test.go` for the new `run` signature**

In `/home/hugh/miniscram/main_test.go`, update the three existing tests to pass a stdout buffer. Replace each of the three `run(...)` call sites:

```go
// TestCLIPackDiscovers
var stdout, stderr bytes.Buffer
code := run([]string{"pack", "--help"}, &stdout, &stderr)

// TestCLIUnknownCommand
var stdout, stderr bytes.Buffer
code := run([]string{"foo"}, &stdout, &stderr)

// TestCLIVersion
var stdout, stderr bytes.Buffer
code := run([]string{"--version"}, &stdout, &stderr)
```

The body of each test continues to assert against `stderr` only — pack help, unknown command, and version all currently route to stderr and that behavior is unchanged.

- [ ] **Step 3: Add `runInspect` and `printInspectHelp` references to `inspect.go`**

Append to `/home/hugh/miniscram/inspect.go`:

```go
import (
	"flag"
	"io"
)
```

(Add to the existing import block — Go will reject duplicate `import` statements; merge into the existing one.)

```go
// runInspect is the CLI entry point for `miniscram inspect`. Reads the
// container, formats per --json/--full flags, writes to stdout. Errors
// go to stderr and produce exit code 4 (I/O); usage errors produce 1.
func runInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	full := fs.Bool("full", false, "list every override record (no cap)")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	help := fs.Bool("help", false, "show help for inspect")
	helpShort := fs.Bool("h", false, "show help for inspect")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *help || *helpShort {
		printInspectHelp(stderr)
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "expected exactly one container path; got %d\n", fs.NArg())
		printInspectHelp(stderr)
		return exitUsage
	}
	path := fs.Arg(0)
	m, delta, err := ReadContainer(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitIO
	}
	if *asJSON {
		body, err := formatJSONInspect(m, delta)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitIO
		}
		fmt.Fprintln(stdout, string(body))
		return exitOK
	}
	fmt.Fprint(stdout, formatHumanInspect(m, containerMagic, containerVersion, delta, *full))
	return exitOK
}
```

- [ ] **Step 4: Add `printInspectHelp` and update `topHelpText` in `help.go`**

In `/home/hugh/miniscram/help.go`, add the printer:

```go
func printInspectHelp(w io.Writer) {
	fmt.Fprint(w, inspectHelpText)
}
```

Add to the `topHelpText` const, in the COMMANDS block (after `unpack`):

```
    inspect    pretty-print a .miniscram container (read-only)
```

So the COMMANDS section becomes:

```
COMMANDS:
    pack       pack a .scram into a compact .miniscram container
    unpack     reproduce a .scram from .bin + .miniscram
    inspect    pretty-print a .miniscram container (read-only)
    help       show this help, or 'miniscram help <command>'
```

Append a new const at the bottom of the file:

```go
const inspectHelpText = `USAGE:
    miniscram inspect [--full] [--json] <container>

ARGUMENTS:
    <container>    path to a .miniscram file

OPTIONS:
    --full         append a per-record listing of every override
                   (no cap). without it, only the override count
                   is printed.
    --json         emit machine-readable JSON: the manifest verbatim
                   plus a delta_records array. always includes all
                   records.
    -h, --help     show this help.

EXIT CODES:
    0    success
    1    usage error (wrong number of positionals, bad flags)
    4    I/O or container parse error
`
```

- [ ] **Step 5: Add CLI-level tests in `inspect_test.go`**

Append to `/home/hugh/miniscram/inspect_test.go`:

```go
import (
	"io"
	"os"
	"path/filepath"
)
```

(Add to the existing import block.)

```go
// packSyntheticContainer builds a real .miniscram on disk via Pack, so
// CLI tests can hit the actual ReadContainer code path.
func packSyntheticContainer(t *testing.T) string {
	t.Helper()
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	out := filepath.Join(dir, "x.miniscram")
	rep := NewReporter(io.Discard, true)
	if err := Pack(PackOptions{
		BinPath: binPath, CuePath: cuePath, ScramPath: scramPath,
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
		"container:  MSCM v2",
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
	// On a clean synthetic disc the override count is 0 — but Pack with
	// LBALeadinStart vs LBAPregapStart will produce some overrides for
	// the leadin region. Use writeSynthDiscFiles + LeadinLBA mismatch by
	// choosing LBAPregapStart (matches synthDisc's actual leadin), so
	// this test should observe 0 overrides and still verify --full
	// doesn't add an overrides: section when there are none.
	path := packSyntheticContainer(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", "--full", path}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	// On the synth disc: clean → 0 overrides → no overrides: section.
	// The override_records: 0 line is still present.
	if !strings.Contains(out, "override_records:       0") {
		t.Errorf("expected override_records: 0 line:\n%s", out)
	}
	if strings.Contains(out, "overrides:\n") {
		t.Errorf("unexpected overrides: section with 0 records:\n%s", out)
	}
}

func TestCLIInspectRejectsV1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v1.miniscram")
	// Hand-build a v1 container header: magic + version 0x01 + 4-byte
	// manifest length (0) — ReadContainer should bail at the version check.
	body := []byte("MSCM\x01\x00\x00\x00\x00")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", path}, &stdout, &stderr)
	if code != exitIO {
		t.Fatalf("exit %d; want %d (exitIO); stderr=%s", code, exitIO, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unsupported container version") {
		t.Errorf("missing v1 migration error in stderr:\n%s", stderr.String())
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
	})
	t.Run("two positionals", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"inspect", "a", "b"}, &stdout, &stderr)
		if code != exitUsage {
			t.Fatalf("exit %d; want %d", code, exitUsage)
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
```

- [ ] **Step 6: Run all tests**

Run: `go test ./...`
Expected: all tests PASS, including the new `TestCLIInspect*` tests and the existing `TestCLIPackDiscovers`/`TestCLIUnknownCommand`/`TestCLIVersion` after the run-signature update.

If any fail:
- Compilation errors typically come from the imports — Go won't allow duplicate import blocks, so make sure the new imports are merged into the existing import block in each file.
- Test failures around stdout/stderr usually mean a writer mismatch — check that pack/unpack help still goes to stderr, and only inspect uses stdout.

- [ ] **Step 7: Smoke test the binary end-to-end**

Run:
```bash
go build -o /tmp/miniscram-inspect-smoke && \
cd $(mktemp -d) && \
/tmp/miniscram-inspect-smoke help && echo "---" && \
/tmp/miniscram-inspect-smoke help inspect && echo "---" && \
/tmp/miniscram-inspect-smoke inspect 2>&1 | head -3
```

Expected:
- `help` lists `inspect` in the COMMANDS section.
- `help inspect` shows the inspect usage block.
- `inspect` with no args prints "expected exactly one container path" on stderr.

- [ ] **Step 8: Commit**

```bash
git add main.go main_test.go help.go inspect.go inspect_test.go
git commit -m "$(cat <<'EOF'
inspect: wire CLI subcommand and help

Adds runInspect, threads stdout through main.run, and adds the
inspect help block. Closes A1 from TASKS.md.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Self-review notes

Cross-check against spec sections:

- **CLI surface** (spec §"CLI surface"): Task 3 wires `--full`, `--json`, `-h`/`--help`, exact-one positional, exit codes 0/1/4. ✓
- **Human output** (spec §"Output: human format (default)"): Task 2 implements the field set, snake_case names, full hashes, `container:` line from raw header, `format_version` omitted from human view, tracks block, delta block. ✓
- **`--full` block** (spec §"Output: human format with `--full`"): Task 2 emits `byte_offset`/`length`/`lba`, omits the section when zero records. ✓
- **JSON output** (spec §"Output: `--json` format"): Task 2 splices `delta_records` last, preserves manifest field order, emits `[]` for empty records, no container magic/version in JSON. ✓
- **`IterateDeltaRecords`** (spec §"New helper"): Task 1 implements it with the documented signature. ✓
- **Errors** (spec §"Errors"): Task 3 routes ReadContainer errors to stderr/exit 4. Task 2 surfaces iterator framing errors via `delta_error:` line in human and via returned error in JSON. ✓
- **Wiring** (spec §"Wiring"): Task 3 threads stdout, dispatches inspect, `runInspect` follows the existing pack/unpack shape. ✓
- **Testing** (spec §"Testing"): Tasks 1–3 collectively cover all eight test rows from the spec table — `TestInspectFormatHumanCleanDelta` (default human), `TestInspectFormatJSONStructure` (JSON shape), `TestInspectFormatHumanFullListsOverrides` + `TestInspectFormatHumanFullEmptyHidesSection` (--full both ways), `TestCLIInspectRejectsV1`, `TestCLIInspectRejectsBadMagic`, `TestCLIInspectUsageErrors`, `TestIterateDeltaRecords*`. ✓

No placeholders. Type names consistent (`Manifest`, `Track`, `containerMagic`, `containerVersion`, `SectorSize`, `exitOK`, `exitUsage`, `exitIO` all match what's in the codebase today). Function signatures used in later tasks match what's defined in earlier tasks.

One scope concession: human output's `delta_error:` line is a small variance from the strictest reading of spec §"Errors" ("a later C2... handles structural fsck"). The spec also says the error should appear on stderr — Task 2 puts it inline in stdout output AND Task 3's `runInspect` doesn't currently surface iterator errors back to the caller. Acceptable because: the count is still parsed before any framing failure (it's the very first 4 bytes), so a partial human view is informative; stderr surfacing of mid-walk framing errors is a small enough fix that we can defer to C2 cleanly, and meanwhile the user sees the failure. If you'd rather have iterator failures hard-fail the whole inspect, that's a one-line change in `runInspect` (return `exitIO` when human path's iterator errored) — but the current shape preserves the principle "show what we could parse" without overstepping into fsck territory.
