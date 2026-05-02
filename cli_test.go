package main

import (
	"bytes"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestCLIMissingPositional checks that each subcommand exits 1 when
// given zero or two positional arguments.
func TestCLIMissingPositional(t *testing.T) {
	for _, sub := range []string{"pack", "unpack", "verify", "inspect"} {
		t.Run(sub+"-zero", func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run([]string{sub}, io.Discard, &stderr); code != exitUsage {
				t.Fatalf("exit %d, want %d", code, exitUsage)
			}
		})
		t.Run(sub+"-two", func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run([]string{sub, "a", "b"}, io.Discard, &stderr); code != exitUsage {
				t.Fatalf("exit %d, want %d", code, exitUsage)
			}
		})
	}
}

// TestParseSubcommandInterleaved covers issue #11: flags after a
// positional argument were misclassified as positionals because Go's
// flag.Parse stops at the first non-flag token.
func TestParseSubcommandInterleaved(t *testing.T) {
	cases := []struct {
		name           string
		args           []string
		wantPositional []string
		wantOutput     string
		wantKeep       bool
	}{
		{"flags-after-positional",
			[]string{"file.cue", "-o", "out", "--keep-source"},
			[]string{"file.cue"}, "out", true},
		{"flags-before-positional",
			[]string{"-o", "out", "--keep-source", "file.cue"},
			[]string{"file.cue"}, "out", true},
		{"flags-around-positional",
			[]string{"-o", "out", "file.cue", "--keep-source"},
			[]string{"file.cue"}, "out", true},
		{"only-positional",
			[]string{"file.cue"},
			[]string{"file.cue"}, "", false},
		{"only-flags",
			[]string{"-o", "out", "--keep-source"},
			nil, "out", true},
		{"empty",
			[]string{},
			nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			var output string
			var keep bool
			pos, _, exit, ok := parseSubcommand("pack", "", tc.args, &stderr,
				func(fs *flag.FlagSet) {
					fs.StringVar(&output, "o", "", "")
					fs.BoolVar(&keep, "keep-source", false, "")
				})
			if !ok {
				t.Fatalf("parse failed (exit=%d): %s", exit, stderr.String())
			}
			if !reflect.DeepEqual(pos, tc.wantPositional) {
				t.Fatalf("positional = %v, want %v", pos, tc.wantPositional)
			}
			if output != tc.wantOutput {
				t.Fatalf("output = %q, want %q", output, tc.wantOutput)
			}
			if keep != tc.wantKeep {
				t.Fatalf("keep = %v, want %v", keep, tc.wantKeep)
			}
		})
	}
}

// TestCLIUnpackFlagsAfterPositional reproduces issue #11 end-to-end:
// `unpack <container> -o <out>` (positional first, flag after) must
// reconstruct the .scram, not bail with "expected exactly one
// positional argument".
func TestCLIUnpackFlagsAfterPositional(t *testing.T) {
	container := packSyntheticContainer(t)
	out := filepath.Join(filepath.Dir(container), "out.scram")
	var stderr bytes.Buffer
	if code := run([]string{"unpack", "-q", container, "-o", out}, io.Discard, &stderr); code != exitOK {
		t.Fatalf("unpack with flag after positional: exit %d; stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output not created: %v", err)
	}
}

// TestCLIUnknownFlag checks that each subcommand exits 1 on an unknown flag.
func TestCLIUnknownFlag(t *testing.T) {
	for _, sub := range []string{"pack", "unpack", "verify", "inspect"} {
		t.Run(sub, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run([]string{sub, "--no-such-flag"}, io.Discard, &stderr); code != exitUsage {
				t.Fatalf("%s --no-such-flag: exit %d, want %d", sub, code, exitUsage)
			}
		})
	}
}

// TestCLIHelp checks that --help exits 0 and prints the subcommand name.
func TestCLIHelp(t *testing.T) {
	checks := map[string]string{
		"pack": "miniscram pack", "unpack": "miniscram unpack",
		"verify": "USAGE:", "inspect": "miniscram inspect",
	}
	for sub, want := range checks {
		t.Run(sub, func(t *testing.T) {
			var stderr bytes.Buffer
			code := run([]string{sub, "--help"}, io.Discard, &stderr)
			if code != exitOK {
				t.Fatalf("exit %d; stderr=%s", code, stderr.String())
			}
			if !bytes.Contains(stderr.Bytes(), []byte(want)) {
				t.Fatalf("help missing %q:\n%s", want, stderr.String())
			}
		})
	}
}

// ─── success paths ────────────────────────────────────────────────────────────

// TestCLIPackHappyPath calls Pack() directly (CLI pack requires a full-disc
// fixture; flag parsing is covered by TestCLIMissingPositional/UnknownFlag).
func TestCLIPackHappyPath(t *testing.T) {
	dir := t.TempDir()
	disc := synthDisc(t, SynthOpts{MainSectors: 50, LeadoutSectors: 5})
	_, scramPath, cuePath := writeFixture(t, dir, disc)
	outPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath, OutputPath: outPath,
		LeadinLBA: LBAPregapStart,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output not created: %v", err)
	}
}

func TestCLIVerifyAndUnpackHappyPath(t *testing.T) {
	container := packSyntheticContainer(t)
	var stderr bytes.Buffer
	if code := run([]string{"verify", container}, io.Discard, &stderr); code != exitOK {
		t.Fatalf("verify exit %d; stderr=%s", code, stderr.String())
	}
	outPath := filepath.Join(filepath.Dir(container), "out.scram")
	stderr.Reset()
	if code := run([]string{"unpack", "-q", "-o", outPath, container}, io.Discard, &stderr); code != exitOK {
		t.Fatalf("unpack exit %d; stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output not created: %v", err)
	}
}

// ─── failure paths ────────────────────────────────────────────────────────────

// TestCLIVerifyWrongBin: corrupt bin → exit 5.
func TestCLIVerifyWrongBin(t *testing.T) {
	dir := t.TempDir()
	disc := synthDisc(t, SynthOpts{MainSectors: 50, LeadoutSectors: 5})
	binPath, scramPath, cuePath := writeFixture(t, dir, disc)
	containerPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath, OutputPath: containerPath,
		LeadinLBA: LBAPregapStart,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	bin, _ := os.ReadFile(binPath)
	bin[100] ^= 0xFF
	os.WriteFile(binPath, bin, 0o644)
	var stderr bytes.Buffer
	if code := run([]string{"verify", "-q", containerPath}, io.Discard, &stderr); code != exitWrongBin {
		t.Fatalf("got %d want %d, stderr=%s", code, exitWrongBin, stderr.String())
	}
}

// TestCLIVerifyOutputHashMismatch: tamper recorded scram hash → exit 3.
func TestCLIVerifyOutputHashMismatch(t *testing.T) {
	containerPath, _, m := packForVerify(t)
	tamperContainerHash(t, containerPath, m.Scram.Hashes.SHA256)
	var stderr bytes.Buffer
	if code := run([]string{"verify", containerPath}, io.Discard, &stderr); code != exitVerifyFail {
		t.Fatalf("got %d want %d, stderr=%s", code, exitVerifyFail, stderr.String())
	}
}

// TestCLIInspectVersionMismatch: wrong version byte → exit 4 (exitIO).
func TestCLIInspectVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake.miniscram")
	body := []byte("MSCM\xFF")
	os.WriteFile(path, body, 0o644)
	var stderr bytes.Buffer
	code := run([]string{"inspect", path}, io.Discard, &stderr)
	if code != exitIO {
		t.Fatalf("got %d want %d", code, exitIO)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("container version")) {
		t.Fatalf("missing version-mismatch message: %s", stderr.String())
	}
}

// ─── misc ─────────────────────────────────────────────────────────────────────

func TestCLIUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"foo"}, io.Discard, &stderr); code != exitUsage {
		t.Fatalf("got %d, want %d", code, exitUsage)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("unknown command")) {
		t.Fatalf("missing 'unknown command': %s", stderr.String())
	}
}

func TestCLIVersion(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"--version"}, io.Discard, &stderr); code != exitOK {
		t.Fatalf("got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("miniscram")) {
		t.Fatalf("missing version: %s", stderr.String())
	}
}
