package main

import (
	"errors"
	"strings"
	"testing"
	"time"

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

func TestStartRedumpTestLoginReturnsBeforeLoginCompletes(t *testing.T) {
	mdl := newTestModel(t)
	mdl.redumpUsername = "saved-user"
	redumpAuthPut(mdl.db, "saved-user", "saved-password")

	var user, pass widget.Editor
	user.SetText("saved-user")
	loginStarted := make(chan struct{})
	loginRelease := make(chan struct{})
	loginDone := make(chan struct{})

	started := startRedumpTestLogin(mdl, &user, &pass, func(username, password string) error {
		if username != "saved-user" {
			t.Errorf("username = %q, want saved-user", username)
		}
		if password != "saved-password" {
			t.Errorf("password = %q, want saved-password", password)
		}
		close(loginStarted)
		<-loginRelease
		close(loginDone)
		return nil
	})
	if !started {
		t.Fatal("startRedumpTestLogin returned false, want true")
	}
	if mdl.redumpStatus != "Testing login..." {
		t.Fatalf("redumpStatus = %q, want Testing login...", mdl.redumpStatus)
	}

	<-loginStarted
	select {
	case <-loginDone:
		t.Fatal("login completed before release; handler likely blocked instead of running async")
	default:
	}

	close(loginRelease)
	waitForRedumpTestLoginResult(t, mdl)
	if mdl.redumpStatus != "Login OK" {
		t.Fatalf("redumpStatus = %q, want Login OK", mdl.redumpStatus)
	}
}

func TestStartRedumpTestLoginReportsAsyncFailure(t *testing.T) {
	mdl := newTestModel(t)

	var user, pass widget.Editor
	user.SetText("bad-user")
	pass.SetText("bad-password")

	started := startRedumpTestLogin(mdl, &user, &pass, func(username, password string) error {
		return errors.New("nope")
	})
	if !started {
		t.Fatal("startRedumpTestLogin returned false, want true")
	}

	waitForRedumpTestLoginResult(t, mdl)
	if mdl.redumpStatus != "Login failed" {
		t.Fatalf("redumpStatus = %q, want Login failed", mdl.redumpStatus)
	}
}

func waitForRedumpTestLoginResult(t *testing.T, mdl *model) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if applyRedumpTestLoginResults(mdl) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for redump test login result")
}
