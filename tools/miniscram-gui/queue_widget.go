// tools/miniscram-gui/queue_widget.go
//
// Queue panel layout — header, add-buttons, items list, stop button, and the
// per-row layout for all six queueState values.
//
// Note: queueSnapshot (read-only snapshot value type) and
// (*queueModel).Snapshot() live in queue.go alongside queueModel — not here —
// so that all model-layer methods stay with their type.
package main

import (
	"fmt"
	"image"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// queuePanelButtons groups the panel's interactive widgets so loop() can own
// their lifetime.
type queuePanelButtons struct {
	AddFiles      widget.Clickable
	AddDir        widget.Clickable
	DeleteScramCB widget.Bool
	Stop          widget.Clickable
	rowClick      map[int64]*widget.Clickable
	rowAction     map[int64]*widget.Clickable // × for ready, ⏹ for running
}

func newQueuePanelButtons() *queuePanelButtons {
	return &queuePanelButtons{
		rowClick:  map[int64]*widget.Clickable{},
		rowAction: map[int64]*widget.Clickable{},
	}
}

// RowClick returns (creating if needed) the clickable for the row background.
func (b *queuePanelButtons) RowClick(id int64) *widget.Clickable {
	if c, ok := b.rowClick[id]; ok {
		return c
	}
	c := new(widget.Clickable)
	b.rowClick[id] = c
	return c
}

// RowAction returns (creating if needed) the clickable for the per-row × / ⏹ button.
func (b *queuePanelButtons) RowAction(id int64) *widget.Clickable {
	if c, ok := b.rowAction[id]; ok {
		return c
	}
	c := new(widget.Clickable)
	b.rowAction[id] = c
	return c
}

// queuePanelWidth is the fixed pixel-independent width of the queue panel.
const queuePanelWidth = 280

// queuePanel renders the left-hand queue. Accepts a snapshot (slice + flags)
// so layout never touches the queue mutex.
func queuePanel(th *material.Theme, snap queueSnapshot, btns *queuePanelButtons, listScroll *widget.List) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Dp(unit.Dp(queuePanelWidth))
		gtx.Constraints.Max.X = gtx.Dp(unit.Dp(queuePanelWidth))

		return layout.Inset{Top: unit.Dp(12), Bottom: unit.Dp(12), Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(queueHeader(th, snap)),
					layout.Rigid(spacer(0, 10)),
					layout.Rigid(queueAddButtons(th, btns)),
					layout.Rigid(spacer(0, 8)),
					layout.Rigid(queueDeleteScramRow(th, btns)),
					layout.Rigid(spacer(0, 8)),
					layout.Rigid(thinDivider),
					queueItemsList(th, snap, btns, listScroll),
					layout.Rigid(thinDivider),
					layout.Rigid(queueStopButton(th, snap, btns)),
				)
			})
	}
}

func queueHeader(th *material.Theme, snap queueSnapshot) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		label := fmt.Sprintf("Queue · %d ready · %d skipped", snap.ReadyCount, snap.SkippedCount)
		lb := material.Label(th, unit.Sp(13), label)
		lb.Color = text2
		lb.Font.Weight = font.SemiBold
		return lb.Layout(gtx)
	}
}

func queueAddButtons(th *material.Theme, btns *queuePanelButtons) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(panelButton(th, &btns.AddFiles, "+ Add files…")),
			layout.Rigid(spacer(8, 0)),
			layout.Rigid(panelButton(th, &btns.AddDir, "+ Add dir…")),
		)
	}
}

func panelButton(th *material.Theme, c *widget.Clickable, label string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		btn := material.Button(th, c, label)
		btn.Background = surface2
		btn.Color = text1
		btn.CornerRadius = unit.Dp(4)
		btn.TextSize = unit.Sp(12)
		btn.Inset = layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}
		return btn.Layout(gtx)
	}
}

func queueDeleteScramRow(th *material.Theme, btns *queuePanelButtons) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		cb := material.CheckBox(th, &btns.DeleteScramCB, "delete .scram after pack")
		cb.TextSize = unit.Sp(11)
		cb.Color = text2
		return cb.Layout(gtx)
	}
}

func queueStopButton(th *material.Theme, snap queueSnapshot, btns *queuePanelButtons) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if !snap.WorkerRunning {
			return layout.Dimensions{}
		}
		return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			btn := material.Button(th, &btns.Stop, "Stop queue")
			btn.Background = mustRGB("3a1a1a")
			btn.Color = bad
			btn.CornerRadius = unit.Dp(4)
			btn.TextSize = unit.Sp(12)
			btn.Inset = layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}
			return btn.Layout(gtx)
		})
	}
}

// queueItemsList returns a Flexed child that either shows the drop-target hint
// (when the queue is empty) or the scrollable list of rows.
func queueItemsList(th *material.Theme, snap queueSnapshot, btns *queuePanelButtons, listScroll *widget.List) layout.FlexChild {
	return layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		if len(snap.Items) == 0 {
			return queueDropTarget(th)(gtx)
		}
		return material.List(th, listScroll).Layout(gtx, len(snap.Items),
			func(gtx layout.Context, i int) layout.Dimensions {
				return queueRow(th, snap.Items[i], btns)(gtx)
			})
	})
}

func queueDropTarget(th *material.Theme) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			lb := material.Label(th, unit.Sp(12), "Drop cues or a\nfolder here")
			lb.Color = text3
			lb.Alignment = text.Middle
			return lb.Layout(gtx)
		})
	}
}

// queueRow returns a widget for one queue row. State-driven:
//   - qReady:     pending dot · basename · [×] button
//   - qRunning:   green fill at width panel_w*Fraction behind row · basename · [⏹] button
//   - qDone:      green dot · basename · duration ("5.4s")
//   - qFailed:    red dot · basename (red) · "fail" tag · row tinted red
//   - qSkipped:   grey dot · basename (grey) · reason at right
//   - qCancelled: grey dot · basename (grey) · "cancelled"
func queueRow(th *material.Theme, it queueItem, btns *queuePanelButtons) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		clickArea := btns.RowClick(it.ID)
		actionBtn := btns.RowAction(it.ID)
		rowH := gtx.Dp(unit.Dp(34))

		return clickArea.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			// Record the row content first, then paint backgrounds behind it.
			macro := op.Record(gtx.Ops)
			content := layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(8), Right: unit.Dp(8)}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(queueRowGlyph(it)),
						layout.Rigid(spacer(8, 0)),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							lb := material.Label(th, unit.Sp(12), it.Basename)
							switch it.State {
							case qSkipped, qCancelled:
								lb.Color = text3
							case qFailed:
								lb.Color = bad
							default:
								lb.Color = text1
							}
							return lb.Layout(gtx)
						}),
						layout.Rigid(queueRowSuffix(th, it)),
						layout.Rigid(spacer(6, 0)),
						layout.Rigid(queueRowActionBtn(th, it, actionBtn)),
					)
				})
			call := macro.Stop()

			// Paint backgrounds behind the recorded content.
			switch it.State {
			case qRunning:
				fillW := int(float64(content.Size.X) * it.Fraction)
				if fillW > 0 {
					paint.FillShape(gtx.Ops, accent, clip.Rect{Max: image.Pt(fillW, content.Size.Y)}.Op())
				}
			case qFailed:
				paint.FillShape(gtx.Ops, mustRGB("2a1212"), clip.Rect{Max: content.Size}.Op())
			}
			call.Add(gtx.Ops)

			// Enforce a minimum row height.
			if content.Size.Y < rowH {
				content.Size.Y = rowH
			}
			return content
		})
	}
}

// queueRowGlyph draws a small coloured circle whose colour encodes the item
// state. Glyph text rendering is left as future polish; the dot colour carries
// enough state information today.
func queueRowGlyph(it queueItem) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		var c = pending
		switch it.State {
		case qReady:
			c = pending
		case qRunning:
			c = accentFg
		case qDone:
			c = good
		case qFailed:
			c = bad
		case qSkipped, qCancelled:
			c = text3
		}
		size := gtx.Dp(unit.Dp(12))
		paint.FillShape(gtx.Ops, c, clip.Ellipse{Max: image.Pt(size, size)}.Op(gtx.Ops))
		return layout.Dimensions{Size: image.Pt(size, size)}
	}
}

// queueRowSuffix renders the trailing annotation for terminal states.
// Returns zero dimensions for ready/running rows (no suffix).
func queueRowSuffix(th *material.Theme, it queueItem) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		var label string
		var col = text3
		switch it.State {
		case qDone:
			label = fmt.Sprintf("%.1fs", float64(it.DurationMs)/1000)
			col = good
		case qFailed:
			label = "fail"
			col = bad
		case qSkipped:
			label = it.Reason
		case qCancelled:
			label = "cancelled"
		default:
			return layout.Dimensions{}
		}
		lb := material.Label(th, unit.Sp(11), label)
		lb.Color = col
		return lb.Layout(gtx)
	}
}

// queueRowActionBtn renders the × (remove ready) or ⏹ (cancel running) button.
// Returns zero dimensions for all other states.
func queueRowActionBtn(th *material.Theme, it queueItem, click *widget.Clickable) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		var label string
		switch it.State {
		case qReady:
			label = "×"
		case qRunning:
			label = "⏹"
		default:
			return layout.Dimensions{}
		}
		btn := material.Button(th, click, label)
		btn.Background = bg
		btn.Color = text3
		btn.CornerRadius = unit.Dp(3)
		btn.TextSize = unit.Sp(11)
		btn.Inset = layout.Inset{Top: 2, Bottom: 2, Left: 6, Right: 6}
		return btn.Layout(gtx)
	}
}
