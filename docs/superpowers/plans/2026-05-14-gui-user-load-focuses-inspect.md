# GUI: pivot to Inspect on user-explicit file loads — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix issue #43 — clicking the sidebar (or Open, or dropping a single .miniscram, or AddFiles → single .miniscram) while on the Stats tab silently loads the file but leaves the view on Stats, so the user sees no change. After this plan, any user-explicit file load pivots the right pane to Inspect; worker-driven loads stay unchanged.

**Architecture:** Add a thin `loadAndFocus(p)` helper on `*model` that sets `m.view = "file"` then delegates to `m.load(p)`. Swap three call sites (sidebar row click, Open button goroutine, addPaths post-load). Worker call sites (queue auto-follow, post-pack reload, startup `-load`) keep calling bare `load`. Pin both invariants with four model-level Go tests.

**Tech Stack:** Go 1.23, Gio UI (`gioui.org`), in-memory SQLite for test fixtures, `CC=/usr/bin/clang CGO_ENABLED=1` required for local builds.

**Spec:** `docs/superpowers/specs/2026-05-14-gui-user-load-focuses-inspect-design.md`.

---

## File map

- **Create:** `tools/miniscram-gui/load_focus_test.go` — four model-level tests pinning the view-pivot rule.
- **Modify:** `tools/miniscram-gui/main.go` — add `loadAndFocus` helper, swap two call sites.
- **Modify:** `tools/miniscram-gui/queue.go` — swap one call site (post-addPaths .miniscram load).

---

### Task 1: Add `loadAndFocus` helper with TDD

**Files:**
- Create: `tools/miniscram-gui/load_focus_test.go`
- Modify: `tools/miniscram-gui/main.go` (add helper next to `load()` at ~line 471)

- [ ] **Step 1.1: Write the four failing/locking tests**

Create `tools/miniscram-gui/load_focus_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeEmptyCue creates a zero-byte .cue in t.TempDir() and returns its path.
// load() reads the file, sets m.cueText, parses zero tracks, and kicks off
// hashCueBins over an empty slice — none of which influences m.view.
func writeEmptyCue(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.cue")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatalf("write cue: %v", err)
	}
	return p
}

func TestLoadAndFocus_FromStats_PivotsToInspect(t *testing.T) {
	m := newTestModel(t)
	m.view = "stats"

	m.loadAndFocus(writeEmptyCue(t))

	if m.view != "file" {
		t.Errorf("view = %q, want %q", m.view, "file")
	}
}

func TestLoadAndFocus_FromInspect_StaysOnInspect(t *testing.T) {
	m := newTestModel(t)
	m.view = "file"

	m.loadAndFocus(writeEmptyCue(t))

	if m.view != "file" {
		t.Errorf("view = %q, want %q", m.view, "file")
	}
}

// Regression lock: bare load() must NEVER touch m.view. The worker-driven
// callers (queue auto-follow, post-pack reload, startup -load) rely on this.
func TestLoad_FromStats_DoesNotPivot(t *testing.T) {
	m := newTestModel(t)
	m.view = "stats"

	m.load(writeEmptyCue(t))

	if m.view != "stats" {
		t.Errorf("view = %q, want %q", m.view, "stats")
	}
}

func TestLoad_FromInspect_StaysOnInspect(t *testing.T) {
	m := newTestModel(t)
	m.view = "file"

	m.load(writeEmptyCue(t))

	if m.view != "file" {
		t.Errorf("view = %q, want %q", m.view, "file")
	}
}
```

`newTestModel(t)` is already defined in `result_handler_test.go` (same package); reuse it.

- [ ] **Step 1.2: Run tests, expect compile failure on `loadAndFocus`**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestLoadAndFocus|TestLoad_' -v ./...
```

Expected: build fails with `m.loadAndFocus undefined (type *model has no field or method loadAndFocus)`. The two `TestLoad_*` tests can't run yet because the package doesn't compile, but they're correct as written — they'll start passing as soon as the file compiles.

- [ ] **Step 1.3: Add `loadAndFocus` helper to `main.go`**

In `tools/miniscram-gui/main.go`, immediately after the closing brace of `func (m *model) load(p string)` (currently at line 471), insert:

```go
// loadAndFocus is load() with an explicit view pivot to "file". Use it
// at sites reached only by an explicit user action (sidebar row click,
// Open button, drag-drop or AddFiles producing a single .miniscram).
// Worker-driven callers (queue auto-follow, post-pack reload, startup
// -load) keep calling bare load() so they never yank the user off Stats.
func (m *model) loadAndFocus(p string) {
	m.view = "file"
	m.load(p)
}
```

- [ ] **Step 1.4: Run tests, expect all four to pass**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestLoadAndFocus|TestLoad_' -v ./...
```

Expected: `PASS` on all four tests. If any fail, stop and diagnose — do not paper over.

- [ ] **Step 1.5: Commit**

```bash
git add tools/miniscram-gui/load_focus_test.go tools/miniscram-gui/main.go
git commit -m "$(cat <<'EOF'
gui: add loadAndFocus helper to pivot view to Inspect

Sites reached only by an explicit user action call loadAndFocus, which
sets m.view = "file" before delegating to load(p). Worker-driven sites
(queue auto-follow, post-pack reload, startup -load) keep calling bare
load() so a long-running queue or finishing pack never yanks the user
off Stats. Four model-level tests pin both invariants.

Towards #43.
EOF
)"
```

---

### Task 2: Swap the three user-explicit call sites

**Files:**
- Modify: `tools/miniscram-gui/main.go:1190` (Open button goroutine)
- Modify: `tools/miniscram-gui/main.go:1288` (sidebar row click)
- Modify: `tools/miniscram-gui/queue.go:172` (post-addPaths .miniscram load)

These three sites are exactly the user-explicit reachings of `load()`. Worker sites at `main.go:605`, `main.go:932`, `queue.go:363`, `queue.go:402` stay on bare `load`.

- [ ] **Step 2.1: Swap the sidebar row click**

In `tools/miniscram-gui/main.go` around line 1287-1292, the row-click block currently reads:

```go
for _, it := range snapForClicks.Items {
    if qBtns.RowClick(it.ID).Clicked(gtx) {
        mdl.load(it.CuePath)
        mdl.queue.mu.Lock()
        mdl.queue.autoFollow = (it.State == qRunning)
        mdl.queue.mu.Unlock()
    }
```

Change `mdl.load(it.CuePath)` to `mdl.loadAndFocus(it.CuePath)`. Nothing else in the block changes.

- [ ] **Step 2.2: Swap the Open-button goroutine**

In `tools/miniscram-gui/main.go` around line 1185-1194, the Open-button goroutine currently reads:

```go
go func() {
    p, err := pickFile()
    if err != nil || p == "" {
        return
    }
    mdl.load(p)
    if mdl.invalidate != nil {
        mdl.invalidate()
    }
}()
```

Change `mdl.load(p)` to `mdl.loadAndFocus(p)`. The picker-cancel guard (`p == ""`) above is unchanged, so cancelling the dialog leaves view untouched. The synchronous `autoFollow = false` write at lines 1182-1184 (outside the goroutine) is also unchanged.

- [ ] **Step 2.3: Swap the post-addPaths .miniscram load**

In `tools/miniscram-gui/queue.go` around line 170-173, the block currently reads:

```go
// Load .miniscram files into the inspect pane without holding the queue lock.
if mdl != nil && len(miniscramPaths) > 0 {
    mdl.load(miniscramPaths[len(miniscramPaths)-1])
}
```

Change `mdl.load(...)` to `mdl.loadAndFocus(...)`. This site is reached only when the user dragged or picked one or more .miniscram files; the queue worker never reaches this branch.

- [ ] **Step 2.4: Verify the build is clean**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go build -o /tmp/miniscram-gui-verify .
```

Expected: build succeeds, binary lands at `/tmp/miniscram-gui-verify`.

- [ ] **Step 2.5: Re-run the load-focus tests**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestLoadAndFocus|TestLoad_' -v ./...
```

Expected: still 4/4 PASS. Switching call sites shouldn't have moved any tests.

- [ ] **Step 2.6: Run the full GUI test package**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test ./...
```

Expected: full pass. No existing test exercises the swapped call sites directly, but this catches surprises.

- [ ] **Step 2.7: Confirm no remaining user-explicit caller is on bare `load`**

```bash
grep -n 'mdl\.load(\|m\.load(' tools/miniscram-gui/main.go tools/miniscram-gui/queue.go
```

Expected output (these are the worker-driven sites that should stay on bare `load`):

```
tools/miniscram-gui/main.go:605:    m.load(res.Output)
tools/miniscram-gui/main.go:932:    mdl.load(*loadPath)
tools/miniscram-gui/queue.go:363:   mdl.load(item.CuePath)
tools/miniscram-gui/queue.go:402:   mdl.load(out)
```

If any other `mdl.load(`/`m.load(` site appears in the output beyond these four, stop — it's either a user-explicit site that wasn't swapped or new code the spec missed. Do not commit until resolved.

- [ ] **Step 2.8: Commit**

```bash
git add tools/miniscram-gui/main.go tools/miniscram-gui/queue.go
git commit -m "$(cat <<'EOF'
gui: pivot to Inspect on user-explicit file loads

The sidebar row, Open button, and single-.miniscram drop/pick paths
all called load() without changing m.view. While the user was on the
Stats tab, this silently mutated the model and produced no visible
feedback — the bug reported in #43. Swap these three sites to the
new loadAndFocus helper. Worker-driven sites (queue auto-follow,
post-pack reload, startup -load) keep bare load() so they don't yank
the user off Stats.

Fixes #43.
EOF
)"
```

---

## Manual verification (post-merge, do once)

Build the GUI:

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go build .
```

Seed the queue (drop a few `.cue` and `.miniscram` files in via the GUI's Add Files), then walk through the table from the spec:

| # | Action | Starting tab | Expected after |
|---|---|---|---|
| 1 | Click a sidebar row | Stats  | Inspect, loaded file |
| 2 | Click a sidebar row | Inspect | Inspect, loaded file |
| 3 | Open button, pick a file | Stats | Inspect, loaded file |
| 4 | Open button, cancel picker | Stats | Stats (unchanged) |
| 5 | Drag-drop a single .miniscram | Stats | Inspect, loaded file |
| 6 | AddFiles, pick a single .miniscram | Stats | Inspect, loaded file |
| 7 | Drag-drop one or more .cue | Stats | Stats; queue panel shows new items |
| 8 | AddDir of a folder of .cue | Stats | Stats; queue panel shows new items |
| 9 | Queue worker auto-advances | Stats | Stats; queue panel + events refresh |
| 10 | Pack completes (single-file) | Stats | Stats; events table refreshes |
| 11 | Verify / Unpack / Pack click | Stats | Stats; runner strip shows progress |

Rows 1–6 are the bug-class cases (must pivot). Rows 7–11 must NOT pivot.

---

## Out of scope (reminder from spec)

- Gio headless test harness for the event-loop wiring. Out of proportion.
- Stats view filtering by selected sidebar item.
- Removing the sidebar from Stats entirely.
