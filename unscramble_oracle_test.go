package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// oracleDescramble is a Go port of redumper's
// Scrambler::descramble (cd/cd_scrambler.ixx:23-61). Test-only
// ground-truth oracle. Returns the bin form and verdict that
// redumper would produce given a scrambled sector and an
// expected-LBA hint (nil = no hint).
//
// Note: this helper deliberately does not call the production
// `isZeroed` (added in Task 3) to keep this file self-contained.
// The inline `bytes.Count(buf, []byte{0}) == len(buf)` expresses
// the same predicate idiomatically.
func oracleDescramble(scrambled []byte, lba *int32) (binForm []byte, verdict bool) {
	// Defensive copy so caller's input is unchanged.
	buf := make([]byte, len(scrambled))
	copy(buf, scrambled)

	// is_zeroed → return false, leave scrambled bytes (bin == scram).
	// sizeof(Sector::sync)=12 + sizeof(Sector::header)=4 → need ≥16 bytes.
	if bytes.Count(buf, []byte{0}) == len(buf) || len(buf) < SyncLen+4 {
		return buf, false
	}

	// process(): XOR with scramble table — same as Scramble.
	for i := SyncLen; i < len(buf); i++ {
		buf[i] ^= scrambleTable[i]
	}

	// Strong MSF check.
	if lba != nil {
		decoded := BCDMSFToLBA([3]byte{buf[12], buf[13], buf[14]})
		if decoded == *lba {
			return buf, true
		}
	}

	// Sync match? (Sync field is invariant under scrambling, so this
	// is equivalent to "did the original scrambled sector start with
	// the canonical sync bytes?".)
	if bytes.Equal(buf[:SyncLen], Sync[:]) {
		mode := buf[15]
		switch mode {
		case 1, 2:
			return buf, true
		case 0:
			// Mode 0: check that user_data (bytes 16..16+2336) is all
			// zero. Redumper checks min(size-16, 2336) bytes, matching
			// MODE0_DATA_SIZE from cdrom.ixx.
			const mode0DataSize = 2336
			end := 16 + mode0DataSize
			if end > len(buf) {
				end = len(buf)
			}
			ud := buf[16:end]
			if bytes.Count(ud, []byte{0}) == len(ud) {
				return buf, true
			}
		}
	}

	// Failure: re-scramble back so bin == original scram.
	for i := SyncLen; i < len(buf); i++ {
		buf[i] ^= scrambleTable[i]
	}
	return buf, false
}

// TestOracleAgainstFixtures pins oracleDescramble to redumper's
// own pass/fail labels on the 46 imported fixtures.
func TestOracleAgainstFixtures(t *testing.T) {
	entries, err := os.ReadDir("testdata/unscramble")
	if err != nil {
		t.Fatalf("read testdata/unscramble: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no fixtures found under testdata/unscramble")
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			tokens := strings.Split(name, ".")
			if len(tokens) != 3 {
				t.Fatalf("malformed fixture name: %s", name)
			}
			var lbaPtr *int32
			if tokens[1] != "null" {
				v, err := strconv.ParseInt(tokens[1], 10, 32)
				if err != nil {
					t.Fatalf("bad LBA in %s: %v", name, err)
				}
				lba := int32(v)
				lbaPtr = &lba
			}
			expectPass := tokens[2] == "pass"
			data, err := os.ReadFile(filepath.Join("testdata/unscramble", name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			_, verdict := oracleDescramble(data, lbaPtr)
			if verdict != expectPass {
				t.Errorf("verdict=%v, want %v", verdict, expectPass)
			}
		})
	}
}
