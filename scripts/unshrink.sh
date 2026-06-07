#! /usr/bin/env nix-shell
#! nix-shell -i zsh -p mame-tools redumper

set -xeuo pipefail

base="${1:r}"

# chd gets you the bin & cue
chdman extractcd -i "${base}.chd" -o "${base}.cue"
rm -fv "${base}.chd"

# bin & cue & miniscram get you the scram
nix run github:hughobrien/miniscram -- unpack "${base}.miniscram"
rm -fv "${base}.miniscram"
rm -fv "${base}.cue" "${base}"*.bin

# uncomp the state and subcode
zstd -dv --rm "${base}.state.zst"
zstd -dv --rm "${base}.subcode.zst"

# toc should be already present

# split uses scram, state, subcode, toc to generate original cue and bins
cp "${base}.log" "${base}.log.orig"
redumper split --force-split --image-name="$base"
mv "${base}.log.orig" "${base}.log"
