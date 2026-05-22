package main

import "testing"

func TestStageMockQueue_AudioFail(t *testing.T) {
	q := newQueueModel()
	stageMockQueue(q, "audio-fail")

	snap := q.Snapshot()
	if snap.WorkerRunning {
		t.Fatal("WorkerRunning = true, want false for completed audio-fail screenshot")
	}
	if len(snap.Items) != 6 {
		t.Fatalf("len(Items) = %d, want 6", len(snap.Items))
	}
	var done, failed int
	for _, it := range snap.Items {
		switch it.State {
		case qDone:
			done++
		case qFailed:
			failed++
			if it.Basename != "SC.cue" {
				t.Errorf("failed Basename = %q, want SC.cue", it.Basename)
			}
			const want = "cue contains only AUDIO tracks; nothing for miniscram to scramble-pack"
			if it.Reason != want {
				t.Errorf("failed Reason = %q, want %q", it.Reason, want)
			}
		}
	}
	if done != 5 {
		t.Errorf("done count = %d, want 5", done)
	}
	if failed != 1 {
		t.Errorf("failed count = %d, want 1", failed)
	}
	if len(snap.UnusedScrams) != 1 {
		t.Fatalf("len(UnusedScrams) = %d, want 1", len(snap.UnusedScrams))
	}
	if snap.UnusedScrams[0].Path != "/audio-fail/SC.scram" {
		t.Errorf("UnusedScrams[0].Path = %q, want /audio-fail/SC.scram", snap.UnusedScrams[0].Path)
	}
}
