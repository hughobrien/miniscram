package main

import "os"

// deleteUnusedScrams snapshots the queue's accumulator, removes each
// path, clears the accumulator regardless of outcome, and returns the
// paths that could not be removed. Errors per file are not surfaced;
// the caller decides how to message a partial failure.
//
// Caller must be the Gio UI goroutine — same goroutine that calls
// appendUnusedScram. The lock is released between snapshot and
// clear; goroutine serialisation is what prevents a newly appended
// entry from being silently wiped by the clear.
func deleteUnusedScrams(q *queueModel) []string {
	snap := q.snapshotUnusedScrams()
	var failed []string
	for _, u := range snap {
		if err := os.Remove(u.Path); err != nil {
			failed = append(failed, u.Path)
		}
	}
	q.clearUnusedScrams()
	return failed
}
