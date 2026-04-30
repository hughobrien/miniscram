package main

import (
	"math"
	"math/rand"
	"testing"
	"testing/quick"
)

// TestClassifyMatchesOracleProperty: for any 2352-byte sector and
// any expected LBA, classifyBinSector applied to the bin form
// returned by the oracle must match the oracle's pass/fail verdict.
//
// This catches edge cases the 46-entry fixture set doesn't exercise.
func TestClassifyMatchesOracleProperty(t *testing.T) {
	cfg := &quick.Config{MaxCount: 1000, Rand: rand.New(rand.NewSource(1))}

	property := func(seed int64, lbaSign bool, lbaMag int32, useNullLBA bool) bool {
		// Generate a deterministic 2352-byte sector from the seed.
		r := rand.New(rand.NewSource(seed))
		sector := make([]byte, SectorSize)
		r.Read(sector)

		// LBA hint. With useNullLBA, simulate the redumper
		// lba=nullptr case by passing math.MinInt32 to the
		// classifier and nil to the oracle.
		var classifyLBA int32
		var oracleLBA *int32
		if useNullLBA {
			classifyLBA = math.MinInt32
		} else {
			lba := lbaMag % (99 * 60 * 75)
			if lbaSign {
				lba = -lba - 150
			}
			classifyLBA = lba
			oracleLBA = &lba
		}

		binForm, oracleVerdict := oracleDescramble(sector, oracleLBA)
		got := classifyBinSector(binForm, classifyLBA)
		return got == oracleVerdict
	}

	if err := quick.Check(property, cfg); err != nil {
		t.Errorf("classifier disagreed with oracle: %v", err)
	}
}
