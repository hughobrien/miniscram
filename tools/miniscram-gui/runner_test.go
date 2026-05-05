// tools/miniscram-gui/runner_test.go
package main

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestMain re-exec mode: when FAKE_MODE is set, the test binary
// pretends to be `miniscram` and behaves per the env var.
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_MODE") {
	case "":
		os.Exit(m.Run())
	case "happy":
		// emit 3 stderr lines on a 50ms cadence, exit 0
		for i, line := range []string{
			"hashing scram ... OK",
			"building scram prediction ... OK 355586 sector(s)",
			"verifying scram hashes ... OK all three match",
		} {
			fmt.Fprintln(os.Stderr, line)
			if i < 2 {
				time.Sleep(50 * time.Millisecond)
			}
		}
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "hashing scram ... started")
		fmt.Fprintln(os.Stderr, "scram not found: /no/such/file.scram")
		os.Exit(1)
	case "long":
		// run long enough to be cancelled; handle SIGTERM and exit 130
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)
		fmt.Fprintln(os.Stderr, "applying delta ...")
		select {
		case <-sig:
			os.Exit(130) // standard SIGTERM exit code
		case <-time.After(5 * time.Second):
			os.Exit(0)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown FAKE_MODE")
		os.Exit(2)
	}
}

func newTestRunner(t *testing.T, fakeMode string) (*actionRunner, chan actionResult) {
	t.Helper()
	done := make(chan actionResult, 1)
	r := &actionRunner{
		binary: os.Args[0],
		onDone: func(res actionResult) { done <- res },
	}
	t.Setenv("FAKE_MODE", fakeMode)
	return r, done
}

func TestActionRunner_Happy(t *testing.T) {
	gBefore := runtime.NumGoroutine()
	r, done := newTestRunner(t, "happy")

	// Use os.Args[0] (the test binary) as a stand-in output path so
	// os.Stat in wait() returns a real non-zero size. The runner only
	// uses Output for state/result; it isn't passed to the command.
	output := os.Args[0]
	if err := r.Start("verify", "/in/path.miniscram", output); err != nil {
		t.Fatalf("Start: %v", err)
	}

	res := waitFor(t, done, 3*time.Second)
	if res.Status != "success" {
		t.Errorf("status = %q, want success (err=%q)", res.Status, res.Error)
	}
	if res.DurationMs <= 0 {
		t.Errorf("duration = %dms, want > 0", res.DurationMs)
	}
	if res.OutputSize <= 0 {
		t.Errorf("OutputSize = %d, want > 0 for existing output path %q", res.OutputSize, output)
	}

	time.Sleep(100 * time.Millisecond)
	if leaked := runtime.NumGoroutine() - gBefore; leaked > 1 {
		t.Errorf("goroutine leak: %d new goroutines after run", leaked)
	}
}

func TestActionRunner_Fail(t *testing.T) {
	r, done := newTestRunner(t, "fail")

	if err := r.Start("pack", "/in/disc.cue", "/out/disc.miniscram"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	res := waitFor(t, done, 3*time.Second)
	if res.Status != "fail" {
		t.Errorf("status = %q, want fail", res.Status)
	}
	if !strings.Contains(res.Error, "scram not found") {
		t.Errorf("Error = %q, want it to contain 'scram not found'", res.Error)
	}
}

func TestActionRunner_Cancel(t *testing.T) {
	r, done := newTestRunner(t, "long")

	if err := r.Start("pack", "/in/disc.cue", "/out/disc.miniscram"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	r.Cancel()

	res := waitFor(t, done, 2*time.Second)
	if res.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", res.Status)
	}
}

func TestActionRunner_SingleFlight(t *testing.T) {
	r, done := newTestRunner(t, "long")

	if err := r.Start("pack", "a", "b"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer func() {
		r.Cancel()
		<-done
	}()

	if err := r.Start("verify", "c", ""); err != errAlreadyRunning {
		t.Errorf("second Start err = %v, want errAlreadyRunning", err)
	}
}

func TestActionRunner_InvalidateOnLine(t *testing.T) {
	r, done := newTestRunner(t, "happy")
	var ticks atomic.Int64
	r.invalidate = func() { ticks.Add(1) }

	if err := r.Start("verify", "/in/path.miniscram", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-done

	if got := ticks.Load(); got < 4 {
		t.Errorf("invalidate calls = %d, want ≥ 4", got)
	}
}

func waitFor(t *testing.T, done <-chan actionResult, timeout time.Duration) actionResult {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(timeout):
		t.Fatalf("onDone did not fire within %v", timeout)
		return actionResult{}
	}
}
