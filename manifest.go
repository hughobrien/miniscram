package main

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	containerMagic   = "MSCM"
	containerVersion = byte(0x02) // v2
)

// fileHeaderSize is magic(4) + version(1).
const fileHeaderSize = 5

// ScramInfo holds size + hashes for the .scram file.
type ScramInfo struct {
	Size   int64      `json:"size"`
	Hashes FileHashes `json:"hashes"`
}

// Manifest is the metadata embedded in every v2 .miniscram container,
// encoded across MFST/TRKS/HASH chunks at write time. JSON tags are
// retained for the inspect --json output path; the on-disk format is
// chunk-based and does not use them.
type Manifest struct {
	ToolVersion      string    `json:"tool_version"`
	CreatedUnix      int64     `json:"created_unix"`
	WriteOffsetBytes int       `json:"write_offset_bytes"`
	LeadinLBA        int32     `json:"leadin_lba"`
	Scram            ScramInfo `json:"scram"`
	Tracks           []Track   `json:"tracks"`
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

// WriteContainer writes a v2 .miniscram file at path: 5-byte header
// (magic + version) followed by MFST, TRKS, HASH, DLTA chunks.
// Atomic: writes to a .tmp file then renames.
func WriteContainer(path string, m *Manifest, deltaSrc io.Reader) error {
	// Compress the delta into memory first — DLTA's chunk length must
	// be known up-front, and the delta is small (KiB to low MiB)
	// relative to scram (hundreds of MiB).
	var dltaBuf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&dltaBuf, zlib.BestCompression)
	if err != nil {
		return fmt.Errorf("creating zlib writer: %w", err)
	}
	if _, err := io.Copy(zw, deltaSrc); err != nil {
		return fmt.Errorf("compressing delta payload: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("flushing zlib writer: %w", err)
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
	if err := writeChunk(f, mfstTag, encodeMFSTPayload(m)); err != nil {
		return fmt.Errorf("writing MFST: %w", err)
	}
	if err := writeChunk(f, trksTag, encodeTRKSPayload(m.Tracks)); err != nil {
		return fmt.Errorf("writing TRKS: %w", err)
	}
	if err := writeChunk(f, hashTag, encodeHASHPayload(m)); err != nil {
		return fmt.Errorf("writing HASH: %w", err)
	}
	if err := writeChunk(f, dltaTag, dltaBuf.Bytes()); err != nil {
		return fmt.Errorf("writing DLTA: %w", err)
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

// ReadContainer parses a v2 .miniscram file and returns its manifest
// and the (zlib-decoded) raw delta bytes.
func ReadContainer(path string) (*Manifest, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var head [fileHeaderSize]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return nil, nil, fmt.Errorf("reading file header: %w", err)
	}
	if string(head[:4]) != containerMagic {
		return nil, nil, fmt.Errorf("not a miniscram container (bad magic %q)", head[:4])
	}
	if head[4] != containerVersion {
		return nil, nil, fmt.Errorf(
			"container version 0x%02x; this build only reads v2.\nrebuild miniscram from a matching commit:\nhttps://github.com/hughobrien/miniscram",
			head[4])
	}

	var (
		m            *Manifest
		dlta         []byte
		hashPayload  []byte
		seen         = map[[4]byte]int{}
		firstChunk   [4]byte
		firstChunkOK bool
	)
	for {
		tag, payload, err := readChunk(f)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if !firstChunkOK {
			firstChunk = tag
			firstChunkOK = true
		}
		seen[tag]++
		if seen[tag] > 1 && isCritical(tag) {
			return nil, nil, fmt.Errorf("duplicate chunk %q", tag)
		}
		switch tag {
		case mfstTag:
			// Always first per the post-loop check; reassign unconditionally.
			decoded, err := decodeMFSTPayload(payload)
			if err != nil {
				return nil, nil, err
			}
			m = decoded
		case trksTag:
			tracks, err := decodeTRKSPayload(payload)
			if err != nil {
				return nil, nil, err
			}
			if m == nil {
				m = &Manifest{}
			}
			m.Tracks = tracks
		case hashTag:
			// Defer HASH decode until post-loop, so missing/out-of-order
			// MFST/TRKS surface as their proper errors rather than as a
			// generic "HASH before MFST/TRKS".
			hashPayload = payload
		case dltaTag:
			zr, err := zlib.NewReader(bytes.NewReader(payload))
			if err != nil {
				return nil, nil, fmt.Errorf("decompressing delta payload: %w", err)
			}
			dlta, err = io.ReadAll(zr)
			zr.Close()
			if err != nil {
				return nil, nil, fmt.Errorf("decompressing delta payload: %w", err)
			}
		default:
			if isCritical(tag) {
				return nil, nil, fmt.Errorf("unsupported critical chunk %q", tag)
			}
			// Lowercase first letter — ancillary, skip silently.
		}
	}
	for _, required := range [][4]byte{mfstTag, trksTag, hashTag, dltaTag} {
		if seen[required] == 0 {
			return nil, nil, fmt.Errorf("missing required chunk %q", required)
		}
	}
	if firstChunkOK && firstChunk != mfstTag {
		return nil, nil, fmt.Errorf("MFST must be the first chunk")
	}
	if err := decodeHASHPayload(hashPayload, m); err != nil {
		return nil, nil, err
	}
	return m, dlta, nil
}

// isCritical reports whether a chunk's first byte is uppercase ASCII.
// Per spec, uppercase = critical (must be understood), lowercase =
// ancillary (readers may skip).
func isCritical(tag [4]byte) bool {
	return tag[0] >= 'A' && tag[0] <= 'Z'
}
