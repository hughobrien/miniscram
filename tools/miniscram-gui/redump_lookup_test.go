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

	got := m.redump[redumpCacheKey(hash, "auth")]
	if got == nil {
		t.Fatal("authenticated memory cache entry is nil")
	}
	if got.State != "found" {
		t.Fatalf("State = %q, want found", got.State)
	}
	if got.Title != "Fallout 4: Featured Music Selections" {
		t.Errorf("Title = %q", got.Title)
	}
}

func TestLookupDoesNotLetAnonymousMemoryMissBlockAuthenticatedFetch(t *testing.T) {
	db := newMemoryDB(t)
	username, password := redumpTestCreds(t)
	const hash = "deadbeef"
	redumpAuthPut(db, username, password)

	var fetched []string
	m := &model{
		db: db,
		redump: map[string]*redumpEntry{
			redumpCacheKey(hash, "anon"): {State: "miss", CheckedUnix: time.Now().Unix()},
		},
		redumpLookup: &redumpLookupService{
			login: func(username, password string) (redumpFetcher, error) {
				return redumpFetchFunc(func(h string) *redumpEntry {
					fetched = append(fetched, h)
					return &redumpEntry{
						State:       "found",
						URL:         "http://redump.org/disc/47784/",
						Title:       "Fallout 4: Featured Music Selections",
						CheckedUnix: time.Now().Unix(),
					}
				}), nil
			},
			anonFetch: redumpFetchFunc(func(h string) *redumpEntry {
				t.Fatalf("anonymous fetch called for authenticated lookup of %s", h)
				return nil
			}),
		},
	}

	m.lookup([]string{hash})

	if len(fetched) != 1 || fetched[0] != hash {
		t.Fatalf("fetched = %v, want [%s]", fetched, hash)
	}
	got := m.redump[redumpCacheKey(hash, "auth")]
	if got == nil {
		t.Fatal("authenticated memory cache entry is nil")
	}
	if got.State != "found" {
		t.Fatalf("State = %q, want found", got.State)
	}
}
