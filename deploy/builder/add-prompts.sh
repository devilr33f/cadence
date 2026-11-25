#!/usr/bin/env bash
# add-prompts.sh <in> <tree>: copies the owner's rendered spoken prompts (cadence-server admin prompts render, never
# committed) from <in>/<lang>/<id>.opus into <tree>/share/prompts/<lang>/<id>.opus, mode 644. none is fine: audiod
# plays its error earcon for a prompt it lacks. a name that is no prompt's ([a-z]{2,3}/[a-z_]{1,32}.opus), anything
# but a regular file or one over 256 KiB (audiod's cap) is refused, and so is any other file
set -euo pipefail
export LC_ALL=C

[ "$#" -eq 2 ] || { echo "usage: deploy/builder/add-prompts.sh <in> <tree>" >&2; exit 2; }
in=${1%/}
tree=${2%/}
max=$((256 * 1024))

[ -d "$in" ] || { echo "add-prompts.sh: no prompts" >&2; exit 0; }
count=0
while IFS= read -r -d '' path; do
  rel=${path#"$in"/}
  if [[ ! $rel =~ ^[a-z]{2,3}/[a-z_]{1,32}\.opus$ ]]; then
    echo "add-prompts.sh: $rel is not a prompt (<lang>/<id>.opus)" >&2
    exit 1
  fi
  if [ -L "$path" ] || [ ! -f "$path" ]; then
    echo "add-prompts.sh: $rel is not a regular file" >&2
    exit 1
  fi
  size=$(wc -c <"$path")
  if [ "$size" -eq 0 ] || [ "$size" -gt "$max" ]; then
    echo "add-prompts.sh: $rel is $size bytes, not 1 to $max" >&2
    exit 1
  fi
  mkdir -p "$tree/share/prompts/${rel%/*}"
  install -m 644 "$path" "$tree/share/prompts/$rel"
  count=$((count + 1))
done < <(find "$in" -mindepth 1 \( -type f -o -type l \) -print0 | sort -z)
echo "add-prompts.sh: $count prompts"
