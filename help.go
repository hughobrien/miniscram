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

func printVerifyHelp(w io.Writer) {
	fmt.Fprint(w, verifyHelpText)
}

func printInspectHelp(w io.Writer) {
	fmt.Fprint(w, inspectHelpText)
}

const topHelpText = `miniscram — compactly preserve scrambled CD-ROM dumps alongside .bin images.

USAGE:
    miniscram <command> [arguments] [options]
    miniscram <command> --help
    miniscram --version

COMMANDS:
    pack       pack a .scram into a compact .miniscram container
    unpack     reproduce a .scram from .bin + .miniscram
    verify     non-destructive integrity check of a .miniscram
    inspect    pretty-print a .miniscram container (read-only)
    help       show this help, or 'miniscram help <command>'

ABOUT:
    miniscram stores the bytes of a .scram (Redumper's scrambled
    intermediate CD-ROM dump) as a small structured delta against the
    unscrambled .bin final dump. With this tool and the .bin, you
    can reproduce the original .scram byte-for-byte. Implements the
    method from Hauenstein, "Compact Preservation of Scrambled CD-ROM
    Data" (IJCSIT, August 2022), specialised for Redumper output.

EXIT CODES:
    0    success
    1    usage / input error
    2    layout mismatch
    3    verification failed
    4    I/O error
    5    wrong .bin for this .miniscram
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
    --no-verify            skip output hash verification (md5/sha1/sha256).
    -q, --quiet            suppress progress output.
    -h, --help             show this help.
`

const verifyHelpText = `USAGE:
    miniscram verify [<bin> <in.miniscram>] [options]

ARGUMENTS (optional — discovered from cwd if omitted):
    <bin>             path to the unscrambled CD image (Redumper *.bin)
    <in.miniscram>    .miniscram container produced by 'miniscram pack'

OPTIONS:
    -q, --quiet       suppress progress output.
    -h, --help        show this help.

DESCRIPTION:
    Rebuilds the original .scram in a temporary file, hashes it
    (md5 + sha1 + sha256), compares against the container's recorded
    hashes, and deletes the temporary file. Used to confirm a
    .miniscram still decodes correctly without producing a
    multi-hundred-MB output.

EXIT CODES:
    0    success
    1    usage / input error
    3    verification failed (one or more of md5/sha1/sha256 mismatched)
    4    I/O error
    5    wrong .bin (one or more recorded hashes mismatched)
`

const inspectHelpText = `USAGE:
    miniscram inspect [--full] [--json] <container>

ARGUMENTS:
    <container>    path to a .miniscram file

OPTIONS:
    --full         append a per-record listing of every override
                   (no cap). without it, only the override count
                   is printed.
    --json         emit machine-readable JSON: the manifest verbatim
                   plus a delta_records array. always includes all
                   records.
    -h, --help     show this help.

EXIT CODES:
    0    success
    1    usage error (wrong number of positionals, bad flags)
    4    I/O or container parse error
`
