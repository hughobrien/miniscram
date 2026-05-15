# GUI: `load(.cue)` prefers packed `.miniscram` — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When `load(.cue)` is called and the sibling `.scram` is absent (or zero-byte) but a sibling `.miniscram` exists, redirect to `load(.miniscram)` so the user sees the packed result instead of a "missing scram — pack can't run" warning.

**Architecture:** A five-line precheck at the top of the `case ".cue":` branch in `tools/miniscram-gui/main.go::load`. On the redirect path it calls `m.load(miniscramPath)` and returns, reusing the existing `.miniscram` branch verbatim. Four behavior-pinning tests cover the four cells of the (scram present? × miniscram present?) matrix.

**Tech Stack:** Go 1.23, `CC=/usr/bin/clang CGO_ENABLED=1` for local GUI builds.

**Spec:** `docs/superpowers/specs/2026-05-14-gui-load-cue-prefers-packed-design.md`.

---

## File map

- **Create:** `tools/miniscram-gui/load_cue_redirect_test.go` — four tests pinning the matrix.
- **Modify:** `tools/miniscram-gui/main.go` — five-line precheck at the top of the `case ".cue":` branch in `load()`.

---

### Task 1: Add the redirect with TDD

**Files:**
- Create: `tools/miniscram-gui/load_cue_redirect_test.go`
- Modify: `tools/miniscram-gui/main.go` (inside the `case ".cue":` branch in `load()`, currently at ~line 448)

- [ ] **Step 1.1: Write the four pinning tests**

Create `tools/miniscram-gui/load_cue_redirect_test.go` with EXACTLY this content:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCueTrio creates the optional sibling files in dir for the given
// base name. When a value is "skip" the file is not written; "" writes
// a zero-byte file; any other value writes those bytes.
func writeCueTrio(t *testing.T, dir, base, cueContent, scramContent, miniscramContent string) (cue, scram, miniscram string) {
	t.Helper()
	cue = filepath.Join(dir, base+".cue")
	scram = filepath.Join(dir, base+".scram")
	miniscram = filepath.Join(dir, base+".miniscram")
	if cueContent != "skip" {
		if err := os.WriteFile(cue, []byte(cueContent), 0o644); err != nil {
			t.Fatalf("write cue: %v", err)
		}
	}
	if scramContent != "skip" {
		if err := os.WriteFile(scram, []byte(scramContent), 0o644); err != nil {
			t.Fatalf("write scram: %v", err)
		}
	}
	if miniscramContent != "skip" {
		if err := os.WriteFile(miniscram, []byte(miniscramContent), 0o644); err != nil {
			t.Fatalf("write miniscram: %v", err)
		}
	}
	return
}

func TestLoad_CueWithMissingScram_RedirectsToMiniscram(t *testing.T) {
	dir := t.TempDir()
	cue, _, miniscram := writeCueTrio(t, dir, "disc", "", "skip", "anything")

	m := newTestModel(t)
	m.load(cue)

	if m.path != miniscram {
		t.Errorf("m.path = %q, want %q (redirect should have switched to .miniscram)", m.path, miniscram)
	}
}

func TestLoad_CueWithEmptyScram_RedirectsToMiniscram(t *testing.T) {
	dir := t.TempDir()
	cue, _, miniscram := writeCueTrio(t, dir, "disc", "", "", "anything")

	m := newTestModel(t)
	m.load(cue)

	if m.path != miniscram {
		t.Errorf("m.path = %q, want %q (zero-byte .scram should be treated as missing)", m.path, miniscram)
	}
}

func TestLoad_CueWithScramAndMiniscram_StaysOnCue(t *testing.T) {
	dir := t.TempDir()
	cue, _, _ := writeCueTrio(t, dir, "disc", "", "scram-bytes", "anything")

	m := newTestModel(t)
	m.load(cue)

	if m.path != cue {
		t.Errorf("m.path = %q, want %q", m.path, cue)
	}
	if m.kind != "cue" {
		t.Errorf("m.kind = %q, want \"cue\"", m.kind)
	}
}

func TestLoad_CueWithNoScramAndNoMiniscram_StaysOnCue(t *testing.T) {
	dir := t.TempDir()
	cue, _, _ := writeCueTrio(t, dir, "disc", "", "skip", "skip")

	m := newTestModel(t)
	m.load(cue)

	if m.path != cue {
		t.Errorf("m.path = %q, want %q", m.path, cue)
	}
	if m.kind != "cue" {
		t.Errorf("m.kind = %q, want \"cue\"", m.kind)
	}
}
```

`newTestModel(t)` is already in `tools/miniscram-gui/result_handler_test.go` and produces a `*model` with an in-memory SQLite. Do not redefine it.

- [ ] **Step 1.2: Run tests, expect 2/4 fail**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestLoad_CueWith' -v ./...
```

Expected, before the precheck is added:
- `TestLoad_CueWithMissingScram_RedirectsToMiniscram` — **FAIL.** `m.path` ends up at the cue path because the redirect doesn't exist yet.
- `TestLoad_CueWithEmptyScram_RedirectsToMiniscram` — **FAIL.** Same reason.
- `TestLoad_CueWithScramAndMiniscram_StaysOnCue` — PASS. Current behavior loads the cue and sets `m.kind = "cue"`.
- `TestLoad_CueWithNoScramAndNoMiniscram_StaysOnCue` — PASS. Same.

Confirm 2 failures and 2 passes before adding the precheck. If anything else fails or the failing tests fail for a different reason than "redirect didn't happen," stop and report BLOCKED.

- [ ] **Step 1.3: Add the redirect to `load()`**

Open `tools/miniscram-gui/main.go`. Find the `func (m *model) load(p string)` declaration (around line 401) and inside it find `case ".cue":` (around line 448). The current branch looks like:

```go
	case ".cue":
		b, err := os.ReadFile(p)
		if err != nil {
			m.err = err.Error()
			return
		}
		m.kind = "cue"
		m.cueText = string(b)
		...
```

Insert the precheck as the FIRST thing inside the `case ".cue":` branch, before `b, err := os.ReadFile(p)`:

```go
	case ".cue":
		// If pack already ran (sibling .scram is gone or empty) and
		// the packed result is sitting next to the cue, prefer to
		// show that result rather than rendering cueView's "missing
		// scram — pack can't run" warning. Common after a batch pack
		// where the queue rows still point at the cues.
		base := strings.TrimSuffix(p, filepath.Ext(p))
		scramPath := base + ".scram"
		miniscramPath := base + ".miniscram"
		scramOK := false
		if st, err := os.Stat(scramPath); err == nil && st.Size() > 0 {
			scramOK = true
		}
		if !scramOK {
			if _, err := os.Stat(miniscramPath); err == nil {
				m.load(miniscramPath)
				return
			}
		}
		b, err := os.ReadFile(p)
		if err != nil {
			m.err = err.Error()
			return
		}
		...
```

The imports `strings`, `filepath`, and `os` are already present in `main.go` (used extensively above). No import changes needed.

- [ ] **Step 1.4: Run tests, expect 4/4 PASS**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestLoad_CueWith' -v ./...
```

Expected: 4/4 PASS. If any fail, stop and diagnose — do not paper over.

- [ ] **Step 1.5: Run the full GUI test package**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test ./...
```

Expected: ok. Catches any test elsewhere that constructed a `.cue` next to a sibling `.miniscram` and expected the cue view (none should, but this is the safety net).

- [ ] **Step 1.6: Build verify**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go build -o /tmp/miniscram-gui-verify .
```

Expected: build succeeds.

- [ ] **Step 1.7: Commit**

```bash
git add tools/miniscram-gui/main.go tools/miniscram-gui/load_cue_redirect_test.go
git commit -m "$(cat <<'EOF'
gui: load(.cue) redirects to packed .miniscram when scram is gone

When a cue's sibling .scram is absent (or zero-sized) but a sibling
.miniscram exists, redirect to load(.miniscram). After a batch pack
with the default delete-scram flow, clicking a queue row used to
show cueView's "missing scram — pack can't run" warning, with the
useful packed result hidden behind a different click. Now every
load() entry point — queue row click, Open, drag-drop, startup
-load — auto-pivots to inspect when packing has clearly happened.

Four tests pin the (scram present? × miniscram present?) matrix.
EOF
)"
```

---

## Manual verification (optional, post-merge)

1. Build the GUI.
2. Start with a `.cue` and its `.scram`, no `.miniscram`. Drop into the GUI → cue view ("Ready to pack"). ✓ unchanged.
3. Click Pack with the default delete-scram. Wait for completion. The .scram is consumed and a .miniscram appears.
4. Now switch to Stats, then click the row in the sidebar (or hit Open and pick the same `.cue`). Expected: lands on the miniscram inspect view, not the "missing scram" warning.

---

## Out of scope (reminder from spec)

- "Back to cue" affordance from the miniscram view.
- Removing the "missing scram" warning for orphaned cues (no sibling miniscram).
- Cross-validating that the sibling .miniscram was actually produced from this .cue.
