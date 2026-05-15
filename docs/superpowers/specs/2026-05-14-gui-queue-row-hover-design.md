# GUI: queue row hover highlight

Date: 2026-05-14

## Motivation

Queue rows are interactive — clicking one loads that item — but the
panel offers no visual hint that rows are hoverable. A small bounding
box outline on hover makes the row's hit area discoverable and
matches the feedback users expect from list UIs.

## Design

`widget.Clickable` already exposes `.Hovered()`. The per-row
`clickArea` value is already in scope inside `queueRow`
(`tools/miniscram-gui/queue_widget.go`), so we don't need to touch
the input layer or the model.

After the existing content + state-background paint sequence in
`queueRow`, paint a 1-pixel frame around the row's bounding box
when the click area is hovered. The frame sits on top of both the
state backgrounds (qRunning's progress fill, qFailed's red tint)
and the content, so it stays visible in all six row states without
fighting any of them.

### Code shape

Inside the closure returned by `queueRow`, after the existing
`call.Add(gtx.Ops)`:

```go
if clickArea.Hovered() {
    drawHoverBorder(gtx.Ops, content.Size)
}
```

New helper at the bottom of `queue_widget.go`:

```go
// drawHoverBorder paints a 1-px frame around a rectangle of the
// given size at (0,0). Four thin filled rects rather than
// clip.Stroke because the latter's Path API has shifted across
// Gio releases — the four-rect form is portable.
func drawHoverBorder(ops *op.Ops, size image.Point) {
    col := text3
    paint.FillShape(ops, col, clip.Rect{Max: image.Pt(size.X, 1)}.Op())                          // top
    paint.FillShape(ops, col, clip.Rect{Min: image.Pt(0, size.Y - 1), Max: size}.Op())            // bottom
    paint.FillShape(ops, col, clip.Rect{Max: image.Pt(1, size.Y)}.Op())                          // left
    paint.FillShape(ops, col, clip.Rect{Min: image.Pt(size.X - 1, 0), Max: size}.Op())            // right
}
```

`text3` (`#6f7682`) is the existing mid-grey from the palette
(`main.go::text3`). It reads against `bg`, against qRunning's mint
fill, and against qFailed's dark red. No new palette entry.

### Layering

Current order in `queueRow`:

1. `macro := op.Record(gtx.Ops)` — record content into a macro.
2. `content := layout...` — render row content into the macro.
3. `call := macro.Stop()`.
4. Paint state backgrounds (qRunning fill, qFailed tint).
5. `call.Add(gtx.Ops)` — replay the recorded content on top.

New order: identical through step 5, then:

6. If `clickArea.Hovered()` → paint the 1-px border on top.

The border on the topmost layer is intentional: it must remain
visible regardless of the underlying state. A 1-px frame doesn't
visually compete with the content beneath.

### What doesn't change

- The Clickable input handling (`clickArea.Layout(gtx, ...)`) is
  unchanged; `Hovered()` is a free read of state Gio already tracks.
- All six row state branches (qReady / qRunning / qDone / qFailed /
  qSkipped / qCancelled) keep their current visuals.
- No model changes. The hover state is event-driven by Gio inside
  `widget.Clickable`; the model doesn't need to know.

## Tests

None. The GUI in this repo has no headless rendering harness, and
the change is purely visual. The two relevant correctness questions —
"does Hovered() return true when the cursor is over the row" and
"does drawHoverBorder paint where I expect" — are exactly the
questions a unit test can't answer without an input/rendering
simulator. Verified manually by mousing over the queue panel.

## Manual verification

Build:

```bash
cd tools/miniscram-gui && CC=/usr/bin/clang CGO_ENABLED=1 go build .
```

Run with a populated queue (e.g. drop a folder of cues onto the
window, or pass one as a positional arg). Move the mouse over each
row. Expected:

- Hovering any row paints a 1-px grey border around its bounding box.
- Moving off the row removes the border.
- The border is visible for all states: ready, running (over the
  progress fill), done, failed (over the red tint), skipped,
  cancelled.

## Out of scope

- Cursor change to "hand"/"pointer" on hover. Separate concern; the
  current default cursor stays.
- Hover effects on non-row widgets (Add buttons, Stop button, action
  × / ⏹ buttons). Stays scoped to the row's bounding box.
- Animations or transitions. The border appears and disappears
  immediately with hover state.
- Hover effects in other panels (inspect view, stats events table).
  Separate decision.

## Risk

Negligible. `Clickable.Hovered()` is a stable Gio API. The four-rect
border paint follows the existing `paint.FillShape(... clip.Rect ...)`
pattern already in `queueRow` (qRunning fill and qFailed tint).
