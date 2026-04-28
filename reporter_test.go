// /home/hugh/miniscram/reporter_test.go
package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestReporterStepDone(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, false)
	st := r.Step("hashing bin")
	st.Done("done sha256:abcdef")
	out := buf.String()
	if !strings.Contains(out, "hashing bin") || !strings.Contains(out, "sha256:abcdef") {
		t.Fatalf("missing pieces in %q", out)
	}
}

func TestReporterStepFail(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, false)
	st := r.Step("hashing bin")
	st.Fail(errors.New("boom"))
	out := buf.String()
	if !strings.Contains(out, "hashing bin") || !strings.Contains(out, "boom") {
		t.Fatalf("missing pieces in %q", out)
	}
}

func TestReporterQuietProducesNoOutput(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, true)
	r.Step("a").Done("done")
	r.Step("b").Fail(errors.New("e"))
	r.Info("ignored")
	r.Warn("ignored")
	if buf.Len() != 0 {
		t.Fatalf("quiet reporter wrote %q", buf.String())
	}
}

func TestReporterInfoAndWarn(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, false)
	r.Info("hello %s", "world")
	r.Warn("watch %d", 42)
	out := buf.String()
	if !strings.Contains(out, "hello world") || !strings.Contains(out, "watch 42") {
		t.Fatalf("missing in %q", out)
	}
}
