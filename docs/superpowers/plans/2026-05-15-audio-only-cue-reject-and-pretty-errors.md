# Audio-only cue reject + pretty GUI errors — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reject audio-only cuesheets at the top of `Pack` with a clean sentinel error, prettify the GUI's failure surfaces, and cap the queue sidebar's reason column to a single line. Closes [#50](https://github.com/hughobrien/miniscram/issues/50).

**Architecture:** Three small, independent changes in three files. (1) New `ErrAudioOnlyDisc` sentinel + `anyDataTrack` helper, fired before any scram I/O in `Pack`. (2) `prettyProgressLine` applied to `res.Error` in the GUI runner's wait-path so the toast and queue row show human-readable text instead of raw NDJSON. (3) A small `reasonLabel` helper in `queue_widget.go` that forces `MaxLines = 1` on the `qFailed` row suffix.

**Tech Stack:** Go 1.21+, Gio v0.9.0 (`gioui.org`), standard `testing`.

**Spec:** [`docs/superpowers/specs/2026-05-15-audio-only-cue-reject-and-pretty-errors-design.md`](../specs/2026-05-15-audio-only-cue-reject-and-pretty-errors-design.md)

---

## File Structure

| Path | Role | Action |
| --- | --- | --- |
| `pack.go` | Pack pipeline | Modify (add `ErrAudioOnlyDisc`, insert early-reject step) |
| `cue.go` | Cue types + helpers | Modify (add `anyDataTrack`) |
| `pack_test.go` | Pack failure-mode tests | Modify (add `TestPackRejectsAudioOnlyCue`) |
| `tools/miniscram-gui/runner.go` | Subprocess wrapper | Modify (prettify `res.Error`) |
| `tools/miniscram-gui/runner_test.go` | Runner tests | Modify (fail-path emits NDJSON; add JSON fail-mode) |
| `tools/miniscram-gui/queue_widget.go` | Queue row widget | Modify (extract `reasonLabel`, set `MaxLines = 1`) |
| `tools/miniscram-gui/queue_widget_test.go` | Widget unit tests | **Create** (cover `reasonLabel`) |

---

## Branch setup

This work should land on a fresh branch off `main`, not on `docs/spec-sess-chunk` (the spec branch). Cherry-pick the spec commit onto the new branch so the spec ships in the same PR as the code.

### Task 0: Create feature branch with spec commit

**Files:** none yet (git ops)

- [ ] **Step 1: Identify the spec commit on `docs/spec-sess-chunk`**

```bash
git log --oneline docs/spec-sess-chunk -- docs/superpowers/specs/2026-05-15-audio-only-cue-reject-and-pretty-errors-design.md
```

Expected: one commit whose subject begins `docs: design spec for audio-only cue reject`. Note its SHA (e.g. `ac826a8`).

- [ ] **Step 2: Branch off main and cherry-pick the spec**

```bash
git fetch origin main
git checkout -b fix/issue-50-audio-only-pretty-errors origin/main
git cherry-pick <SPEC_SHA>
```

Expected: clean cherry-pick (the new spec file is independent of the SESS-chunk docs).

- [ ] **Step 3: Verify clean tree**

```bash
git status
git log --oneline -3
```

Expected: clean tree; top commit is the cherry-picked spec.

---

## Section 1 — Reject audio-only cuesheets in `Pack`

### Task 1: Add `anyDataTrack` helper and `ErrAudioOnlyDisc` sentinel + early reject

**Files:**
- Modify: `cue.go` (add `anyDataTrack` near `Track.IsData`, around line 48)
- Modify: `pack.go` (declare `ErrAudioOnlyDisc` in the error block at lines 28–42; insert reject between the "resolving cue" step `st.Done` at line 87 and `os.Stat` at line 90)
- Modify: `pack_test.go` (append a new test function)

- [ ] **Step 1: Write the failing test**

Append to `/home/hugh/miniscram/pack_test.go`:

```go
// TestPackRejectsAudioOnlyCue confirms Pack short-circuits before any
// scram I/O when every TRACK in the cue is AUDIO. ScramPath points to
// a nonexistent file on purpose: if the early-reject ever regresses,
// os.Stat would surface a different error and this test would still
// fail (just with the wrong message), making the regression obvious.
func TestPackRejectsAudioOnlyCue(t *testing.T) {
	dir := t.TempDir()
	cue := "FILE \"x (Track 1).bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n" +
		"FILE \"x (Track 2).bin\" BINARY\n  TRACK 02 AUDIO\n    INDEX 01 00:00:00\n"
	if err := os.WriteFile(filepath.Join(dir, "x.cue"), []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	// ResolveCue stats each FILE; one-sector audio bins are enough.
	for _, name := range []string{"x (Track 1).bin", "x (Track 2).bin"} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, SectorSize), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := Pack(PackOptions{
		CuePath:    filepath.Join(dir, "x.cue"),
		ScramPath:  filepath.Join(dir, "does-not-exist.scram"),
		OutputPath: filepath.Join(dir, "x.miniscram"),
	}, nil)
	if !errors.Is(err, ErrAudioOnlyDisc) {
		t.Fatalf("expected ErrAudioOnlyDisc, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /home/hugh/miniscram
go test -run TestPackRejectsAudioOnlyCue ./... -count=1
```

Expected: FAIL — `ErrAudioOnlyDisc` is undeclared (compile error).

- [ ] **Step 3: Declare `ErrAudioOnlyDisc` in `pack.go`**

Edit `/home/hugh/miniscram/pack.go`. Find the existing error block ending with `ErrSessionFirstTrackNotData` (around line 41). After the closing `)` of that var block (currently line 42), add a new entry inside the same block — i.e. add this line just before the closing `)`:

```go
	// ErrAudioOnlyDisc means the cuesheet has zero data tracks
	// (every TRACK is AUDIO). Miniscram only packs scrambled data
	// tracks; on an audio-only disc there is nothing to do, and
	// detectWriteOffset would otherwise spin through the entire scram
	// file before failing with "no plausible scrambled sync field
	// found".
	ErrAudioOnlyDisc = errors.New("cue contains only AUDIO tracks; nothing for miniscram to scramble-pack")
```

- [ ] **Step 4: Add `anyDataTrack` helper in `cue.go`**

Edit `/home/hugh/miniscram/cue.go`. Immediately after the existing `Track.IsData` function (currently line 48), add:

```go
// anyDataTrack reports whether the slice contains at least one
// non-AUDIO track. Used by Pack to short-circuit on audio-only discs
// before any scram I/O.
func anyDataTrack(tracks []Track) bool {
	for _, t := range tracks {
		if t.IsData() {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Wire the reject into `Pack`**

Edit `/home/hugh/miniscram/pack.go`. Find the "resolving cue" step block that ends with `st.Done("%d track(s), %d bytes total", len(tracks), binSize)` (currently line 87). Immediately after that line and before the `// 2. stat scram.` comment, insert:

```go

	// 1b. audio-only short-circuit. Pack has nothing to do when every
	// track is AUDIO — detectWriteOffset would scan the entire scram
	// before failing. Fail fast with a clean sentinel.
	if !anyDataTrack(tracks) {
		st = r.Step("checking disc type")
		st.Fail(ErrAudioOnlyDisc)
		return ErrAudioOnlyDisc
	}
```

- [ ] **Step 6: Run test to verify it passes**

```bash
cd /home/hugh/miniscram
go test -run TestPackRejectsAudioOnlyCue ./... -count=1
```

Expected: PASS.

- [ ] **Step 7: Run the full root-package test suite**

```bash
cd /home/hugh/miniscram
go test ./... -count=1
```

Expected: PASS. No existing test should regress (the only audio-related Pack test is `TestPackEnhancedCDRejectsAudioSecondSession`, which has a data track in session 1 and so does not trigger the new check).

- [ ] **Step 8: Smoke-test the CLI against a real audio cue (optional, manual)**

If a redumper-produced audio-only cue is handy locally, run:

```bash
cd /home/hugh/miniscram
go build -o /tmp/miniscram-audio-fix .
/tmp/miniscram-audio-fix pack /path/to/AudioCD.cue
```

Expected: exits non-zero within ~1 second with stderr line ending in `cue contains only AUDIO tracks; nothing for miniscram to scramble-pack`. No multi-minute scan of the scram.

If no audio fixture is at hand, skip — the unit test is authoritative.

- [ ] **Step 9: Commit**

```bash
cd /home/hugh/miniscram
git add pack.go cue.go pack_test.go
git commit -m "$(cat <<'EOF'
pack: reject audio-only cues with a clean sentinel error (#50)

Audio-only discs have no scrambled sync field, so detectWriteOffset
scans the entire scram before failing with a low-level "no plausible
scrambled sync field found" message. Short-circuit right after cue
resolution with ErrAudioOnlyDisc so the GUI surfaces a clear message
and the CLI exits in under a second.
EOF
)"
```

---

## Section 2 — Prettify `res.Error` in the GUI runner

### Task 2: Apply `prettyProgressLine` in `runner.go` wait-path

**Files:**
- Modify: `tools/miniscram-gui/runner.go:200-215` (the fail branch in `wait()`)
- Modify: `tools/miniscram-gui/runner_test.go` (extend `TestMain` with a JSON fail fake-mode; add a new test)

- [ ] **Step 1: Write the failing test**

Edit `/home/hugh/miniscram/tools/miniscram-gui/runner_test.go`. In the `TestMain` switch (currently has cases `"happy"`, `"fail"`, `"long"`, `"json_happy"`, `"json_long"`), add a new case before the `default:` label:

```go
	case "json_fail":
		// Emit one step then a fail event mirroring what pack.go writes
		// via Reporter.Fail when --progress=json is in effect.
		fmt.Fprintln(os.Stderr, `{"type":"step","label":"detecting write offset"}`)
		fmt.Fprintln(os.Stderr, `{"type":"fail","label":"detecting write offset","error":"no plausible scrambled sync field found in 765077352 bytes of scram"}`)
		os.Exit(1)
```

Below `TestActionRunner_Fail` (the existing fail test ends around line 142), append a new test:

```go
// TestActionRunner_FailNDJSONReason confirms the wait-path renders an
// NDJSON fail event into a human-readable res.Error rather than passing
// the raw JSON line through. Without prettyProgressLine in runner.go,
// the toast and queue row show {"type":"fail",...} which wraps over
// many lines (see issue #50).
func TestActionRunner_FailNDJSONReason(t *testing.T) {
	r, done := newTestRunner(t, "json_fail")

	if err := r.Start("pack", "/in/disc.cue", "/out/disc.miniscram"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	res := waitFor(t, done, 3*time.Second)
	if res.Status != "fail" {
		t.Errorf("status = %q, want fail", res.Status)
	}
	const want = "no plausible scrambled sync field found in 765077352 bytes of scram"
	if res.Error != want {
		t.Errorf("Error = %q, want %q", res.Error, want)
	}
	if strings.Contains(res.Error, `{"type"`) {
		t.Errorf("Error %q still contains raw NDJSON; prettyProgressLine should have stripped it", res.Error)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test -run TestActionRunner_FailNDJSONReason -count=1
```

Expected: FAIL — `res.Error` equals the raw NDJSON line, not the inner `error` field.

(`CC=/usr/bin/clang CGO_ENABLED=1` per the `reference_gui_build_env` memory; the default `go` invocation hits a Gio vk build-constraint issue locally.)

- [ ] **Step 3: Apply `prettyProgressLine` in `runner.go`**

Edit `/home/hugh/miniscram/tools/miniscram-gui/runner.go`. Find the fail-classification block in `wait()`:

```go
	case err != nil:
		res.Status = "fail"
		res.Error = state.LastLine
		if res.Error == "" {
			res.Error = err.Error()
		}
```

Change to:

```go
	case err != nil:
		res.Status = "fail"
		res.Error = prettyProgressLine(state.LastLine)
		if res.Error == "" {
			res.Error = err.Error()
		}
```

`prettyProgressLine` is already defined in the same package (`queue.go:193`) so no import changes are needed.

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test -run TestActionRunner_FailNDJSONReason -count=1
```

Expected: PASS.

- [ ] **Step 5: Confirm existing fail-path tests still pass**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test -run "TestActionRunner_Fail|TestHandleActionResult" -count=1
```

Expected: PASS. The legacy `TestActionRunner_Fail` uses non-JSON stderr (`"scram not found: /no/such/file.scram"`); `prettyProgressLine` returns non-JSON strings unchanged, so its `strings.Contains(res.Error, "scram not found")` assertion still holds. The `result_handler_test.go` fixtures construct `actionResult` directly with literal `Error: "scram not found"` and don't touch this path.

- [ ] **Step 6: Run the full GUI test suite**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /home/hugh/miniscram
git add tools/miniscram-gui/runner.go tools/miniscram-gui/runner_test.go
git commit -m "$(cat <<'EOF'
gui: prettify subprocess fail reason for toast + queue (#50)

The runner stored state.LastLine verbatim in res.Error, which for
--progress=json mode is a raw NDJSON event. Both the bottom toast
(FailMsg) and the sidebar queue row (Reason) read res.Error, so every
failure rendered as {"type":"fail","label":"…","error":"…"} —
wrapping over many lines in the narrow sidebar (see issue #50).

Apply prettyProgressLine in runner.go's wait-path: it extracts the
inner error field, falls back to "<label> failed" when error is
empty, and passes non-JSON lines through unchanged.
EOF
)"
```

---

## Section 3 — Cap sidebar reason to one line

### Task 3: Extract `reasonLabel` helper and force `MaxLines = 1` for `qFailed`

**Files:**
- Modify: `tools/miniscram-gui/queue_widget.go` (extract helper near `queueRowSuffix`)
- Create: `tools/miniscram-gui/queue_widget_test.go`

- [ ] **Step 1: Write the failing test**

Create `/home/hugh/miniscram/tools/miniscram-gui/queue_widget_test.go`:

```go
// tools/miniscram-gui/queue_widget_test.go
package main

import (
	"testing"

	"gioui.org/widget/material"
)

// TestReasonLabel_FailedSingleLine confirms qFailed reasons render as
// a single line with the default truncator. Without this cap, error
// strings wrap multi-line and tower over the queue (see issue #50).
func TestReasonLabel_FailedSingleLine(t *testing.T) {
	th := material.NewTheme()
	long := "no plausible scrambled sync field found in 765077352 bytes of scram"
	lb := reasonLabel(th, qFailed, long)
	if lb.MaxLines != 1 {
		t.Errorf("reasonLabel(qFailed).MaxLines = %d, want 1", lb.MaxLines)
	}
}

// TestReasonLabel_OtherStatesUncapped confirms non-failed states keep
// the default (unbounded) wrapping behaviour.
func TestReasonLabel_OtherStatesUncapped(t *testing.T) {
	th := material.NewTheme()
	for _, st := range []qState{qDone, qSkipped, qCancelled} {
		lb := reasonLabel(th, st, "short")
		if lb.MaxLines != 0 {
			t.Errorf("reasonLabel(%v).MaxLines = %d, want 0", st, lb.MaxLines)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test -run TestReasonLabel -count=1
```

Expected: FAIL — `reasonLabel` is undeclared (compile error).

- [ ] **Step 3: Add `reasonLabel` helper and wire it into `queueRowSuffix`**

Edit `/home/hugh/miniscram/tools/miniscram-gui/queue_widget.go`. Find `queueRowSuffix` (currently lines ~266–291). The body builds the label like this:

```go
		lb := material.Label(th, unit.Sp(11), label)
		lb.Color = col
		return lb.Layout(gtx)
```

Replace those three lines with:

```go
		lb := reasonLabel(th, it.State, label)
		lb.Color = col
		return lb.Layout(gtx)
```

Immediately after the `queueRowSuffix` function (before `queueRowActionBtn`), add:

```go
// reasonLabel builds the label-style for a queue-row suffix. The
// qFailed state forces MaxLines = 1 so long error strings (e.g.
// "no plausible scrambled sync field found in 765077352 bytes of
// scram") don't tower over the queue. Truncator defaults to "…".
func reasonLabel(th *material.Theme, state qState, label string) material.LabelStyle {
	lb := material.Label(th, unit.Sp(11), label)
	if state == qFailed {
		lb.MaxLines = 1
	}
	return lb
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test -run TestReasonLabel -count=1
```

Expected: PASS (both tests).

- [ ] **Step 5: Run the full GUI test suite**

```bash
cd /home/hugh/miniscram/tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 6: Smoke-test the GUI visually (optional, manual)**

```bash
cd /home/hugh/miniscram
CC=/usr/bin/clang CGO_ENABLED=1 go build -o /tmp/miniscram .
cd tools/miniscram-gui
CC=/usr/bin/clang CGO_ENABLED=1 go build -o /tmp/miniscram-gui .
PATH=/tmp:$PATH /tmp/miniscram-gui
```

Drop an audio-only cue (with sibling `.scram`) onto the queue, click Pack. Expected: row turns red within ~1 second; suffix reads `cue contains only AUDIO tracks; nothing for miniscram to scramble-pack` on a single line with ellipsis if it overflows; toast at the bottom shows the same text in full. No JSON braces anywhere.

- [ ] **Step 7: Commit**

```bash
cd /home/hugh/miniscram
git add tools/miniscram-gui/queue_widget.go tools/miniscram-gui/queue_widget_test.go
git commit -m "$(cat <<'EOF'
gui: cap failed-row reason to one line in the queue sidebar (#50)

Even after prettifying res.Error, a plain error string like
"no plausible scrambled sync field found in 765077352 bytes of scram"
wraps to three lines in the narrow sidebar and stacks ugly when
multiple rows fail. Force MaxLines = 1 (Truncator defaults to "…")
for the qFailed state only. Full text remains visible in the toast
and the events history.

Extract a small reasonLabel helper so the MaxLines policy is unit-
testable without a Gio render harness.
EOF
)"
```

---

## Task 4: Open the pull request

**Files:** none (git/gh ops)

- [ ] **Step 1: Push the branch**

```bash
cd /home/hugh/miniscram
git push -u origin fix/issue-50-audio-only-pretty-errors
```

- [ ] **Step 2: Verify CI is green before opening the PR**

```bash
gh run list --branch fix/issue-50-audio-only-pretty-errors --limit 5
```

Wait until the most recent `build + test` run shows `completed success`. If it fails, fix locally and push again.

- [ ] **Step 3: Open the PR**

```bash
gh pr create --title "fix: audio-only cue reject + pretty GUI errors (#50)" --body "$(cat <<'EOF'
## Summary

- Reject audio-only cuesheets at the top of `Pack` with `ErrAudioOnlyDisc` instead of letting `detectWriteOffset` scan the entire scram before failing.
- Prettify subprocess fail reasons in `tools/miniscram-gui/runner.go` so the toast and queue row no longer render raw `--progress=json` NDJSON.
- Cap the queue sidebar's `qFailed` reason to a single line with ellipsis; full text stays in the toast and events history.

Closes #50.

## Test plan

- [ ] `go test ./...` passes at repo root
- [ ] `go test ./...` passes in `tools/miniscram-gui` (`CC=/usr/bin/clang CGO_ENABLED=1`)
- [ ] Manual: pack an audio-only cue via CLI — exits non-zero in under a second with the clean message
- [ ] Manual: pack an audio-only cue via GUI — sidebar shows a one-line red message, toast shows the same in full, no JSON braces visible

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: prints the PR URL.

---

## Self-Review (writer's checklist)

**Spec coverage:**
- Spec §1 (`Pack` reject) → Task 1.
- Spec §2 (`runner.go` prettify) → Task 2.
- Spec §3 (queue widget cap) → Task 3.
- Spec Testing section → Tasks 1 (Pack test), 2 (runner test), 3 (widget test).
- Spec "Migration notes" mentions optionally adding `"checking disc type"` to `packPhases`. The new step only fires on the failure path, so progress-bar monotonicity on a *successful* pack is unaffected. Leaving it out keeps the patch minimal; the spec called this optional.

**Placeholder scan:** no TBD/TODO/"similar to". Every code step shows the actual code. Every command shows the exact invocation.

**Type consistency:** `ErrAudioOnlyDisc`, `anyDataTrack`, `reasonLabel`, and `qState` (existing) used identically across tasks. The TestMain case name `"json_fail"` is referenced once in the new test and once in the new switch case.
