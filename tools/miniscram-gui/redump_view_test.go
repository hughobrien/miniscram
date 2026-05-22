package main

import (
	"strings"
	"testing"
)

func TestRedumpPlaintextCautionText(t *testing.T) {
	got := redumpPlaintextCautionText()
	if !strings.Contains(got, "plaintext") {
		t.Fatalf("caution text = %q, want plaintext warning", got)
	}
	if !strings.Contains(got, "SQLite") {
		t.Fatalf("caution text = %q, want SQLite mention", got)
	}
}
