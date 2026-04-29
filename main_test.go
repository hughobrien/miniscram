// /home/hugh/miniscram/main_test.go
package main

import (
	"bytes"
	"io"
	"testing"
)

func TestCLIPackRequiresOnePositional(t *testing.T) {
	var stderr bytes.Buffer
	// Zero positionals.
	code := run([]string{"pack"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("zero positionals exit %d, want %d", code, exitUsage)
	}
	// Two positionals.
	stderr.Reset()
	code = run([]string{"pack", "a.cue", "b.scram"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("two positionals exit %d, want %d", code, exitUsage)
	}
}

func TestCLIUnpackRequiresOnePositional(t *testing.T) {
	var stderr bytes.Buffer
	// Zero positionals.
	code := run([]string{"unpack"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("zero positionals exit %d, want %d", code, exitUsage)
	}
	// Two positionals.
	stderr.Reset()
	code = run([]string{"unpack", "a.miniscram", "b.extra"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("two positionals exit %d, want %d", code, exitUsage)
	}
}

func TestCLIVerifyRequiresOnePositional(t *testing.T) {
	var stderr bytes.Buffer
	// Zero positionals.
	code := run([]string{"verify"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("zero positionals exit %d, want %d", code, exitUsage)
	}
	// Two positionals.
	stderr.Reset()
	code = run([]string{"verify", "a.miniscram", "b.extra"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("two positionals exit %d, want %d", code, exitUsage)
	}
}

func TestCLIUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"foo"}, io.Discard, &stderr)
	if code != exitUsage {
		t.Fatalf("got exit %d, want %d", code, exitUsage)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("unknown command")) {
		t.Fatalf("missing 'unknown command' in stderr")
	}
}

func TestCLIVersion(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--version"}, io.Discard, &stderr)
	if code != exitOK {
		t.Fatalf("got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("miniscram")) {
		t.Fatalf("missing version: %s", stderr.String())
	}
}
