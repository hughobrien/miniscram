package main

import (
	"strings"
	"testing"

	"gioui.org/widget"
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

func TestInitRedumpEditorsDoesNotPrefillSavedPassword(t *testing.T) {
	var user, pass widget.Editor
	mdl := &model{
		redumpUsername: "saved-user",
	}

	initRedumpEditors(mdl, &user, &pass)

	if got := user.Text(); got != "saved-user" {
		t.Fatalf("username editor text = %q, want saved-user", got)
	}
	if got := pass.Text(); got != "" {
		t.Fatalf("password editor text = %q, want empty string", got)
	}
}
