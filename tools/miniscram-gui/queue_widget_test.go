package main

import (
	"testing"

	"gioui.org/widget/material"
)

// TestReasonLabel_FailedCapped confirms qFailed reasons are capped
// at two lines. A single line hides too much of many real error texts
// (e.g. "cue contains only AUDIO tracks; nothing for miniscram to
// scramble-pack"); unlimited lines would tower over the queue (issue #50).
func TestReasonLabel_FailedCapped(t *testing.T) {
	th := material.NewTheme()
	long := "no plausible scrambled sync field found in 765077352 bytes of scram"
	lb := reasonLabel(th, qFailed, long)
	if lb.MaxLines != 2 {
		t.Errorf("reasonLabel(qFailed).MaxLines = %d, want 2", lb.MaxLines)
	}
}

// TestReasonLabel_OtherStatesUncapped confirms non-failed states keep
// the default (unbounded) wrapping behaviour.
func TestReasonLabel_OtherStatesUncapped(t *testing.T) {
	th := material.NewTheme()
	// qReady and qRunning aren't covered: queueRowSuffix returns
	// early for those states, so reasonLabel is never called.
	for _, st := range []queueState{qDone, qSkipped, qCancelled} {
		lb := reasonLabel(th, st, "short")
		if lb.MaxLines != 0 {
			t.Errorf("reasonLabel(%v).MaxLines = %d, want 0", st, lb.MaxLines)
		}
	}
}
