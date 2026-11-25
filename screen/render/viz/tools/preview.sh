#!/bin/sh
# a visualizer source as a video with its sound, rendered offline from a track (host/bench/viz-render.cpp): the loop
# for judging a look before it reaches a screen. the track and the video stay local; nothing here is committed
#
# usage: preview.sh <viz-render> <audio file> <source id> <out.mp4> [seconds] [start] [cover image]
# with PLOT=<python with matplotlib>, also <out>.png: the analysis per frame as a timeline (tools/plot-features.py)
set -eu
render=$1 audio=$2 source=$3 out=$4 seconds=${5:-20} start=${6:-0} cover=${7:-}
size=960x540 fps=60
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
ffmpeg -loglevel error -y -i "$audio" -ac 2 -ar 48000 -f f32le "$tmp/pcm.f32"
art=""
if [ -n "$cover" ]; then
  ffmpeg -loglevel error -y -i "$cover" -vf "scale=256:256:force_original_aspect_ratio=increase,crop=256:256" \
    -f rawvideo -pix_fmt rgba "$tmp/art.rgba"
  art="--art $tmp/art.rgba --art-size 256x256"
fi
# shellcheck disable=SC2086
"$render" --pcm "$tmp/pcm.f32" --source "$source" --seconds "$seconds" --start "$start" --fps "$fps" --size "$size" $art \
  --features "$tmp/features.csv" |
  ffmpeg -loglevel error -y -f rawvideo -pix_fmt rgba -s "$size" -r "$fps" -i - \
    -ss "$start" -t "$seconds" -i "$audio" -map 0:v -map 1:a -vf vflip -c:v libx264 -preset veryfast -crf 20 \
    -pix_fmt yuv420p -c:a aac -shortest "$out"
if [ -n "${PLOT:-}" ]; then
  "$PLOT" "$(dirname "$0")/plot-features.py" "$tmp/features.csv" "${out%.*}.png" "$(basename "$audio") · $source"
fi
