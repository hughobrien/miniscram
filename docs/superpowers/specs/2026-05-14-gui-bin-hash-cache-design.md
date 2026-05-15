# GUI: per-bin hash cache

Date: 2026-05-14

## Motivation

`hashCueBins` (`tools/miniscram-gui/main.go:727`) is called every time
`load()` runs against a `.cue`. It fans out a goroutine per bin file
and streams each through MD5+SHA-1+SHA-256 in one pass. For a
typical PSX disc the bin is tens of MB; for a multi-disc PS2 or
PSX corpus the click-to-result latency is multiple seconds, and for
the half-life DVD-sized fixture it's noticeably longer. The SHA-1
is then fed into the redump lookup pipeline.

After processing a queue, the user clicks rows to inspect status
and waits for the same hashes to be recomputed every click. Same
file, same content, same hashes — no reason to recompute.

This spec adds a SQLite-backed per-bin hash cache keyed on file
identity (path) and freshness witness (size + mtime). On a hit
`hashCueBins` skips the stream and populates `tracks[i].hashes`
directly; on a miss the existing hash path runs and the result
is written back. No new behavior, only a fast path.

The `.miniscram` side does not need this: its inspect-JSON output
already includes per-track hashes from the container, and the CLI
parse is cheap.

## Design

### Schema

Add one table to `tools/miniscram-gui/db.go::schema`:

```sql
CREATE TABLE IF NOT EXISTS bin_hash_cache (
    path          TEXT PRIMARY KEY,
    size          INTEGER NOT NULL,
    mtime_unix    INTEGER NOT NULL,
    md5           TEXT NOT NULL,
    sha1          TEXT NOT NULL,
    sha256        TEXT NOT NULL,
    computed_unix INTEGER NOT NULL
);
```

Path is the primary key — one row per bin file. (size, mtime_unix)
is the freshness witness. Both must match the observed file for a
cache hit.

`CREATE TABLE IF NOT EXISTS` is the only migration. Existing
installs pick up the new table on next open. No data migration.

### Helpers in `db.go`

Two new functions, mirroring the `redumpGet`/`redumpPut` shape:

```go
// binHashLookup returns the cached digests for path iff the stored
// (size, mtime_unix) match the observed size and mtime. A miss is
// returned for "no row", "row but stale", or db == nil.
func binHashLookup(db *sql.DB, path string, size, mtimeUnix int64) (md5, sha1, sha256 string, ok bool)

// binHashPut writes (or replaces) the row for path with the
// observed size+mtime and the computed digests.
func binHashPut(db *sql.DB, path string, size, mtimeUnix int64, md5, sha1, sha256 string)
```

The freshness check lives in `binHashLookup`, not in callers.
Callers pass the size+mtime they just observed and either get the
hashes or get told to compute them. This keeps the staleness
policy in one place.

### Integration in `hashCueBins`

The existing per-bin goroutine in
`tools/miniscram-gui/main.go::hashCueBins` (~line 750) becomes:

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
        tracks[j.idx].hashes = map[string]string{"md5": md5h, "sha1": sha1h, "sha256": sha256h}
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

`statErr` is tolerated: if the file disappears between the outer
existence check (in `hashCueBins`'s job-building loop) and the
goroutine running, we fall through to the existing hash path,
which will surface the open failure as `state = "fail"` exactly
like today. We do not cache on the stat-error path because we
have no size/mtime to key by.

The outer "filter by `tracks[i].exists`" loop in `hashCueBins` is
unchanged — non-existent bins are still excluded before the
goroutine fan-out.

### Concurrency

`modernc.org/sqlite` serializes writes at the SQLite layer. The
existing `redump_cache` writes (`redumpPut`) rely on this; the
same pattern applies here. Two concurrent goroutines racing on
the same `binHashPut(path)` resolve via the table's primary key —
the second write replaces the first; both wrote identical hashes
by construction so the order does not matter.

A rapid double click on the same row could in principle race two
`hashCueBins` invocations: both miss the cache, both stream, both
write through. SQLite serializes the writes; the redundant work
is the only cost, and the second invocation populates the cache
for any third click. Not worth coordinating around.

### What does not change

- `hashCueBin` (the single-file streamer at line 699) is untouched.
- `m.lookup` (redump match cache and HTTP fetch) is untouched. It
  already disk-caches via the `redump_cache` table keyed on hash
  (`db.go::redumpGet`/`redumpPut`), so once a SHA-1 has been looked
  up, future calls for it never reach redump.org. The bin-hash cache
  composes cleanly: a cache hit skips the stream *and* routes the
  cached SHA-1 straight into `m.lookup`, which serves it from the
  redump cache without an HTTP call.
- The cue view's row layout, state machine, and redump green-row
  treatment are untouched.
- `.miniscram` loading is untouched — inspect-JSON already provides
  hashes.

## Tests

A new test file `tools/miniscram-gui/db_test.go` (or extension of
`result_handler_test.go` if a single test file feels lighter) covers
the helpers and the integration:

- **`TestBinHashCache_RoundTrip`** — `binHashPut` then
  `binHashLookup` with matching size+mtime returns the same digests
  with `ok == true`.
- **`TestBinHashCache_StaleMtime`** — `binHashPut` with mtime T,
  `binHashLookup` with mtime T+1 → `ok == false`. Caller must
  re-hash.
- **`TestBinHashCache_StaleSize`** — `binHashPut` with size N,
  `binHashLookup` with size N+1 → `ok == false`.
- **`TestBinHashCache_NilDB`** — `binHashLookup` against a `nil`
  db returns `ok == false` without panicking; `binHashPut` against
  a `nil` db is a no-op. Mirrors the `redumpGet`/`redumpPut` nil
  guards.
- **`TestHashCueBins_PopulatesCache`** — write a small bin
  (a few KB of random bytes), write a minimal one-track cue
  pointing at it, call `hashCueBins`. Assert the `bin_hash_cache`
  row exists, the stored digests equal an independent
  `hashCueBin` call's output, and the stored (size, mtime) match
  `os.Stat` of the bin.
- **`TestHashCueBins_UsesCache`** — pre-seed the cache with
  deliberately wrong digests for the bin path (using the actual
  stat'd size+mtime). Call `hashCueBins`. Assert
  `tracks[0].hashes["sha1"]` equals the wrong-but-cached value.
  This proves the read path short-circuits — the only way
  `hashCueBins` could return that sha1 is by trusting the cache
  rather than re-hashing the bin.

The integration tests need a real bin file on disk for `os.Stat`
to work; use `t.TempDir()` and the existing `writeTempBytes`
helper.

## Out of scope

- Cache eviction or size limits. Each row is roughly 200 bytes
  (3 hex digests + path + ints); 1000 entries is ~200 KB. The
  table will not grow large enough to matter for any realistic
  per-user corpus.
- Explicit invalidation API or `clear-cache` command. Stale rows
  are overwritten on next compute; if the user really wants a
  reset, deleting `db.sqlite` is the existing escape hatch.
- Instrumentation/metrics (hit rate, time saved).
- Migrating the existing `redump_cache` to use the same freshness
  pattern. `redump_cache` is keyed on content hash, not file
  identity — different invariant, different cache.
- Caching `.miniscram` inspect-JSON output. The inspect parse is
  already fast enough; the bin-stream is the bottleneck.

## Risk

- **mtime second-precision miss.** A file touched (e.g. `touch`,
  `chmod`, rsync with `-t` semantics that round) without content
  change appears stale, forcing a re-hash. Cost: one redundant
  hash run, then re-cached. Not a correctness hazard. Worth living
  with — the alternative (content sniffing or partial-hash
  pre-check) is meaningfully more complex.
- **Concurrent writes from two `hashCueBins` invocations.** SQLite
  serializes; final state is one of the two byte-identical writes.
  Documented above.
- **Bin path normalization.** `hashCueBins` is called with
  `fullPaths` built via `filepath.Join(m.dir, t.filename)` (`main.go:462`).
  If the same file is reached via two different absolute paths
  (e.g. symlink vs realpath), the cache treats them as separate
  entries. Cost is one extra row per alias. Acceptable; not worth
  adding `filepath.EvalSymlinks` overhead to every lookup.
