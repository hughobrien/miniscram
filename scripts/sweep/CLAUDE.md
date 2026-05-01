# CLAUDE.md — `scripts/sweep`

A SQLite-backed pack-and-verify sweep over a local CD-ROM corpus. Each
invocation processes up to `maxPerRun` (10) pending cases and exits;
state is durable in `/tmp/miniscram-sweep.db` so a long sweep can be
resumed by re-running.

## How the DB gets populated

The DB is created and seeded **lazily on first run**:

1. `sql.Open("sqlite", "/tmp/miniscram-sweep.db")` creates the file if
   it doesn't exist.
2. `CREATE TABLE IF NOT EXISTS cases (...)` is always run.
3. `seed()` checks `SELECT COUNT(*) FROM cases`. If non-zero, it
   returns immediately (the corpus is already enrolled). If zero, it
   walks `romsRoot` (default `/roms`) and inserts one row per
   `*.cue` that has a same-stem `*.scram` sibling.

The seed is **one-shot**: once any rows exist, it never runs again.
This is intentional — re-seeding mid-sweep would either clobber
in-progress results or no-op insert-conflict on the `cue_path`
primary key.

### Re-seed (e.g. after adding new dumps to `/roms`)

```bash
rm /tmp/miniscram-sweep.db
go run -C scripts/sweep .
```

The next run rebuilds the case list from a clean DB.

### Re-run completed cases without re-seeding

```bash
sqlite3 /tmp/miniscram-sweep.db "UPDATE cases SET status=NULL, exit_code=NULL, seconds=NULL, error_last_line=NULL, started_at=NULL, finished_at=NULL"
go run -C scripts/sweep .
```

## Running to completion

A single invocation does up to 10 cases and exits. To process the
whole corpus, loop:

```bash
while true; do
  go run -C scripts/sweep . || break
  pending=$(sqlite3 /tmp/miniscram-sweep.db "SELECT COUNT(*) FROM cases WHERE status IS NULL")
  [ "$pending" = "0" ] && break
done
```

`sqlite3` is a runtime dependency for the loop check. On Debian:
`sudo apt-get install -y sqlite3`.

## Per-case behaviour

For each pending case the tool:

1. Marks `status='running'` with `started_at`.
2. Symlinks every file in the cue's sibling directory into
   `/tmp/miniscram-sweep-work/`. Symlinks (not copies) because `/roms`
   is read-only and the source files (`.bin`, `.scram`) are hundreds
   of MB to ~1 GB each — copying would either exhaust `/tmp`'s 2 GB
   tmpfs or balloon the host's disk.
3. Runs `miniscram pack --keep-source --quiet` with a 30-minute
   per-case timeout. Default verify (round-trip rebuild + hash
   compare) stays enabled; `--keep-source` prevents the binary from
   touching the original `.scram` symlink.
4. Records `status` (PASS / FAIL / CRASH / TIMEOUT), `exit_code`,
   `seconds`, `scram_bytes`, `container_bytes`, and the last
   non-empty stderr line.
5. Removes `workDir`. Pack/verify intermediates (hat tempfile,
   verify tempfile, final container) all live in `/tmp` and are
   cleaned up by `miniscram` itself.

## Status values

| Status     | Meaning                                                    |
| ---------- | ---------------------------------------------------------- |
| `NULL`     | Pending — picked up by the next run.                       |
| `running`  | A prior invocation crashed mid-case. Reset to NULL on next startup. |
| `PASS`     | `miniscram pack` exited 0 (verify passed end-to-end).      |
| `FAIL`     | Non-zero exit, stderr does not match a crash signature.    |
| `CRASH`    | Stderr contains `panic:` / `runtime error` / `sigsegv` / `signal: `. |
| `TIMEOUT`  | Exceeded `perCaseTimeout` (30 min). Exit code recorded as 124. |

For non-PASS cases the tool also writes the **full** stderr to
`/tmp/miniscram-sweep-logs/<game>__<file>.log` for forensics.

## Hardcoded paths

All in `main.go:33-41`. Change the constants if running outside the
default layout:

| Constant         | Default                                | Notes                                                   |
| ---------------- | -------------------------------------- | ------------------------------------------------------- |
| `dbPath`         | `/tmp/miniscram-sweep.db`              | Persistent across runs; survives reboots if /tmp does.  |
| `workDir`        | `/tmp/miniscram-sweep-work`            | Recreated per case; must be on a writable filesystem.   |
| `binaryPath`     | `/tmp/miniscram-sweep-bin`             | `miniscram` build cache; rebuilt only if missing.       |
| `romsRoot`       | `/roms`                                | Walk root for the seed.                                 |
| `maxRomsDepth`   | `2`                                    | Directory-walk depth limit (separator count in rel path). Cues at `/roms/<game>/<sub>/<file>.cue` (depth 3) are reached because `c >= maxRomsDepth` skips dirs but doesn't filter files. |
| `maxPerRun`      | `10`                                   | Cases per invocation. Loop externally for full sweeps.  |
| `perCaseTimeout` | `30 * time.Minute`                     | Generous; real cases run 5-40s. Anything near this is a problem. |

## Why this lives in its own Go module

`scripts/sweep` imports `modernc.org/sqlite` (a pure-Go SQLite driver).
Keeping it in a nested module (`go.mod` here, distinct from the repo
root's `go.mod`) means the main `miniscram` binary doesn't pull
~30 transitive deps it doesn't need. The `go run -C scripts/sweep .`
invocation builds the sweep tool against its own module without
touching the parent.

## Recovering useful info from the DB

```bash
# Top 10 slowest cases
sqlite3 /tmp/miniscram-sweep.db \
  "SELECT ROUND(seconds,1)||'s', cue_path FROM cases WHERE status='PASS' ORDER BY seconds DESC LIMIT 10"

# Compression ratios
sqlite3 /tmp/miniscram-sweep.db \
  "SELECT cue_path, scram_bytes, container_bytes, ROUND(1.0*scram_bytes/container_bytes,1)||'x' AS ratio FROM cases WHERE status='PASS' ORDER BY ratio DESC LIMIT 10"

# Failure forensics
sqlite3 /tmp/miniscram-sweep.db \
  "SELECT status, cue_path, error_last_line FROM cases WHERE status NOT IN ('PASS') AND status IS NOT NULL"
```
