package main

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Wire format (big-endian):
//
//	u32 override_count
//	for each override:
//	  u64 file_offset
//	  u32 length          (1 ≤ length ≤ MaxDeltaRecordLength)
//	  length bytes payload
//
// MaxDeltaRecordLength is the upper bound enforced by readers (Apply,
// Iterate). It's a sanity ceiling for framing-corruption detection;
// real records are bounded by scram size at write time.
const MaxDeltaRecordLength = 1 << 30 // 1 GiB

// DeltaEncoder writes delta records to an io.Writer in two phases:
//  1. Append() buffers records in memory.
//  2. Close() emits the count header followed by the buffered body.
//
// This shape lets the encoder be driven by streaming sources (the
// builder's mismatch callback) without the caller having to count
// records ahead of time.
//
// Memory usage is proportional to total delta size; on healthy discs
// this is KiB-scale, even on copy-protected discs it stays below a few
// MiB. Call Close exactly once.
type DeltaEncoder struct {
	out   io.Writer
	body  []byte
	count uint32
}

func NewDeltaEncoder(out io.Writer) *DeltaEncoder {
	return &DeltaEncoder{out: out}
}

// Append records that the byte run starting at off should be applied
// to the output during ApplyDelta. A zero-length run is a no-op.
// Errors are deferred to Close — Append never fails.
func (e *DeltaEncoder) Append(off int64, run []byte) {
	if len(run) == 0 {
		return
	}
	var hdr [12]byte
	binary.BigEndian.PutUint64(hdr[:8], uint64(off))
	binary.BigEndian.PutUint32(hdr[8:], uint32(len(run)))
	e.body = append(e.body, hdr[:]...)
	e.body = append(e.body, run...)
	e.count++
}

// Close emits the count + body and returns the final count. Must be
// called exactly once per encoder.
func (e *DeltaEncoder) Close() (int, error) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], e.count)
	if _, err := e.out.Write(hdr[:]); err != nil {
		return 0, err
	}
	if _, err := e.out.Write(e.body); err != nil {
		return 0, err
	}
	return int(e.count), nil
}

// EncodeDelta walks epsilonHat and scram in lockstep, emitting one
// override record per contiguous mismatch run. Records can be of any
// length up to scramSize.
func EncodeDelta(out io.Writer, epsilonHat, scram io.Reader, scramSize int64) (int, error) {
	enc := NewDeltaEncoder(out)
	const chunk = 1 << 20
	hatBuf := make([]byte, chunk)
	scrBuf := make([]byte, chunk)
	var pos int64
	var run []byte
	var runStart int64
	for pos < scramSize {
		want := int64(chunk)
		if pos+want > scramSize {
			want = scramSize - pos
		}
		if _, err := io.ReadFull(epsilonHat, hatBuf[:want]); err != nil {
			return 0, fmt.Errorf("reading epsilonHat at %d: %w", pos, err)
		}
		if _, err := io.ReadFull(scram, scrBuf[:want]); err != nil {
			return 0, fmt.Errorf("reading scram at %d: %w", pos, err)
		}
		for i := int64(0); i < want; i++ {
			if hatBuf[i] != scrBuf[i] {
				if len(run) == 0 {
					runStart = pos + i
				}
				run = append(run, scrBuf[i])
			} else if len(run) > 0 {
				enc.Append(runStart, run)
				run = run[:0]
			}
		}
		pos += want
	}
	if len(run) > 0 {
		enc.Append(runStart, run)
	}
	return enc.Close()
}

// ApplyDelta reads override records from delta and writes their
// payloads at the recorded offsets in out.
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
		if length == 0 || length > MaxDeltaRecordLength {
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

// IterateDeltaRecords walks the override records in delta, calling fn
// for each record's byte offset and length. fn is not given the
// payload bytes; consumers like inspect/verify use it to enumerate
// records without materializing payloads.
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
		if length == 0 || length > MaxDeltaRecordLength {
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
