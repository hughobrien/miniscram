// /home/hugh/miniscram/discover.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PackInputs holds the three input paths Pack consumes.
type PackInputs struct {
	Bin   string
	Cue   string
	Scram string
}

// UnpackInputs holds the two input paths Unpack consumes.
type UnpackInputs struct {
	Bin       string
	Container string
}

// DiscoverPack scans dir for exactly one *.bin, *.cue, *.scram and
// returns the trio. Errors clearly when zero or many are found.
func DiscoverPack(dir string) (PackInputs, error) {
	bin, err := uniqueByExt(dir, ".bin")
	if err != nil {
		return PackInputs{}, err
	}
	cue, err := uniqueByExt(dir, ".cue")
	if err != nil {
		return PackInputs{}, err
	}
	scr, err := uniqueByExt(dir, ".scram")
	if err != nil {
		return PackInputs{}, err
	}
	return PackInputs{Bin: bin, Cue: cue, Scram: scr}, nil
}

// DiscoverPackFromArg interprets a single positional arg as either a
// directory (in which case it falls back to DiscoverPack) or a stem
// with optional path. Stem extensions .bin/.cue/.scram/.miniscram are
// stripped before resolving.
func DiscoverPackFromArg(arg string) (PackInputs, error) {
	if fi, err := os.Stat(arg); err == nil && fi.IsDir() {
		return DiscoverPack(arg)
	}
	stem := stripKnownExt(arg)
	files := PackInputs{
		Bin:   stem + ".bin",
		Cue:   stem + ".cue",
		Scram: stem + ".scram",
	}
	for _, p := range []string{files.Bin, files.Cue, files.Scram} {
		if _, err := os.Stat(p); err != nil {
			return PackInputs{}, fmt.Errorf("expected %s: %w", p, err)
		}
	}
	return files, nil
}

// DiscoverUnpack scans dir for exactly one *.bin and one *.miniscram.
func DiscoverUnpack(dir string) (UnpackInputs, error) {
	bin, err := uniqueByExt(dir, ".bin")
	if err != nil {
		return UnpackInputs{}, err
	}
	c, err := uniqueByExt(dir, ".miniscram")
	if err != nil {
		return UnpackInputs{}, err
	}
	return UnpackInputs{Bin: bin, Container: c}, nil
}

// DiscoverUnpackFromArg interprets a single positional arg as either a
// directory or a stem.
func DiscoverUnpackFromArg(arg string) (UnpackInputs, error) {
	if fi, err := os.Stat(arg); err == nil && fi.IsDir() {
		return DiscoverUnpack(arg)
	}
	stem := stripKnownExt(arg)
	bin := stem + ".bin"
	cont := stem + ".miniscram"
	if _, err := os.Stat(bin); err != nil {
		return UnpackInputs{}, fmt.Errorf("expected %s: %w", bin, err)
	}
	if _, err := os.Stat(cont); err != nil {
		return UnpackInputs{}, fmt.Errorf("expected %s: %w", cont, err)
	}
	return UnpackInputs{Bin: bin, Container: cont}, nil
}

func DefaultPackOutput(binPath string) string {
	return strings.TrimSuffix(binPath, filepath.Ext(binPath)) + ".miniscram"
}

func DefaultUnpackOutput(containerPath string) string {
	return strings.TrimSuffix(containerPath, filepath.Ext(containerPath)) + ".scram"
}

func uniqueByExt(dir, ext string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*"+ext))
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no %s file in %s; pass it explicitly", ext, dir)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("found more than one %s file in %s: %s; please specify explicitly",
			ext, dir, strings.Join(matches, ", "))
	}
}

func stripKnownExt(s string) string {
	for _, ext := range []string{".bin", ".cue", ".scram", ".miniscram"} {
		if strings.HasSuffix(s, ext) {
			return strings.TrimSuffix(s, ext)
		}
	}
	return s
}
