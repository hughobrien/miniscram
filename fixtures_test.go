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
		ToolVersion: "miniscram 1.0.0", CreatedUnix: 1714435200,
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

// synthDiscWithFailSector returns a synthetic single-track Mode 1 disc whose
// data track contains one "fail" sector at failOffset (0-based sector index
// from track start, i.e. LBA = failOffset). The fail sector has a valid
// scrambled sync but an invalid mode byte (0xF7), so classifyBinSector
// returns false and the packer passes it through unchanged — the sector
// appears as identical bytes in both .bin and .scram.
//
// Used by TestE2EFailSectorRoundTrip to verify the pass-through path
// round-trips byte-for-byte.
func synthDiscWithFailSector(t *testing.T, mainSectors, leadoutSectors, failOffset int) SynthDisc {
	t.Helper()
	disc := synthDisc(t, SynthOpts{
		MainSectors:    mainSectors,
		LeadoutSectors: int32(leadoutSectors),
	})

	// Build the fail sector: valid sync, deterministic non-zero noise
	// elsewhere, invalid mode byte 0xF7.
	var failSec [SectorSize]byte
	copy(failSec[:SyncLen], Sync[:])
	for i := SyncLen; i < SectorSize; i++ {
		failSec[i] = byte(i*7 + 13)
	}
	failSec[15] = 0xF7 // invalid mode — classifier returns false

	// Overwrite bin at failOffset.
	binOff := failOffset * SectorSize
	copy(disc.Bin[binOff:binOff+SectorSize], failSec[:])

	// Overwrite scram at the corresponding position. The synthetic scram
	// starts at LBAPregapStart (-150) with zero write offset; for a data
	// LBA = failOffset the scram index is (failOffset - LBAPregapStart).
	scramIdx := failOffset - int(LBAPregapStart) // failOffset + 150
	scramOff := scramIdx * SectorSize
	copy(disc.Scram[scramOff:scramOff+SectorSize], failSec[:])

	return disc
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

// SynthEnhancedCDOpts configures synthEnhancedCD.
type SynthEnhancedCDOpts struct {
	AudioTracks       int   // number of audio tracks in session 1 (default 3)
	AudioSectorsEach  int32 // sectors per audio track (default 200)
	DataSectors       int32 // sectors in the session-2 data track (default 500)
	GapLeadoutSectors int32 // sec.1 lead-out (default 6750, redumper minimum)
	GapLeadinSectors  int32 // sec.2 lead-in  (default 4500)
	GapPregapSectors  int32 // sec.2 pregap   (default 150)
	TrailingLeadout   int32 // post-disc leadout sectors (default 5)
	WriteOffset       int   // bytes (default 0)
}

// SynthEnhancedCD is the result of synthEnhancedCD.
type SynthEnhancedCD struct {
	AudioBins            [][]byte
	DataBin              []byte
	Scram                []byte
	Cue                  string
	LeadinLBA            int32
	ExpectedDataFirstLBA int32 // post-fix value; matches the actual scram layout
	GapSectors           int32 // total inter-session gap
}

// synthEnhancedCD builds an in-memory bin/scram/cue triple modelling
// a CD-Extra disc: N audio tracks (session 1) + REM SESSION 02 +
// 1 data track (session 2). The scram has the inter-session gap
// baked in (Mode 0 leadout + zero leadin + Mode 1 pregap), so a
// correct packer must detect that gap from the scram alone.
func synthEnhancedCD(t *testing.T, opts SynthEnhancedCDOpts) SynthEnhancedCD {
	t.Helper()

	if opts.AudioTracks == 0 {
		opts.AudioTracks = 3
	}
	if opts.AudioSectorsEach == 0 {
		opts.AudioSectorsEach = 200
	}
	if opts.DataSectors == 0 {
		opts.DataSectors = 500
	}
	if opts.GapLeadoutSectors == 0 {
		opts.GapLeadoutSectors = 6750
	}
	if opts.GapLeadinSectors == 0 {
		opts.GapLeadinSectors = 4500
	}
	if opts.GapPregapSectors == 0 {
		opts.GapPregapSectors = 150
	}
	if opts.TrailingLeadout == 0 {
		opts.TrailingLeadout = 5
	}

	const (
		leadinLBA int32 = LBALeadinStart
		pregap          = 150
	)
	leadinSectors := int32(LBAPregapStart - LBALeadinStart) // 45000

	// Audio bins: deterministic, non-zero, and chosen so no 12-byte
	// run accidentally matches the scrambled-sync pattern. The Sync
	// pattern's first byte is 0x00 and its 2..11 bytes are 0xFF;
	// our pattern only produces 0xFF when (j*3 + a*17) % 256 == 0xFF,
	// which can't appear 10 bytes in a row.
	audioBins := make([][]byte, opts.AudioTracks)
	totalAudioSectors := int32(0)
	for a := range audioBins {
		ab := make([]byte, int(opts.AudioSectorsEach)*SectorSize)
		for j := range ab {
			ab[j] = byte(j*3 + (a+1)*17)
		}
		audioBins[a] = ab
		totalAudioSectors += opts.AudioSectorsEach
	}

	// Data bin: descrambled Mode 1 sectors at LBAs starting at the
	// data track's actual FirstLBA on disc. The bin holds the
	// descrambled bytes; the scram below scrambles them.
	dataFirstLBA := totalAudioSectors + opts.GapLeadoutSectors + opts.GapLeadinSectors + opts.GapPregapSectors
	dataBin := make([]byte, int(opts.DataSectors)*SectorSize)
	for i := int32(0); i < opts.DataSectors; i++ {
		s := dataBin[int(i)*SectorSize : (int(i)+1)*SectorSize]
		copy(s[:SyncLen], Sync[:])
		lba := dataFirstLBA + i
		msf := LBAToBCDMSF(lba)
		s[12], s[13], s[14], s[15] = msf[0], msf[1], msf[2], 0x01
		for j := 16; j < SectorSize; j++ {
			s[j] = byte(j * int(i+1))
		}
	}

	// Scram layout in disc order.
	totalScramSectors := leadinSectors + pregap + totalAudioSectors +
		opts.GapLeadoutSectors + opts.GapLeadinSectors + opts.GapPregapSectors +
		opts.DataSectors + opts.TrailingLeadout
	scramLen := int64(totalScramSectors)*int64(SectorSize) + int64(opts.WriteOffset)
	if scramLen < 0 {
		scramLen = 0
	}
	scram := make([]byte, scramLen)

	writeSec := func(idx int32, sec [SectorSize]byte) {
		pos := int64(idx)*int64(SectorSize) + int64(opts.WriteOffset)
		writeAt(scram, pos, sec[:])
	}

	idx := int32(0)
	// Disc lead-in: zeros (already zeroed).
	idx += leadinSectors
	// Track-1 pregap: silent audio (zeros) because track 1 is audio.
	idx += pregap
	// Audio tracks: PCM as-is, no scrambling.
	for a := range audioBins {
		for j := int32(0); j < opts.AudioSectorsEach; j++ {
			var sec [SectorSize]byte
			copy(sec[:], audioBins[a][int(j)*SectorSize:(int(j)+1)*SectorSize])
			writeSec(idx, sec)
			idx++
		}
	}
	// Session-1 lead-out: Mode 0 scrambled zero, increasing LBA.
	for j := int32(0); j < opts.GapLeadoutSectors; j++ {
		writeSec(idx, generateLeadoutSector(leadinLBA+idx))
		idx++
	}
	// Session-2 lead-in: zeros.
	idx += opts.GapLeadinSectors
	// Session-2 pregap: Mode 1 scrambled zero.
	for j := int32(0); j < opts.GapPregapSectors; j++ {
		writeSec(idx, generateMode1ZeroSector(leadinLBA+idx))
		idx++
	}
	// Data track: Scramble() applied to the bin sectors.
	for j := int32(0); j < opts.DataSectors; j++ {
		var sec [SectorSize]byte
		copy(sec[:], dataBin[int(j)*SectorSize:(int(j)+1)*SectorSize])
		Scramble(&sec)
		writeSec(idx, sec)
		idx++
	}
	// Trailing lead-out: Mode 0 scrambled zero.
	for j := int32(0); j < opts.TrailingLeadout; j++ {
		writeSec(idx, generateLeadoutSector(leadinLBA+idx))
		idx++
	}

	// Cue.
	var cue strings.Builder
	for a := 0; a < opts.AudioTracks; a++ {
		fmt.Fprintf(&cue, "FILE \"x (Track %d).bin\" BINARY\n  TRACK %02d AUDIO\n    INDEX 01 00:00:00\n",
			a+1, a+1)
	}
	cue.WriteString("REM SESSION 02\n")
	fmt.Fprintf(&cue, "FILE \"x (Track %d).bin\" BINARY\n  TRACK %02d MODE1/2352\n    INDEX 01 00:00:00\n",
		opts.AudioTracks+1, opts.AudioTracks+1)

	return SynthEnhancedCD{
		AudioBins:            audioBins,
		DataBin:              dataBin,
		Scram:                scram,
		Cue:                  cue.String(),
		LeadinLBA:            leadinLBA,
		ExpectedDataFirstLBA: dataFirstLBA,
		GapSectors:           opts.GapLeadoutSectors + opts.GapLeadinSectors + opts.GapPregapSectors,
	}
}

// writeEnhancedCDFixture writes the enhanced-CD bin/scram/cue files
// into dir. Returns the cue path.
func writeEnhancedCDFixture(t *testing.T, dir string, d SynthEnhancedCD) string {
	t.Helper()
	for a, ab := range d.AudioBins {
		p := filepath.Join(dir, fmt.Sprintf("x (Track %d).bin", a+1))
		if err := os.WriteFile(p, ab, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dataPath := filepath.Join(dir, fmt.Sprintf("x (Track %d).bin", len(d.AudioBins)+1))
	if err := os.WriteFile(dataPath, d.DataBin, 0o644); err != nil {
		t.Fatal(err)
	}
	scramPath := filepath.Join(dir, "x.scram")
	if err := os.WriteFile(scramPath, d.Scram, 0o644); err != nil {
		t.Fatal(err)
	}
	cuePath := filepath.Join(dir, "x.cue")
	if err := os.WriteFile(cuePath, []byte(d.Cue), 0o644); err != nil {
		t.Fatal(err)
	}
	return cuePath
}
