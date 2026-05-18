package main

import (
	"testing"

	"gioui.org/text"
	"gioui.org/widget/material"
)

// TestReasonLabel_FailedWrapsAtGraphemes confirms qFailed reasons use
// WrapGraphemes instead of MaxLines=1, so text wraps at character
// boundaries rather than being truncated with ellipsis.
func TestReasonLabel_FailedWrapsAtGraphemes(t *testing.T) {
	th := material.NewTheme()
	long := "cue contains only AUDIO tracks; nothing for miniscram to scramble-pack"
	lb := reasonLabel(th, qFailed, long)
	if lb.MaxLines != 0 {
		t.Errorf("reasonLabel(qFailed).MaxLines = %d, want 0 (unbounded)", lb.MaxLines)
	}
	if lb.WrapPolicy != text.WrapGraphemes {
		t.Errorf("reasonLabel(qFailed).WrapPolicy = %v, want text.WrapGraphemes", lb.WrapPolicy)
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
