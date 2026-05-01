// scripts/sweep is a SQLite-backed pack-and-verify sweep over the
// CD-ROM dumps in /roms. Each invocation processes up to maxPerRun
// pending cases and exits, so progress is durable across runs.
//
// Usage (from repo root):
//
//	go run -C scripts/sweep .
//
// First run seeds the cases table by walking /roms for *.cue paired
// with same-stem *.scram. Each subsequent run picks the next pending
// case, copies its directory's contents to /tmp, runs `miniscram pack
// --keep-source --quiet` (default verify enabled), records the
// result, and cleans up.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	dbPath         = "/tmp/miniscram-sweep.db"
	workDir        = "/tmp/miniscram-sweep-work"
	binaryPath     = "/tmp/miniscram-sweep-bin"
	romsRoot       = "/roms"
	maxRomsDepth   = 2 // /roms/<game>/<maybe-disc-subdir>/<files>
	maxPerRun      = 10
	perCaseTimeout = 30 * time.Minute
)

var schema = `
CREATE TABLE IF NOT EXISTS cases (
    cue_path        TEXT PRIMARY KEY,
    status          TEXT,
    exit_code       INTEGER,
    seconds         REAL,
    scram_bytes     INTEGER,
    container_bytes INTEGER,
    error_last_line TEXT,
    started_at      TEXT,
    finished_at     TEXT
);
`

func main() {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(schema); err != nil {
		log.Fatal(err)
	}

	// Recover any cases left in 'running' from a killed prior run.
	if _, err := db.Exec(`UPDATE cases SET status=NULL, started_at=NULL WHERE status='running'`); err != nil {
		log.Fatal(err)
	}

	if err := seed(db); err != nil {
		log.Fatal(err)
	}

	if err := build(); err != nil {
		log.Fatalf("building miniscram: %v", err)
	}

	processed := 0
	for processed < maxPerRun {
		cue, err := pickOne(db)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		if err := process(db, cue); err != nil {
			log.Printf("process %s: %v", cue, err)
		}
		processed++
	}

	summarize(db, processed)
}

func seed(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cases`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	fmt.Println("seeding case list from", romsRoot, "...")
	var paths []string
	err := filepath.WalkDir(romsRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(romsRoot, p)
		if err != nil {
			return nil
		}
		if d.IsDir() {
			depth := strings.Count(rel, string(filepath.Separator))
			if depth > maxRomsDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".cue") {
			return nil
		}
		stem := strings.TrimSuffix(p, ".cue")
		if _, err := os.Stat(stem + ".scram"); err == nil {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO cases(cue_path) VALUES(?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, p := range paths {
		if _, err := stmt.Exec(p); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Printf("seeded %d cases\n", len(paths))
	return nil
}

func pickOne(db *sql.DB) (string, error) {
	var cue string
	err := db.QueryRow(`SELECT cue_path FROM cases WHERE status IS NULL ORDER BY cue_path LIMIT 1`).Scan(&cue)
	return cue, err
}

func process(db *sql.DB, cue string) error {
	startedAt := time.Now()
	if _, err := db.Exec(`UPDATE cases SET status='running', started_at=? WHERE cue_path=?`,
		startedAt.UTC().Format(time.RFC3339), cue); err != nil {
		return err
	}

	if err := os.RemoveAll(workDir); err != nil {
		return err
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	// Symlink sibling files into workDir rather than copying them.
	// /roms is RO and the source files (bin, scram) can be hundreds of
	// MB each — copying would either fill /tmp's 2 GB tmpfs or balloon
	// the VM's qcow disk if we used a non-tmpfs work area. The only
	// new bytes the case writes are pack's intermediates (hat tempfile,
	// scram-sized, deleted at end of pack; verify tempfile, scram-sized,
	// deleted at end of verify) and the final .miniscram container
	// (small). All of those land in /tmp where they belong.
	srcDir := filepath.Dir(cue)
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return finalize(db, cue, "FAIL", -1, time.Since(startedAt).Seconds(), 0, 0, "readdir: "+err.Error())
	}
	var scramSize int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(srcDir, e.Name())
		dst := filepath.Join(workDir, e.Name())
		if err := os.Symlink(src, dst); err != nil {
			return finalize(db, cue, "FAIL", -1, time.Since(startedAt).Seconds(), 0, 0, "symlink "+e.Name()+": "+err.Error())
		}
		if strings.HasSuffix(e.Name(), ".scram") {
			if info, err := os.Stat(src); err == nil {
				scramSize = info.Size()
			}
		}
	}

	base := strings.TrimSuffix(filepath.Base(cue), ".cue")
	workCue := filepath.Join(workDir, filepath.Base(cue))
	out := filepath.Join(workDir, base+".miniscram")

	ctx, cancel := context.WithTimeout(context.Background(), perCaseTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "pack", workCue, "-o", out, "--keep-source", "--quiet")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmdErr := cmd.Run()
	secs := time.Since(startedAt).Seconds()

	var (
		exitCode int
		status   string
		lastLine string
	)
	stderrText := stderr.String()
	switch {
	case cmdErr == nil:
		status = "PASS"
	case ctx.Err() == context.DeadlineExceeded:
		status = "TIMEOUT"
		exitCode = 124
		lastLine = lastNonEmpty(stderrText)
	default:
		var ee *exec.ExitError
		if errors.As(cmdErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
		if hasCrashSignature(stderrText) {
			status = "CRASH"
		} else {
			status = "FAIL"
		}
		lastLine = lastNonEmpty(stderrText)
	}

	var containerBytes int64
	if info, err := os.Stat(out); err == nil {
		containerBytes = info.Size()
	}

	if status != "PASS" {
		// Persist full stderr to a per-case log file for forensics.
		logDir := "/tmp/miniscram-sweep-logs"
		_ = os.MkdirAll(logDir, 0o755)
		logName := strings.ReplaceAll(strings.TrimPrefix(cue, "/roms/"), "/", "__")
		_ = os.WriteFile(filepath.Join(logDir, logName+".log"), []byte(stderrText), 0o644)
	}

	return finalize(db, cue, status, exitCode, secs, scramSize, containerBytes, lastLine)
}

func finalize(db *sql.DB, cue, status string, exitCode int, secs float64, scramBytes, containerBytes int64, errLine string) error {
	finishedAt := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
        UPDATE cases SET status=?, exit_code=?, seconds=?, scram_bytes=?, container_bytes=?, error_last_line=?, finished_at=?
        WHERE cue_path=?`,
		status, exitCode, secs, scramBytes, containerBytes, errLine, finishedAt, cue)
	tag := status
	if status != "PASS" {
		tag = status + " (" + errLine + ")"
	}
	fmt.Printf("[%6.1fs] %-7s %s\n", secs, tag, cue)
	return err
}

func lastNonEmpty(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			if len(line) > 200 {
				return line[:200]
			}
			return line
		}
	}
	return ""
}

func hasCrashSignature(s string) bool {
	low := strings.ToLower(s)
	for _, kw := range []string{"panic:", "runtime error", "sigsegv", "signal: "} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

func build() error {
	if _, err := os.Stat(binaryPath); err == nil {
		return nil
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// findRepoRoot returns the absolute path to the miniscram repo root,
// derived from this source file's location at build time.
func findRepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime.Caller(0) failed")
	}
	return filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
}

func summarize(db *sql.DB, processedThisRun int) {
	rows, err := db.Query(`SELECT status, COUNT(*) FROM cases GROUP BY status ORDER BY status`)
	if err != nil {
		log.Print(err)
		return
	}
	defer rows.Close()
	fmt.Println()
	fmt.Printf("Processed this run: %d (max %d)\n", processedThisRun, maxPerRun)
	fmt.Println("Overall status:")
	for rows.Next() {
		var s sql.NullString
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			log.Print(err)
			return
		}
		label := "PENDING"
		if s.Valid {
			label = s.String
		}
		fmt.Printf("  %-10s %d\n", label, n)
	}
}
