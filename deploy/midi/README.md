# Cadence v2 on a midi

## Quick path

You need:
- A Yandex Station Midi that went through v1's provisioning access phase: adbd and dropbear in `/etc/inittab`.
  - To get there on a fresh speaker, run `git worktree add ../cadence-v1 v1-final`, then
    `provision.sh --board yandexmidi --preserve-stock` from that tree's `deploy/speaker/README.md`.
- Router rules that block Yandex's OTA servers for the speaker. Keep them in place.

Then:

1. Start the server ([deploy/quickstart](../quickstart/README.md)). In the dashboard, open **Devices → Enroll** and
   copy a code.
   `<server>` below is the address `setup.sh` printed: `https://cadence.lan`, or `https://192.168.1.10:8443` on
   another port.
2. Build the tarball and the installer. Docker is needed, and the first build takes a while:

   ```sh
   make device-build BOARD=yandex-midi
   go build -o build/cadence-install.exe ./device/cmd/cadence-install
   ```

3. Do a dry run, then install. If the server uses `CADENCE_TLS=internal`, add
   `--ca-file deploy/quickstart/data/ca.crt` to both commands.

   ```sh
   speaker="--board yandex-midi --ssh root@<device-ip> --key <ssh key> --adb <device-ip>:5555"
   tarball=build/device/cadence-<version>-yandex-midi-armhf.tar
   build/cadence-install.exe $speaker --tarball "$tarball" --server <server> --ota-blocked --dry-run
   build/cadence-install.exe $speaker --tarball "$tarball" --server <server> --ota-blocked --code <code>
   ```

4. Check that the speaker shows as online in the dashboard. If the install failed, run the same command again: it
   resumes.
5. To undo, run `build/cadence-install.exe $speaker --restore-stock --ota-blocked` (Alice comes back) or
   `build/cadence-install.exe $speaker --to-v1`.

The rest of this page is the full reference.

## Reference

Owner-gated: an agent never runs any of this against a speaker. `cadence-install --board yandex-midi` swaps a Yandex
Station Midi from v1 to v2 with its stock assistant kept, back with `--to-v1`, or gives it back to the stock with
`--restore-stock`. It runs on the owner's machine and drives the speaker over ssh (the dropbear v1's provisioning
installed, key only) or adb, the fallback.

A midi keeps Quasar installed (M2m-R2): `cadence-run` suspends it before every start of cadenced and again whenever it
comes back while cadenced runs. Nothing on the speaker blocks the vendor's OTA yet (M2m-R3): the router does, so every
install and every `--restore-stock` needs `--ota-blocked`, the owner's word that its OTA block rules are in place.
Without it the run refuses before anything changes. Pickle never needs the flag.

## What the swap does

Each phase is recorded in `/data/cadence/install.state` once it finished, so a run cut short resumes after the last
one; suspend runs again while stage, enroll or start-v2 is still to run (a reboot between runs starts the stock
again); preflight runs every time and only reads.

| Phase | What it does |
| --- | --- |
| preflight | refuses without `--ota-blocked`, and unless the model is exactly `Yandex Station Midi` on kernel 4.9, the firmware is the one v1 inspected (kernel 4.9.68, `VERSION` 3.21, `version_full` 0.369.1.46.4793639783.20261020.295; `--accept-firmware` to accept another), adbd and dropbear are in `/etc/inittab` and running, the stock is whole (`/system/vendor/quasar` with an executable `run.sh`, `quasar_launcher2` and `libs/libyandex_evil.so`; only their presence is checked), init's table starts nothing under `/system/vendor/`, the device has every program the scripts run (`start-stop-daemon` among them), and, while stage is still to run, `/data` has room for the tarball plus the larger of the tarball and 64 MiB (the tarball is gone before Soloist's first download). It lists the hardening steps (`ota-key`, `ota-script-stub`, `updatesd-launch`) as `not_inspected`: none is applied |
| stop-v1 | if v1 runs: backs `/etc/inittab` up to `/data/cadence/backup/`, removes v1's line (and only it), stops v1's supervisor, cadenced and media helper (each matched by its `/proc` exe and command line before any signal), blanks the ring |
| suspend | runs the tarball's own `cadence-run prestart` from `/data/cadence/.install/`: the suspend every start of cadenced runs. Every process whose executable lies under `/system/vendor/quasar/` gets TERM, `quasar_launcher2` first, KILL after 5 s; one still there fails the run here, with nothing staged and recovery access untouched (`reason=stock_still_running`) |
| stage | moves v1's tree to `/data/cadence/v1/` (with `provision.yandexmidi-preserved.state`, `board-control`, `stock-mode`, `board.txt` and v1's `runtime/`; `stock/` stays), converts v1's `state/volume` as on pickle, pushes the tarball, checks its sha256 and every file against its `MANIFEST`, and puts `bin/`, `share/`, `licenses/` and the Soloist (arm64) and AirPlay (armhf) runtimes in place |
| enroll | `cadenced enroll` with the dashboard's code, read on the speaker from a file that goes right after |
| start-v2 | installs `bin/cadence-run` with midi's `bin/board-hook` beside it and puts `::respawn:/data/cadence/bin/cadence-run` right after adbd's line |
| verify | adbd and dropbear still answer, v1 is gone, cadenced logged `link_up` within 60 s, `/system/vendor/quasar` is still there and nothing runs from it, three wrapper polls (~6 s) later the log shows fewer than two `event=stock_resuspended` since cadenced's latest start (else `stock_keeps_returning`), and `Master` is at the persisted level |

The root filesystem is remounted read-write only for the inittab writes and read-only again on every exit. Recovery
access is checked before each write, and again after it and init's reload before anything else happens. The device
token never leaves the speaker; the code is never printed. Stock files are never read or copied: only the version
files' text and the presence of the others.

While cadenced runs, `/data/cadence/log/cadenced.log` shows `event=stock_suspended count=N` before each start,
`event=stock_resuspended count=N` when stock came back mid-run (its init script started late, or a watchdog), after
which the wrapper sets `Master` to the persisted level again (it logs `event=volume_applied` only when the level
changed), and `event=board_control_failed code=suspend reason=stock_still_running` while it will not stop: cadenced
then does not start, and a running one keeps running while the suspend is tried again, 2 s after each failed attempt,
which itself takes up to ~7 s. The stock still starts at boot and runs until the wrapper's first suspend, seconds at
most.

If `run.sh`, a shell the suspend leaves running, restarts the launcher, the log shows one of two symptoms (an open item
in `AGENTS.md`): a fast loop, back before a suspend ends, repeats `board_control_failed code=suspend`; a slow one, back
between polls, repeats `stock_resuspended`, and verify fails with `stock_keeps_returning`.

## Before

1. The router's rules that block the vendor's OTA servers are in place for the midi's address, and stay so.
2. The midi went through v1's provisioning access phase (`provision.sh --board yandexmidi --preserve-stock`): adbd and
   dropbear in `/etc/inittab`, its ssh host key in `~/.ssh/known_hosts`. v1 itself may or may not be installed.
3. The v2 server runs (`deploy/server/README.md`) and the dashboard shows an enroll code for a `yandex-midi`.
4. Build the tarball from a clean commit, and the installer:

   ```sh
   make device-build BOARD=yandex-midi
   go build -o build/cadence-install.exe ./device/cmd/cadence-install
   ```

   The build fetches the pinned third-party parts (midi's zig cross deps, the soloist and airplay runtimes) from the
   project's package registry by their recipe hash (`deploy/builder/prebuilt.sh`, which builds them as before on a
   miss); only audiod and cadenced compile every time.

## Swap to v2

```sh
tarball=build/device/cadence-<version>-yandex-midi-armhf.tar
speaker="--board yandex-midi --ssh root@<midi ip> --key <ssh key> --adb <midi ip>:5555"
build/cadence-install.exe $speaker --tarball "$tarball" --server https://<v2 host> --ota-blocked --dry-run
build/cadence-install.exe $speaker --tarball "$tarball" --server https://<v2 host> --ota-blocked --code <code>
```

The dry run reads the speaker and prints every command it would run. A failed run says which phase failed: run it
again to resume (a code the server refused needs a new one), or swap back.

Check afterwards: `ssh -i <ssh key> root@<midi ip> 'tail -n 30 /data/cadence/log/cadenced.log'`.

## Swap back to v1

```sh
build/cadence-install.exe $speaker --to-v1
```

As on pickle (`deploy/pickle/README.md`): v2 leaves init and stops, its tree goes to `/data/cadence/v2/`, v1's comes
back file for file, and v1's line returns from the inittab backup. v1's supervisor then suspends the stock itself.

## Give it back to the stock

```sh
build/cadence-install.exe $speaker --restore-stock --ota-blocked
```

It refuses, before anything changes, without `--ota-blocked`, while v1 runs from init (v1's own
`rollback.sh --restore-stock --ota-blocked` does that), or while a process runs from `/data/cadence/bin/` that is not
one cadence started. Then it takes v2's line out of `/etc/inittab` (every other byte as it was, backed up first),
stops v2, blanks the ring and starts the stock's start script detached:
`start-stop-daemon -S -b -o -x /bin/sh -n run.sh -- /system/vendor/quasar/run.sh`, never its boot routine
(`onboot.sh`). It waits for `quasar_launcher2` and checks recovery access. v2's tree and config stay, and an install
with the tarball starts v2 again.
