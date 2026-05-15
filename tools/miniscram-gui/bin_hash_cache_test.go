package main

import (
	"testing"
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
