// /home/hugh/miniscram/discover_test.go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustTouch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverPackCwd(t *testing.T) {
	dir := t.TempDir()
	mustTouch(t, filepath.Join(dir, "g.bin"))
	mustTouch(t, filepath.Join(dir, "g.cue"))
	mustTouch(t, filepath.Join(dir, "g.scram"))
	got, err := DiscoverPack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got.Bin) != "g.bin" || filepath.Base(got.Cue) != "g.cue" || filepath.Base(got.Scram) != "g.scram" {
		t.Fatalf("unexpected discovery: %+v", got)
	}
}

func TestDiscoverPackStemWithPath(t *testing.T) {
	dir := t.TempDir()
	mustTouch(t, filepath.Join(dir, "g.bin"))
	mustTouch(t, filepath.Join(dir, "g.cue"))
	mustTouch(t, filepath.Join(dir, "g.scram"))
	got, err := DiscoverPackFromArg(filepath.Join(dir, "g"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got.Bin) != "g.bin" {
		t.Fatalf("got %+v", got)
	}
}

func TestDiscoverPackAmbiguousCwd(t *testing.T) {
	dir := t.TempDir()
	mustTouch(t, filepath.Join(dir, "a.bin"))
	mustTouch(t, filepath.Join(dir, "b.bin"))
	mustTouch(t, filepath.Join(dir, "g.cue"))
	mustTouch(t, filepath.Join(dir, "g.scram"))
	_, err := DiscoverPack(dir)
	if err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
}

func TestDiscoverUnpackStem(t *testing.T) {
	dir := t.TempDir()
	mustTouch(t, filepath.Join(dir, "g.bin"))
	mustTouch(t, filepath.Join(dir, "g.miniscram"))
	got, err := DiscoverUnpackFromArg(filepath.Join(dir, "g.miniscram"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got.Bin) != "g.bin" || filepath.Base(got.Container) != "g.miniscram" {
		t.Fatalf("got %+v", got)
	}
}

func TestDefaultPackOutput(t *testing.T) {
	got := DefaultPackOutput("/some/dir/Game.bin")
	if got != "/some/dir/Game.miniscram" {
		t.Fatalf("got %q", got)
	}
}

func TestDefaultUnpackOutput(t *testing.T) {
	got := DefaultUnpackOutput("/some/dir/Game.miniscram")
	if got != "/some/dir/Game.scram" {
		t.Fatalf("got %q", got)
	}
}
