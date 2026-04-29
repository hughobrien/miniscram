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
		printHelp(stderr, topHelpText)
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
				printHelp(stderr, packHelpText)
				return exitOK
			case "unpack":
				printHelp(stderr, unpackHelpText)
				return exitOK
			case "verify":
				printHelp(stderr, verifyHelpText)
				return exitOK
			case "inspect":
				printHelp(stderr, inspectHelpText)
				return exitOK
			}
		}
		printHelp(stderr, topHelpText)
		return exitOK
	case "--version":
		fmt.Fprintln(stderr, toolVersion)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printHelp(stderr, topHelpText)
		return exitUsage
	}
}

// commonFlags is the set of flags every subcommand shares.
type commonFlags struct {
	quiet bool
}

// parseSubcommand registers help + quiet flags, parses args, and
// handles the help/usage exit code logic. The caller passes a
// configure callback to register subcommand-specific flags.
//
// Returns the positional args (caller checks NArg requirements) and
// the parsed common flags.
//
// If parsing failed or help was requested, returns (nil, _, exitCode,
// false) and the caller should return exitCode immediately. Otherwise
// returns (positional, flags, 0, true).
func parseSubcommand(name, helpText string, args []string, stderr io.Writer, configure func(*flag.FlagSet)) ([]string, commonFlags, int, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	quiet := fs.Bool("q", false, "quiet")
	quietLong := fs.Bool("quiet", false, "quiet")
	help := fs.Bool("h", false, "help")
	helpLong := fs.Bool("help", false, "help")
	if configure != nil {
		configure(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, commonFlags{}, exitUsage, false
	}
	if *help || *helpLong {
		fmt.Fprint(stderr, helpText)
		return nil, commonFlags{}, exitOK, false
	}
	return fs.Args(), commonFlags{quiet: *quiet || *quietLong}, 0, true
}

// requireOnePositional asserts exactly one positional and prints a
// usage error otherwise.
func requireOnePositional(stderr io.Writer, helpText string, positional []string, label string) bool {
	if len(positional) != 1 {
		fmt.Fprintf(stderr, "expected exactly one %s; got %d\n", label, len(positional))
		fmt.Fprint(stderr, helpText)
		return false
	}
	return true
}

func runPack(args []string, stderr io.Writer) int {
	var output, outputLong string
	var keepSource, noVerify, allowCrossFS, force, forceLong bool
	positional, common, exit, ok := parseSubcommand("pack", packHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&output, "o", "", "output path")
		fs.StringVar(&outputLong, "output", "", "output path")
		fs.BoolVar(&keepSource, "keep-source", false, "keep .scram after verified pack")
		fs.BoolVar(&noVerify, "no-verify", false, "skip round-trip verification")
		fs.BoolVar(&allowCrossFS, "allow-cross-fs", false, "permit auto-delete across filesystems")
		fs.BoolVar(&force, "f", false, "overwrite output")
		fs.BoolVar(&forceLong, "force", false, "overwrite output")
	})
	if !ok {
		return exit
	}
	if !requireOnePositional(stderr, packHelpText, positional, "positional argument (cue path)") {
		return exitUsage
	}
	cuePath := positional[0]
	scramPath := strings.TrimSuffix(cuePath, filepath.Ext(cuePath)) + ".scram"
	out := pickFirst(output, outputLong)
	if out == "" {
		out = DefaultPackOutput(cuePath)
	}
	beForce := force || forceLong
	if !beForce {
		if _, err := os.Stat(out); err == nil {
			fmt.Fprintf(stderr, "output %s already exists (pass -f / --force to overwrite)\n", out)
			return exitUsage
		}
	}
	noVerifyImpliesKeep := noVerify && !keepSource
	if noVerify {
		keepSource = true
	}
	rep := NewReporter(stderr, common.quiet)
	if noVerifyImpliesKeep {
		rep.Info("--no-verify implies --keep-source; original .scram will be kept")
	}
	err := Pack(PackOptions{
		CuePath: cuePath, ScramPath: scramPath, OutputPath: out,
		LeadinLBA: LBALeadinStart, Verify: !noVerify,
	}, rep)
	if err != nil {
		return errToExit(err)
	}
	if !keepSource {
		if removed, removeErr := maybeRemoveSource(scramPath, out, allowCrossFS, rep); removeErr != nil {
			rep.Warn("source removal skipped: %v", removeErr)
		} else if removed {
			rep.Info("removed source %s", scramPath)
		}
	}
	return exitOK
}

func runUnpack(args []string, stderr io.Writer) int {
	var output, outputLong string
	var noVerify, force, forceLong bool
	positional, common, exit, ok := parseSubcommand("unpack", unpackHelpText, args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&output, "o", "", "output path")
		fs.StringVar(&outputLong, "output", "", "output path")
		fs.BoolVar(&noVerify, "no-verify", false, "skip output hash verification")
		fs.BoolVar(&force, "f", false, "overwrite output")
		fs.BoolVar(&forceLong, "force", false, "overwrite output")
	})
	if !ok {
		return exit
	}
	if !requireOnePositional(stderr, unpackHelpText, positional, "positional argument (container path)") {
		return exitUsage
	}
	containerPath := positional[0]
	out := pickFirst(output, outputLong)
	if out == "" {
		out = DefaultUnpackOutput(containerPath)
	}
	rep := NewReporter(stderr, common.quiet)
	if err := Unpack(UnpackOptions{
		ContainerPath: containerPath, OutputPath: out,
		Verify: !noVerify, Force: force || forceLong,
	}, rep); err != nil {
		return errToExit(err)
	}
	return exitOK
}

func runVerify(args []string, stderr io.Writer) int {
	positional, common, exit, ok := parseSubcommand("verify", verifyHelpText, args, stderr, nil)
	if !ok {
		return exit
	}
	if !requireOnePositional(stderr, verifyHelpText, positional, "positional argument (container path)") {
		return exitUsage
	}
	rep := NewReporter(stderr, common.quiet)
	if err := Verify(VerifyOptions{ContainerPath: positional[0]}, rep); err != nil {
		return errToExit(err)
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
	sb, errB := os.Stat(filepath.Dir(b))
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

func errToExit(err error) int {
	if err == nil {
		return exitOK
	}
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
