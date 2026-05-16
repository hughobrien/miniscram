package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestDeleteUnusedScrams_AllOK confirms the handler removes every
// path in the accumulator and returns zero failures.
func TestDeleteUnusedScrams_AllOK(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.scram")
	b := filepath.Join(dir, "b.scram")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	q := newQueueModel()
	q.appendUnusedScram(unusedScram{Path: a, Size: 1})
	q.appendUnusedScram(unusedScram{Path: b, Size: 1})

	failed := deleteUnusedScrams(q)
	if len(failed) != 0 {
		t.Errorf("failed = %v, want empty", failed)
	}
	for _, p := range []string{a, b} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists (err=%v)", p, err)
		}
	}
	if len(q.snapshotUnusedScrams()) != 0 {
		t.Errorf("accumulator not cleared after full success")
	}
}

// TestDeleteUnusedScrams_PartialFailure confirms missing paths are
// reported as failures, but the slice is still cleared and the
// existing file is still removed.
func TestDeleteUnusedScrams_PartialFailure(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.scram")
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "does-not-exist.scram")
	q := newQueueModel()
	q.appendUnusedScram(unusedScram{Path: a, Size: 1})
	q.appendUnusedScram(unusedScram{Path: missing, Size: 1})

	failed := deleteUnusedScrams(q)
	if len(failed) != 1 || failed[0] != missing {
		t.Errorf("failed = %v, want [%s]", failed, missing)
	}
	if _, err := os.Stat(a); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s still exists (err=%v); want removed", a, err)
	}
	if len(q.snapshotUnusedScrams()) != 0 {
		t.Errorf("accumulator not cleared after partial failure")
	}
}
