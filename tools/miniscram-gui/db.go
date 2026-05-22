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
CREATE TABLE IF NOT EXISTS redump_auth (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    username     TEXT NOT NULL,
    password     TEXT NOT NULL,
    updated_unix INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS redump_cache_v2 (
    hash         TEXT NOT NULL,
    auth_mode    TEXT NOT NULL,
    state        TEXT NOT NULL,
    url          TEXT,
    title        TEXT,
    checked_unix INTEGER NOT NULL,
    PRIMARY KEY (hash, auth_mode)
);
INSERT OR IGNORE INTO redump_cache_v2 (hash, auth_mode, state, url, title, checked_unix)
    SELECT hash, 'anon', state, url, title, checked_unix FROM redump_cache;
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
CREATE TABLE IF NOT EXISTS bin_hash_cache (
    path         TEXT PRIMARY KEY,
    size         INTEGER NOT NULL,
    mtime_unix   INTEGER NOT NULL,
    md5          TEXT NOT NULL,
    sha1         TEXT NOT NULL,
    sha256       TEXT NOT NULL,
    computed_unix INTEGER NOT NULL
);
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

// redump auth (plaintext local credentials) -------------------

type redumpAuth struct {
	Username    string
	Password    string
	UpdatedUnix int64
}

func redumpAuthGet(db *sql.DB) (redumpAuth, bool) {
	if db == nil {
		return redumpAuth{}, false
	}
	var a redumpAuth
	err := db.QueryRow(`SELECT username, password, updated_unix FROM redump_auth WHERE id = 1`).
		Scan(&a.Username, &a.Password, &a.UpdatedUnix)
	if err != nil {
		return redumpAuth{}, false
	}
	return a, true
}

func redumpAuthPut(db *sql.DB, username, password string) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`
		INSERT INTO redump_auth (id, username, password, updated_unix)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			username = excluded.username,
			password = excluded.password,
			updated_unix = excluded.updated_unix
	`, username, password, time.Now().Unix())
}

func redumpAuthClear(db *sql.DB) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`DELETE FROM redump_auth WHERE id = 1`)
}

// redump cache (per-hash) -------------------------------------

func redumpGet(db *sql.DB, hash, authMode string) (*redumpEntry, bool) {
	if db == nil {
		return nil, false
	}
	row := db.QueryRow(`SELECT state, COALESCE(url,''), COALESCE(title,''), checked_unix FROM redump_cache_v2 WHERE hash = ? AND auth_mode = ?`, hash, authMode)
	e := &redumpEntry{}
	if err := row.Scan(&e.State, &e.URL, &e.Title, &e.CheckedUnix); err != nil {
		return nil, false
	}
	return e, true
}

func redumpPut(db *sql.DB, hash, authMode string, e *redumpEntry) {
	if db == nil || e == nil {
		return
	}
	_, _ = db.Exec(`
		INSERT INTO redump_cache_v2 (hash, auth_mode, state, url, title, checked_unix)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(hash, auth_mode) DO UPDATE SET
			state = excluded.state,
			url = excluded.url,
			title = excluded.title,
			checked_unix = excluded.checked_unix
	`, hash, authMode, e.State, e.URL, e.Title, e.CheckedUnix)
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
	Status          string // "success" | "fail" | "cancelled"
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

// bin hash cache (per-bin file) ------------------------------

// binHashLookup returns the cached digests for path iff the stored
// (size, mtime_unix) match the observed values. A miss is returned
// for "no row", "stale row", or db == nil.
func binHashLookup(db *sql.DB, path string, size, mtimeUnix int64) (md5h, sha1h, sha256h string, ok bool) {
	if db == nil {
		return "", "", "", false
	}
	row := db.QueryRow(`
		SELECT md5, sha1, sha256 FROM bin_hash_cache
		WHERE path = ? AND size = ? AND mtime_unix = ?
	`, path, size, mtimeUnix)
	if err := row.Scan(&md5h, &sha1h, &sha256h); err != nil {
		return "", "", "", false
	}
	return md5h, sha1h, sha256h, true
}

// binHashPut writes (or replaces) the row for path with the observed
// size+mtime and the computed digests. No-op when db == nil.
func binHashPut(db *sql.DB, path string, size, mtimeUnix int64, md5h, sha1h, sha256h string) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`
		INSERT INTO bin_hash_cache (path, size, mtime_unix, md5, sha1, sha256, computed_unix)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			size = excluded.size,
			mtime_unix = excluded.mtime_unix,
			md5 = excluded.md5,
			sha1 = excluded.sha1,
			sha256 = excluded.sha256,
			computed_unix = excluded.computed_unix
	`, path, size, mtimeUnix, md5h, sha1h, sha256h, time.Now().Unix())
}
