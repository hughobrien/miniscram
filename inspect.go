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
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return keys
		}
	}
	return keys
}
