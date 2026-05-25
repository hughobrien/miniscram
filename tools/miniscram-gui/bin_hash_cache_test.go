package main

import (
	"os"
	"sync"
	"testing"
	"time"
)

func TestBinHashCache_RoundTrip(t *testing.T) {
	m := newTestModel(t)

	binHashPut(m.db, "/tmp/a.bin", 1024, 1700000000,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")

	md5h, sha1h, sha256h, ok := binHashLookup(m.db, "/tmp/a.bin", 1024, 1700000000)
	if !ok {
		t.Fatal("expected cache hit, got miss")
	}
	if md5h != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("md5 = %q", md5h)
	}
	if sha1h != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("sha1 = %q", sha1h)
	}
	if sha256h != "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" {
		t.Errorf("sha256 = %q", sha256h)
	}
}

func TestBinHashCache_StaleMtime(t *testing.T) {
	m := newTestModel(t)
	binHashPut(m.db, "/tmp/a.bin", 1024, 1700000000, "m", "s1", "s2")

	if _, _, _, ok := binHashLookup(m.db, "/tmp/a.bin", 1024, 1700000001); ok {
		t.Error("expected miss on stale mtime, got hit")
	}
}

func TestBinHashCache_StaleSize(t *testing.T) {
	m := newTestModel(t)
	binHashPut(m.db, "/tmp/a.bin", 1024, 1700000000, "m", "s1", "s2")

	if _, _, _, ok := binHashLookup(m.db, "/tmp/a.bin", 1025, 1700000000); ok {
		t.Error("expected miss on stale size, got hit")
	}
}

func TestBinHashCache_NilDB(t *testing.T) {
	if _, _, _, ok := binHashLookup(nil, "/tmp/a.bin", 1, 1); ok {
		t.Error("nil db: expected miss, got hit")
	}
	// must not panic
	binHashPut(nil, "/tmp/a.bin", 1, 1, "m", "s1", "s2")
}

// m.hashCueBins consults redump_cache via m.lookup() after hashing.
// Pre-seeding redump_cache for the sha1 keeps the test fully offline.

func TestHashCueBins_PopulatesCache(t *testing.T) {
	m := newTestModel(t)
	m.redump = map[string]*redumpEntry{} // hashCueBins → lookup() writes here

	binPath := writeTempBytes(t, "track01.bin", 4096)

	// Compute the expected digests independently so we can pre-seed
	// the redump cache (avoid HTTP) and assert against them.
	expMd5, expSha1, expSha256, err := hashCueBin(binPath)
	if err != nil {
		t.Fatalf("hashCueBin: %v", err)
	}
	redumpPut(m.db, expSha1, "anon", &redumpEntry{State: "miss", CheckedUnix: time.Now().Unix()})

	tracks := []cueTrack{{num: 1, mode: "MODE1/2352", filename: "track01.bin", exists: true}}
	m.hashCueBins(tracks, []string{binPath})

	if tracks[0].state != "done" {
		t.Fatalf("state = %q, want done", tracks[0].state)
	}
	if tracks[0].hashes["sha1"] != expSha1 {
		t.Errorf("sha1 = %q, want %q", tracks[0].hashes["sha1"], expSha1)
	}

	st, err := os.Stat(binPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	cMd5, cSha1, cSha256, ok := binHashLookup(m.db, binPath, st.Size(), st.ModTime().Unix())
	if !ok {
		t.Fatal("expected cache hit after hashCueBins, got miss")
	}
	if cMd5 != expMd5 || cSha1 != expSha1 || cSha256 != expSha256 {
		t.Errorf("cached digests don't match computed digests")
	}
}

func TestHashCueBins_UsesCache(t *testing.T) {
	m := newTestModel(t)
	m.redump = map[string]*redumpEntry{}

	binPath := writeTempBytes(t, "track01.bin", 4096)
	st, err := os.Stat(binPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Pre-seed bin_hash_cache with deliberately wrong digests at the
	// real (size, mtime). The only way hashCueBins can return these
	// is by reading the cache instead of re-streaming the file.
	wrongMd5 := "deadbeefdeadbeefdeadbeefdeadbeef"
	wrongSha1 := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	wrongSha256 := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	binHashPut(m.db, binPath, st.Size(), st.ModTime().Unix(), wrongMd5, wrongSha1, wrongSha256)
	redumpPut(m.db, wrongSha1, "anon", &redumpEntry{State: "miss", CheckedUnix: time.Now().Unix()})

	tracks := []cueTrack{{num: 1, mode: "MODE1/2352", filename: "track01.bin", exists: true}}
	m.hashCueBins(tracks, []string{binPath})

	if tracks[0].state != "done" {
		t.Fatalf("state = %q, want done", tracks[0].state)
	}
	if tracks[0].hashes["sha1"] != wrongSha1 {
		t.Errorf("sha1 = %q, want %q (cache should have short-circuited the hash)", tracks[0].hashes["sha1"], wrongSha1)
	}
}

func TestHashCueBins_BatchesAuthenticatedRedumpLookups(t *testing.T) {
	m := newTestModel(t)
	m.redump = map[string]*redumpEntry{}
	username, password := redumpTestCreds(t)
	redumpAuthPut(m.db, username, password)

	var (
		mu         sync.Mutex
		loginCount int
		fetched    []string
		badCreds   bool
	)
	m.redumpLookup = &redumpLookupService{
		login: func(gotUser, gotPass string) (redumpFetcher, error) {
			mu.Lock()
			if gotUser != username || gotPass != password {
				badCreds = true
			}
			loginCount++
			mu.Unlock()
			return redumpFetchFunc(func(hash string) *redumpEntry {
				mu.Lock()
				fetched = append(fetched, hash)
				mu.Unlock()
				return &redumpEntry{State: "miss", CheckedUnix: time.Now().Unix()}
			}), nil
		},
	}

	paths := []string{
		writeTempBytes(t, "track01.bin", 1),
		writeTempBytes(t, "track02.bin", 2),
		writeTempBytes(t, "track03.bin", 3),
	}
	tracks := []cueTrack{
		{num: 1, mode: "AUDIO", filename: "track01.bin", exists: true},
		{num: 2, mode: "AUDIO", filename: "track02.bin", exists: true},
		{num: 3, mode: "AUDIO", filename: "track03.bin", exists: true},
	}

	m.hashCueBins(tracks, paths)

	if badCreds {
		t.Fatal("lookup did not use saved Redump credentials")
	}
	if loginCount != 1 {
		t.Fatalf("loginCount = %d, want 1", loginCount)
	}
	if len(fetched) != len(tracks) {
		t.Fatalf("fetched %d hashes, want %d", len(fetched), len(tracks))
	}
}
