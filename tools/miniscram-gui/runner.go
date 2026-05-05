// tools/miniscram-gui/runner.go
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// runningState is the snapshot of an in-flight subprocess.
type runningState struct {
	Action     string    // "pack" | "unpack" | "verify"
	Input      string    // source file path
	Output     string    // optional; destination path for pack/unpack
	StartedAt  time.Time
	LastLine   string // most recent non-empty stderr line
	Cancelling bool   // set between Cancel() and process exit
}

// actionResult is what onDone receives when a subprocess finishes.
type actionResult struct {
	Action     string
	Input      string
	Output     string
	DurationMs int64
	Status     string // "success" | "fail" | "cancelled"
	Error      string // tail of stderr on fail
	OutputSize int64  // os.Stat(output).Size() on success, 0 otherwise
}

// actionRunner orchestrates a single in-flight miniscram subprocess.
// Single-flight: Start refuses while one is running.
type actionRunner struct {
	mu         sync.Mutex
	binary     string // defaults to "miniscram"; tests override
	cmd        *exec.Cmd
	state      *runningState
	onDone     func(actionResult)
	invalidate func()
}

func newActionRunner(invalidate func(), onDone func(actionResult)) *actionRunner {
	return &actionRunner{
		binary:     "miniscram",
		invalidate: invalidate,
		onDone:     onDone,
	}
}

// Snapshot returns a copy of the current state, or nil when idle.
// Safe to call from any goroutine.
func (r *actionRunner) Snapshot() *runningState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		return nil
	}
	s := *r.state
	return &s
}

// Running reports whether a subprocess is in flight.
func (r *actionRunner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state != nil
}

var errAlreadyRunning = errors.New("an action is already running")

// Start spawns miniscram with the given args. action/input/output are
// recorded in runningState for UI display; they do NOT shape the
// command line — caller passes the literal argv via args.
//
// Returns errAlreadyRunning if single-flight is violated.
func (r *actionRunner) Start(action, input, output string, args ...string) error {
	r.mu.Lock()
	if r.state != nil {
		r.mu.Unlock()
		return errAlreadyRunning
	}
	cmd := exec.Command(r.binary, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		r.mu.Unlock()
		return err
	}
	if err := cmd.Start(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.cmd = cmd
	r.state = &runningState{
		Action:    action,
		Input:     input,
		Output:    output,
		StartedAt: time.Now(),
	}
	r.mu.Unlock()

	go r.readStderr(stderr)
	go r.wait()
	return nil
}

// Cancel sends SIGTERM to the in-flight subprocess. No-op if idle.
func (r *actionRunner) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil || r.cmd == nil || r.cmd.Process == nil {
		return
	}
	r.state.Cancelling = true
	_ = r.cmd.Process.Signal(syscall.SIGTERM)
	if r.invalidate != nil {
		r.invalidate()
	}
}

// readStderr line-tails the subprocess stderr into state.LastLine.
// Runs on its own goroutine; exits when the pipe closes.
func (r *actionRunner) readStderr(stderr io.ReadCloser) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		r.mu.Lock()
		if r.state != nil {
			r.state.LastLine = line
		}
		r.mu.Unlock()
		if r.invalidate != nil {
			r.invalidate()
		}
	}
}

// wait blocks until the subprocess exits, fires onDone with the
// classified result, and clears state for the next Start.
func (r *actionRunner) wait() {
	err := r.cmd.Wait()

	r.mu.Lock()
	state := r.state
	r.cmd = nil
	r.state = nil
	r.mu.Unlock()
	if state == nil {
		// Defensive: wait runs exactly once per Start, so this is unreachable
		// unless a future change clears state from elsewhere. Don't panic.
		return
	}

	res := actionResult{
		Action:     state.Action,
		Input:      state.Input,
		Output:     state.Output,
		DurationMs: time.Since(state.StartedAt).Milliseconds(),
	}
	switch {
	case state.Cancelling:
		res.Status = "cancelled"
	case err != nil:
		res.Status = "fail"
		res.Error = state.LastLine
		if res.Error == "" {
			res.Error = err.Error()
		}
	default:
		res.Status = "success"
		if state.Output != "" {
			if st, err := os.Stat(state.Output); err == nil {
				res.OutputSize = st.Size()
			}
		}
	}

	if r.onDone != nil {
		r.onDone(res)
	}
	if r.invalidate != nil {
		r.invalidate()
	}
}

// runningStripWidget renders the running-state strip just under the top
// bar's divider when an action is in flight. Returns zero dimensions
// when state is nil so the layout collapses.
func runningStripWidget(th *material.Theme, state *runningState, cancelBtn *widget.Clickable) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if state == nil {
			return layout.Dimensions{}
		}
		actionVerb := map[string]string{
			"pack":   "Packing",
			"unpack": "Unpacking",
			"verify": "Verifying",
		}[state.Action]
		if actionVerb == "" {
			actionVerb = "Running"
		}
		basename := filepath.Base(state.Input)
		elapsed := time.Since(state.StartedAt).Truncate(time.Second)
		stepText := state.LastLine
		if stepText == "" {
			stepText = "Starting…"
		}
		cancelLabel := "Cancel"
		if state.Cancelling {
			cancelLabel = "Cancelling…"
		}

		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(24), Right: unit.Dp(24)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lb := material.Label(th, unit.Sp(13), actionVerb+" "+basename)
						lb.Color = text1
						lb.Font.Weight = font.SemiBold
						return lb.Layout(gtx)
					}),
					layout.Rigid(spacer(12, 0)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						lb := material.Label(th, unit.Sp(12), stepText)
						lb.Color = text2
						lb.Font.Typeface = "Go Mono"
						return lb.Layout(gtx)
					}),
					layout.Rigid(spacer(12, 0)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lb := material.Label(th, unit.Sp(12), fmt.Sprintf("%ds", int(elapsed.Seconds())))
						lb.Color = text3
						lb.Font.Typeface = "Go Mono"
						return lb.Layout(gtx)
					}),
					layout.Rigid(spacer(12, 0)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if state.Cancelling {
							gtx = gtx.Disabled()
						}
						btn := material.Button(th, cancelBtn, cancelLabel)
						btn.Background = surface2
						btn.Color = text1
						btn.CornerRadius = unit.Dp(4)
						btn.TextSize = unit.Sp(12)
						btn.Inset = layout.Inset{Top: 5, Bottom: 5, Left: 12, Right: 12}
						return btn.Layout(gtx)
					}),
				)
			})
		call := macro.Stop()
		bg := mustRGB("13262d")
		paint.FillShape(gtx.Ops, bg, clip.Rect{Max: dims.Size}.Op())
		call.Add(gtx.Ops)
		// Re-draw on every animation frame so the elapsed counter ticks.
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(200 * time.Millisecond)})
		return dims
	}
}
