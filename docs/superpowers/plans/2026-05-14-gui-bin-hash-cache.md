# GUI bin-hash cache — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop re-streaming every bin file's md5/sha1/sha256 on each click of a `.cue` queue row. Persist per-bin digests in SQLite keyed on path with size+mtime as freshness witness; on a hit, populate `tracks[i].hashes` directly and skip the stream.

**Architecture:** New `bin_hash_cache` table beside the existing `redump_cache` in `tools/miniscram-gui/db.go`. Two helpers `binHashLookup` / `binHashPut` mirror the `redumpGet` / `redumpPut` shape. `hashCueBins` (`tools/miniscram-gui/main.go`) consults the cache inside each per-bin goroutine before calling `hashCueBin`, and writes through on success. No behavior change elsewhere — the `.miniscram` path already gets per-track hashes from inspect-JSON, and `m.lookup` already disk-caches redump matches keyed by hash.

**Tech Stack:** Go 1.23, `modernc.org/sqlite`, `CC=/usr/bin/clang CGO_ENABLED=1` required for local GUI builds.

**Spec:** `docs/superpowers/specs/2026-05-14-gui-bin-hash-cache-design.md`.

---

## File map

- **Modify:** `tools/miniscram-gui/db.go` — add `bin_hash_cache` clause to `schema`, add `binHashLookup` and `binHashPut`.
- **Modify:** `tools/miniscram-gui/main.go` — rewrite the per-bin goroutine in `hashCueBins` to consult the cache.
- **Create:** `tools/miniscram-gui/bin_hash_cache_test.go` — six tests covering the helpers and the integration.

---

### Task 1: Schema + helper functions + helper tests (TDD)

**Files:**
- Create: `tools/miniscram-gui/bin_hash_cache_test.go`
- Modify: `tools/miniscram-gui/db.go` (add schema clause and two functions)

- [ ] **Step 1.1: Write the four helper tests**

Create `tools/miniscram-gui/bin_hash_cache_test.go`:

```go
package main

import (
	"testing"
)

func TestBinHashCache_RoundTrip(t *testing.T) {
	m := newTestModel(t)

	binHashPut(m.db, "/tmp/a.bin", 1024, 1700000000,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")

	md5h, sha1h, sha256h, ok := binHashLookup(m.db, "/tmp/a.bin", 1024, 1700000000)
	if !ok {
		t.Fatal("expected cache hit, got miss")
	}
	if md5h != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("md5 = %q", md5h)
	}
	if sha1h != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("sha1 = %q", sha1h)
	}
	if sha256h != "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" {
		t.Errorf("sha256 = %q", sha256h)
	}
}

func TestBinHashCache_StaleMtime(t *testing.T) {
	m := newTestModel(t)
	binHashPut(m.db, "/tmp/a.bin", 1024, 1700000000, "m", "s1", "s2")

	if _, _, _, ok := binHashLookup(m.db, "/tmp/a.bin", 1024, 1700000001); ok {
		t.Error("expected miss on stale mtime, got hit")
	}
}

func TestBinHashCache_StaleSize(t *testing.T) {
	m := newTestModel(t)
	binHashPut(m.db, "/tmp/a.bin", 1024, 1700000000, "m", "s1", "s2")

	if _, _, _, ok := binHashLookup(m.db, "/tmp/a.bin", 1025, 1700000000); ok {
		t.Error("expected miss on stale size, got hit")
	}
}

func TestBinHashCache_NilDB(t *testing.T) {
	if _, _, _, ok := binHashLookup(nil, "/tmp/a.bin", 1, 1); ok {
		t.Error("nil db: expected miss, got hit")
	}
	// must not panic
	binHashPut(nil, "/tmp/a.bin", 1, 1, "m", "s1", "s2")
}
```

`newTestModel(t)` is defined in `tools/miniscram-gui/result_handler_test.go` (same package) and constructs a `*model` with an in-memory SQLite that has `schema` applied. Reuse it.

- [ ] **Step 1.2: Run tests — expect compile failure**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestBinHashCache' -v ./...
```

Expected: the build fails because `binHashLookup` and `binHashPut` don't exist yet. Confirm the error message names those identifiers, then proceed.

- [ ] **Step 1.3: Add the schema clause and helpers to `db.go`**

In `tools/miniscram-gui/db.go`, find the `schema` constant (currently starts at line 13). Append the new table clause to the end of the existing string literal, just before the closing backtick. The final schema should read:

```go
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
CREATE TABLE IF NOT EXISTS bin_hash_cache (
    path          TEXT PRIMARY KEY,
    size          INTEGER NOT NULL,
    mtime_unix    INTEGER NOT NULL,
    md5           TEXT NOT NULL,
    sha1          TEXT NOT NULL,
    sha256        TEXT NOT NULL,
    computed_unix INTEGER NOT NULL
);
`
```

Then, at the very end of `db.go` (after `eventsRecent`'s closing brace, which currently lives near the bottom of the file), append:

```go

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
```

Note: `time` is already imported by `db.go` (line 8). `sql` is also already imported.

- [ ] **Step 1.4: Run helper tests — expect all four to pass**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestBinHashCache' -v ./...
```

Expected: 4/4 PASS.

- [ ] **Step 1.5: Run the full GUI test package as a regression check**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test ./...
```

Expected: ok. Adding the schema clause shouldn't break any existing tests; this confirms it.

- [ ] **Step 1.6: Commit**

```bash
git add tools/miniscram-gui/db.go tools/miniscram-gui/bin_hash_cache_test.go
git commit -m "$(cat <<'EOF'
gui: add bin_hash_cache table and lookup/put helpers

New SQLite table keyed on absolute path with (size, mtime_unix) as
freshness witness. binHashLookup returns digests only when both match;
binHashPut UPSERTs after a successful hash. Mirrors the redump_cache
shape. Schema migration is the CREATE TABLE IF NOT EXISTS clause;
existing installs pick it up on next open. No call sites yet — wiring
into hashCueBins lands separately.
EOF
)"
```

---

### Task 2: Wire cache into `hashCueBins` + integration tests

**Files:**
- Modify: `tools/miniscram-gui/main.go` (the per-bin goroutine inside `hashCueBins`, around lines 749-773)
- Modify: `tools/miniscram-gui/bin_hash_cache_test.go` (add two integration tests)

- [ ] **Step 2.1: Write the two failing integration tests**

Append to `tools/miniscram-gui/bin_hash_cache_test.go`:

```go

// The integration tests use m.hashCueBins, which calls m.lookup() at
// the end of each per-bin goroutine. m.lookup() consults redump_cache
// before HTTPing redump.org; pre-seeding redump_cache for the sha1
// keeps the test fully offline.

func TestHashCueBins_PopulatesCache(t *testing.T) {
	m := newTestModel(t)
	m.redump = map[string]*redumpEntry{} // hashCueBins → lookup() writes here

	binPath := writeTempBytes(t, "track01.bin", 4096)

	// Compute the expected digests independently so we can pre-seed
	// the redump cache (avoid HTTP) and assert against them.
	expMd5, expSha1, expSha256, err := hashCueBin(binPath)
	if err != nil {
		t.Fatalf("hashCueBin: %v", err)
	}
	redumpPut(m.db, expSha1, &redumpEntry{State: "miss", CheckedUnix: time.Now().Unix()})

	tracks := []cueTrack{{num: 1, mode: "MODE1/2352", filename: "track01.bin", exists: true}}
	m.hashCueBins(tracks, []string{binPath})

	if tracks[0].state != "done" {
		t.Fatalf("state = %q, want done", tracks[0].state)
	}
	if tracks[0].hashes["sha1"] != expSha1 {
		t.Errorf("sha1 = %q, want %q", tracks[0].hashes["sha1"], expSha1)
	}

	st, err := os.Stat(binPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	cMd5, cSha1, cSha256, ok := binHashLookup(m.db, binPath, st.Size(), st.ModTime().Unix())
	if !ok {
		t.Fatal("expected cache hit after hashCueBins, got miss")
	}
	if cMd5 != expMd5 || cSha1 != expSha1 || cSha256 != expSha256 {
		t.Errorf("cached digests don't match computed digests")
	}
}

func TestHashCueBins_UsesCache(t *testing.T) {
	m := newTestModel(t)
	m.redump = map[string]*redumpEntry{}

	binPath := writeTempBytes(t, "track01.bin", 4096)
	st, err := os.Stat(binPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Pre-seed bin_hash_cache with deliberately wrong digests at the
	// real (size, mtime). The only way hashCueBins can return these
	// is by reading the cache instead of re-streaming the file.
	wrongMd5 := "deadbeefdeadbeefdeadbeefdeadbeef"
	wrongSha1 := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	wrongSha256 := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	binHashPut(m.db, binPath, st.Size(), st.ModTime().Unix(), wrongMd5, wrongSha1, wrongSha256)
	redumpPut(m.db, wrongSha1, &redumpEntry{State: "miss", CheckedUnix: time.Now().Unix()})

	tracks := []cueTrack{{num: 1, mode: "MODE1/2352", filename: "track01.bin", exists: true}}
	m.hashCueBins(tracks, []string{binPath})

	if tracks[0].state != "done" {
		t.Fatalf("state = %q, want done", tracks[0].state)
	}
	if tracks[0].hashes["sha1"] != wrongSha1 {
		t.Errorf("sha1 = %q, want %q (cache should have short-circuited the hash)", tracks[0].hashes["sha1"], wrongSha1)
	}
}
```

These tests need new imports in the test file. Update the import block at the top of `bin_hash_cache_test.go` from:

```go
import (
	"testing"
)
```

to:

```go
import (
	"os"
	"testing"
	"time"
)
```

The helpers `writeTempBytes` and `redumpPut` are already in the package; no further imports needed.

- [ ] **Step 2.2: Run integration tests — expect failure on `PopulatesCache`**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestHashCueBins_' -v ./...
```

Expected:
- `TestHashCueBins_PopulatesCache` — FAILS. The current `hashCueBins` doesn't write to `bin_hash_cache`, so the post-run `binHashLookup` returns `ok == false`, and the test calls `t.Fatal("expected cache hit ...")`.
- `TestHashCueBins_UsesCache` — passes by coincidence today (the cached values match an actual re-hash only when the file content happens to hash to the wrong values, which it doesn't — so this test fails because `tracks[0].hashes["sha1"]` equals the real sha1 of a 4 KB zero file, not the `deadbeef…` cached one). Confirm both fail in the expected way before proceeding.

- [ ] **Step 2.3: Rewrite the per-bin goroutine in `hashCueBins`**

In `tools/miniscram-gui/main.go`, locate `hashCueBins` (the function declaration starts around line 727). Inside it, find the per-bin goroutine block — currently:

```go
go func() {
    defer wg.Done()
    md5h, sha1h, sha256h, err := hashCueBin(j.full)
    m.redumpMu.Lock()
    if err != nil {
        tracks[j.idx].state = "fail"
    } else {
        tracks[j.idx].hashes = map[string]string{
            "md5":    md5h,
            "sha1":   sha1h,
            "sha256": sha256h,
        }
        tracks[j.idx].state = "done"
    }
    m.redumpMu.Unlock()
    if m.invalidate != nil {
        m.invalidate()
    }
    if err == nil && sha1h != "" {
        m.lookup([]string{sha1h})
    }
}()
```

Replace the entire goroutine body with:

```go
go func() {
    defer wg.Done()
    var (
        md5h, sha1h, sha256h string
        hashErr              error
    )
    st, statErr := os.Stat(j.full)
    if statErr == nil {
        if cMd5, cSha1, cSha256, hit := binHashLookup(m.db, j.full, st.Size(), st.ModTime().Unix()); hit {
            md5h, sha1h, sha256h = cMd5, cSha1, cSha256
        }
    }
    if sha1h == "" {
        md5h, sha1h, sha256h, hashErr = hashCueBin(j.full)
        if hashErr == nil && statErr == nil {
            binHashPut(m.db, j.full, st.Size(), st.ModTime().Unix(), md5h, sha1h, sha256h)
        }
    }
    m.redumpMu.Lock()
    if hashErr != nil {
        tracks[j.idx].state = "fail"
    } else {
        tracks[j.idx].hashes = map[string]string{
            "md5":    md5h,
            "sha1":   sha1h,
            "sha256": sha256h,
        }
        tracks[j.idx].state = "done"
    }
    m.redumpMu.Unlock()
    if m.invalidate != nil {
        m.invalidate()
    }
    if hashErr == nil && sha1h != "" {
        m.lookup([]string{sha1h})
    }
}()
```

`os` is already imported by `main.go` (used elsewhere). No other imports change.

- [ ] **Step 2.4: Run integration tests — expect 2/2 pass**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestHashCueBins_' -v ./...
```

Expected: both PASS. If `PopulatesCache` still fails, the cache write didn't get wired; if `UsesCache` still fails, the cache read didn't short-circuit.

- [ ] **Step 2.5: Run all helper + integration tests together**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestBinHashCache|TestHashCueBins_' -v ./...
```

Expected: 6/6 PASS.

- [ ] **Step 2.6: Run the full GUI test package**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test ./...
```

Expected: ok. Catches accidental regressions.

- [ ] **Step 2.7: Build verify**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go build -o /tmp/miniscram-gui-verify .
```

Expected: build succeeds.

- [ ] **Step 2.8: Commit**

```bash
git add tools/miniscram-gui/main.go tools/miniscram-gui/bin_hash_cache_test.go
git commit -m "$(cat <<'EOF'
gui: consult bin_hash_cache before streaming each bin

hashCueBins now stats each bin, queries bin_hash_cache by
(path, size, mtime_unix), and on hit assigns the cached digests
without streaming. On miss it falls through to the existing
hashCueBin path and writes the result back. The subsequent
m.lookup() composes with the existing redump_cache so a repeat
click on the same .cue never streams or hits redump.org.

Stat errors are tolerated: hashCueBin will surface the open
failure as state="fail" exactly as before, and we skip the cache
write because we have no size/mtime to key by.
EOF
)"
```

---

## Manual verification (optional, post-merge)

Build the GUI and load a packed `.cue` with non-trivial bins twice. The first click pays the streaming cost (visible per-track "hashing…" state); the second click resolves "done" almost immediately. The persistence path is also testable: launch the GUI, click, quit, relaunch, click — should still be instant because the cache lives at `$XDG_DATA_HOME/miniscram-gui/db.sqlite` (or `~/.local/share/miniscram-gui/db.sqlite`).

---

## Out of scope (reminder from spec)

- Cache eviction or size limits. Rows are <200 B; thousands fit fine.
- Explicit invalidation API or clear-cache command. The size+mtime guard handles stale rows; deleting `db.sqlite` is the existing escape hatch.
- Instrumentation/metrics (hit rate, time saved).
- Bin-path symlink normalization. Aliased paths just produce two rows.
- Caching `.miniscram` inspect-JSON output. The inspect parse is already fast.
