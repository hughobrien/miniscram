// tools/miniscram-gui/widgets.go
package main

import (
	"fmt"
	"path/filepath"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// runningStripWidget renders the running-state strip just under the top
// bar's divider when an action is in flight. Returns zero dimensions
// when state is nil so the layout collapses.
func runningStripWidget(th *material.Theme, state *runningState, cancelBtn *widget.Clickable) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if state == nil {
			return layout.Dimensions{}
		}
		actionVerb := map[string]string{
			"pack":   "Packing",
			"unpack": "Unpacking",
			"verify": "Verifying",
		}[state.Action]
		if actionVerb == "" {
			actionVerb = "Running"
		}
		basename := filepath.Base(state.Input)
		elapsed := time.Since(state.StartedAt).Truncate(time.Second)
		stepText := state.LastLine
		if stepText == "" {
			stepText = "Starting…"
		}
		cancelLabel := "Cancel"
		if state.Cancelling {
			cancelLabel = "Cancelling…"
		}

		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(24), Right: unit.Dp(24)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lb := material.Label(th, unit.Sp(13), actionVerb+" "+basename)
						lb.Color = text1
						lb.Font.Weight = font.SemiBold
						return lb.Layout(gtx)
					}),
					layout.Rigid(spacer(12, 0)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						lb := material.Label(th, unit.Sp(12), stepText)
						lb.Color = text2
						lb.Font.Typeface = "Go Mono"
						return lb.Layout(gtx)
					}),
					layout.Rigid(spacer(12, 0)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lb := material.Label(th, unit.Sp(12), fmt.Sprintf("%ds", int(elapsed.Seconds())))
						lb.Color = text3
						lb.Font.Typeface = "Go Mono"
						return lb.Layout(gtx)
					}),
					layout.Rigid(spacer(12, 0)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if state.Cancelling {
							gtx = gtx.Disabled()
						}
						btn := material.Button(th, cancelBtn, cancelLabel)
						btn.Background = surface2
						btn.Color = text1
						btn.CornerRadius = unit.Dp(4)
						btn.TextSize = unit.Sp(12)
						btn.Inset = layout.Inset{Top: 5, Bottom: 5, Left: 12, Right: 12}
						return btn.Layout(gtx)
					}),
				)
			})
		call := macro.Stop()
		bg := mustRGB("13262d")
		paint.FillShape(gtx.Ops, bg, clip.Rect{Max: dims.Size}.Op())
		call.Add(gtx.Ops)
		// Re-draw on every animation frame so the elapsed counter ticks.
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(200 * time.Millisecond)})
		return dims
	}
}
