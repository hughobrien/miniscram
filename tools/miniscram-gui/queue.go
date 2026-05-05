// tools/miniscram-gui/queue.go
package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type queueState string

const (
	qReady     queueState = "ready"
	qSkipped   queueState = "skipped"
	qRunning   queueState = "running"
	qDone      queueState = "done"
	qFailed    queueState = "failed"
	qCancelled queueState = "cancelled"
)

type queueItem struct {
	ID         int64
	CuePath    string // absolute, filepath.Clean'd
	Basename   string
	Reason     string
	State      queueState
	Label      string  // current --progress=json step label
	Fraction   float64 // 0..1
	StartedAt  time.Time
	DurationMs int64
}

type queueModel struct {
	mu            sync.Mutex
	items         []queueItem
	nextID        int64
	deleteScram   bool
	stopped       bool
	autoFollow    bool
	workerRunning bool
}

func newQueueModel() *queueModel {
	return &queueModel{
		deleteScram: true, // matches the existing cue-view checkbox default
		autoFollow:  true,
	}
}

// Note: also add `queue *queueModel` to the `model` struct in main.go in
// this task. The field is referenced by tests in later tasks. Initialize it
// in `main()` after `mdl.runner = ...` with `mdl.queue = newQueueModel()`.

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// classify inspects a cue's siblings to determine its initial state.
func classify(cue string, id int64) queueItem {
	base := strings.TrimSuffix(cue, filepath.Ext(cue))
	item := queueItem{
		ID:       id,
		CuePath:  cue,
		Basename: filepath.Base(cue),
	}
	switch {
	case !exists(base + ".scram"):
		item.State = qSkipped
		item.Reason = "no sibling .scram"
	case exists(base + ".miniscram"):
		item.State = qSkipped
		item.Reason = "already packed"
	default:
		item.State = qReady
	}
	return item
}

// walkForCues finds *.cue under root recursively. Returns absolute
// paths sorted ascending. Skips dot-prefixed dirs except the root.
func walkForCues(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".cue") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func (q *queueModel) hasPath(abs string) bool {
	for _, it := range q.items {
		if it.CuePath == abs {
			return true
		}
	}
	return false
}

// addPaths is the single funnel for drop / multi-pick / dir-pick.
// mdl may be nil in tests (no .miniscram inspect-load triggered).
func (q *queueModel) addPaths(mdl *model, paths []string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	var cues []string
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		st, err := os.Stat(abs)
		if err != nil {
			continue
		}
		switch {
		case st.IsDir():
			cues = append(cues, walkForCues(abs)...)
		case strings.EqualFold(filepath.Ext(abs), ".cue"):
			cues = append(cues, abs)
		case strings.EqualFold(filepath.Ext(abs), ".miniscram"):
			// Single-file flow: load into inspect. Disengages autoFollow.
			if mdl != nil {
				mdl.load(abs)
				q.autoFollow = false
			}
		}
	}
	for _, cue := range cues {
		if q.hasPath(cue) {
			continue
		}
		q.items = append(q.items, classify(cue, q.nextID))
		q.nextID++
	}
	// TODO(Task 4): if !q.workerRunning && q.hasReady() { go q.drain(mdl) }
}

// progressEvent is the wire shape emitted by `miniscram --progress=json`.
// Mirrors the struct in reporter.go; redefined here because the GUI is its
// own Go module and cannot import the main package.
type progressEvent struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Msg   string `json:"msg,omitempty"`
	Error string `json:"error,omitempty"`
}

// prettyProgressLine renders a stderr line for human display. NDJSON
// events from --progress=json get a friendly form; non-JSON falls
// through unchanged so the strip keeps working in any future mode.
func prettyProgressLine(s string) string {
	var ev progressEvent
	if err := json.Unmarshal([]byte(s), &ev); err != nil {
		return s
	}
	switch ev.Type {
	case "step":
		return "step: " + ev.Label + "…"
	case "done":
		return "done: " + ev.Label + " ✓"
	case "info", "warn":
		return ev.Msg
	case "fail":
		if ev.Error != "" {
			return ev.Error
		}
		return ev.Label + " failed"
	}
	return s
}

// packPhases maps known pack-step label prefixes to a target fraction.
// Order matters only for human readability; lookup is by prefix-match.
// Labels and their order come from pack.go (see the spec for citations).
var packPhases = []struct {
	Prefix   string
	Fraction float64
}{
	{"resolving cue", 0.02},
	{"detecting write offset", 0.05},
	{"checking constant offset", 0.08}, // conditional
	{"hashing tracks", 0.15},
	{"hashing scram", 0.30},
	{"building scram prediction + delta", 0.65},
	{"writing container", 0.95},
}

func lookupFraction(label string) (float64, bool) {
	for _, p := range packPhases {
		if strings.HasPrefix(label, p.Prefix) {
			return p.Fraction, true
		}
	}
	return 0, false
}
