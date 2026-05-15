// tools/miniscram-gui/queue_widget_test.go
package main

import (
	"testing"

	"gioui.org/widget/material"
)

// TestReasonLabel_FailedSingleLine confirms qFailed reasons render as
// a single line with the default truncator. Without this cap, error
// strings wrap multi-line and tower over the queue (see issue #50).
func TestReasonLabel_FailedSingleLine(t *testing.T) {
	th := material.NewTheme()
	long := "no plausible scrambled sync field found in 765077352 bytes of scram"
	lb := reasonLabel(th, qFailed, long)
	if lb.MaxLines != 1 {
		t.Errorf("reasonLabel(qFailed).MaxLines = %d, want 1", lb.MaxLines)
	}
}

// TestReasonLabel_OtherStatesUncapped confirms non-failed states keep
// the default (unbounded) wrapping behaviour.
func TestReasonLabel_OtherStatesUncapped(t *testing.T) {
	th := material.NewTheme()
	for _, st := range []queueState{qDone, qSkipped, qCancelled} {
		lb := reasonLabel(th, st, "short")
		if lb.MaxLines != 0 {
			t.Errorf("reasonLabel(%v).MaxLines = %d, want 0", st, lb.MaxLines)
		}
	}
}
