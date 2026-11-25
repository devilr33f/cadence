#!/usr/bin/env bash
# tests of assemble-soloist-runtime.sh, run in the media builder's soloist-runtime stage after it assembled both
# runtimes: what each tar holds, entry by entry, and the shims and debs the script refuses. the refusals run on amd64
# with stub shims built here and the debs SOLOIST_DEBS already holds, so they need no network.
# usage: assemble-soloist-runtime.test.sh <arm64.tar> <amd64.tar>
set -euo pipefail
export LC_ALL=C

[ "$#" -eq 2 ] || { echo "usage: assemble-soloist-runtime.test.sh <arm64.tar> <amd64.tar>" >&2; exit 2; }
arm64_tar=$1
amd64_tar=$2

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script=$here/assemble-soloist-runtime.sh
symbols=$here/../../media/pulse-shim/symbols.txt
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0

pass() { echo "ok - $1"; }
flunk() {
  echo "not ok - $1" >&2
  failures=$((failures + 1))
}

# expected TRIPLE LOADER: the tree's entries as mode and path, sorted
expected() {
  local triple=$1 loader=$2 lib
  {
    for dir in ./ ./dev/ ./etc/ ./etc/ssl/ ./etc/ssl/certs/ ./home/ ./home/soloist/ ./lib/ "./lib/$triple/" ./proc/ \
      ./run/ ./run/cadence/ ./usr/ ./usr/lib/ "./usr/lib/$triple/" ./usr/share/ ./usr/share/doc/ \
      ./usr/share/doc/libatomic1/ ./usr/share/doc/libc6/ ./usr/share/doc/libgcc-s1/ "./${loader%/*}/"; do
      echo "drwxr-xr-x $dir"
    done
    for file in soloist dev/null dev/urandom dev/zero etc/resolv.conf etc/ssl/certs/ca-certificates.crt etc/passwd \
      etc/group etc/nsswitch.conf etc/hosts usr/share/doc/libc6/copyright usr/share/doc/libgcc-s1/copyright \
      usr/share/doc/libatomic1/copyright; do
      echo "-rw-r--r-- ./$file"
    done
    for lib in libc.so.6 libm.so.6 libpthread.so.0 libdl.so.2 librt.so.1 libgcc_s.so.1 libatomic.so.1; do
      echo "-rwxr-xr-x ./lib/$triple/$lib"
    done
    echo "-rwxr-xr-x ./$loader"
    echo "-rwxr-xr-x ./usr/lib/$triple/libpulse.so.0"
    echo "-rwxr-xr-x ./usr/lib/$triple/libcadence-resolv.so"
  } | sort -u
}

# listing TAR: its entries as mode and path, sorted; every one owned by 0:0 at time 0
listing() {
  tar -tvf "$1" --numeric-owner | awk '
    $2 != "0/0" || $4 != "1970-01-01" || $5 != "00:00" { print "bad owner or time: " $0; next }
    { print $1, $NF }' | sort
}

# runtime_holds NAME TAR TRIPLE LOADER MACHINE
runtime_holds() {
  local name=$1 tar=$2 triple=$3 loader=$4 machine=$5 dir="$tmp/$1"
  if ! diff <(expected "$triple" "$loader") <(listing "$tar") >"$tmp/diff"; then
    cat "$tmp/diff" >&2
    flunk "$name: the tar holds exactly the runtime's entries"
    return
  fi
  pass "$name: the tar holds exactly the runtime's entries"
  mkdir "$dir"
  tar -xf "$tar" -C "$dir"
  local ok=1
  for elf in "$loader" "lib/$triple/libc.so.6" "usr/lib/$triple/libpulse.so.0" "usr/lib/$triple/libcadence-resolv.so"; do
    grep -qE "^ +Machine: +$machine\$" <<<"$(readelf -hW "$dir/$elf")" || { echo "$elf is not for $machine" >&2; ok=; }
  done
  for doc in libc6 libgcc-s1 libatomic1; do
    grep -qi 'copyright' "$dir/usr/share/doc/$doc/copyright" || { echo "$doc has no copyright text" >&2; ok=; }
  done
  grep -qx 'nobody:x:65534:65534:nobody:/home/soloist:/bin/false' "$dir/etc/passwd" || { echo "no nobody" >&2; ok=; }
  # the jail binds over these: empty, so nothing of the build host is in them
  for placeholder in soloist dev/null etc/resolv.conf etc/ssl/certs/ca-certificates.crt; do
    [ ! -s "$dir/$placeholder" ] || { echo "$placeholder is not empty" >&2; ok=; }
  done
  if [ -n "$ok" ]; then pass "$name: elf files for its cpu, the copyrights and the jail's files"; else
    flunk "$name: elf files for its cpu, the copyrights and the jail's files"
  fi
}

runtime_holds arm64 "$arm64_tar" aarch64-linux-gnu lib/ld-linux-aarch64.so.1 AArch64
runtime_holds amd64 "$amd64_tar" x86_64-linux-gnu lib64/ld-linux-x86-64.so.2 'Advanced Micro Devices X86-64'

# stub NAME [SKIP] [EXTRA C] [LINK FLAGS...]: a shim exporting symbols.txt but SKIP, plus EXTRA, built for amd64
stub() {
  local name=$1 skip=${2:-} extra=${3:-}
  shift 3 || shift $#
  {
    grep -vx -- "${skip:-^$}" "$symbols" | while read -r symbol; do printf 'void %s(void) {}\n' "$symbol"; done
    printf '%s\n' "$extra"
  } >"$tmp/$name.c"
  gcc -shared -fPIC -o "$tmp/$name.so" -Wl,-soname,libpulse.so.0 "$tmp/$name.c" "$@"
}

# refused NAME ARCH SHIM MESSAGE [DEBS]: the script fails naming MESSAGE, and writes no tar
refused() {
  local name=$1 arch=$2 shim=$3 message=$4 debs=${5:-$SOLOIST_DEBS} out="$tmp/$1.tar"
  if CADENCE_PULSE_SHIM=$shim CADENCE_RESOLV_SHIM=$tmp/resolv-good.so SOLOIST_DEBS=$debs bash "$script" "$arch" \
    "$out" >"$tmp/$name.log" 2>&1; then
    flunk "$name: refused"
  elif ! grep -q -- "$message" "$tmp/$name.log"; then
    cat "$tmp/$name.log" >&2
    flunk "$name: refused for $message"
  elif [ -e "$out" ]; then
    flunk "$name: no tar written"
  else
    pass "$name: refused for $message"
  fi
}

stub good
# a stub resolv preload: getaddrinfo and freeaddrinfo alone, and only libc
resolv_stub() {
  local name=$1 extra=${2:-}
  printf 'void getaddrinfo(void) {}\nvoid freeaddrinfo(void) {}\n%s\n' "$extra" >"$tmp/$name.c"
  gcc -shared -fPIC -o "$tmp/$name.so" "$tmp/$name.c"
}
resolv_stub resolv-good
if CADENCE_PULSE_SHIM=$tmp/good.so CADENCE_RESOLV_SHIM=$tmp/resolv-good.so bash "$script" amd64 "$tmp/good.tar" \
  >"$tmp/good.log" 2>&1 && [ -s "$tmp/good.tar" ]; then
  pass "a stub shim and resolv preload with exactly their exports and only libc pass"
else
  cat "$tmp/good.log" >&2
  flunk "a stub shim and resolv preload with exactly their exports and only libc pass"
fi

resolv_stub resolv-extra 'void gai_reviewed(void) {}'
if CADENCE_PULSE_SHIM=$tmp/good.so CADENCE_RESOLV_SHIM=$tmp/resolv-extra.so bash "$script" amd64 \
  "$tmp/resolv-extra.tar" >"$tmp/resolv-extra.log" 2>&1; then
  flunk "a resolv preload with an export the map lacks is refused"
elif ! grep -q "exports names the preload's map lacks: gai_reviewed" "$tmp/resolv-extra.log"; then
  cat "$tmp/resolv-extra.log" >&2
  flunk "a resolv preload with an export the map lacks names it"
else
  pass "a resolv preload with an export the map lacks is refused"
fi

printf 'void getaddrinfo(void) {}\n' >"$tmp/resolv-missing.c"
gcc -shared -fPIC -o "$tmp/resolv-missing.so" "$tmp/resolv-missing.c"
if CADENCE_PULSE_SHIM=$tmp/good.so CADENCE_RESOLV_SHIM=$tmp/resolv-missing.so bash "$script" amd64 \
  "$tmp/resolv-missing.tar" >"$tmp/resolv-missing.log" 2>&1; then
  flunk "a resolv preload missing an export is refused"
elif ! grep -q "lacks the exports the preload's map names: freeaddrinfo" "$tmp/resolv-missing.log"; then
  cat "$tmp/resolv-missing.log" >&2
  flunk "a resolv preload missing an export names it"
else
  pass "a resolv preload missing an export is refused"
fi

stub extra '' 'void pa_unreviewed(void) {}'
refused extra amd64 "$tmp/extra.so" "exports names symbols.txt lacks: pa_unreviewed"

stub missing pa_stream_flush
refused missing amd64 "$tmp/missing.so" "lacks the exports symbols.txt names: pa_stream_flush"

stub stdcxx '' '' -Wl,--no-as-needed -lstdc++
refused stdcxx amd64 "$tmp/stdcxx.so" "needs libstdc++.so.6"

# initial-exec tls in a shared library marks it STATIC_TLS, which a dlopen may refuse
stub tls '' 'static __thread int count __attribute__((tls_model("initial-exec")));
__attribute__((visibility("hidden"))) int cadence_count(void) { return ++count; }'
refused tls amd64 "$tmp/tls.so" "STATIC_TLS"

# a libc of the future: its symbol version is newer than the runtime's glibc
mkdir "$tmp/future"
printf 'GLIBC_2.99 { global: cadence_future; };\n' >"$tmp/future/libc.map"
printf 'void cadence_future(void) {}\n' >"$tmp/future/libc.c"
gcc -shared -fPIC -o "$tmp/future/libc.so.6" -Wl,-soname,libc.so.6 -Wl,--version-script="$tmp/future/libc.map" \
  "$tmp/future/libc.c"
stub future '' 'extern void cadence_future(void);
__attribute__((visibility("hidden"))) void cadence_call(void) { cadence_future(); }' "$tmp/future/libc.so.6"
refused future amd64 "$tmp/future.so" "needs glibc 2.99"

refused machine arm64 "$tmp/good.so" "is not for AArch64"

# a cached deb that is not the pinned one
mkdir "$tmp/debs"
cp "$SOLOIST_DEBS"/*.deb "$tmp/debs/"
for deb in "$tmp"/debs/libatomic1_*_amd64.deb; do printf 'x' >>"$deb"; done
refused deb amd64 "$tmp/good.so" "libatomic1_14.2.0-19_amd64.deb does not match its pin" "$tmp/debs"

if bash "$script" riscv64 "$tmp/riscv64.tar" >/dev/null 2>&1; then flunk "an unknown arch is refused"; else
  [ "$?" -eq 2 ] && pass "an unknown arch is refused" || flunk "an unknown arch is refused with usage"
fi

[ "$failures" -eq 0 ] || { echo "assemble-soloist-runtime.test.sh: $failures failed" >&2; exit 1; }
echo "assemble-soloist-runtime.test.sh: all passed"
