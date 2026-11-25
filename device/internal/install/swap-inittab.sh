# swap-inittab.sh <inittab> <staged> <old-sha256> <new-sha256> <mounts> <mount> <kill>, run with sh by cadence-install:
# replaces inittab with the staged copy if inittab is still the one the installer read and the copy arrived whole,
# then makes init reload it. / is made writable for the write alone and read-only again however the script ends,
# also when the session running it ends with a hangup or TERM, though no trap outlives a SIGKILL (rule 3; pickle's
# root may be rw by the owner's choice, and it still ends read-only). a failure leaves inittab as it was
set -u
inittab=$1
staged=$2
old=$3
new=$4
mounts=$5
MOUNT=$6
KILL=$7

writable=no
finish() {
    status=$?
    if [ "$writable" = yes ]; then
        rm -f "$inittab.cadence-new" || status=1
        sync
        # a deleted file a process still holds keeps / busy for a moment
        tries=1
        until "$MOUNT" -o remount,ro /; do
            if [ "$tries" -ge 5 ]; then
                echo 'swap-inittab: root_still_writable' >&2
                status=1
                break
            fi
            tries=$((tries + 1))
            sleep 1
        done
    fi
    exit "$status"
}
trap finish EXIT
trap 'exit 1' HUP INT TERM PIPE

sum() {
    sha256sum < "$1" | cut -c1-64
}

[ "$(sum "$inittab")" = "$old" ] || { echo 'swap-inittab: inittab_changed' >&2; exit 1; }
[ "$(sum "$staged")" = "$new" ] || { echo 'swap-inittab: staged_differs' >&2; exit 1; }
writable=yes
if ! awk '$2 == "/" { options = $4 } END { exit options !~ /^rw(,|$)/ }' "$mounts"; then
    "$MOUNT" -o remount,rw / || exit 1
fi
# a copy of inittab first, so the new one keeps its mode and owner, not the staged copy's
rm -f "$inittab.cadence-new" || exit 1
cp -p "$inittab" "$inittab.cadence-new" || exit 1
cat "$staged" > "$inittab.cadence-new" || exit 1
cmp -s "$staged" "$inittab.cadence-new" || exit 1
mv -f "$inittab.cadence-new" "$inittab" || exit 1
"$KILL" -HUP 1 || exit 1
