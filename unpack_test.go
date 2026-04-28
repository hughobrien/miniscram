// /home/hugh/miniscram/unpack_test.go
package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestUnpackRoundTripSynthDisc(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, -48, 10)
	containerPath := filepath.Join(dir, "x.miniscram")
	rep := NewReporter(io.Discard, true)
	if err := Pack(PackOptions{
		BinPath: binPath, CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, rep); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "x.scram.recovered")
	if err := Unpack(UnpackOptions{
		BinPath: binPath, ContainerPath: containerPath,
		OutputPath: outPath, Verify: true, Force: false,
	}, rep); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(scramPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip differs (got %d bytes, want %d)", len(got), len(want))
	}
}

func TestUnpackRejectsWrongBin(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	containerPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		BinPath: binPath, CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	wrongBin := filepath.Join(dir, "wrong.bin")
	if err := os.WriteFile(wrongBin, []byte("not the right bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "x.scram.recovered")
	err := Unpack(UnpackOptions{
		BinPath: wrongBin, ContainerPath: containerPath,
		OutputPath: outPath, Verify: true,
	}, NewReporter(io.Discard, true))
	if err == nil {
		t.Fatal("expected error with wrong bin")
	}
}

func TestUnpackRefusesOverwrite(t *testing.T) {
	binPath, cuePath, scramPath, dir := writeSynthDiscFiles(t, 100, 0, 10)
	containerPath := filepath.Join(dir, "x.miniscram")
	if err := Pack(PackOptions{
		BinPath: binPath, CuePath: cuePath, ScramPath: scramPath,
		OutputPath: containerPath, LeadinLBA: LBAPregapStart, Verify: true,
	}, NewReporter(io.Discard, true)); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "exists.scram")
	if err := os.WriteFile(outPath, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Unpack(UnpackOptions{
		BinPath: binPath, ContainerPath: containerPath,
		OutputPath: outPath, Verify: true, Force: false,
	}, NewReporter(io.Discard, true))
	if err == nil {
		t.Fatal("expected error refusing to overwrite")
	}
}
