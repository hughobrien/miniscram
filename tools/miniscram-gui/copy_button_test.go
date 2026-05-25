package main

import (
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
)

func TestHandleCopyClicksWritesCurrentValue(t *testing.T) {
	var ops op.Ops
	gtx := layout.Context{Ops: &ops}
	entry := &copyEntry{click: new(widget.Clickable), value: "abc123"}
	entry.click.Click()

	var copied []string
	handleCopyClicks(gtx, map[string]*copyEntry{"track": entry}, func(_ layout.Context, value string) {
		copied = append(copied, value)
	})

	if len(copied) != 1 || copied[0] != "abc123" {
		t.Fatalf("copied = %v, want [abc123]", copied)
	}
}

func TestHandleCopyClicksIgnoresEmptyValues(t *testing.T) {
	var ops op.Ops
	gtx := layout.Context{Ops: &ops}
	entry := &copyEntry{click: new(widget.Clickable)}
	entry.click.Click()

	var copied []string
	handleCopyClicks(gtx, map[string]*copyEntry{"track": entry}, func(_ layout.Context, value string) {
		copied = append(copied, value)
	})

	if len(copied) != 0 {
		t.Fatalf("copied = %v, want none", copied)
	}
}
