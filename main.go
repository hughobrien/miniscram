// /home/hugh/miniscram/main.go
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

// Exit codes match the spec.
const (
	exitOK         = 0
	exitUsage      = 1
	exitLayout     = 2
	exitXDelta     = 3
	exitVerifyFail = 4
	exitIO         = 5
	exitWrongBin   = 6
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		printTopHelp(stderr)
		return exitUsage
	}
	switch args[0] {
	case "pack":
		return runPack(args[1:], stderr)
	case "unpack":
		return runUnpack(args[1:], stderr)
	case "help", "--help", "-h":
		if len(args) >= 2 {
			switch args[1] {
			case "pack":
				printPackHelp(stderr)
				return exitOK
			case "unpack":
				printUnpackHelp(stderr)
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
	out := pickFirst(*output, *outputLong)
	beQuiet := *quiet || *quietLong
	beForce := *force || *forceLong

	in, err := resolvePackInputs(fs.Args())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if out == "" {
		out = DefaultPackOutput(in.Bin)
	}
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
	err = Pack(PackOptions{
		BinPath: in.Bin, CuePath: in.Cue, ScramPath: in.Scram,
		OutputPath: out, Verify: !*noVerify,
	}, rep)
	if err != nil {
		return packErrorToExit(err)
	}
	if !*keepSource {
		if removed, removeErr := maybeRemoveSource(in.Scram, out, *allowCrossFS, rep); removeErr != nil {
			rep.Warn("source removal skipped: %v", removeErr)
		} else if removed {
			rep.Info("removed source %s", in.Scram)
		}
	}
	return exitOK
}

func runUnpack(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("unpack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	output := fs.String("o", "", "output path")
	outputLong := fs.String("output", "", "output path")
	noVerify := fs.Bool("no-verify", false, "skip output sha256 verification")
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
	out := pickFirst(*output, *outputLong)
	beQuiet := *quiet || *quietLong
	beForce := *force || *forceLong

	in, err := resolveUnpackInputs(fs.Args())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if out == "" {
		out = DefaultUnpackOutput(in.Container)
	}
	if !beForce {
		if _, err := os.Stat(out); err == nil {
			fmt.Fprintf(stderr, "output %s already exists (pass -f / --force to overwrite)\n", out)
			return exitUsage
		}
	}
	rep := NewReporter(stderr, beQuiet)
	err = Unpack(UnpackOptions{
		BinPath: in.Bin, ContainerPath: in.Container,
		OutputPath: out, Verify: !*noVerify, Force: beForce,
	}, rep)
	if err != nil {
		return unpackErrorToExit(err)
	}
	return exitOK
}

func resolvePackInputs(positional []string) (PackInputs, error) {
	switch len(positional) {
	case 0:
		cwd, err := os.Getwd()
		if err != nil {
			return PackInputs{}, err
		}
		return DiscoverPack(cwd)
	case 1:
		return DiscoverPackFromArg(positional[0])
	case 3:
		return PackInputs{Bin: positional[0], Cue: positional[1], Scram: positional[2]}, nil
	default:
		return PackInputs{}, fmt.Errorf("expected 0, 1, or 3 positional arguments to pack; got %d", len(positional))
	}
}

func resolveUnpackInputs(positional []string) (UnpackInputs, error) {
	switch len(positional) {
	case 0:
		cwd, err := os.Getwd()
		if err != nil {
			return UnpackInputs{}, err
		}
		return DiscoverUnpack(cwd)
	case 1:
		return DiscoverUnpackFromArg(positional[0])
	case 2:
		return UnpackInputs{Bin: positional[0], Container: positional[1]}, nil
	default:
		return UnpackInputs{}, fmt.Errorf("expected 0, 1, or 2 positional arguments to unpack; got %d", len(positional))
	}
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

func packErrorToExit(err error) int {
	var lme *LayoutMismatchError
	switch {
	case errors.As(err, &lme):
		return exitLayout
	case errors.Is(err, errBinSHA256Mismatch):
		return exitWrongBin
	case errors.Is(err, errVerifyMismatch),
		errors.Is(err, errOutputSHA256Mismatch):
		return exitVerifyFail
	case strings.Contains(err.Error(), "xdelta3"):
		return exitXDelta
	default:
		return exitIO
	}
}

func unpackErrorToExit(err error) int {
	switch {
	case errors.Is(err, errBinSHA256Mismatch):
		return exitWrongBin
	case errors.Is(err, errOutputSHA256Mismatch):
		return exitVerifyFail
	case strings.Contains(err.Error(), "xdelta3"):
		return exitXDelta
	default:
		return exitIO
	}
}
