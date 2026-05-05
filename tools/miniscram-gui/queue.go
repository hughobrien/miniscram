// tools/miniscram-gui/queue.go
//
// Note: queueSnapshot (read-only snapshot struct) and (*queueModel).Snapshot()
// live here alongside the queueModel type, not in queue_widget.go, so that
// all model-layer code stays in one file. The widget file is read-only data.
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

	var cues []string
	var miniscramPaths []string // .miniscram files to load after releasing the lock
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
			miniscramPaths = append(miniscramPaths, abs)
			q.autoFollow = false
		}
	}
	added := 0
	for _, cue := range cues {
		if q.hasPath(cue) {
			continue
		}
		q.items = append(q.items, classify(cue, q.nextID))
		q.nextID++
		added++
	}
	// Adding new ready items reopens a stopped queue. Without this, a Stop
	// click leaves q.stopped=true forever and subsequent drops silently
	// no-op because nextReadyIndex always returns -1.
	if added > 0 && q.hasReady() {
		q.stopped = false
	}
	needWorker := !q.workerRunning && q.hasReady() && mdl != nil
	q.mu.Unlock()

	// Load .miniscram files into the inspect pane without holding the queue lock.
	if mdl != nil && len(miniscramPaths) > 0 {
		mdl.load(miniscramPaths[len(miniscramPaths)-1])
	}

	if needWorker {
		go q.drain(mdl)
	}
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
		return ev.Label + "…"
	case "done":
		return ev.Label + " ✓"
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
// Labels and their order come from pack.go and unpack.go (verify phase).
// Pack emits steps up to 0.95, then Verify (via Unpack) continues to 1.0.
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
	// Verify (round-trip) steps, emitted by unpack.go
	{"reading manifest", 0.96},
	{"reading container", 0.97},
	{"verifying bin hashes", 0.98},
	{"building scram prediction", 0.99},
	{"applying delta", 0.99},
	{"verifying output hashes", 0.995},
	{"verifying scram hashes", 1.0},
}

func lookupFraction(label string) (float64, bool) {
	for _, p := range packPhases {
		if strings.HasPrefix(label, p.Prefix) {
			return p.Fraction, true
		}
	}
	return 0, false
}

func (q *queueModel) hasReady() bool {
	for _, it := range q.items {
		if it.State == qReady {
			return true
		}
	}
	return false
}

// nextReadyIndex returns the index of the first qReady item, or -1 if none
// exist or if the queue is stopped.
func (q *queueModel) nextReadyIndex() int {
	if q.stopped {
		return -1
	}
	for i, it := range q.items {
		if it.State == qReady {
			return i
		}
	}
	return -1
}

// cancelRemainingReady flips all qReady items to qCancelled without spawning
// subprocesses. Called by drain's deferred cleanup.
func (q *queueModel) cancelRemainingReady() {
	for i, it := range q.items {
		if it.State == qReady {
			q.items[i].State = qCancelled
			_ = it
		}
	}
}

func (q *queueModel) markRunning(idx int) {
	q.items[idx].State = qRunning
	q.items[idx].StartedAt = time.Now()
}

func (q *queueModel) markFailed(idx int, reason string) {
	q.items[idx].State = qFailed
	q.items[idx].Reason = reason
}

// UpdateRunningProgress advances the currently-running item's Label and
// Fraction. The running-strip already shows the latest line; this drives
// the per-row green progress fill in the queue panel. Called from the
// FrameEvent loop on each frame; cheap no-op if no item is qRunning or
// the new fraction would not be a forward step.
func (q *queueModel) UpdateRunningProgress(label string, frac float64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.items {
		if q.items[i].State == qRunning {
			q.items[i].Label = label
			if frac > q.items[i].Fraction {
				q.items[i].Fraction = frac
			}
			return
		}
	}
}

// recordResult flips the row state + records duration based on the
// runner's actionResult classification.
func (q *queueModel) recordResult(idx int, res actionResult) {
	it := &q.items[idx]
	it.DurationMs = res.DurationMs
	switch res.Status {
	case "success":
		it.State = qDone
		it.Fraction = 1.0
	case "fail":
		it.State = qFailed
		it.Reason = res.Error
	case "cancelled":
		it.State = qCancelled
	}
}

// drain processes qReady items sequentially. The single-flight invariant
// in actionRunner means only one subprocess runs at a time. The worker
// owns mdl.runner.done while workerRunning == true.
func (q *queueModel) drain(mdl *model) {
	q.mu.Lock()
	if q.workerRunning {
		q.mu.Unlock()
		return
	}
	q.workerRunning = true
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.cancelRemainingReady() // flush any qReady left after stop / loop exit
		q.workerRunning = false
		q.mu.Unlock()
		if mdl != nil && mdl.invalidate != nil {
			mdl.invalidate()
		}
	}()

	for {
		q.mu.Lock()
		idx := q.nextReadyIndex()
		if idx == -1 {
			q.mu.Unlock()
			return
		}
		q.markRunning(idx)
		item := q.items[idx]
		autoFollow := q.autoFollow
		deleteScram := q.deleteScram
		q.mu.Unlock()

		if autoFollow && mdl != nil {
			mdl.load(item.CuePath)
		}
		if mdl != nil && mdl.invalidate != nil {
			mdl.invalidate()
		}

		out := strings.TrimSuffix(item.CuePath, filepath.Ext(item.CuePath)) + ".miniscram"
		args := []string{"pack", "--progress=json", item.CuePath}
		if !deleteScram {
			args = append(args, "--keep-source")
		}

		if err := mdl.runner.Start("pack", item.CuePath, out, args...); err != nil {
			q.mu.Lock()
			q.markFailed(idx, err.Error())
			q.mu.Unlock()
			continue
		}

		res := <-mdl.runner.done

		q.mu.Lock()
		q.recordResult(idx, res)
		item = q.items[idx] // refresh for buildEventRec
		q.mu.Unlock()

		if mdl != nil && mdl.db != nil {
			ev := buildEventRec(mdl, "pack", item.CuePath, out, res)
			eventInsert(mdl.db, ev)
			mdl.refreshStats()
		}
		// Mirror the single-file flow's post-pack auto-load: switch the
		// right pane onto the freshly written .miniscram. For the last
		// item this is the final view the user sees; mid-queue this
		// briefly shows the result before the next iteration's
		// mdl.load(nextCue) at the top of the loop replaces it.
		// Gated on autoFollow because the user may have taken control
		// of the right pane mid-queue and we don't want to yank it.
		if autoFollow && res.Status == "success" && mdl != nil {
			mdl.load(out)
		}
		if mdl != nil && mdl.invalidate != nil {
			mdl.invalidate()
		}
	}
}

// queueSnapshot is a read-only copy of the queue state for the UI thread.
// It is built under the mutex and passed to layout without holding the lock.
type queueSnapshot struct {
	Items         []queueItem
	DeleteScram   bool
	WorkerRunning bool
	ReadyCount    int
	SkippedCount  int
}

// removeByID removes the item with the given ID from the queue.
// Safe to call from the UI goroutine.
func (q *queueModel) removeByID(id int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, it := range q.items {
		if it.ID == id {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return
		}
	}
}

// Snapshot builds a queueSnapshot under the mutex. Call from the UI goroutine
// each frame; do not hold the result across frames (stale item data).
func (q *queueModel) Snapshot() queueSnapshot {
	q.mu.Lock()
	defer q.mu.Unlock()
	cp := make([]queueItem, len(q.items))
	copy(cp, q.items)
	s := queueSnapshot{
		Items:         cp,
		DeleteScram:   q.deleteScram,
		WorkerRunning: q.workerRunning,
	}
	for _, it := range cp {
		switch it.State {
		case qReady:
			s.ReadyCount++
		case qSkipped:
			s.SkippedCount++
		}
	}
	return s
}
