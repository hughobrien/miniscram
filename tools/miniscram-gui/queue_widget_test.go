package main

import (
	"testing"

	"gioui.org/widget/material"
)

// TestFailedReasonLabel_StaysOneLine confirms failed-row detail text
// is capped to one line. Failed rows render the reason on their own
// second line, not as a wrapping right-hand suffix.
func TestFailedReasonLabel_StaysOneLine(t *testing.T) {
	th := material.NewTheme()
	long := "cue contains only AUDIO tracks; nothing for miniscram to scramble-pack"
	lb := failedReasonLabel(th, long)
	if lb.MaxLines != 1 {
		t.Errorf("failedReasonLabel().MaxLines = %d, want 1", lb.MaxLines)
	}
	if lb.Text == "" {
		t.Error("failedReasonLabel().Text is empty")
	}
}

func TestFailedReasonLabel_OnlyShowsTextBeforeSemicolon(t *testing.T) {
	th := material.NewTheme()
	lb := failedReasonLabel(th, "cue contains only AUDIO tracks; nothing for miniscram to scramble-pack")
	if lb.Text != "cue contains only AUDIO tracks" {
		t.Errorf("failedReasonLabel().Text = %q, want %q", lb.Text, "cue contains only AUDIO tracks")
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
