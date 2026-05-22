package main

import (
	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

func redumpPlaintextCautionText() string {
	return "Caution: Redump credentials are stored in plaintext in the local SQLite database."
}

func redumpView(th *material.Theme, mdl *model, user, pass *widget.Editor, save, test, clear *widget.Clickable) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 28, Left: 32, Right: 32}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(20), "Redump")
					l.Color = text1
					return l.Layout(gtx)
				}),
				layout.Rigid(spacer(0, 16)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(12), "Username")
					l.Color = text2
					return l.Layout(gtx)
				}),
				layout.Rigid(editorField(th, user, "Redump username")),
				layout.Rigid(spacer(0, 10)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(12), "Password")
					l.Color = text2
					return l.Layout(gtx)
				}),
				layout.Rigid(editorField(th, pass, "Redump password")),
				layout.Rigid(spacer(0, 12)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(panelButton(th, save, "Save credentials")),
						layout.Rigid(spacer(8, 0)),
						layout.Rigid(panelButton(th, test, "Test login")),
						layout.Rigid(spacer(8, 0)),
						layout.Rigid(panelButton(th, clear, "Clear credentials")),
					)
				}),
				layout.Rigid(spacer(0, 12)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(12), mdl.redumpStatus)
					l.Color = text2
					return l.Layout(gtx)
				}),
				layout.Rigid(spacer(0, 8)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := material.Label(th, unit.Sp(10), redumpPlaintextCautionText())
					l.Color = text3
					l.Alignment = text.Start
					return l.Layout(gtx)
				}),
			)
		})
	}
}

func editorField(th *material.Theme, ed *widget.Editor, hint string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 6, Bottom: 6, Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			st := material.Editor(th, ed, hint)
			st.Color = text1
			st.HintColor = text3
			st.TextSize = unit.Sp(13)
			return st.Layout(gtx)
		})
	}
}
