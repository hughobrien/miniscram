package main

import (
	"database/sql"
	"time"
)

// seed inserts example events into the events table for screenshot purposes.
// idempotent: only seeds when events table is empty.
func seedEvents(db *sql.DB) {
	if db == nil {
		return
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	if n > 0 {
		return
	}
	now := time.Now()
	mk := func(ago time.Duration, action, in, out, title string, ss, ms int64, ovr, wo int, dur int64, status, errs string) eventRec {
		return eventRec{
			TS: now.Add(-ago), Action: action, InputPath: in, OutputPath: out, Title: title,
			ScramSize: ss, MiniscramSize: ms, OverrideRecords: ovr, WriteOffset: wo,
			DurationMs: dur, Status: status, Error: errs,
		}
	}
	rows := []eventRec{
		// most recent at top
		mk(2*time.Minute, "pack",
			"/home/hugh/miniscram/test-discs/half-life/HALFLIFE.cue",
			"/home/hugh/miniscram/test-discs/half-life/HALFLIFE.miniscram",
			"Half-Life", 802170648, 339968, 39008, 0, 4830, "success", ""),
		mk(8*time.Minute, "pack",
			"/home/hugh/miniscram/test-discs/freelancer/FL_v1.cue",
			"/home/hugh/miniscram/test-discs/freelancer/FL_v1.miniscram",
			"Freelancer", 836338152, 1572864, 45927, -48, 6210, "success", ""),
		mk(11*time.Minute, "verify",
			"/home/hugh/miniscram/test-discs/deus-ex/DeusEx_v1002f.miniscram",
			"", "Deus Ex", 897527784, 327, 0, -48, 1830, "success", ""),
		mk(35*time.Minute, "pack",
			"/home/hugh/miniscram/test-discs/deus-ex/DeusEx_v1002f.cue",
			"/home/hugh/miniscram/test-discs/deus-ex/DeusEx_v1002f.miniscram",
			"Deus Ex", 897527784, 327, 0, -48, 4120, "success", ""),
		mk(2*time.Hour+12*time.Minute, "pack",
			"/home/hugh/games/SLUS-00892.cue",
			"/home/hugh/games/SLUS-00892.miniscram",
			"Final Fantasy VIII (PSX)", 838860800, 211968, 39875, -2588, 5420, "success", ""),
		mk(3*time.Hour+5*time.Minute, "pack",
			"/home/hugh/games/MP2_Play.cue",
			"/home/hugh/games/MP2_Play.miniscram",
			"Max Payne 2", 850231296, 374784, 1820, 0, 5630, "success", ""),
		mk(4*time.Hour, "unpack",
			"/home/hugh/games/MP2_Play.miniscram",
			"/home/hugh/games/MP2_Play.scram",
			"Max Payne 2", 850231296, 374784, 1820, 0, 4880, "success", ""),
		mk(20*time.Hour, "pack",
			"/home/hugh/staging/UNKNOWN.cue", "",
			"", 0, 0, 0, 0, 1230, "fail", "layout mismatch ratio 0.07 exceeds 0.05"),
	}
	for _, r := range rows {
		eventInsert(db, r)
	}
}
