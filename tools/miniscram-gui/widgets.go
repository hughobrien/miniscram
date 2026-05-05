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

// toastState is set by handleActionResult on a successful action.
// The widget hides itself when ExpiresAt < now or Hide is true.
type toastState struct {
	Action     string // "pack" | "unpack" | "verify"
	Output     string // path to the output file; "" for verify
	OutputSize int64
	DurationMs int64
	ExpiresAt  time.Time
	Hide       bool // set when user clicks the ✕
}

func toastWidget(th *material.Theme, ts *toastState, dismissBtn, revealBtn *widget.Clickable) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if ts == nil || ts.Hide || time.Now().After(ts.ExpiresAt) {
			return layout.Dimensions{}
		}
		verb := map[string]string{
			"pack":   "Packed",
			"unpack": "Unpacked",
			"verify": "Verified",
		}[ts.Action]
		if verb == "" {
			verb = "Done"
		}
		basename := filepath.Base(ts.Output)
		if basename == "." || basename == "" {
			basename = ts.Action + " complete"
		}
		summary := verb + "  " + basename
		if ts.OutputSize > 0 {
			summary += "  ·  " + humanBytes(ts.OutputSize)
		}
		summary += "  ·  " + fmt.Sprintf("%.1fs", float64(ts.DurationMs)/1000)

		macro := op.Record(gtx.Ops)
		dims := layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(24), Right: unit.Dp(24)}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return statusDot(gtx, good)
					}),
					layout.Rigid(spacer(10, 0)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						lb := material.Label(th, unit.Sp(13), summary)
						lb.Color = text1
						return lb.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if ts.Output == "" {
							return layout.Dimensions{}
						}
						btn := material.Button(th, revealBtn, "Reveal in folder")
						btn.Background = surface2
						btn.Color = text2
						btn.CornerRadius = unit.Dp(4)
						btn.TextSize = unit.Sp(11)
						btn.Inset = layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 10}
						return btn.Layout(gtx)
					}),
					layout.Rigid(spacer(8, 0)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						btn := material.Button(th, dismissBtn, "✕")
						btn.Background = bg
						btn.Color = text3
						btn.CornerRadius = unit.Dp(4)
						btn.TextSize = unit.Sp(13)
						btn.Inset = layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}
						return btn.Layout(gtx)
					}),
				)
			})
		call := macro.Stop()
		paint.FillShape(gtx.Ops, mustRGB("17392d"), clip.Rect{Max: dims.Size}.Op())
		call.Add(gtx.Ops)
		// Tick at 250ms so the toast self-expires within ~the second it should.
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(250 * time.Millisecond)})
		return dims
	}
}
