package main

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestClassifyAgainstFixtures asserts classifyBinSector returns
// the same verdict redumper's descramble does on every entry of
// the imported fixture set.
//
// For each fixture we compute the bin form via the test oracle
// (oracleDescramble), then ask classifyBinSector to classify it.
// The classifier sees only what miniscram would see in production:
// the bin bytes plus the expected LBA.
func TestClassifyAgainstFixtures(t *testing.T) {
	entries, err := os.ReadDir("testdata/unscramble")
	if err != nil {
		t.Fatalf("read testdata/unscramble: %v", err)
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

			// expectedLBA passed to classifier. For .null fixtures
			// (redumper's lba=nullptr path) we use math.MinInt32
			// which BCDMSFToLBA can never produce, so the strong-
			// MSF branch will not match — exercising the sync+mode
			// fallback exclusively.
			var classifyLBA int32 = math.MinInt32
			var oracleLBA *int32
			if tokens[1] != "null" {
				v, err := strconv.ParseInt(tokens[1], 10, 32)
				if err != nil {
					t.Fatalf("bad LBA in %s: %v", name, err)
				}
				classifyLBA = int32(v)
				oracleLBA = &classifyLBA
			}

			expectPass := tokens[2] == "pass"
			data, err := os.ReadFile(filepath.Join("testdata/unscramble", name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}

			// Pass the raw fixture bytes to the oracle — matching
			// how TestOracleAgainstFixtures calls it. The oracle
			// handles truncated fixtures correctly (it only XORs up
			// to len(data)).
			binForm, _ := oracleDescramble(data, oracleLBA)

			// Pad binForm to SectorSize for pass-verdict truncated
			// fixtures (e.g. mode-0 with zeroed data that is shorter
			// than a full sector). These represent sectors that are
			// valid but happen to have been stored truncated in the
			// fixture file; the classifier always receives full-size
			// sectors in production.
			//
			// For fail-verdict fixtures that are short (e.g.
			// not_enough_data), do NOT pad: the oracle returned the
			// raw scram bytes unchanged (it exited early on the
			// length check), and padding zeros to those bytes would
			// create synthetic data that looks like a valid mode-0
			// zeroed sector — a false positive. Instead, pass the
			// short binForm directly; classifyBinSector's len-guard
			// fires and returns false, matching the fail label.
			if expectPass && len(binForm) < SectorSize {
				padded := make([]byte, SectorSize)
				copy(padded, binForm)
				binForm = padded
			}

			got := classifyBinSector(binForm, classifyLBA)
			if got != expectPass {
				t.Errorf("classifyBinSector verdict=%v, want %v", got, expectPass)
			}
		})
	}
}
