#!/bin/sh
# the screen tests' clips, made in the test image (never committed): out dir as $1
#   colors.mkv: 2 s at 320x180 25 fps h.264, the first second solid red, the second solid blue; two audio tracks (a
#     440 Hz sine tagged eng "Main", a 660 Hz one tagged rus) and an srt subtitle tagged rus. bitexact, so every run
#     makes the same bytes
set -eu
out=$1
mkdir -p "$out"
tmp=$(mktemp -d)
printf '1\n00:00:00,000 --> 00:00:02,000\nsub\n' >"$tmp/sub.srt"
ffmpeg -hide_banner -loglevel error -y \
  -f lavfi -i "color=c=red:s=320x180:r=25:d=1,format=yuv420p[a];color=c=blue:s=320x180:r=25:d=1,format=yuv420p[b];[a][b]concat=n=2:v=1:a=0" \
  -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=2" \
  -f lavfi -i "sine=frequency=660:sample_rate=48000:duration=2" \
  -i "$tmp/sub.srt" \
  -map 0:v -map 1:a -map 2:a -map 3:s \
  -c:v libx264 -preset veryfast -g 25 -bf 0 -pix_fmt yuv420p -c:a flac -c:s srt \
  -metadata:s:a:0 language=eng -metadata:s:a:0 title=Main -metadata:s:a:1 language=rus -metadata:s:s:0 language=rus \
  -map_metadata -1 -fflags +bitexact -flags +bitexact \
  "$out/colors.mkv"
rm -rf "$tmp"
