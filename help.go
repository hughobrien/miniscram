// /home/hugh/miniscram/help.go
package main

import (
	"fmt"
	"io"
)

func printTopHelp(w io.Writer) {
	fmt.Fprint(w, topHelpText)
}

func printPackHelp(w io.Writer) {
	fmt.Fprint(w, packHelpText)
}

func printUnpackHelp(w io.Writer) {
	fmt.Fprint(w, unpackHelpText)
}

const topHelpText = `miniscram — compactly preserve scrambled CD-ROM dumps alongside .bin images.

USAGE:
    miniscram <command> [arguments] [options]
    miniscram <command> --help
    miniscram --version

COMMANDS:
    pack       pack a .scram into a compact .miniscram container
    unpack     reproduce a .scram from .bin + .miniscram
    help       show this help, or 'miniscram help <command>'

REQUIRES:
    xdelta3 binary on PATH (e.g. apt install xdelta3)

EXIT CODES:
    0    success
    1    usage / input error
    2    layout mismatch
    3    xdelta3 failed
    4    verification failed
    5    I/O error
    6    wrong .bin for this .miniscram
`

const packHelpText = `USAGE:
    miniscram pack [<bin> <cue> <scram>] [-o <out.miniscram>] [options]

ARGUMENTS (optional — discovered from cwd if omitted):
    <bin>      path to the unscrambled CD image (Redumper *.bin)
    <cue>      path to the cue sheet (Redumper *.cue)
    <scram>    path to the scrambled intermediate dump (Redumper *.scram)

OPTIONS:
    -o, --output <path>    where to write the .miniscram container.
                           default: <bin-stem>.miniscram next to <bin>.
    -f, --force            overwrite existing output.
    --keep-source          do not remove <scram> after verified pack.
    --no-verify            skip inline round-trip verification.
                           implies --keep-source.
    --allow-cross-fs       permit auto-delete of <scram> when <out>
                           is on a different filesystem.
    -q, --quiet            suppress progress output.
    -h, --help             show this help.
`

const unpackHelpText = `USAGE:
    miniscram unpack [<bin> <in.miniscram>] [-o <out.scram>] [options]

ARGUMENTS (optional — discovered from cwd if omitted):
    <bin>             path to the unscrambled CD image (Redumper *.bin)
    <in.miniscram>    .miniscram container produced by 'miniscram pack'

OPTIONS:
    -o, --output <path>    where to write the reconstructed .scram.
                           default: <miniscram-stem>.scram next to
                           <in.miniscram>.
    -f, --force            overwrite existing output.
    --no-verify            skip output sha256 verification.
    -q, --quiet            suppress progress output.
    -h, --help             show this help.
`
