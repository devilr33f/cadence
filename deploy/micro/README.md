# Experimental Yandex Station Micro v1.2 (Lite)

## Quick path

**Experimental.** Only one firmware is supported (below). You need:
- root ADB on port 5555 that survives reboots. The [provisioning proxy](../yandex/README.md) keeps it on.
- Router rules that block Yandex's OTA servers for the speaker.

1. Start the server ([deploy/quickstart](../quickstart/README.md)). In the dashboard, open **Devices → Enroll** and
   copy a code.
   `<server>` below is the address `setup.sh` printed: `https://cadence.lan`, or `https://192.168.1.10:8443` on
   another port.
2. Build:

   ```sh
   make device-build BOARD=yandex-micro
   go build -o build/cadence-install ./device/cmd/cadence-install
   ```

3. Install. If the server uses `CADENCE_TLS=internal`, add `--ca-file deploy/quickstart/data/ca.crt`:

   ```sh
   speaker="--board yandex-micro --adb <device-ip>:5555 --server <server> --ota-blocked"
   build/cadence-install $speaker --tarball build/device/cadence-<version>-yandex-micro-armhf.tar --code <code>
   ```

4. To undo, run `build/cadence-install --board yandex-micro --adb <device-ip>:5555 --ota-blocked --restore-stock`.

## Reference

Owner-gated hardware bring-up for `yandex-micro`. The supported fingerprint is the probed Lite on kernel 4.9.68,
firmware `0.369.1.48.4790768027.20261019.297` / `VERSION 3.21`, with root ADB on :5555. The installer checks the ADB
transport before and after every change; keep the provisioning proxy or another persistent recovery mechanism active.
The router OTA block must be acknowledged with `--ota-blocked`, as on midi.

This slim artifact deliberately excludes Soloist and AirPlay: the Lite has only 87.5 MiB of `/data`. Voice with the
stock `yandexmicro` wake/VQE engines, URL playback, Bluetooth receive, multiroom, alarms/timers, telemetry,
action/volume buttons and the three-LED ring are included. Bluetooth uses the stock BlueZ/bluealsa services with
Cadence's pairing window; agents never pair or run a real-device group. LED colour order and orientation need
owner-attended acceptance; the mute key is not mapped.

```sh
make device-build BOARD=yandex-micro
go build -o build/cadence-install ./device/cmd/cadence-install
```

Replace `<code>` by a dashboard enroll code for a `yandex-micro` device, without printing it:

```sh
speaker="--board yandex-micro --adb <device-ip>:5555 --server https://<v2 host> --ota-blocked"
tarball=build/device/cadence-<version>-yandex-micro-armhf.tar
build/cadence-install $speaker --tarball "$tarball" --dry-run
build/cadence-install $speaker --tarball "$tarball" --code <code>
```

The stock files stay installed and the wrapper suspends their processes before and during Cadence. Rollback is
`build/cadence-install --board yandex-micro --adb <device-ip>:5555 --ota-blocked --restore-stock`. Rootfs writes
remain limited to the reversible inittab operation.
