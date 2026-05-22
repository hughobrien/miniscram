package main

func stageMockQueue(q *queueModel, label string) {
	type mockItem struct {
		Name       string
		State      queueState
		Frac       float64
		Reason     string
		DurationMs int64
	}

	mockBasenames := []mockItem{
		{"freelancer.cue", qDone, 1.0, "", 5400},
		{"deus-ex.cue", qRunning, 0.55, "", 0},
		{"half-life.cue", qReady, 0, "", 0},
		{"mp2-play.cue", qReady, 0, "", 0},
		{"oddworld.cue", qSkipped, 0, "no sibling .scram", 0},
		{"baldurs-gate.cue", qSkipped, 0, "already packed", 0},
	}
	workerRunning := true
	unusedScrams := []unusedScram(nil)

	if label == "audio-fail" {
		const audioOnlyReason = "cue contains only AUDIO tracks; nothing for miniscram to scramble-pack"
		mockBasenames = []mockItem{
			{"PCP149A.cue", qDone, 1.0, "", 4200},
			{"PCP149B.cue", qDone, 1.0, "", 3900},
			{"SC.cue", qFailed, 0, audioOnlyReason, 0},
			{"SLES_03396.cue", qDone, 1.0, "", 5100},
			{"THRASHER.cue", qDone, 1.0, "", 4700},
			{"TPC_Games.cue", qDone, 1.0, "", 4400},
		}
		workerRunning = false
		unusedScrams = []unusedScram{{Path: "/audio-fail/SC.scram", Size: 765077352}}
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = nil
	q.unusedScrams = nil
	q.nextID = 0
	for _, m := range mockBasenames {
		q.items = append(q.items, queueItem{
			ID:         q.nextID,
			CuePath:    "/" + label + "/" + m.Name,
			Basename:   m.Name,
			State:      m.State,
			Fraction:   m.Frac,
			Reason:     m.Reason,
			DurationMs: m.DurationMs,
		})
		q.nextID++
	}
	q.workerRunning = workerRunning
	q.unusedScrams = append(q.unusedScrams, unusedScrams...)
}
