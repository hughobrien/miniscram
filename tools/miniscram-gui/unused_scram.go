package main

import "os"

// deleteUnusedScrams snapshots the queue's accumulator, removes each
// path, clears the accumulator regardless of outcome, and returns the
// paths that could not be removed. Errors per file are not surfaced;
// the caller decides how to message a partial failure.
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
