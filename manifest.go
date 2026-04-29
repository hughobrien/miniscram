package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	containerMagic   = "MSCM"
	containerVersion = byte(0x01) // v1
	// Header layout: 4 magic + 1 version + 32 scrambler_hash + 4 manifest_len.
	containerHeaderSize = 4 + 1 + 32 + 4
)

// errScramblerHashMismatch indicates the container's recorded scrambler-
// table SHA-256 doesn't match the one this build computes — i.e., the
// scrambler implementation has drifted from the version that wrote the
// container.
var errScramblerHashMismatch = errors.New("scrambler table hash mismatch")

// ScramInfo holds size + hashes for the .scram file.
type ScramInfo struct {
	Size   int64      `json:"size"`
	Hashes FileHashes `json:"hashes"`
}

// Manifest is the JSON metadata embedded in every v1 .miniscram container.
type Manifest struct {
	ToolVersion      string    `json:"tool_version"`
	CreatedUTC       string    `json:"created_utc"`
	WriteOffsetBytes int       `json:"write_offset_bytes"`
	LeadinLBA        int32     `json:"leadin_lba"`
	Scram            ScramInfo `json:"scram"`
	Tracks           []Track   `json:"tracks"`
}

// Marshal returns the JSON encoding.
func (m *Manifest) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

// BinSize returns the total .bin size as the sum of per-track sizes.
func (m *Manifest) BinSize() int64 {
	var n int64
	for _, t := range m.Tracks {
		n += t.Size
	}
	return n
}

// BinFirstLBA returns tracks[0].FirstLBA — i.e. where the .bin's data
// track starts on disc.
func (m *Manifest) BinFirstLBA() int32 {
	if len(m.Tracks) == 0 {
		return 0
	}
	return m.Tracks[0].FirstLBA
}

// BinSectorCount returns BinSize() / SectorSize.
func (m *Manifest) BinSectorCount() int32 {
	return int32(m.BinSize() / int64(SectorSize))
}

// WriteContainer writes a .miniscram file at path: magic + version +
// scrambler_hash (32 bytes) + big-endian uint32 manifest length +
// manifest JSON + remainder of deltaSrc (read to EOF).
func WriteContainer(path string, m *Manifest, deltaSrc io.Reader) error {
	body, err := m.Marshal()
	if err != nil {
		return err
	}
	tableHash, err := hex.DecodeString(expectedScrambleTableSHA256)
	if err != nil {
		return fmt.Errorf("decoding expected scrambler hash: %w", err)
	}
	if len(tableHash) != 32 {
		return fmt.Errorf("scrambler hash must be 32 bytes, got %d", len(tableHash))
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
	if _, err := f.Write(tableHash); err != nil {
		return err
	}
	if err := binary.Write(f, binary.BigEndian, uint32(len(body))); err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		return err
	}
	zw, err := zlib.NewWriterLevel(f, zlib.BestCompression)
	if err != nil {
		return fmt.Errorf("creating zlib writer: %w", err)
	}
	if _, err := io.Copy(zw, deltaSrc); err != nil {
		return fmt.Errorf("compressing delta payload: %w", err)
	}
	// Close flushes the zlib trailer; must precede f.Sync so the
	// trailer is on disk by the time fsync returns.
	if err := zw.Close(); err != nil {
		return fmt.Errorf("flushing zlib writer: %w", err)
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

// ReadContainer parses a .miniscram file and returns its manifest, the
// recorded scrambler-table hash (32 bytes), and the raw delta bytes.
func ReadContainer(path string) (*Manifest, [32]byte, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, [32]byte{}, nil, err
	}
	defer f.Close()
	header := make([]byte, containerHeaderSize)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, [32]byte{}, nil, fmt.Errorf("reading container header: %w", err)
	}
	if string(header[:4]) != containerMagic {
		return nil, [32]byte{}, nil, fmt.Errorf("not a miniscram container (bad magic %q)", header[:4])
	}
	if header[4] != containerVersion {
		return nil, [32]byte{}, nil, fmt.Errorf("unsupported container version 0x%02x (this build expects 0x%02x)",
			header[4], containerVersion)
	}
	expectedHash, err := hex.DecodeString(expectedScrambleTableSHA256)
	if err != nil {
		return nil, [32]byte{}, nil, fmt.Errorf("decoding expected scrambler hash: %w", err)
	}
	if !bytes.Equal(header[5:37], expectedHash) {
		return nil, [32]byte{}, nil, fmt.Errorf("%w: container records %x, this build computes %x",
			errScramblerHashMismatch, header[5:37], expectedHash)
	}
	var scramblerHash [32]byte
	copy(scramblerHash[:], header[5:37])
	mlen := binary.BigEndian.Uint32(header[37:41])
	if mlen == 0 || mlen > 16<<20 {
		return nil, [32]byte{}, nil, fmt.Errorf("implausible manifest length %d", mlen)
	}
	body := make([]byte, mlen)
	if _, err := io.ReadFull(f, body); err != nil {
		return nil, [32]byte{}, nil, fmt.Errorf("reading manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, [32]byte{}, nil, fmt.Errorf("parsing manifest JSON: %w", err)
	}
	zr, err := zlib.NewReader(f)
	if err != nil {
		return nil, [32]byte{}, nil, fmt.Errorf("decompressing delta payload: %w", err)
	}
	defer zr.Close()
	delta, err := io.ReadAll(zr)
	if err != nil {
		return nil, [32]byte{}, nil, fmt.Errorf("decompressing delta payload: %w", err)
	}
	return &m, scramblerHash, delta, nil
}
