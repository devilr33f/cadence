# Cadence v2 on pickle

## Quick path

You need a Yandex Station Mini 3 that v1 provisioned:
- root access;
- adbd and dropbear in `/etc/inittab`;
- Quasar removed.

A fresh speaker needs two steps first:
1. Root it ([hackalice.me](https://hackalice.me/start/root/)).
2. Run v1's provisioning from the `v1-final` tag: `git worktree add ../cadence-v1 v1-final`, then follow
   `deploy/speaker/README.md` there.

Then:

1. Start the server ([deploy/quickstart](../quickstart/README.md)). In the dashboard, open **Devices → Enroll** and
   copy a code.
   `<server>` below is the address `setup.sh` printed: `https://cadence.lan`, or `https://192.168.1.10:8443` on
   another port.
2. Build the tarball and the installer. Docker is needed, and the first build takes a while:

   ```sh
   make device-build BOARD=yandex-pickle
   go build -o build/cadence-install.exe ./device/cmd/cadence-install
   ```

3. Do a dry run, then install. If the server uses `CADENCE_TLS=internal`, add
   `--ca-file deploy/quickstart/data/ca.crt` to both commands.

   ```sh
   speaker="--ssh root@<device-ip> --key <ssh key> --adb <device-ip>:5555"
   tarball=build/device/cadence-<version>-yandex-pickle-arm64.tar
   build/cadence-install.exe $speaker --tarball "$tarball" --server <server> --dry-run
   build/cadence-install.exe $speaker --tarball "$tarball" --server <server> --code <code>
   ```

4. Check that the speaker shows as online in the dashboard. If the install failed, run the same command again: it
   resumes where it stopped.
5. To undo, run `build/cadence-install.exe $speaker --to-v1`.

The rest of this page is the full reference.

## Reference

Owner-gated: an agent never runs any of this against the speaker. `cadence-install` swaps a pickle that v1
provisioned from v1 to v2, and back with `--to-v1`. It runs on the owner's machine and drives the
speaker over ssh (the dropbear provisioning installed, key only) or adb, the fallback.

## What the swap does

Each phase is recorded in `/data/cadence/install.state` once it finished, so a run cut short resumes after the last
one; preflight runs every time and only reads.

| Phase | What it does |
| --- | --- |
| preflight | refuses unless the model is `Yandex Pickle*` on kernel 5.4, the firmware is the tested one (3.21, `--accept-firmware` to accept another), adbd and dropbear are in `/etc/inittab` and running, Quasar and its updater are gone, and `/data` has room |
| stop-v1 | backs `/etc/inittab` up to `/data/cadence/backup/`, removes v1's line (and only it), stops v1's supervisor and cadenced (each matched by its `/proc` exe and command line before any signal), blanks the lights |
| stage | moves v1's tree to `/data/cadence/v1/` (`stock/`, with the owner's command model, stays: v1 and v2 both read it there), converts v1's `state/volume`, read exactly as v1 read it (1–3 digits, at most 100, never clamped), to v2's `{"level":N}` (a file it cannot read gives none: v2 then starts at 30 with the level unknown, and a sender's volume counts only once a level is set), pushes the tarball, checks its sha256 and every file against its `MANIFEST`, and puts `bin/`, `share/`, `licenses/` and the Soloist and AirPlay runtimes in place |
| enroll | `cadenced enroll` with the dashboard's code, read on the speaker from a file that goes right after; cadenced writes `config.json` (mode 600) |
| start-v2 | installs `bin/cadence-run` with pickle's `bin/board-hook` beside it and puts `::respawn:/data/cadence/bin/cadence-run` where v1's line was |
| verify | adbd and dropbear still answer, v1 is gone, cadenced logged `link_up` within 60 s, and `Master` is at the persisted level |

The root filesystem is remounted read-write only for the two inittab writes and read-only again on every exit, also
when the session ends mid-write with a hangup or TERM (a SIGKILL cannot be caught). Recovery access is checked before
each write, and again after it and init's reload before anything else happens. The device token never leaves the
speaker; the code is never printed.

`cadence-run` blanks the lights and sets `Master` to the persisted level (else 30) with `amixer` before every start
of cadenced, so the stock boot's 100 % never lasts. A run shorter than a minute is followed by a pause (5 s doubling
to 60 s). Its log, with cadenced's, is `/data/cadence/log/cadenced.log` (+ `.1`).

v1's own state (alarms, settings, the Soloist key) stays in `/data/cadence/v1/`; v2 starts without it.

## Before

1. The v2 server runs (`deploy/server/README.md`) and the dashboard shows an enroll code for a `yandex-pickle`.
2. The speaker's ssh host key is in `~/.ssh/known_hosts` (v1's provisioning put it there); an unknown key fails.
3. Build the tarball from a clean commit, and the installer:

   ```sh
   make device-build BOARD=yandex-pickle
   go build -o build/cadence-install.exe ./device/cmd/cadence-install
   ```

   The build fetches the pinned third-party parts (pickle's ffmpeg, the soloist and airplay runtimes) from the
   project's package registry by their recipe hash (`deploy/builder/prebuilt.sh`, which builds them as before on a
   miss); only audiod and cadenced compile every time.

## Swap to v2

```sh
tarball=build/device/cadence-<version>-yandex-pickle-arm64.tar
speaker="--ssh root@<device-ip> --key <ssh key> --adb <device-ip>:5555"
build/cadence-install.exe $speaker --tarball "$tarball" --server https://<v2 host> --dry-run
build/cadence-install.exe $speaker --tarball "$tarball" --server https://<v2 host> --code <code>
```

The dry run reads the speaker and prints every command it would run. A failed run says which phase failed: run it
again to resume (a code the server refused needs a new one), or swap back.

Check afterwards: `ssh -i <ssh key> root@<device-ip> 'tail -n 30 /data/cadence/log/cadenced.log'`.

## Swap back to v1

```sh
build/cadence-install.exe $speaker --to-v1
```

It takes v2's line out of init and stops it, moves v2's tree to `/data/cadence/v2/` (replacing an older one there)
and v1's back, gives v1 the level v2 kept (v1 keeps its own when v2 kept none), and restores `/etc/inittab` from the
backup taken before v1's line was removed (byte for byte when nothing else changed it; else v1's line goes back after
adbd's). Init then starts v1.
