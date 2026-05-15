# GUI startup positional argument — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `miniscram-gui` accept a single positional argument at startup — a directory acts like AddDir, a file acts like `-load`.

**Architecture:** Pure helper `resolveStartupAction(loadFlag, args)` classifies the requested path. `main()` switches on the result and routes to either `mdl.queue.addPaths` (dir) or `mdl.load` (file). The queue init moves up one block so the dir branch can call `addPaths` immediately.

**Tech Stack:** Go 1.23, standard library only. `CC=/usr/bin/clang CGO_ENABLED=1` for local GUI builds.

**Spec:** `docs/superpowers/specs/2026-05-14-gui-startup-arg-design.md`.

---

## File map

- **Create:** `tools/miniscram-gui/startup_arg_test.go` — eight unit tests on the new helper.
- **Modify:** `tools/miniscram-gui/main.go` — add `startupAction` type + `resolveStartupAction` helper, replace the `-load`-only block in `main()` with a switch.

---

### Task 1: Add `resolveStartupAction` helper with TDD

**Files:**
- Create: `tools/miniscram-gui/startup_arg_test.go`
- Modify: `tools/miniscram-gui/main.go` (add type + helper near other top-level helpers)

- [ ] **Step 1.1: Write the eight pinning tests**

Create `tools/miniscram-gui/startup_arg_test.go` with EXACTLY this content:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWriteFile(t *testing.T, p string) string {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestResolveStartupAction_Empty(t *testing.T) {
	a := resolveStartupAction("", nil)
	if a.Kind != "" {
		t.Errorf("Kind = %q, want \"\"", a.Kind)
	}
	if a.Path != "" {
		t.Errorf("Path = %q, want \"\"", a.Path)
	}
}

func TestResolveStartupAction_LoadFlagOnly_File(t *testing.T) {
	f := mustWriteFile(t, filepath.Join(t.TempDir(), "x.cue"))

	a := resolveStartupAction(f, nil)
	if a.Kind != "file" {
		t.Errorf("Kind = %q, want \"file\"", a.Kind)
	}
	if a.Path != f {
		t.Errorf("Path = %q, want %q", a.Path, f)
	}
}

func TestResolveStartupAction_LoadFlagOnly_Dir(t *testing.T) {
	d := t.TempDir()

	a := resolveStartupAction(d, nil)
	if a.Kind != "dir" {
		t.Errorf("Kind = %q, want \"dir\"", a.Kind)
	}
	if a.Path != d {
		t.Errorf("Path = %q, want %q", a.Path, d)
	}
}

func TestResolveStartupAction_DirPositional(t *testing.T) {
	d := t.TempDir()

	a := resolveStartupAction("", []string{d})
	if a.Kind != "dir" {
		t.Errorf("Kind = %q, want \"dir\"", a.Kind)
	}
	if a.Path != d {
		t.Errorf("Path = %q, want %q", a.Path, d)
	}
}

func TestResolveStartupAction_FilePositional(t *testing.T) {
	f := mustWriteFile(t, filepath.Join(t.TempDir(), "x.cue"))

	a := resolveStartupAction("", []string{f})
	if a.Kind != "file" {
		t.Errorf("Kind = %q, want \"file\"", a.Kind)
	}
	if a.Path != f {
		t.Errorf("Path = %q, want %q", a.Path, f)
	}
}

func TestResolveStartupAction_NonexistentPositional(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	a := resolveStartupAction("", []string{missing})
	if a.Kind != "file" {
		t.Errorf("Kind = %q, want \"file\" (fallthrough so load() can surface the error)", a.Kind)
	}
	if a.Path != missing {
		t.Errorf("Path = %q, want %q", a.Path, missing)
	}
}

func TestResolveStartupAction_LoadFlagWinsOverPositional(t *testing.T) {
	flagFile := mustWriteFile(t, filepath.Join(t.TempDir(), "from-flag.cue"))
	positionalDir := t.TempDir()

	a := resolveStartupAction(flagFile, []string{positionalDir})
	if a.Kind != "file" {
		t.Errorf("Kind = %q, want \"file\" (flag should win)", a.Kind)
	}
	if a.Path != flagFile {
		t.Errorf("Path = %q, want %q", a.Path, flagFile)
	}
}

func TestResolveStartupAction_FirstPositionalWins(t *testing.T) {
	first := t.TempDir()
	second := mustWriteFile(t, filepath.Join(t.TempDir(), "second.cue"))

	a := resolveStartupAction("", []string{first, second})
	if a.Kind != "dir" {
		t.Errorf("Kind = %q, want \"dir\" (first arg wins)", a.Kind)
	}
	if a.Path != first {
		t.Errorf("Path = %q, want %q", a.Path, first)
	}
}
```

- [ ] **Step 1.2: Run tests, expect compile failure**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestResolveStartupAction' -v ./...
```

Expected: build fails — `resolveStartupAction` undefined, `startupAction` undefined. Confirm the error names those identifiers.

- [ ] **Step 1.3: Add the helper to `main.go`**

Open `tools/miniscram-gui/main.go`. Find the `func main() {` declaration (around line 925). Immediately BEFORE `func main() {`, insert the type and helper:

```go
// startupAction is the resolved intent of CLI invocation: load a
// file (-load <file> or positional file), enqueue a directory
// (positional dir, equivalent of clicking AddDir on startup), or
// do nothing.
type startupAction struct {
	Kind string // "" | "dir" | "file"
	Path string
}

// resolveStartupAction picks the path to act on at startup and
// classifies it. -load wins over a positional arg; among multiple
// positional args the first one wins. A nonexistent path is
// classified as "file" so the caller can route it through load(),
// which already surfaces a sensible error.
func resolveStartupAction(loadFlag string, args []string) startupAction {
	p := loadFlag
	if p == "" && len(args) > 0 {
		p = args[0]
	}
	if p == "" {
		return startupAction{}
	}
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return startupAction{Kind: "dir", Path: p}
	}
	return startupAction{Kind: "file", Path: p}
}
```

`os` is already imported by `main.go`. No new imports.

- [ ] **Step 1.4: Run tests, expect 8/8 PASS**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test -run 'TestResolveStartupAction' -v ./...
```

Expected: 8/8 PASS.

- [ ] **Step 1.5: Run the full GUI test package**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test ./...
```

Expected: ok.

- [ ] **Step 1.6: Commit**

Use absolute paths in case the shell working directory is stale from earlier commands.

```bash
git add /home/hugh/miniscram/tools/miniscram-gui/main.go /home/hugh/miniscram/tools/miniscram-gui/startup_arg_test.go
git commit -m "$(cat <<'EOF'
gui: add resolveStartupAction helper for CLI args

Pure function that classifies a startup target as "dir", "file",
or "" based on a -load flag value and positional args. -load wins
over positionals; first positional wins among many; nonexistent
paths fall through to "file" so the caller can route through
load() and surface load()'s standard error. Eight unit tests
pin precedence and classification. No call sites yet — wiring
into main() lands separately.
EOF
)"
```

---

### Task 2: Wire `resolveStartupAction` into `main()`

**Files:**
- Modify: `tools/miniscram-gui/main.go` (lines ~973-980)

- [ ] **Step 2.1: Replace the `-load` block and move queue init up**

Open `tools/miniscram-gui/main.go`. Find the block at lines 973-980, which currently reads:

```go
	if *loadPath != "" {
		mdl.load(*loadPath)
	}
	if mdl.view == "stats" {
		mdl.refreshStats()
	}

	mdl.queue = newQueueModel()
```

Replace it with:

```go
	mdl.queue = newQueueModel()

	switch a := resolveStartupAction(*loadPath, flag.Args()); a.Kind {
	case "dir":
		mdl.queue.addPaths(mdl, []string{a.Path})
	case "file":
		mdl.load(a.Path)
	}
	if mdl.view == "stats" {
		mdl.refreshStats()
	}
```

Order safety: `mdl.queue = newQueueModel()` now runs BEFORE the switch, so the dir branch's `addPaths` call has the queue available. `mdl.load` does not read or write `mdl.queue`, so moving the queue init earlier does not affect the file branch.

`flag.Args()` is already available via the existing `flag.Parse()` call near the top of `main()`. No new imports.

- [ ] **Step 2.2: Verify the build is clean**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go build -o /tmp/miniscram-gui-verify .
```

Expected: build succeeds.

- [ ] **Step 2.3: Run the full GUI test package**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test ./...
```

Expected: ok. The new helper's eight tests still pass; nothing else regresses.

- [ ] **Step 2.4: Manual smoke check (optional but recommended)**

Run the GUI with each of the four shapes from the spec:

```bash
# Dir positional arg → queue populated.
/tmp/miniscram-gui-verify /tmp

# File positional arg → cue/miniscram inspect view.
/tmp/miniscram-gui-verify /tmp/some.cue   # (skip if no fixture handy)

# -load wins over positional dir.
/tmp/miniscram-gui-verify -load /tmp/some.cue /tmp

# No args → empty GUI, current behavior.
/tmp/miniscram-gui-verify
```

Close the window after a few seconds in each case. (Skip whichever runs you can't conveniently set up — the unit tests cover the classification logic; this is just a sanity check that wiring is intact.)

- [ ] **Step 2.5: Commit**

Use absolute paths.

```bash
git add /home/hugh/miniscram/tools/miniscram-gui/main.go
git commit -m "$(cat <<'EOF'
gui: accept a positional startup arg (dir → AddDir, file → load)

main() now routes the first positional arg through
resolveStartupAction. A directory becomes equivalent to clicking
AddDir on startup (mdl.queue.addPaths); a file becomes equivalent
to -load (mdl.load). -load still wins if both are set, and only
the first positional is considered. Queue init moves up one block
so the dir branch can call addPaths immediately — safe because
load() does not touch mdl.queue.
EOF
)"
```

---

## Out of scope (reminder from spec)

- Multi-arg handling beyond first-wins.
- Updating `-help` / usage line.
- README documentation.
- Combining `-load <file>` with a positional dir in any way other than "flag wins".
