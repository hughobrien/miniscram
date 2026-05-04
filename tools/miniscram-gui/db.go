package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS redump_cache (
    hash         TEXT PRIMARY KEY,
    state        TEXT NOT NULL,
    url          TEXT,
    title        TEXT,
    checked_unix INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    ts_unix          INTEGER NOT NULL,
    action           TEXT NOT NULL,
    input_path       TEXT NOT NULL,
    output_path      TEXT,
    title            TEXT,
    scram_size       INTEGER,
    miniscram_size   INTEGER,
    override_records INTEGER,
    write_offset     INTEGER,
    duration_ms      INTEGER,
    status           TEXT NOT NULL,
    error            TEXT
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts_unix DESC);
`

func dataPath() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "miniscram-gui", "db.sqlite")
}

func dbOpen() (*sql.DB, error) {
	p := dataPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", p)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// redump cache (per-hash) -------------------------------------

func redumpGet(db *sql.DB, hash string) (*redumpEntry, bool) {
	if db == nil {
		return nil, false
	}
	row := db.QueryRow(`SELECT state, COALESCE(url,''), COALESCE(title,''), checked_unix FROM redump_cache WHERE hash = ?`, hash)
	e := &redumpEntry{}
	if err := row.Scan(&e.State, &e.URL, &e.Title, &e.CheckedUnix); err != nil {
		return nil, false
	}
	return e, true
}

func redumpPut(db *sql.DB, hash string, e *redumpEntry) {
	if db == nil || e == nil {
		return
	}
	_, _ = db.Exec(`
		INSERT INTO redump_cache (hash, state, url, title, checked_unix)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(hash) DO UPDATE SET
			state = excluded.state,
			url = excluded.url,
			title = excluded.title,
			checked_unix = excluded.checked_unix
	`, hash, e.State, e.URL, e.Title, e.CheckedUnix)
}

// events --------------------------------------------------

type eventRec struct {
	ID              int64
	TS              time.Time
	Action          string // "pack" | "unpack" | "verify"
	InputPath       string
	OutputPath      string
	Title           string
	ScramSize       int64
	MiniscramSize   int64
	OverrideRecords int
	WriteOffset     int
	DurationMs      int64
	Status          string // "success" | "fail"
	Error           string
}

func eventDelete(db *sql.DB, id int64) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`DELETE FROM events WHERE id = ?`, id)
}

func eventInsert(db *sql.DB, ev eventRec) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`
		INSERT INTO events (ts_unix, action, input_path, output_path, title, scram_size, miniscram_size,
		                    override_records, write_offset, duration_ms, status, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ev.TS.Unix(), ev.Action, ev.InputPath, ev.OutputPath, nilIfEmpty(ev.Title),
		ev.ScramSize, ev.MiniscramSize, ev.OverrideRecords, ev.WriteOffset,
		ev.DurationMs, ev.Status, nilIfEmpty(ev.Error))
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func eventsRecent(db *sql.DB, n int) []eventRec {
	if db == nil {
		return nil
	}
	rows, err := db.Query(`
		SELECT id, ts_unix, action, input_path, COALESCE(output_path,''), COALESCE(title,''),
		       COALESCE(scram_size,0), COALESCE(miniscram_size,0), COALESCE(override_records,0),
		       COALESCE(write_offset,0), COALESCE(duration_ms,0), status, COALESCE(error,'')
		FROM events ORDER BY ts_unix DESC LIMIT ?`, n)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []eventRec
	for rows.Next() {
		var ts int64
		ev := eventRec{}
		_ = rows.Scan(&ev.ID, &ts, &ev.Action, &ev.InputPath, &ev.OutputPath, &ev.Title,
			&ev.ScramSize, &ev.MiniscramSize, &ev.OverrideRecords, &ev.WriteOffset,
			&ev.DurationMs, &ev.Status, &ev.Error)
		ev.TS = time.Unix(ts, 0)
		out = append(out, ev)
	}
	return out
}

type statsAgg struct {
	TotalOps        int
	PackOps         int
	TotalSavedBytes int64 // sum(scram_size - miniscram_size) over successful packs
	BestRatio       float64
	BestRatioTitle  string
	WorstRatio      float64
	WorstRatioTitle string
	OverrideTotal   int64
}

func eventsAggregate(db *sql.DB) statsAgg {
	var a statsAgg
	if db == nil {
		return a
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&a.TotalOps)
	_ = db.QueryRow(`SELECT COUNT(*) FROM events WHERE action='pack' AND status='success'`).Scan(&a.PackOps)
	_ = db.QueryRow(`SELECT COALESCE(SUM(scram_size - miniscram_size),0) FROM events WHERE action='pack' AND status='success'`).Scan(&a.TotalSavedBytes)
	_ = db.QueryRow(`SELECT COALESCE(SUM(override_records),0) FROM events WHERE action='pack' AND status='success'`).Scan(&a.OverrideTotal)

	rows, err := db.Query(`SELECT COALESCE(title, input_path), scram_size, miniscram_size FROM events WHERE action='pack' AND status='success' AND scram_size > 0 AND miniscram_size > 0`)
	if err == nil {
		defer rows.Close()
		type r struct {
			t string
			s int64
			m int64
		}
		var rs []r
		for rows.Next() {
			var x r
			_ = rows.Scan(&x.t, &x.s, &x.m)
			rs = append(rs, x)
		}
		sort.Slice(rs, func(i, j int) bool {
			ri := float64(rs[i].s) / float64(rs[i].m)
			rj := float64(rs[j].s) / float64(rs[j].m)
			return ri > rj
		})
		if len(rs) > 0 {
			a.BestRatio = float64(rs[0].s) / float64(rs[0].m)
			a.BestRatioTitle = rs[0].t
			last := rs[len(rs)-1]
			a.WorstRatio = float64(last.s) / float64(last.m)
			a.WorstRatioTitle = last.t
		}
	}
	return a
}
