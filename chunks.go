package main

import (
	"encoding/binary"
	"encoding/hex"
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

var (
	mfstTag = fourcc("MFST")
	trksTag = fourcc("TRKS")
	hashTag = fourcc("HASH")
)

// encodeMFSTPayload emits the MFST chunk payload per spec §"MFST":
// tool_version_len(uint16 BE) || tool_version(UTF-8) ||
// created_unix(int64 BE) || write_offset_bytes(int32 BE) ||
// leadin_lba(int32 BE) || scram_size(int64 BE).
func encodeMFSTPayload(m *Manifest) []byte {
	var b []byte
	tv := []byte(m.ToolVersion)
	b = binary.BigEndian.AppendUint16(b, uint16(len(tv)))
	b = append(b, tv...)
	b = binary.BigEndian.AppendUint64(b, uint64(m.CreatedUnix))
	b = binary.BigEndian.AppendUint32(b, uint32(int32(m.WriteOffsetBytes)))
	b = binary.BigEndian.AppendUint32(b, uint32(m.LeadinLBA))
	b = binary.BigEndian.AppendUint64(b, uint64(m.Scram.Size))
	return b
}

// decodeMFSTPayload inverts encodeMFSTPayload. Populates only the
// MFST scalar fields on the returned Manifest.
func decodeMFSTPayload(payload []byte) (*Manifest, error) {
	r := payloadReader{buf: payload}
	tvLen, err := r.uint16()
	if err != nil {
		return nil, fmt.Errorf("MFST tool_version_len: %w", err)
	}
	tv, err := r.bytes(int(tvLen))
	if err != nil {
		return nil, fmt.Errorf("MFST tool_version: %w", err)
	}
	created, err := r.uint64()
	if err != nil {
		return nil, fmt.Errorf("MFST created_unix: %w", err)
	}
	wo, err := r.uint32()
	if err != nil {
		return nil, fmt.Errorf("MFST write_offset_bytes: %w", err)
	}
	lba, err := r.uint32()
	if err != nil {
		return nil, fmt.Errorf("MFST leadin_lba: %w", err)
	}
	ss, err := r.uint64()
	if err != nil {
		return nil, fmt.Errorf("MFST scram_size: %w", err)
	}
	return &Manifest{
		ToolVersion:      string(tv),
		CreatedUnix:      int64(created),
		WriteOffsetBytes: int(int32(wo)),
		LeadinLBA:        int32(lba),
		Scram:            ScramInfo{Size: int64(ss)},
	}, nil
}

// encodeTRKSPayload emits the TRKS chunk payload per spec §"TRKS".
// Per-track Hashes are emitted in the HASH chunk, not here.
func encodeTRKSPayload(tracks []Track) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, uint16(len(tracks)))
	for _, t := range tracks {
		mode := []byte(t.Mode)
		filename := []byte(t.Filename)
		b = append(b, byte(t.Number), byte(len(mode)))
		b = append(b, mode...)
		b = binary.BigEndian.AppendUint32(b, uint32(t.FirstLBA))
		b = binary.BigEndian.AppendUint64(b, uint64(t.Size))
		b = binary.BigEndian.AppendUint16(b, uint16(len(filename)))
		b = append(b, filename...)
	}
	return b
}

// decodeTRKSPayload inverts encodeTRKSPayload. Per-track Hashes are
// left zero; HASH chunk populates them.
func decodeTRKSPayload(payload []byte) ([]Track, error) {
	r := payloadReader{buf: payload}
	count, err := r.uint16()
	if err != nil {
		return nil, fmt.Errorf("TRKS count: %w", err)
	}
	tracks := make([]Track, count)
	for i := range tracks {
		num, err := r.uint8()
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] number: %w", i, err)
		}
		modeLen, err := r.uint8()
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] mode_len: %w", i, err)
		}
		mode, err := r.bytes(int(modeLen))
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] mode: %w", i, err)
		}
		firstLBA, err := r.uint32()
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] first_lba: %w", i, err)
		}
		size, err := r.uint64()
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] size: %w", i, err)
		}
		fnLen, err := r.uint16()
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] filename_len: %w", i, err)
		}
		fn, err := r.bytes(int(fnLen))
		if err != nil {
			return nil, fmt.Errorf("TRKS track[%d] filename: %w", i, err)
		}
		tracks[i] = Track{
			Number:   int(num),
			Mode:     string(mode),
			FirstLBA: int32(firstLBA),
			Size:     int64(size),
			Filename: string(fn),
		}
	}
	if !r.eof() {
		return nil, fmt.Errorf("TRKS: %d trailing bytes after %d tracks", len(payload)-r.pos, count)
	}
	return tracks, nil
}

// hashAlgoLen maps the on-disk algo tag to its expected digest length.
var hashAlgoLen = map[[4]byte]int{
	fourcc("MD5 "): 16,
	fourcc("SHA1"): 20,
	fourcc("S256"): 32,
}

// encodeHASHPayload emits the HASH chunk payload per spec §"HASH".
// One record per (file × algorithm).
func encodeHASHPayload(m *Manifest) []byte {
	var b []byte
	count := uint16((1 + len(m.Tracks)) * 3) // scram + tracks, each × MD5/SHA1/SHA256
	b = binary.BigEndian.AppendUint16(b, count)

	emit := func(target uint8, h FileHashes) {
		b = appendHashRecord(b, target, fourcc("MD5 "), h.MD5, 16)
		b = appendHashRecord(b, target, fourcc("SHA1"), h.SHA1, 20)
		b = appendHashRecord(b, target, fourcc("S256"), h.SHA256, 32)
	}
	emit(0, m.Scram.Hashes)
	for i, t := range m.Tracks {
		emit(uint8(i+1), t.Hashes)
	}
	return b
}

// appendHashRecord parses the hex string and appends one record.
// Panics if the hex doesn't decode to exactly digestLen bytes — only
// called from encodeHASHPayload, where lengths are invariants.
func appendHashRecord(b []byte, target uint8, algo [4]byte, hexDigest string, digestLen int) []byte {
	digest, err := hex.DecodeString(hexDigest)
	if err != nil || len(digest) != digestLen {
		panic(fmt.Sprintf("HASH encode: bad %v digest %q (decode err %v, len %d, want %d)",
			algo, hexDigest, err, len(digest), digestLen))
	}
	b = append(b, target)
	b = append(b, algo[:]...)
	b = append(b, byte(digestLen))
	b = append(b, digest...)
	return b
}

// decodeHASHPayload reads the HASH chunk and populates Hashes fields
// on the supplied Manifest. m.Tracks must already be sized to match
// what encodeHASHPayload produced (i.e., decodeTRKSPayload ran first).
func decodeHASHPayload(payload []byte, m *Manifest) error {
	r := payloadReader{buf: payload}
	count, err := r.uint16()
	if err != nil {
		return fmt.Errorf("HASH count: %w", err)
	}
	for i := uint16(0); i < count; i++ {
		target, err := r.uint8()
		if err != nil {
			return fmt.Errorf("HASH record[%d] target: %w", i, err)
		}
		algoBytes, err := r.bytes(4)
		if err != nil {
			return fmt.Errorf("HASH record[%d] algo: %w", i, err)
		}
		var algo [4]byte
		copy(algo[:], algoBytes)
		want, ok := hashAlgoLen[algo]
		if !ok {
			return fmt.Errorf("HASH record[%d]: unknown algo %q", i, algo)
		}
		digestLen, err := r.uint8()
		if err != nil {
			return fmt.Errorf("HASH record[%d] digest_len: %w", i, err)
		}
		if int(digestLen) != want {
			return fmt.Errorf("HASH record[%d] %q: digest length %d, want %d", i, algo, digestLen, want)
		}
		digest, err := r.bytes(int(digestLen))
		if err != nil {
			return fmt.Errorf("HASH record[%d] digest: %w", i, err)
		}
		hexDigest := hex.EncodeToString(digest)
		var dest *FileHashes
		switch {
		case target == 0:
			dest = &m.Scram.Hashes
		case int(target) <= len(m.Tracks):
			dest = &m.Tracks[target-1].Hashes
		default:
			return fmt.Errorf("HASH record[%d]: target %d out of range (have %d tracks)", i, target, len(m.Tracks))
		}
		switch algo {
		case fourcc("MD5 "):
			dest.MD5 = hexDigest
		case fourcc("SHA1"):
			dest.SHA1 = hexDigest
		case fourcc("S256"):
			dest.SHA256 = hexDigest
		default:
			panic(fmt.Sprintf("HASH decode: algo %q present in hashAlgoLen but not handled in switch — update both", algo))
		}
	}
	if !r.eof() {
		return fmt.Errorf("HASH: %d trailing bytes after %d records", len(payload)-r.pos, count)
	}
	return nil
}

// payloadReader is a thin cursor over a byte slice that returns
// io.ErrUnexpectedEOF on any short read, with helper methods for
// the integer widths the codecs use.
type payloadReader struct {
	buf []byte
	pos int
}

func (r *payloadReader) bytes(n int) ([]byte, error) {
	if r.pos+n > len(r.buf) {
		return nil, io.ErrUnexpectedEOF
	}
	out := r.buf[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *payloadReader) uint8() (uint8, error) {
	b, err := r.bytes(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (r *payloadReader) uint16() (uint16, error) {
	b, err := r.bytes(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (r *payloadReader) uint32() (uint32, error) {
	b, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (r *payloadReader) uint64() (uint64, error) {
	b, err := r.bytes(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

func (r *payloadReader) eof() bool { return r.pos == len(r.buf) }

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
