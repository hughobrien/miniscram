#! /usr/bin/env nix-shell
#! nix-shell -i zsh -p mame-tools

set -xeuo pipefail

base="${1:r}"

nix run github:hughobrien/miniscram -- pack "${base}.cue"

chdman createcd -i "${base}.cue" -o "${base}.chd"
rm -fv "${base}.cue" "${base}"*.bin

zstd -T0v19e --rm "${base}.state"
zstd -T0v19e --rm "${base}.subcode"
