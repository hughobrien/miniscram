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

func TestStepDoneEmptyMessage(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, false)
	r.Step("foo").Done("")
	if got, want := buf.String(), "foo ... OK\n"; got != want {
		t.Fatalf("Done(\"\") = %q, want %q", got, want)
	}

	buf.Reset()
	r.Step("bar").Done("baz")
	if got, want := buf.String(), "bar ... OK baz\n"; got != want {
		t.Fatalf("Done(\"baz\") = %q, want %q", got, want)
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

func TestQuietReporterEmitsFailures(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, true)
	r.Step("resolving cue").Fail(errors.New("does not look like a cuesheet"))
	out := buf.String()
	if !strings.Contains(out, "resolving cue") {
		t.Fatalf("missing label in %q", out)
	}
	if !strings.Contains(out, "does not look like a cuesheet") {
		t.Fatalf("missing error text in %q", out)
	}
}

func TestQuietReporterSilencesProgress(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, true)
	r.Step("a").Done("done")
	r.Info("ignored")
	r.Warn("ignored")
	if buf.Len() != 0 {
		t.Fatalf("quiet reporter wrote %q on Done/Info/Warn", buf.String())
	}
}

func TestJSONReporter(t *testing.T) {
	var buf bytes.Buffer
	r := NewJSONReporter(&buf)

	s := r.Step("hashing scram")
	s.Done("c98323550138")

	s2 := r.Step("checking constant offset")
	s2.Done("") // empty msg — Msg field is omitempty so it disappears from output

	s3 := r.Step("layout sanity")
	s3.Fail(errors.New("layout mismatch ratio 0.07 exceeds 0.05"))

	r.Info("hello")
	r.Warn("careful")

	want := strings.Join([]string{
		`{"type":"step","label":"hashing scram"}`,
		`{"type":"done","label":"hashing scram","msg":"c98323550138"}`,
		`{"type":"step","label":"checking constant offset"}`,
		`{"type":"done","label":"checking constant offset"}`,
		`{"type":"step","label":"layout sanity"}`,
		`{"type":"fail","label":"layout sanity","error":"layout mismatch ratio 0.07 exceeds 0.05"}`,
		`{"type":"info","msg":"hello"}`,
		`{"type":"warn","msg":"careful"}`,
		``, // trailing newline from the last Encode
	}, "\n")
	if got := buf.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
