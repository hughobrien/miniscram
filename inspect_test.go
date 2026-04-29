package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectFormatHuman(t *testing.T) {
	m := sampleManifest()

	t.Run("clean-delta", func(t *testing.T) {
		out, err := formatHumanInspect(m, "MSCM", 0x02, []byte{0, 0, 0, 0}, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"container:  MSCM v2",
			"scram.hashes.sha256:    " + strings.Repeat("c", 64),
			"track 1: MODE1/2352",
			"md5:    " + strings.Repeat("a", 32),
			"override_records:       0",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in output:\n%s", want, out)
			}
		}
		// v0.2 fields must not appear.
		for _, absent := range []string{"bin_sha256:", "delta_size:", "error_sector_count:"} {
			if strings.Contains(out, absent) {
				t.Errorf("v0.2 field %q in output", absent)
			}
		}
	})

	t.Run("full-lists-overrides", func(t *testing.T) {
		delta := buildDelta(t, []uint64{2352, 4804, 7056})
		out, err := formatHumanInspect(m, "MSCM", 0x02, delta, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "overrides:\n") {
			t.Errorf("expected overrides: section:\n%s", out)
		}
		for _, want := range []string{"byte_offset=2352", "byte_offset=4804", "byte_offset=7056"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("full-empty-hides-section", func(t *testing.T) {
		out, _ := formatHumanInspect(m, "MSCM", 0x02, []byte{0, 0, 0, 0}, true)
		if strings.Contains(out, "overrides:") {
			t.Errorf("unexpected overrides: section with 0 records:\n%s", out)
		}
	})

	t.Run("delta-error-reported", func(t *testing.T) {
		out, err := formatHumanInspect(m, "MSCM", 0x02, []byte{0, 0, 0, 1}, false)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(out, "delta:\n") {
			t.Errorf("expected partial output through delta: header:\n%s", out)
		}
	})

	t.Run("track-padding", func(t *testing.T) {
		m2 := sampleManifest()
		m2.Tracks = append(m2.Tracks, Track{Number: 2, Mode: "AUDIO", FirstLBA: 12345, Size: 47040, Filename: "y.bin"})
		out, err := formatHumanInspect(m2, "MSCM", 0x02, []byte{0, 0, 0, 0}, false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "track 2: AUDIO       first_lba=12345") {
			t.Errorf("expected padded AUDIO line:\n%s", out)
		}
	})
}

func TestInspectFormatJSON(t *testing.T) {
	m := sampleManifest()
	delta := buildDelta(t, []uint64{2352, 7056})
	body, err := formatJSONInspect(m, delta)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("JSON did not parse: %v\n%s", err, body)
	}
	for _, k := range []string{"tool_version", "scram", "tracks", "delta_records"} {
		if _, ok := top[k]; !ok {
			t.Errorf("missing key %q in JSON", k)
		}
	}
	if len(top["delta_records"].([]any)) != 2 {
		t.Fatalf("delta_records len = %d; want 2", len(top["delta_records"].([]any)))
	}
	if !strings.HasPrefix(string(body), `{"tool_version":`) {
		t.Errorf("JSON does not start with tool_version")
	}
	// Empty delta → [].
	body2, _ := formatJSONInspect(m, []byte{0, 0, 0, 0})
	if !bytes.Contains(body2, []byte(`"delta_records":[]`)) {
		t.Errorf("expected delta_records:[] for empty delta")
	}
	// Bad delta → error.
	if _, err := formatJSONInspect(m, []byte{0, 0, 0, 1}); err == nil {
		t.Error("expected error for truncated delta")
	}
}

func TestCLIInspect(t *testing.T) {
	t.Run("human-output", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"inspect", packSyntheticContainer(t)}, &stdout, &stderr); code != exitOK {
			t.Fatalf("exit %d; stderr=%s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "container:  MSCM v2") {
			t.Errorf("missing header in stdout:\n%s", stdout.String())
		}
		if stderr.Len() > 0 {
			t.Errorf("unexpected stderr: %s", stderr.String())
		}
	})

	t.Run("json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"inspect", "--json", packSyntheticContainer(t)}, &stdout, &stderr); code != exitOK {
			t.Fatalf("exit %d", code)
		}
		var top map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &top); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, stdout.String())
		}
		if _, ok := top["delta_records"]; !ok {
			t.Error("delta_records missing")
		}
	})

	t.Run("full-with-overrides", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "with-overrides.miniscram")
		if err := WriteContainer(path, sampleManifest(), bytes.NewReader(buildDelta(t, []uint64{2352, 4704, 7056}))); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := run([]string{"inspect", "--full", path}, &stdout, &stderr); code != exitOK {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(stdout.String(), "overrides:\n") {
			t.Errorf("expected overrides: section:\n%s", stdout.String())
		}
	})

	t.Run("bad-magic", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "junk.miniscram")
		body := make([]byte, containerHeaderSize)
		copy(body, "XXXX")
		os.WriteFile(path, body, 0o644)
		var stdout, stderr bytes.Buffer
		if code := run([]string{"inspect", path}, &stdout, &stderr); code != exitIO {
			t.Fatalf("exit %d; want %d", code, exitIO)
		}
		if !strings.Contains(stderr.String(), "not a miniscram container") {
			t.Errorf("missing bad-magic error: %s", stderr.String())
		}
	})
}
