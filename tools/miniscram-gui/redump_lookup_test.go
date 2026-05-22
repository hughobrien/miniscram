package main

import (
	"testing"
	"time"
)

func TestLookupUsesAuthenticatedCacheWhenCredentialsExist(t *testing.T) {
	db := newMemoryDB(t)
	username, password := redumpTestCreds(t)
	const hash = "deadbeef"
	redumpPut(db, hash, "anon", &redumpEntry{State: "miss", CheckedUnix: time.Now().Unix()})
	redumpPut(db, hash, "auth", &redumpEntry{
		State:       "found",
		URL:         "http://redump.org/disc/47784/",
		Title:       "Fallout 4: Featured Music Selections",
		CheckedUnix: time.Now().Unix(),
	})
	redumpAuthPut(db, username, password)

	m := &model{db: db, redump: map[string]*redumpEntry{}}
	m.lookup([]string{hash})

	got := m.redump[hash]
	if got == nil {
		t.Fatal("m.redump[hash] is nil")
	}
	if got.State != "found" {
		t.Fatalf("State = %q, want found", got.State)
	}
	if got.Title != "Fallout 4: Featured Music Selections" {
		t.Errorf("Title = %q", got.Title)
	}
}
