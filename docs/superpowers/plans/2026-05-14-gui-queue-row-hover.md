# GUI: queue row hover highlight — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Paint a 1-px grey border around a queue row's bounding box when the cursor is over it.

**Architecture:** `widget.Clickable.Hovered()` already tracks hover state for the per-row click area in `queueRow` (`tools/miniscram-gui/queue_widget.go`). Add a single conditional at the end of the row render that paints four thin rectangles forming a frame. Color is the existing palette's `text3`.

**Tech Stack:** Go 1.23, Gio UI (`gioui.org`), `CC=/usr/bin/clang CGO_ENABLED=1` for local GUI builds.

**Spec:** `docs/superpowers/specs/2026-05-14-gui-queue-row-hover-design.md`.

---

## File map

- **Modify:** `tools/miniscram-gui/queue_widget.go` — add ~3-line conditional inside `queueRow` and a small `drawHoverBorder` helper near the bottom of the file.

No new tests: the GUI has no headless rendering harness, and the change is purely visual. Manual verification per the spec.

---

### Task 1: Paint the hover border

**Files:**
- Modify: `tools/miniscram-gui/queue_widget.go`

- [ ] **Step 1.1: Add the hover check inside `queueRow`**

In `tools/miniscram-gui/queue_widget.go`, find the `queueRow` function (around line 182). Inside the closure it returns, find the line:

```go
call.Add(gtx.Ops)
```

(currently around line 225). Immediately AFTER that line, BEFORE the existing `if content.Size.Y < rowH {` block, insert:

```go
if clickArea.Hovered() {
    drawHoverBorder(gtx.Ops, content.Size)
}
```

`clickArea` is in scope from earlier in the same closure (`clickArea := btns.RowClick(it.ID)` at line 184).

- [ ] **Step 1.2: Add the `drawHoverBorder` helper at the bottom of the file**

At the very end of `tools/miniscram-gui/queue_widget.go` (after the closing brace of `queueRowActionBtn`), append:

```go

// drawHoverBorder paints a 1-px frame around a rectangle of the
// given size at (0,0). Four thin filled rects rather than
// clip.Stroke because the latter's Path API has shifted across Gio
// releases — the four-rect form is portable.
func drawHoverBorder(ops *op.Ops, size image.Point) {
	col := text3
	paint.FillShape(ops, col, clip.Rect{Max: image.Pt(size.X, 1)}.Op())                       // top
	paint.FillShape(ops, col, clip.Rect{Min: image.Pt(0, size.Y - 1), Max: size}.Op())         // bottom
	paint.FillShape(ops, col, clip.Rect{Max: image.Pt(1, size.Y)}.Op())                       // left
	paint.FillShape(ops, col, clip.Rect{Min: image.Pt(size.X - 1, 0), Max: size}.Op())         // right
}
```

`op`, `clip`, `paint`, and `image` are all already imported by `queue_widget.go` (used elsewhere in the same file). `text3` is the palette color from `main.go`. No import changes needed.

- [ ] **Step 1.3: Build verify**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go build -o /tmp/miniscram-gui-verify .
```

Expected: build succeeds.

- [ ] **Step 1.4: Run the full GUI test package as a regression check**

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go test ./...
```

Expected: ok. Nothing tests the row-render path, but this catches any compile-time breakage in adjacent code.

- [ ] **Step 1.5: Commit**

Use absolute paths in case the shell working directory is stale.

```bash
git add /home/hugh/miniscram/tools/miniscram-gui/queue_widget.go
git commit -m "$(cat <<'EOF'
gui: hover highlight on queue rows

Paint a 1-px text3-grey border around a queue row's bounding box
when the cursor is over it. Reads widget.Clickable.Hovered() on the
existing per-row click area — no input wiring or model changes. The
border lays on top of state backgrounds and content so it stays
visible across all six row states.

No automated tests: the GUI has no headless rendering harness and
the change is purely visual.
EOF
)"
```

---

## Manual verification (post-merge)

Build and run the GUI with a populated queue:

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go build .
./miniscram-gui /path/to/dir/of/cues
```

Move the mouse over each row. Expected:

- Hovering any row paints a 1-px grey border around its bounding box.
- Moving off the row removes the border immediately.
- The border is visible for all six states (ready, running over the mint progress fill, done, failed over the dark red tint, skipped, cancelled).

---

## Out of scope (reminder from spec)

- Cursor change to pointer/hand on hover.
- Hover effects on non-row widgets (Add buttons, Stop button, row × / ⏹ buttons).
- Animation or transition on hover state change.
- Hover effects in other panels (inspect view, stats events table).
