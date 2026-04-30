package main

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

// chunkLengthCap is the maximum payload length accepted for any chunk
// other than DLTA. Matches MAME CHD's metadata cap and prevents
// malloc(garbage) on a corrupt-but-CRC-valid hostile payload. DLTA is
// exempt because the delta payload is genuinely large.
const chunkLengthCap = 16 << 20 // 16 MiB

var crc32Table = crc32.IEEETable

// fourcc converts a 4-character ASCII string to a [4]byte at compile
// time. Panics if s is not exactly 4 bytes — only used with literal
// constants like "MFST", "TRKS", "HASH", "DLTA".
func fourcc(s string) [4]byte {
	if len(s) != 4 {
		panic(fmt.Sprintf("fourcc: %q is not 4 bytes", s))
	}
	var t [4]byte
	copy(t[:], s)
	return t
}

// dltaTag is the one tag exempt from the length cap.
var dltaTag = fourcc("DLTA")

// writeChunk emits a chunk: tag(4) + length(4 BE) + payload + CRC32(4 BE).
// CRC is computed over (tag || payload).
func writeChunk(w io.Writer, tag [4]byte, payload []byte) error {
	if _, err := w.Write(tag[:]); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint32(len(payload))); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	h := crc32.New(crc32Table)
	h.Write(tag[:])
	h.Write(payload)
	return binary.Write(w, binary.BigEndian, h.Sum32())
}

// readChunk reads one chunk and returns (tag, payload, err).
// On clean EOF before any byte is read, returns (_, _, io.EOF).
// On any partial read, wraps io.ErrUnexpectedEOF.
// Rejects length > chunkLengthCap for any tag other than DLTA.
func readChunk(r io.Reader) ([4]byte, []byte, error) {
	var head [8]byte
	n, err := io.ReadFull(r, head[:])
	if err == io.EOF && n == 0 {
		return [4]byte{}, nil, io.EOF
	}
	if err != nil {
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return [4]byte{}, nil, fmt.Errorf("reading chunk header: %w", io.ErrUnexpectedEOF)
		}
		return [4]byte{}, nil, fmt.Errorf("reading chunk header: %w", err)
	}
	var tag [4]byte
	copy(tag[:], head[:4])
	length := binary.BigEndian.Uint32(head[4:8])
	if tag != dltaTag && int(length) > chunkLengthCap {
		return tag, nil, fmt.Errorf("chunk %q length %d exceeds 16 MiB cap", tag, length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return tag, nil, fmt.Errorf("reading chunk %q payload: %w", tag, io.ErrUnexpectedEOF)
		}
		return tag, nil, err
	}
	var crcBuf [4]byte
	if _, err := io.ReadFull(r, crcBuf[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return tag, nil, fmt.Errorf("reading chunk %q crc: %w", tag, io.ErrUnexpectedEOF)
		}
		return tag, nil, err
	}
	wantCRC := binary.BigEndian.Uint32(crcBuf[:])
	h := crc32.New(crc32Table)
	h.Write(tag[:])
	h.Write(payload)
	if h.Sum32() != wantCRC {
		return tag, nil, fmt.Errorf("chunk %q crc mismatch", tag)
	}
	return tag, payload, nil
}
