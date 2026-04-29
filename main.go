// /home/hugh/miniscram/main.go
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Exit codes match the spec.
const (
	exitOK         = 0
	exitUsage      = 1
	exitLayout     = 2
	exitVerifyFail = 3
	exitIO         = 4
	exitWrongBin   = 5
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printTopHelp(stderr)
		return exitUsage
	}
	switch args[0] {
	case "pack":
		return runPack(args[1:], stderr)
	case "unpack":
		return runUnpack(args[1:], stderr)
	case "verify":
		return runVerify(args[1:], stderr)
	case "inspect":
		return runInspect(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		if len(args) >= 2 {
			switch args[1] {
			case "pack":
				printPackHelp(stderr)
				return exitOK
			case "unpack":
				printUnpackHelp(stderr)
				return exitOK
			case "verify":
				printVerifyHelp(stderr)
				return exitOK
			case "inspect":
				printInspectHelp(stderr)
				return exitOK
			}
		}
		printTopHelp(stderr)
		return exitOK
	case "--version":
		fmt.Fprintln(stderr, toolVersion)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printTopHelp(stderr)
		return exitUsage
	}
}

func runPack(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	output := fs.String("o", "", "output path (alias --output)")
	outputLong := fs.String("output", "", "output path")
	keepSource := fs.Bool("keep-source", false, "do not remove .scram after verification")
	noVerify := fs.Bool("no-verify", false, "skip inline round-trip verification")
	allowCrossFS := fs.Bool("allow-cross-fs", false, "allow auto-delete across filesystems")
	force := fs.Bool("f", false, "overwrite output if it exists (alias --force)")
	forceLong := fs.Bool("force", false, "overwrite output if it exists")
	quiet := fs.Bool("q", false, "suppress progress (alias --quiet)")
	quietLong := fs.Bool("quiet", false, "suppress progress")
	help := fs.Bool("help", false, "show help for pack")
	helpShort := fs.Bool("h", false, "show help for pack")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *help || *helpShort {
		printPackHelp(stderr)
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "expected exactly one positional argument (cue path); got %d\n", fs.NArg())
		printPackHelp(stderr)
		return exitUsage
	}
	cuePath := fs.Arg(0)
	scramPath := strings.TrimSuffix(cuePath, filepath.Ext(cuePath)) + ".scram"
	out := pickFirst(*output, *outputLong)
	if out == "" {
		out = DefaultPackOutput(cuePath)
	}
	beQuiet := *quiet || *quietLong
	beForce := *force || *forceLong

	if !beForce {
		if _, err := os.Stat(out); err == nil {
			fmt.Fprintf(stderr, "output %s already exists (pass -f / --force to overwrite)\n", out)
			return exitUsage
		}
	}
	noVerifyImpliesKeep := *noVerify && !*keepSource
	if *noVerify {
		*keepSource = true
	}
	rep := NewReporter(stderr, beQuiet)
	if noVerifyImpliesKeep {
		rep.Info("--no-verify implies --keep-source; original .scram will be kept")
	}
	err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath, OutputPath: out,
		LeadinLBA: LBALeadinStart,
		Verify:    !*noVerify,
	}, rep)
	if err != nil {
		return packErrorToExit(err)
	}
	if !*keepSource {
		if removed, removeErr := maybeRemoveSource(scramPath, out, *allowCrossFS, rep); removeErr != nil {
			rep.Warn("source removal skipped: %v", removeErr)
		} else if removed {
			rep.Info("removed source %s", scramPath)
		}
	}
	return exitOK
}

func runUnpack(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("unpack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	output := fs.String("o", "", "output path")
	outputLong := fs.String("output", "", "output path")
	noVerify := fs.Bool("no-verify", false, "skip output hash verification")
	force := fs.Bool("f", false, "overwrite output")
	forceLong := fs.Bool("force", false, "overwrite output")
	quiet := fs.Bool("q", false, "quiet")
	quietLong := fs.Bool("quiet", false, "quiet")
	help := fs.Bool("help", false, "show help for unpack")
	helpShort := fs.Bool("h", false, "show help for unpack")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *help || *helpShort {
		printUnpackHelp(stderr)
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "expected exactly one positional argument (container path); got %d\n", fs.NArg())
		printUnpackHelp(stderr)
		return exitUsage
	}
	containerPath := fs.Arg(0)
	out := pickFirst(*output, *outputLong)
	if out == "" {
		out = DefaultUnpackOutput(containerPath)
	}
	beQuiet := *quiet || *quietLong
	beForce := *force || *forceLong

	rep := NewReporter(stderr, beQuiet)
	err := Unpack(UnpackOptions{
		ContainerPath: containerPath, OutputPath: out,
		Verify: !*noVerify, Force: beForce,
	}, rep)
	if err != nil {
		return unpackErrorToExit(err)
	}
	return exitOK
}

func runVerify(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	quiet := fs.Bool("q", false, "quiet")
	quietLong := fs.Bool("quiet", false, "quiet")
	help := fs.Bool("help", false, "show help for verify")
	helpShort := fs.Bool("h", false, "show help for verify")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *help || *helpShort {
		printVerifyHelp(stderr)
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "expected exactly one positional argument (container path); got %d\n", fs.NArg())
		printVerifyHelp(stderr)
		return exitUsage
	}
	containerPath := fs.Arg(0)
	beQuiet := *quiet || *quietLong
	rep := NewReporter(stderr, beQuiet)
	if err := Verify(VerifyOptions{ContainerPath: containerPath}, rep); err != nil {
		return verifyErrorToExit(err)
	}
	return exitOK
}

func maybeRemoveSource(scramPath, outPath string, allowCrossFS bool, r Reporter) (bool, error) {
	if !sameFilesystem(scramPath, outPath) && !allowCrossFS {
		return false, fmt.Errorf("output %s is on a different filesystem from %s; pass --allow-cross-fs to permit auto-delete",
			outPath, scramPath)
	}
	if err := os.Remove(scramPath); err != nil {
		return false, err
	}
	return true, nil
}

func sameFilesystem(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(filepathDir(b))
	if errA != nil || errB != nil {
		return false
	}
	stA, okA := sa.Sys().(*syscall.Stat_t)
	stB, okB := sb.Sys().(*syscall.Stat_t)
	if !okA || !okB {
		return false
	}
	return stA.Dev == stB.Dev
}

func filepathDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i+1]
	}
	return "."
}

func pickFirst(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func DefaultPackOutput(cuePath string) string {
	return strings.TrimSuffix(cuePath, filepath.Ext(cuePath)) + ".miniscram"
}

func DefaultUnpackOutput(containerPath string) string {
	return strings.TrimSuffix(containerPath, filepath.Ext(containerPath)) + ".scram"
}

func packErrorToExit(err error) int {
	var lme *LayoutMismatchError
	switch {
	case errors.As(err, &lme):
		return exitLayout
	case errors.Is(err, errBinHashMismatch):
		return exitWrongBin
	case errors.Is(err, errVerifyMismatch),
		errors.Is(err, errOutputHashMismatch):
		return exitVerifyFail
	default:
		return exitIO
	}
}

func unpackErrorToExit(err error) int {
	switch {
	case errors.Is(err, errBinHashMismatch):
		return exitWrongBin
	case errors.Is(err, errOutputHashMismatch):
		return exitVerifyFail
	default:
		return exitIO
	}
}

func verifyErrorToExit(err error) int {
	switch {
	case errors.Is(err, errBinHashMismatch):
		return exitWrongBin
	case errors.Is(err, errOutputHashMismatch):
		return exitVerifyFail
	default:
		return exitIO
	}
}
