package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// formatHumanInspect produces the default `miniscram inspect` text
// output. magic and version come from the raw container header. delta
// is the full delta payload as returned by ReadContainer. If full is
// true and there is at least one override record, an `overrides:`
// block is appended.
//
// On a framing error walking the delta, partial output before the
// failure is returned as the string and the iterator error is returned
// as the error. The caller is responsible for routing the error to
// stderr and producing the I/O exit code (per spec §Errors).
func formatHumanInspect(m *Manifest, magic string, version byte, delta []byte, full bool) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "container:  %s v%d\n", magic, version)
	b.WriteString("manifest:\n")
	fmt.Fprintf(&b, "  tool_version:           %s\n", m.ToolVersion)
	fmt.Fprintf(&b, "  created_utc:            %s\n", time.Unix(m.CreatedUnix, 0).UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "  write_offset_bytes:     %d\n", m.WriteOffsetBytes)
	fmt.Fprintf(&b, "  leadin_lba:             %d\n", m.LeadinLBA)
	fmt.Fprintf(&b, "  scram.size:             %d\n", m.Scram.Size)
	fmt.Fprintf(&b, "  scram.hashes.md5:       %s\n", m.Scram.Hashes.MD5)
	fmt.Fprintf(&b, "  scram.hashes.sha1:      %s\n", m.Scram.Hashes.SHA1)
	fmt.Fprintf(&b, "  scram.hashes.sha256:    %s\n", m.Scram.Hashes.SHA256)

	b.WriteString("tracks:\n")
	maxMode := 0
	for _, t := range m.Tracks {
		if len(t.Mode) > maxMode {
			maxMode = len(t.Mode)
		}
	}
	for _, t := range m.Tracks {
		fmt.Fprintf(&b, "  track %d: %-*s  first_lba=%d  size=%d  filename=%s\n",
			t.Number, maxMode, t.Mode, t.FirstLBA, t.Size, t.Filename)
		fmt.Fprintf(&b, "    md5:    %s\n", t.Hashes.MD5)
		fmt.Fprintf(&b, "    sha1:   %s\n", t.Hashes.SHA1)
		fmt.Fprintf(&b, "    sha256: %s\n", t.Hashes.SHA256)
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
	if full && len(records) > 0 {
		// Sort by offset for deterministic output.
		sort.Slice(records, func(i, j int) bool { return records[i].off < records[j].off })
		b.WriteString("overrides:\n")
		for _, r := range records {
			lba := int64(r.off)/int64(SectorSize) + int64(m.BinFirstLBA())
			fmt.Fprintf(&b, "  byte_offset=%-12d length=%-5d lba=%d\n", r.off, r.length, lba)
		}
	}
	return b.String(), iterErr
}

// formatJSONInspect emits the manifest JSON plus a top-level
// `delta_records` array of {byte_offset, length, lba} objects.
// Always includes all records (no cap).
func formatJSONInspect(m *Manifest, delta []byte) ([]byte, error) {
	type recordOut struct {
		ByteOffset uint64 `json:"byte_offset"`
		Length     uint32 `json:"length"`
		LBA        int64  `json:"lba"`
	}
	var records []recordOut
	if _, err := IterateDeltaRecords(delta, func(off uint64, length uint32) error {
		lba := int64(off)/int64(SectorSize) + int64(m.BinFirstLBA())
		records = append(records, recordOut{ByteOffset: off, Length: length, LBA: lba})
		return nil
	}); err != nil {
		return nil, err
	}
	if records == nil {
		records = []recordOut{}
	}
	type out struct {
		*Manifest
		DeltaRecords []recordOut `json:"delta_records"`
	}
	return json.Marshal(out{Manifest: m, DeltaRecords: records})
}

// runInspect is the CLI entry point for `miniscram inspect`. Reads the
// container, formats per --json/--full flags, writes to stdout. Errors
// go to stderr and produce exit code 4 (I/O); usage errors produce 1.
func runInspect(args []string, stdout, stderr io.Writer) int {
	var full, asJSON bool
	positional, _, exit, ok := parseSubcommand("inspect", inspectHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&full, "full", false, "list every override record")
		fs.BoolVar(&asJSON, "json", false, "machine-readable JSON")
	})
	if !ok {
		return exit
	}
	if !requireOnePositional(stderr, inspectHelpText, positional, "container path") {
		return exitUsage
	}
	path := positional[0]
	m, delta, err := ReadContainer(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitIO
	}
	if asJSON {
		body, err := formatJSONInspect(m, delta)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitIO
		}
		fmt.Fprintln(stdout, string(body))
		return exitOK
	}
	human, ferr := formatHumanInspect(m, containerMagic, containerVersion, delta, full)
	fmt.Fprint(stdout, human)
	if ferr != nil {
		fmt.Fprintln(stderr, ferr)
		return exitIO
	}
	return exitOK
}
