package main

import (
	"bytes"
	"encoding/hex"
	mathrand "math/rand"
	"path/filepath"
	"reflect"
	"testing"
	"testing/quick"
)

// TASKS.md Theme E1 candidates not covered here, with reasons:
//
//   - BuildEpsilonHat lockstep — needs a full valid disc-layout
//     generator (BinFirstLBA, BinSectorCount, scram size, tracks,
//     write offset, all consistent). Defer to a focused follow-up.
//   - ScramOffset / TotalLBAs algebraic identity — needs a generator
//     for valid (scramSize, writeOffsetBytes) pairs satisfying the
//     `scramSize > |writeOffsetBytes|` precondition. Defer.
//   - IterateDeltaRecords round-trip — needs a generator for valid
//     delta byte streams. Defer.

// TestScrambleInvolutionProperty: Scramble XORs sector bytes 12..2351
// with the Annex B keystream, so applying it twice is identity.
func TestScrambleInvolutionProperty(t *testing.T) {
	f := func(seed [SectorSize]byte) bool {
		original := seed
		Scramble(&seed)
		Scramble(&seed)
		return seed == original
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 1000}); err != nil {
		t.Fatal(err)
	}
}

// TestBCDMSFRoundTripProperty: BCDMSFToLBA(LBAToBCDMSF(lba)) == lba
// across the addressable LBA range [-150, 99·60·75 - 150).
func TestBCDMSFRoundTripProperty(t *testing.T) {
	const (
		minLBA = -150
		maxLBA = 99*60*75 - 150 // 445350 (exclusive)
	)
	f := func(raw int32) bool {
		// Map the raw int32 onto the valid LBA range.
		span := int64(maxLBA - minLBA)
		lba := int32(int64(uint32(raw))%span) + minLBA
		bcd := LBAToBCDMSF(lba)
		got := BCDMSFToLBA(bcd)
		return got == lba
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 1000}); err != nil {
		t.Fatal(err)
	}
}

// TestMFSTCodecRoundTripProperty: encode/decode round-trip for the
// MFST chunk codec on randomized inputs.
func TestMFSTCodecRoundTripProperty(t *testing.T) {
	f := func(toolVersion string, createdUnix int64, writeOffset int32, leadinLBA int32, scramSize int64) bool {
		// tool_version_len is uint16 — clamp to <= 65535.
		if len(toolVersion) > 0xFFFF {
			toolVersion = toolVersion[:0xFFFF]
		}
		in := &Manifest{
			ToolVersion:      toolVersion,
			CreatedUnix:      createdUnix,
			WriteOffsetBytes: int(writeOffset),
			LeadinLBA:        leadinLBA,
			Scram:            ScramInfo{Size: scramSize},
		}
		out, err := decodeMFSTPayload(encodeMFSTPayload(in))
		if err != nil {
			return false
		}
		return out.ToolVersion == in.ToolVersion &&
			out.CreatedUnix == in.CreatedUnix &&
			out.WriteOffsetBytes == in.WriteOffsetBytes &&
			out.LeadinLBA == in.LeadinLBA &&
			out.Scram.Size == in.Scram.Size
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 1000}); err != nil {
		t.Fatal(err)
	}
}

// TestTRKSCodecRoundTripProperty: encode/decode round-trip for the
// TRKS chunk codec on randomized track slices.
func TestTRKSCodecRoundTripProperty(t *testing.T) {
	f := func(rawTracks []Track) bool {
		// Sanitize: number fits uint8, mode_len fits uint8, filename_len
		// fits uint16. Zero per-track Hashes since TRKS doesn't encode them.
		clean := make([]Track, len(rawTracks))
		for i, tk := range rawTracks {
			tk.Number = tk.Number & 0xFF
			if len(tk.Mode) > 0xFF {
				tk.Mode = tk.Mode[:0xFF]
			}
			if len(tk.Filename) > 0xFFFF {
				tk.Filename = tk.Filename[:0xFFFF]
			}
			tk.Hashes = FileHashes{}
			clean[i] = tk
		}
		out, err := decodeTRKSPayload(encodeTRKSPayload(clean))
		if err != nil {
			return false
		}
		if len(out) != len(clean) {
			return false
		}
		for i := range clean {
			a, b := clean[i], out[i]
			if a.Number != b.Number || a.Mode != b.Mode ||
				a.FirstLBA != b.FirstLBA || a.Size != b.Size ||
				a.Filename != b.Filename {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 1000}); err != nil {
		t.Fatal(err)
	}
}

// validHashes wraps a set of valid (correct-length hex) FileHashes
// for the scram file plus 0..4 tracks. Implements quick.Generator so
// the encoder's panic-on-bad-hex never fires.
type validHashes struct {
	scram  FileHashes
	tracks []FileHashes
}

func (validHashes) Generate(rand *mathrand.Rand, _ int) reflect.Value {
	hexN := func(n int) string {
		b := make([]byte, n)
		rand.Read(b)
		return hex.EncodeToString(b)
	}
	mkHashes := func() FileHashes {
		return FileHashes{MD5: hexN(16), SHA1: hexN(20), SHA256: hexN(32)}
	}
	v := validHashes{scram: mkHashes()}
	for i := 0; i < rand.Intn(5); i++ {
		v.tracks = append(v.tracks, mkHashes())
	}
	return reflect.ValueOf(v)
}

// TestHASHCodecRoundTripProperty: encode/decode round-trip for the
// HASH chunk codec on randomized (but length-valid) hash inputs.
func TestHASHCodecRoundTripProperty(t *testing.T) {
	f := func(v validHashes) bool {
		in := &Manifest{
			Scram:  ScramInfo{Hashes: v.scram},
			Tracks: make([]Track, len(v.tracks)),
		}
		for i := range v.tracks {
			in.Tracks[i].Hashes = v.tracks[i]
		}
		out := &Manifest{Tracks: make([]Track, len(v.tracks))}
		if err := decodeHASHPayload(encodeHASHPayload(in), out); err != nil {
			return false
		}
		if out.Scram.Hashes != in.Scram.Hashes {
			return false
		}
		for i := range in.Tracks {
			if out.Tracks[i].Hashes != in.Tracks[i].Hashes {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 1000}); err != nil {
		t.Fatal(err)
	}
}

// validContainer is a fully-valid (Manifest + delta) bundle for
// exercising WriteContainer/ReadContainer end-to-end.
type validContainer struct {
	tools     string
	created   int64
	writeOff  int32
	leadinLBA int32
	scramSize int64
	tracks    []validTrack
	scramH    FileHashes
	delta     []byte
}

type validTrack struct {
	Number   uint8
	Mode     string
	FirstLBA int32
	Size     int64
	Filename string
	Hashes   FileHashes
}

func (validContainer) Generate(rand *mathrand.Rand, _ int) reflect.Value {
	hexN := func(n int) string {
		b := make([]byte, n)
		rand.Read(b)
		return hex.EncodeToString(b)
	}
	mkHashes := func() FileHashes {
		return FileHashes{MD5: hexN(16), SHA1: hexN(20), SHA256: hexN(32)}
	}
	v := validContainer{
		tools:     "miniscram-prop",
		created:   rand.Int63(),
		writeOff:  rand.Int31() - (1 << 30),
		leadinLBA: rand.Int31() - (1 << 30),
		scramSize: rand.Int63(),
		scramH:    mkHashes(),
	}
	nTracks := 1 + rand.Intn(4)
	for i := 0; i < nTracks; i++ {
		fnLen := 1 + rand.Intn(20)
		fn := make([]byte, fnLen)
		rand.Read(fn)
		// Filename can be arbitrary bytes; hex-encode to keep printable.
		v.tracks = append(v.tracks, validTrack{
			Number:   uint8(i + 1),
			Mode:     "MODE1/2352",
			FirstLBA: rand.Int31() - (1 << 30),
			Size:     rand.Int63(),
			Filename: hex.EncodeToString(fn),
			Hashes:   mkHashes(),
		})
	}
	deltaLen := rand.Intn(1024)
	v.delta = make([]byte, deltaLen)
	rand.Read(v.delta)
	return reflect.ValueOf(v)
}

// TestContainerRoundTripProperty: full WriteContainer/ReadContainer
// round-trip via tempfile, including all four chunks and zlib-encoded
// delta payload.
func TestContainerRoundTripProperty(t *testing.T) {
	f := func(v validContainer) bool {
		m := &Manifest{
			ToolVersion:      v.tools,
			CreatedUnix:      v.created,
			WriteOffsetBytes: int(v.writeOff),
			LeadinLBA:        v.leadinLBA,
			Scram:            ScramInfo{Size: v.scramSize, Hashes: v.scramH},
		}
		for _, vt := range v.tracks {
			m.Tracks = append(m.Tracks, Track{
				Number: int(vt.Number), Mode: vt.Mode, FirstLBA: vt.FirstLBA,
				Size: vt.Size, Filename: vt.Filename, Hashes: vt.Hashes,
			})
		}
		path := filepath.Join(t.TempDir(), "x.miniscram")
		if err := WriteContainer(path, m, bytes.NewReader(v.delta)); err != nil {
			return false
		}
		gotM, gotDelta, err := ReadContainer(path)
		if err != nil {
			return false
		}
		if gotM.ToolVersion != m.ToolVersion || gotM.CreatedUnix != m.CreatedUnix ||
			gotM.WriteOffsetBytes != m.WriteOffsetBytes || gotM.LeadinLBA != m.LeadinLBA ||
			gotM.Scram.Size != m.Scram.Size || gotM.Scram.Hashes != m.Scram.Hashes {
			return false
		}
		if len(gotM.Tracks) != len(m.Tracks) {
			return false
		}
		for i := range m.Tracks {
			if gotM.Tracks[i] != m.Tracks[i] {
				return false
			}
		}
		if !bytes.Equal(gotDelta, v.delta) {
			return false
		}
		return true
	}
	// MaxCount: 100 — each iteration involves a tempfile + zlib roundtrip.
	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}
