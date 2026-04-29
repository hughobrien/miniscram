package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SynthOpts configures synthDisc.
type SynthOpts struct {
	MainSectors    int
	WriteOffset    int // bytes
	LeadoutSectors int32
	Mode           string // default "MODE1/2352"
	ModeByte       byte   // default 0x01
	AudioTracks    int
	InjectErrors   []int // 0-based sector indices to corrupt in scram
}

// SynthDisc is the result of synthDisc.
type SynthDisc struct {
	Bin       []byte
	Scram     []byte
	Cue       string
	AudioBins [][]byte // one per AudioTrack
	LeadinLBA int32
}

// synthDisc builds an in-memory bin + scram pair satisfying opts.
func synthDisc(t *testing.T, opts SynthOpts) SynthDisc {
	t.Helper()
	mode := opts.Mode
	if mode == "" {
		mode = "MODE1/2352"
	}
	modeByte := opts.ModeByte
	if modeByte == 0 {
		modeByte = 0x01
	}
	if opts.MainSectors == 0 {
		opts.MainSectors = 10
	}
	if opts.LeadoutSectors == 0 {
		opts.LeadoutSectors = 5
	}
	const (
		leadinLBA    int32 = LBAPregapStart
		pregap             = 150
		audioSectors       = 50
	)

	// Data bin.
	bin := make([]byte, opts.MainSectors*SectorSize)
	for i := 0; i < opts.MainSectors; i++ {
		s := bin[i*SectorSize : (i+1)*SectorSize]
		copy(s[:SyncLen], Sync[:])
		m, sec, f := lbaToBCDMSF(int32(i))
		s[12], s[13], s[14], s[15] = m, sec, f, modeByte
		for j := 16; j < SectorSize; j++ {
			s[j] = byte(j * (i + 1))
		}
	}

	// Audio bins.
	audioBins := make([][]byte, opts.AudioTracks)
	for a := range audioBins {
		ab := make([]byte, audioSectors*SectorSize)
		for j := range ab {
			ab[j] = byte(j*3 + a*17)
		}
		audioBins[a] = ab
	}

	// Scram: pregap → data → audio → leadout (LBA order).
	lbaAudioStart := int32(opts.MainSectors)
	lbaLeadoutStart := lbaAudioStart + int32(opts.AudioTracks*audioSectors)
	totalSectors := int32(pregap+opts.MainSectors) + int32(opts.AudioTracks*audioSectors) + opts.LeadoutSectors
	scramLen := int64(totalSectors)*int64(SectorSize) + int64(opts.WriteOffset)
	if scramLen < 0 {
		scramLen = 0
	}
	scram := make([]byte, scramLen)
	for i := int32(0); i < totalSectors; i++ {
		lba := leadinLBA + i
		var sec [SectorSize]byte
		switch {
		case lba < 0:
			sec = generateMode1ZeroSector(lba)
		case lba < lbaAudioStart:
			copy(sec[:], bin[int(lba)*SectorSize:(int(lba)+1)*SectorSize])
			Scramble(&sec)
		case lba < lbaLeadoutStart:
			gi := int(lba - lbaAudioStart)
			a, wi := gi/audioSectors, gi%audioSectors
			if a < len(audioBins) {
				copy(sec[:], audioBins[a][wi*SectorSize:(wi+1)*SectorSize])
			}
		default:
			sec = generateLeadoutSector(lba)
		}
		writeAt(scram, int64(i)*int64(SectorSize)+int64(opts.WriteOffset), sec[:])
	}

	// Inject errors.
	for _, idx := range opts.InjectErrors {
		pos := (int64(pregap)+int64(idx))*int64(SectorSize) + int64(opts.WriteOffset) + 200
		if pos >= 0 && pos < int64(len(scram)) {
			scram[pos] ^= 0xFF
		}
	}

	// Cuesheet.
	cue := fmt.Sprintf("FILE \"x.bin\" BINARY\n  TRACK 01 %s\n    INDEX 01 00:00:00\n", mode)
	for a := 0; a < opts.AudioTracks; a++ {
		cue += fmt.Sprintf("FILE \"audio%c.bin\" BINARY\n  TRACK 02 AUDIO\n    INDEX 01 00:00:00\n", rune('1'+a))
	}

	return SynthDisc{Bin: bin, Scram: scram, Cue: cue, AudioBins: audioBins, LeadinLBA: leadinLBA}
}

// writeFixture writes disc files into dir. Returns bin/scram/cue paths.
func writeFixture(t *testing.T, dir string, disc SynthDisc) (binPath, scramPath, cuePath string) {
	t.Helper()
	binPath = filepath.Join(dir, "x.bin")
	scramPath = filepath.Join(dir, "x.scram")
	cuePath = filepath.Join(dir, "x.cue")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(binPath, disc.Bin, 0o644))
	must(os.WriteFile(scramPath, disc.Scram, 0o644))
	must(os.WriteFile(cuePath, []byte(disc.Cue), 0o644))
	for i, ab := range disc.AudioBins {
		must(os.WriteFile(filepath.Join(dir, fmt.Sprintf("audio%c.bin", rune('1'+i))), ab, 0o644))
	}
	return
}

// sampleManifest returns a deterministic Manifest for format tests.
func sampleManifest() *Manifest {
	return &Manifest{
		ToolVersion: "miniscram 1.0.0 (go1.22)", CreatedUTC: "2026-04-28T14:30:21Z",
		WriteOffsetBytes: -52, LeadinLBA: -150,
		Scram: ScramInfo{Size: 739729728, Hashes: FileHashes{
			MD5: strings.Repeat("1", 32), SHA1: strings.Repeat("2", 40), SHA256: strings.Repeat("c", 64),
		}},
		Tracks: []Track{{
			Number: 1, Mode: "MODE1/2352", FirstLBA: 0, Size: 235200, Filename: "x.bin",
			Hashes: FileHashes{
				MD5: strings.Repeat("a", 32), SHA1: strings.Repeat("b", 40), SHA256: strings.Repeat("d", 64),
			},
		}},
	}
}

// mustHashFile streams path through MD5+SHA-1+SHA-256 and returns all three.
func mustHashFile(t *testing.T, path string) FileHashes {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, s1, s256 := md5.New(), sha1.New(), sha256.New()
	if _, err := io.Copy(io.MultiWriter(m, s1, s256), f); err != nil {
		t.Fatal(err)
	}
	return FileHashes{
		MD5:    hex.EncodeToString(m.Sum(nil)),
		SHA1:   hex.EncodeToString(s1.Sum(nil)),
		SHA256: hex.EncodeToString(s256.Sum(nil)),
	}
}

// buildDelta returns a delta payload with one 1-byte override per offset.
func buildDelta(t *testing.T, offs []uint64) []byte {
	t.Helper()
	var buf bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(offs)))
	buf.Write(hdr[:])
	for _, off := range offs {
		var rec [12]byte
		binary.BigEndian.PutUint64(rec[:8], off)
		binary.BigEndian.PutUint32(rec[8:], 1)
		buf.Write(rec[:])
		buf.WriteByte(0xFF)
	}
	return buf.Bytes()
}
