package main

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestRedumpAuth_SaveLoadClear(t *testing.T) {
	db := newMemoryDB(t)
	username, password := redumpTestCreds(t)

	if _, ok := redumpAuthGet(db); ok {
		t.Fatal("redumpAuthGet before save returned ok=true")
	}

	redumpAuthPut(db, username, password)
	auth, ok := redumpAuthGet(db)
	if !ok {
		t.Fatal("redumpAuthGet after save returned ok=false")
	}
	if auth.Username != username {
		t.Errorf("Username = %q, want %q", auth.Username, username)
	}
	if auth.Password != password {
		t.Errorf("Password = %q, want env password", auth.Password)
	}
	if auth.UpdatedUnix == 0 {
		t.Error("UpdatedUnix = 0, want non-zero")
	}

	redumpAuthClear(db)
	if _, ok := redumpAuthGet(db); ok {
		t.Fatal("redumpAuthGet after clear returned ok=true")
	}
}

func TestRedumpCache_SeparatesAnonymousAndAuthenticated(t *testing.T) {
	db := newMemoryDB(t)
	const hash = "deadbeef"

	redumpPut(db, hash, "anon", &redumpEntry{State: "miss", CheckedUnix: time.Now().Unix()})
	redumpPut(db, hash, "auth", &redumpEntry{
		State:       "found",
		URL:         "http://redump.org/disc/47784/",
		Title:       "Fallout 4: Featured Music Selections",
		CheckedUnix: time.Now().Unix(),
	})

	anon, ok := redumpGet(db, hash, "anon")
	if !ok {
		t.Fatal("redumpGet anon returned ok=false")
	}
	if anon.State != "miss" {
		t.Errorf("anon.State = %q, want miss", anon.State)
	}

	auth, ok := redumpGet(db, hash, "auth")
	if !ok {
		t.Fatal("redumpGet auth returned ok=false")
	}
	if auth.State != "found" {
		t.Errorf("auth.State = %q, want found", auth.State)
	}
	if auth.Title != "Fallout 4: Featured Music Selections" {
		t.Errorf("auth.Title = %q", auth.Title)
	}
}

func TestRedumpCache_DoesNotPersistTransientErrors(t *testing.T) {
	db := newMemoryDB(t)
	const hash = "deadbeef"

	redumpPut(db, hash, "auth", &redumpEntry{State: "err", CheckedUnix: time.Now().Unix()})

	if got, ok := redumpGet(db, hash, "auth"); ok {
		t.Fatalf("redumpGet returned %q for transient error; want no cached entry", got.State)
	}
}
