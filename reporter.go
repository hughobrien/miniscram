package main

import (
	"fmt"
	"io"
	"os"
	"time"
)

// Reporter writes human-readable progress to a writer. Implementations
// must be safe to call sequentially; concurrent access is not required.
type Reporter interface {
	Step(label string) StepHandle
	Info(format string, args ...any)
	Warn(format string, args ...any)
}

// StepHandle is returned from Reporter.Step. Done or Fail must be
// called exactly once per handle.
type StepHandle interface {
	Done(format string, args ...any)
	Fail(err error)
}

// NewReporter returns a reporter that writes to w. If quiet is true,
// it discards all output. ANSI/TTY decoration is enabled when w is the
// current process's stderr and stderr is a TTY.
func NewReporter(w io.Writer, quiet bool) Reporter {
	if quiet {
		return quietReporter{}
	}
	return &textReporter{w: w, tty: isStderrTTY(w)}
}

type textReporter struct {
	w   io.Writer
	tty bool
}

func (r *textReporter) Step(label string) StepHandle {
	fmt.Fprintf(r.w, "[%s] %s", time.Now().Format("15:04:05"), label)
	return &textStep{r: r}
}

func (r *textReporter) Info(format string, args ...any) {
	fmt.Fprintf(r.w, "[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func (r *textReporter) Warn(format string, args ...any) {
	fmt.Fprintf(r.w, "[%s] warning: %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

type textStep struct {
	r    *textReporter
	done bool
}

func (s *textStep) Done(format string, args ...any) {
	if s.done {
		return
	}
	s.done = true
	mark := "OK"
	if s.r.tty {
		mark = "✓"
	}
	fmt.Fprintf(s.r.w, " ... %s %s\n", mark, fmt.Sprintf(format, args...))
}

func (s *textStep) Fail(err error) {
	if s.done {
		return
	}
	s.done = true
	mark := "FAIL"
	if s.r.tty {
		mark = "✗"
	}
	fmt.Fprintf(s.r.w, " ... %s %s\n", mark, err.Error())
}

type quietReporter struct{}

func (quietReporter) Step(string) StepHandle { return quietStep{} }
func (quietReporter) Info(string, ...any)    {}
func (quietReporter) Warn(string, ...any)    {}

type quietStep struct{}

func (quietStep) Done(string, ...any) {}
func (quietStep) Fail(error)          {}

// runStep wraps the Step/Done/Fail pattern. fn returns (doneMsg, err);
// on success runStep calls Done(doneMsg), on failure Fail(err) and
// returns the error.
//
// Use for the common case where a step's body is a single computation
// whose result narrates the Done line. Steps with mid-body Info/Warn
// calls or whose Done message depends on multiple values should still
// hand-roll.
func runStep(r Reporter, label string, fn func() (string, error)) error {
	st := r.Step(label)
	msg, err := fn()
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done("%s", msg)
	return nil
}

// isStderrTTY returns true when w is the same fd as os.Stderr and that
// fd is a TTY. We deliberately avoid third-party deps here.
func isStderrTTY(w io.Writer) bool {
	if w != os.Stderr {
		return false
	}
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
