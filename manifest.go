// /home/hugh/miniscram/manifest.go
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const (
	containerMagic      = "MSCM"
	containerVersion    = byte(0x03)
	errorSectorsListCap = 10000
)

// Manifest is the JSON metadata embedded in every .miniscram container.
type Manifest struct {
	FormatVersion    int     `json:"format_version"`
	ToolVersion      string  `json:"tool_version"`
	CreatedUTC       string  `json:"created_utc"`
	ScramSize        int64   `json:"scram_size"`
	ScramMD5         string  `json:"scram_md5"`
	ScramSHA1        string  `json:"scram_sha1"`
	ScramSHA256      string  `json:"scram_sha256"`
	BinSize          int64   `json:"bin_size"`
	BinMD5           string  `json:"bin_md5"`
	BinSHA1          string  `json:"bin_sha1"`
	BinSHA256        string  `json:"bin_sha256"`
	WriteOffsetBytes int     `json:"write_offset_bytes"`
	LeadinLBA        int32   `json:"leadin_lba"`
	Tracks           []Track `json:"tracks"`
	BinFirstLBA      int32   `json:"bin_first_lba"`
	BinSectorCount   int32   `json:"bin_sector_count"`
	ErrorSectors     []int32 `json:"error_sectors,omitempty"`
	// ErrorSectorCount is the number of sectors that required a delta
	// override at pack time — i.e., where Pack's bin-driven prediction
	// disagreed with the real .scram. This is NOT the same as Redumper's
	// "errors count" (which counts only data-track ECC/EDC errors): it
	// also includes lead-in noise from drive-buffered reads of scratched
	// areas and any boundary-sector mismatches. For a SafeDisc 2.70 disc
	// like Freelancer, expect this to be in the low thousands while
	// Redumper reports ~588 — both numbers are correct, they just count
	// different things. See e2e_redump_test.go's countDataTrackErrors
	// helper for the Redumper-equivalent metric.
	ErrorSectorCount     int    `json:"error_sector_count"`
	DeltaSize            int64  `json:"delta_size"`
	ScramblerTableSHA256 string `json:"scrambler_table_sha256"`
}

// Marshal returns the JSON encoding of m, dropping ErrorSectors when
// the list exceeds errorSectorsListCap.
func (m *Manifest) Marshal() ([]byte, error) {
	clone := *m
	if len(clone.ErrorSectors) > errorSectorsListCap {
		clone.ErrorSectors = nil
	}
	return json.Marshal(&clone)
}

// WriteContainer writes a .miniscram file at path: magic + version +
// big-endian uint32 manifest length + manifest JSON + remainder of
// deltaSrc (read to EOF).
func WriteContainer(path string, m *Manifest, deltaSrc io.Reader) error {
	body, err := m.Marshal()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write([]byte(containerMagic)); err != nil {
		return err
	}
	if _, err := f.Write([]byte{containerVersion}); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, uint32(len(body))); err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		return err
	}
	if _, err := io.Copy(f, deltaSrc); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	closed = true
	return os.Rename(tmp, path)
}

// ReadContainer parses a .miniscram file and returns its manifest plus
// the raw delta bytes.
func ReadContainer(path string) (*Manifest, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	header := make([]byte, 4+1+4)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, nil, fmt.Errorf("reading container header: %w", err)
	}
	if string(header[:4]) != containerMagic {
		return nil, nil, fmt.Errorf("not a miniscram container (bad magic %q)", header[:4])
	}
	if header[4] != containerVersion {
		return nil, nil, fmt.Errorf("unsupported container version 0x%02x (this build expects 0x%02x); "+
			"v0.2 .miniscram files cannot be read directly by this build — re-pack from the original .bin",
			header[4], containerVersion)
	}
	mlen := binary.BigEndian.Uint32(header[5:9])
	if mlen == 0 || mlen > 16<<20 {
		return nil, nil, fmt.Errorf("implausible manifest length %d", mlen)
	}
	body := make([]byte, mlen)
	if _, err := io.ReadFull(f, body); err != nil {
		return nil, nil, fmt.Errorf("reading manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, nil, fmt.Errorf("parsing manifest JSON: %w", err)
	}
	delta, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	return &m, delta, nil
}
