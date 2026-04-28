// /home/hugh/miniscram/delta.go
package main

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Wire format (big-endian):
//   u32 override_count
//   for each override:
//     u64 file_offset
//     u32 length    (1 ≤ length ≤ SectorSize)
//     length bytes  (payload to write at file_offset)

const deltaChunkSize = 1 << 20 // 1 MiB read-ahead chunk

// EncodeDelta walks epsilonHat and scram in lockstep, writing override
// records for byte ranges where they differ. Returns the override count.
//
// Both readers must yield exactly scramSize bytes.
func EncodeDelta(out io.Writer, epsilonHat, scram io.Reader, scramSize int64) (int, error) {
	var body []byte
	count := 0

	flush := func(off int64, run []byte) {
		i := 0
		for i < len(run) {
			n := len(run) - i
			if n > SectorSize {
				n = SectorSize
			}
			var hdr [12]byte
			binary.BigEndian.PutUint64(hdr[:8], uint64(off+int64(i)))
			binary.BigEndian.PutUint32(hdr[8:], uint32(n))
			body = append(body, hdr[:]...)
			body = append(body, run[i:i+n]...)
			count++
			i += n
		}
	}

	hatBuf := make([]byte, deltaChunkSize)
	scrBuf := make([]byte, deltaChunkSize)
	var pos int64
	var run []byte
	var runStart int64

	for pos < scramSize {
		want := int64(deltaChunkSize)
		if pos+want > scramSize {
			want = scramSize - pos
		}
		hn, err := io.ReadFull(epsilonHat, hatBuf[:want])
		if err != nil && err != io.ErrUnexpectedEOF {
			return 0, fmt.Errorf("reading epsilonHat at %d: %w", pos, err)
		}
		sn, err := io.ReadFull(scram, scrBuf[:want])
		if err != nil && err != io.ErrUnexpectedEOF {
			return 0, fmt.Errorf("reading scram at %d: %w", pos, err)
		}
		if hn != int(want) || sn != int(want) {
			return 0, fmt.Errorf("short read at %d (hat=%d scram=%d want=%d)", pos, hn, sn, want)
		}
		for i := int64(0); i < want; i++ {
			if hatBuf[i] != scrBuf[i] {
				if len(run) == 0 {
					runStart = pos + i
				}
				run = append(run, scrBuf[i])
			} else if len(run) > 0 {
				flush(runStart, run)
				run = run[:0]
			}
		}
		pos += want
	}
	if len(run) > 0 {
		flush(runStart, run)
	}

	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(count))
	if _, err := out.Write(hdr[:]); err != nil {
		return 0, err
	}
	if _, err := out.Write(body); err != nil {
		return 0, err
	}
	return count, nil
}

// ApplyDelta reads override records from delta and writes their
// payloads at the recorded offsets in out. out must already contain
// the ε̂ buffer; this overlays the differences.
func ApplyDelta(out io.WriterAt, delta io.Reader) error {
	var hdr [4]byte
	if _, err := io.ReadFull(delta, hdr[:]); err != nil {
		return fmt.Errorf("reading override count: %w", err)
	}
	count := binary.BigEndian.Uint32(hdr[:])
	for i := uint32(0); i < count; i++ {
		var rec [12]byte
		if _, err := io.ReadFull(delta, rec[:]); err != nil {
			return fmt.Errorf("reading override %d header: %w", i, err)
		}
		offset := int64(binary.BigEndian.Uint64(rec[:8]))
		length := binary.BigEndian.Uint32(rec[8:])
		if length == 0 || length > SectorSize {
			return fmt.Errorf("override %d has implausible length %d", i, length)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(delta, payload); err != nil {
			return fmt.Errorf("reading override %d payload: %w", i, err)
		}
		if _, err := out.WriteAt(payload, offset); err != nil {
			return fmt.Errorf("writing override %d at %d: %w", i, offset, err)
		}
	}
	return nil
}
