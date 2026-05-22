package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
// reported as failures, and the queue keeps only failed paths so the
// user can see what still needs action.
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
	remaining := q.snapshotUnusedScrams()
	if len(remaining) != 1 || remaining[0].Path != missing {
		t.Errorf("remaining unused scrams = %+v, want only %s", remaining, missing)
	}
}

func TestHandleDeleteUnusedScrams_FullSuccessToast(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.scram")
	b := filepath.Join(dir, "b.scram")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	q := newQueueModel()
	q.appendUnusedScram(unusedScram{Path: a, Size: 100})
	q.appendUnusedScram(unusedScram{Path: b, Size: 200})

	ts := handleDeleteUnusedScrams(q, time.Unix(100, 0))

	if ts == nil {
		t.Fatal("toast is nil")
	}
	if ts.Status == "fail" {
		t.Fatalf("toast.Status = fail, want success")
	}
	if !strings.Contains(ts.Message, "Deleted 2") {
		t.Fatalf("toast.Message = %q, want deleted count", ts.Message)
	}
	if len(q.snapshotUnusedScrams()) != 0 {
		t.Fatal("unused scram accumulator not cleared")
	}
}

func TestHandleDeleteUnusedScrams_PartialFailureToastAndRemaining(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.scram")
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.scram")
	q := newQueueModel()
	q.appendUnusedScram(unusedScram{Path: a, Size: 100})
	q.appendUnusedScram(unusedScram{Path: missing, Size: 200})

	ts := handleDeleteUnusedScrams(q, time.Unix(100, 0))

	if ts == nil {
		t.Fatal("toast is nil")
	}
	if ts.Status != "fail" {
		t.Fatalf("toast.Status = %q, want fail", ts.Status)
	}
	if !strings.Contains(ts.FailMsg, "Deleted 1; could not delete 1") {
		t.Fatalf("toast.FailMsg = %q", ts.FailMsg)
	}
	remaining := q.snapshotUnusedScrams()
	if len(remaining) != 1 || remaining[0].Path != missing {
		t.Fatalf("remaining unused scrams = %+v, want only %s", remaining, missing)
	}
}
