# Cadence v2 on a VK Capsula Mini

## Quick path

Two ways to run Cadence on a Capsula Mini:
- **With [capsulad](https://github.com/m4l3vich/capsulad):** install its Cadence Client app and point it at your
  server. The quickstart guide explains the CA exchange ([deploy/quickstart](../quickstart/README.md#add-a-speaker)).
  Nothing on this page is needed.
- **With Cadence's own daemon in place of the stock assistant:** follow the steps below.

You need capsula-root installed on the speaker (its password ssh). Then:

1. Write capsula-root's root password into a file only you can read.
2. Connect once with `ssh root@<device-ip>` and accept the host key.
3. Start the server ([deploy/quickstart](../quickstart/README.md)). In the dashboard, open **Devices → Enroll** and
   copy a code.
   `<server>` below is the address `setup.sh` printed: `https://cadence.lan`, or `https://192.168.1.10:8443` on
   another port.
4. Build:

   ```sh
   make device-build BOARD=vk-capsula-mini
   go build -o build/cadence-install.exe ./device/cmd/cadence-install
   ```

5. Do a dry run, then install. If the server uses `CADENCE_TLS=internal`, add
   `--ca-file deploy/quickstart/data/ca.crt` to both commands.

   ```sh
   speaker="--board vk-capsula-mini --ssh root@<device-ip> --password-file <password file>"
   tarball=build/device/cadence-<version>-vk-capsula-mini-arm64.tar
   build/cadence-install.exe $speaker --tarball "$tarball" --server <server> --dry-run
   build/cadence-install.exe $speaker --tarball "$tarball" --server <server> --code <code>
   ```

6. To undo, run `build/cadence-install.exe $speaker --restore-stock`.

The rest of this page is the full reference.

## Reference

Owner-gated: an agent never runs any of this against a speaker. `cadence-install --board vk-capsula-mini` swaps a VK
Capsula Mini (Amlogic A113X, Buildroot 2022.11.1, kernel 4.9.113) from its stock assistant to v2, or gives it back
with `--restore-stock`. It runs on the owner's machine and drives the speaker over ssh as root, through capsula-root.

Only the probed firmware is accepted: the kernel and `/proc/version`, `/etc/hwrevision` `S4xx 1.0` and the Buildroot
`os-release` it was probed with. Any other firmware is refused unless you pass `--accept-firmware`; a unit with
another clock controller (the AW21036 variant) needs its clock map walked first. The RK3308 revision is out of scope.

Two firmwares are known. 44.90 (Buildroot 2022.11.1) is the one above, with capsula-root's ssh. The 2021 firmware
(Buildroot 2019.11.1, a kernel built 2021-12-29 with gcc 8.3) is installed over capsula-jb's root adb
(`--adb <ip>:5555`): the installer picks it by its facts, checks adbd as its recovery access, stubs its `fwupdater` and
`swupdate` binary, and cadence captures through its `uroc` beamformer pcm (`plug:uroc`) instead of `single_plugin`. Its
stock wakes on a msgpack model that cadence cannot read, so on it the talk key alone starts a turn.

## Before

1. capsula-root is installed on the speaker (its password ssh, its supervisor and `dropbearmulti`). Cadence never
   installs, changes or restarts it: it is the recovery access, checked before and after every change.
2. capsula-root's dropbear takes passwords only (no public keys). Write its root password into a file only you can
   read, and pass `--password-file <file>`: `cadence-install` runs itself as ssh's `SSH_ASKPASS` helper, so the
   password is never on a command line. Have the speaker's host key in `~/.ssh/known_hosts` (connect once with plain
   `ssh root@<ip>` and accept it), or pass `--known-hosts <file>` holding it; an unknown key fails.
3. The v2 server runs (`deploy/server/README.md`) and the dashboard shows an enroll code for a `vk-capsula-mini`.
4. Build the tarball from a clean commit, and the installer:

   ```sh
   make device-build BOARD=vk-capsula-mini
   go build -o build/cadence-install.exe ./device/cmd/cadence-install
   ```

   The tarball is arm64, audiod cross-built with zig for the firmware's glibc 2.28 with OpenSSL 3.0 in `bin/lib` and
   the «Маруся» keyword engine, and carries no Soloist, no AirPlay runtime and no wake model.

## Swap to v2

```sh
tarball=build/device/cadence-<version>-vk-capsula-mini-arm64.tar
speaker="--board vk-capsula-mini --ssh root@<capsula ip> --password-file <password file>"
build/cadence-install.exe $speaker --tarball "$tarball" --server https://<v2 host> --dry-run
build/cadence-install.exe $speaker --tarball "$tarball" --server https://<v2 host> --code <code>
```

No `--ota-blocked`: the board's OTA stubs are applied, so the router's rules are not what the install relies on. The
installer refuses while `/data/update_successful` exists (the stock's self-check would reboot the board once
`voice-agent` stops); wait for the stock to finish its check.

## What changes on the speaker

Stock, kept installed and otherwise untouched: `/usr/bin/voice-agent`, its sounds and wake model, and every other stock file.
The stock's own copies stay readable under the overlay in `/rom`.

Wake by voice: cadence wakes on «Маруся» with the stock assistant's own model, `/usr/share/voice-agent/model.bin`,
which audiod reads from the speaker at runtime; it is never shipped or copied. audiod loads only the model it was
probed with (its sha256 is fixed in the board): another file, as another firmware might carry, is read and hashed,
then refused before it is parsed, and the talk key alone starts a turn, as it does when the file is missing.

Cadence:

- `/etc/init.d/S97voice-agent.sh` is replaced, through the overlay, by a launcher: it starts
  `/data/cadence/bin/cadence-run` detached, or the stock's `/rom` copy when the cadence tree is missing or
  `/data/cadence/disabled` exists.
- `/usr/bin/fwupdater` and `/usr/bin/swupdate.sh` are overlay stubs that log one syslog line and report no update, so
  the vendor's OTA cannot fetch anything and the stock assistant keeps running when it is restored. They stay after
  `--restore-stock`.
- While cadence runs, the board hook stops `okcall`, `stereoctl`, `sysmon` and the volume syncer (which rewrites
  `TV-Set`), then the stock assistant: `voice-agent`'s master, its app, `btctl` and `tgclient`, and stops them again
  whenever they come back.
- The hardware watchdog: the stock's `voice-agent` holds `/dev/watchdog` (10 s timeout), and the board resets 10 s
  after its last holder stops. The hook takes it over with a feeder tied to `cadence-run`: it keeps feeding across
  cadenced's restarts, and a dead `cadence-run` lets the board reset. `--restore-stock` stops the feeder right before
  it starts the stock, which opens the watchdog within the 10 s.
- `/data/cadence/` holds the tree, config, state and logs. The root is an rw overlay on `/data` by design: it is
  never remounted.

## Verify

1. The installer's verify phase passes: capsula-root still answers, `cadenced` logged `link_up`, no stock process runs.
2. Reboot twice; the board comes back on cadence each time (`/data/cadence/log/cadenced.log`), and ssh still works.
3. Say «Маруся» for a voice turn (audiod logs `vendor_status` with `code=keyword` at its start, then `kws_started`),
   press the talk key for another, then try volume and mute, the ring and the clock.

## Back to the stock

```sh
build/cadence-install.exe $speaker --restore-stock
```

It stops cadence, writes the stock's S97 bytes back from `/rom` over the launcher, stops the feeder right before it
starts the stock (handing the watchdog over), then starts the platform scripts it stopped (okcall, stereoctl, sysmon
and the volume syncer, skipping any already running) and checks recovery access. The stubs stay. `/data/cadence/` stays too, and an
install with the tarball starts v2 again.

Never `rm` `/etc/init.d/S97voice-agent.sh` (or the stubs) by hand: on the overlay a delete leaves a whiteout that hides
the stock's copy. To go back by hand, copy the `/rom` file over it
(`cp /rom/etc/init.d/S97voice-agent.sh /etc/init.d/S97voice-agent.sh`).

To boot the stock once without restoring, `touch /data/cadence/disabled` over ssh and reboot; remove the file to boot
cadence again.

## Caveats

- Holding vol+ and vol- together at boot is the stock's factory reset: it wipes `/data`, so cadence and capsula-root
  are gone and the stock returns. Nothing in cadence handles it; reinstall capsula-root first.
- The stock's own OTA stays blocked for good, by the stubs. Updating the stock firmware is a manual job.
- Wake by voice rests on an offline check with synthesized voices, run on the host through the same int8 path the
  speaker runs: with a threshold stricter than the stock's (0.999) it woke on all 72 synthesized clips and on none of
  two hours of public-domain speech and music, whose highest score was 0.9983. That margin is thin, real voices
  across a room may score lower, and no voice has woken a speaker yet. If «Маруся» is missed or wakes on its own, the
  talk key still starts a turn.
