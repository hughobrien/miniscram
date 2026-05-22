package main

import (
	"fmt"
	"os"
	"time"
)

// deleteUnusedScrams snapshots the queue's accumulator, removes each
// path, keeps only failed entries in the accumulator, and returns the
// paths that could not be removed. Errors per file are not surfaced;
// the caller decides how to message a partial failure.
//
// Caller must be the Gio UI goroutine — same goroutine that calls
// appendUnusedScram. The lock is released between snapshot and
// replacement; goroutine serialisation is what prevents a newly
// appended entry from being silently wiped by the replacement.
func deleteUnusedScrams(q *queueModel) []string {
	snap := q.snapshotUnusedScrams()
	var failed []string
	var failedEntries []unusedScram
	for _, u := range snap {
		if err := os.Remove(u.Path); err != nil {
			failed = append(failed, u.Path)
			failedEntries = append(failedEntries, u)
		}
	}
	q.mu.Lock()
	q.unusedScrams = failedEntries
	q.mu.Unlock()
	return failed
}

func handleDeleteUnusedScrams(q *queueModel, now time.Time) *toastState {
	snap := q.snapshotUnusedScrams()
	if len(snap) == 0 {
		return nil
	}
	failed := deleteUnusedScrams(q)
	deleted := len(snap) - len(failed)
	if len(failed) > 0 {
		msg := fmt.Sprintf("could not delete %d .scram file(s) — check permissions or remove manually", len(failed))
		if deleted > 0 {
			msg = fmt.Sprintf("Deleted %d; could not delete %d", deleted, len(failed))
		}
		return &toastState{
			Status:    "fail",
			FailMsg:   msg,
			ExpiresAt: now.Add(8 * time.Second),
		}
	}
	var total int64
	for _, u := range snap {
		total += u.Size
	}
	noun := "audio .scram"
	if deleted != 1 {
		noun = "audio .scrams"
	}
	return &toastState{
		Message:   fmt.Sprintf("Deleted %d %s (%s)", deleted, noun, humanBytes(total)),
		ExpiresAt: now.Add(8 * time.Second),
	}
}
