package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestReporterStep(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, false)
	r.Step("hashing bin").Done("done sha256:abcdef")
	out := buf.String()
	if !strings.Contains(out, "hashing bin") || !strings.Contains(out, "sha256:abcdef") {
		t.Fatalf("Step.Done missing pieces in %q", out)
	}
	buf.Reset()
	r.Step("another").Fail(errors.New("boom"))
	out = buf.String()
	if !strings.Contains(out, "another") || !strings.Contains(out, "boom") {
		t.Fatalf("Step.Fail missing pieces in %q", out)
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
