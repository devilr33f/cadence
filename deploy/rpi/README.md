# Cadence on rpi

## Quick path

You need a Raspberry Pi 4 or 5 on Debian 13 trixie arm64 (Raspberry Pi OS Lite or DietPi) reachable as the ssh alias
`pi`, logged in as root: every command below needs root. The build itself needs an arm64 machine with Docker.

1. Start the server ([deploy/quickstart](../quickstart/README.md)). `<server>` below is the address `setup.sh` printed:
   `https://cadence.lan`, or `https://192.168.1.10:8443` on another port.
2. Build the packages:

   ```sh
   make dist DISTRO=rpi
   ```

3. Install them on the Pi:

   ```sh
   scp build/dist/cadence-*-rpi-arm64.deb build/mpv/deb-0.40-arm64/libmpv2_*+cadence1_arm64.deb pi:/tmp/
   ssh pi 'apt-get update && apt-get install -y /tmp/cadence-*-rpi-arm64.deb /tmp/libmpv2_*+cadence1_arm64.deb'
   ```

4. If the server uses `CADENCE_TLS=internal`, have the Pi trust its CA:

   ```sh
   scp deploy/quickstart/data/ca.crt pi:/tmp/cadence-ca.crt
   ssh pi 'cp /tmp/cadence-ca.crt /usr/local/share/ca-certificates/cadence.crt && update-ca-certificates'
   ```

5. Enroll. The command asks for a code from **Devices → Enroll**. Then start the service (see
   [New enrollment](#new-enrollment)):

   ```sh
   ssh -t pi '/opt/cadence/bin/cadenced enroll --server <server> --board rpi \
     --config /etc/cadence/config.json --data-dir /var/lib/cadence --audiod-binary /opt/cadence/bin/cadence-audiod'
   ```

## Reference

One Raspberry Pi 4/5 on Debian 13 trixie arm64 (DietPi or Raspberry Pi OS Lite) as `rpi`: cadence-screen on its HDMI
output, HDMI-CEC on `/dev/cec0`, audiod on HDMI audio or an active Bluetooth speaker, Bluetooth receive and multiroom.
It has no microphone of its own; a room speaker listens. The Pi is the owner's Debian: nothing is hardened and ssh is
never touched (rule 2). Pairing and routing still need a woken session, and a new speaker starts at no more than 40
(rule 7).

The screen is optional. cadenced reads the HDMI connectors' status (`/sys/class/drm/card*-HDMI-A-*/status`) every
2 s, and a status counts once it held 1.5 s (a tv's input switch or a loose plug moves nothing); the first read at boot
counts at once. With no sink plugged in the Pi is a speaker: `cadence-screen.service` is stopped, and `display.screen`,
`media.video`, `media.mpv`, `input.remote` and `display.cec` are reported **unavailable** (still reported and
configured on the server, their commands answered `unavailable`, their state hidden from the server's screen features,
and the dashboard's capability cards say Unavailable). audiod, Bluetooth, AirPlay audio (`media.screencast`) and voice
are unaffected. Plugging a display in makes them available again, starts the unit and rescans CEC; unplugging stops it
(`hdmi_sink connected=…` in cadenced's log). A card with no HDMI connector at all leaves the screen on, as before.
`CADENCE_RPI_SCREEN_UNIT` in `cadence.service` names another unit, or `none` to leave the screen to whoever runs it
(availability still follows the sink). A tv that drops hotplug in standby makes the screen stop while it sleeps; a
`video=HDMI-A-1:1920x1080@60D` kernel argument forces the connector on.

SDL uses its **KMS/DRM** driver directly. No X11, Wayland compositor, gamescope or desktop session is needed.
`cadence-screen.service` runs `/opt/cadence/bin/cadence-screen --driver kmsdrm --hwdec v4l2m2m --mpv-profile fast --max-display-fps 60`: h264 decodes on the bcm2835 block and reaches the gpu as dma-bufs (zero-copy; mpv's auto-safe never picks v4l2m2m), mpv's `fast` profile keeps its scalers within the v3d's budget, and the build itself is the cadence-ao one (`--ao cadence` would count as an override and mute split audio). Video is timed by the audio clock: 60 fps content plays smoothly at about 30 presented fps, as mpv's color conversion pass alone takes ~15 ms of the v3d a frame (the kernel has no transparent hugepages, which the v3d driver asks for at boot). The patched libmpv sends its audio to the same audiod mixer as everything else.
movie and music audio into the same audiod mixer. With no active Bluetooth speaker audiod opens
`hdmi:CARD=vc4hdmi0`; with one it opens that speaker's BlueALSA PCM, returning to HDMI on disconnect.

## Build

From a clean commit:

```sh
make dist DISTRO=rpi
```

This writes `build/dist/cadence-<version>-rpi-arm64.deb` and ensures
`build/mpv/deb-0.40-arm64/libmpv2_<version>+cadence1_arm64.deb`, the mpv build with `ao_cadence`. The build runs
natively in linux/arm64 on an arm64 host. `libairplay` is built from the pinned submodules; RmlUi and every other
third-party input are pinned by the builder.

## Install or upgrade

Copy the Cadence deb and the patched `libmpv2` deb to the Pi, then let apt install both and their dependencies:

```sh
scp build/dist/cadence-*-rpi-arm64.deb build/mpv/deb-0.40-arm64/libmpv2_*+cadence1_arm64.deb pi:/tmp/
ssh pi 'apt-get update && apt-get install -y /tmp/cadence-*-rpi-arm64.deb /tmp/libmpv2_*+cadence1_arm64.deb'
```

The package installs binaries and UI assets under `/opt/cadence`, plus `cadence.service` and
`cadence-screen.service`. Only `cadence.service` is enabled; cadenced starts and stops `cadence-screen.service` itself
(it has no `[Install]` section). The package does not start or restart anything by itself; after an upgrade run
`systemctl restart cadence.service`.

### Upgrade from a deb with an enabled screen unit

Debs up to 2.0.0+6d51653c801f enabled `cadence-screen.service` for `graphical.target` with `Restart=always`, which
crash-looped on `sdl_init_failed` with nothing plugged in. The new package's `postinst` removes that enablement
(`graphical.target.wants` and `multi-user.target.wants` symlinks), reloads systemd and clears the unit's failed state;
the reloaded unit (`Restart=on-failure`, at most 5 starts in 60 s) ends a running loop on its own. Then:

```sh
ssh pi 'systemctl restart cadence.service && systemctl is-enabled cadence-screen.service; \
  journalctl -u cadence.service -n 40 --no-pager | grep -E "hdmi_sink|screen_"'
```

`is-enabled` answers `static`. The old deb's unit can be restored only by installing that deb again.

If `/boot/firmware/config.txt` (or `/boot/config.txt`) has `dtoverlay=vc4-kms-v3d,noaudio`, back it up and change that
entry to `dtoverlay=vc4-kms-v3d`; `noaudio` disables the HDMI sound cards. Keep `dtparam=audio=off` if the analog
jack is unused. Reboot after changing the overlay. `aplay -l` should then show `vc4hdmi0` (and possibly `vc4hdmi1`).

### Existing rpi-bt device

Preserve the device id, token, volume and known speakers by changing only the board in the existing config. Stop the
old service first and back up the tree, units and config; never print the config:

```sh
ssh pi 'systemctl disable --now cadence.service; cp -a /opt/cadence /opt/cadence.pre-rpi-screen; \
  cp -a /etc/systemd/system/cadence.service /root/cadence.service.pre-rpi-screen; \
  cp -a /etc/cadence/config.json /root/config.json.pre-rpi-screen; \
  sed -i "s/\"board\":\"rpi-bt\"/\"board\":\"rpi\"/" /etc/cadence/config.json'
```

Install the debs as above, then:

```sh
ssh pi 'systemctl daemon-reload && systemctl enable --now cadence.service'
```

The old `rpi-bt` tree can be restored from `/opt/cadence.pre-rpi-screen` together with its unit and config backup if a
rollback is needed. Remove the package first so its service does not remain enabled.

### New enrollment

The code is read from stdin, never an argument or a log:

```sh
ssh -t pi '/opt/cadence/bin/cadenced enroll --server https://<v2-host> --board rpi \
  --config /etc/cadence/config.json --data-dir /var/lib/cadence \
  --audiod-binary /opt/cadence/bin/cadence-audiod'
```

Set the config to mode 600, then run `systemctl daemon-reload` and enable `cadence.service` as above. A device-first
`cadenced claim --board rpi` is also supported.

## Check

```sh
ssh pi 'systemctl status cadence.service cadence-screen.service; \
  journalctl -u cadence.service -u cadence-screen.service -n 80 --no-pager'
```

The daemon reports `display.screen`, `display.cec`, `bluetooth` and (when BlueALSA has a2dp-source)
`bluetooth.output`; the screen's and CEC's are unavailable while no HDMI sink is plugged in (above). CEC is enabled
by default; with no sink the kernel keeps cadenced's one playback claim pending until a sink brings a physical address
(`cec-ctl` shows `Logical Address: Not Allocated`), and cadenced claims only an adapter nobody configured. `{"enabled":false}` in the `display.cec` capability config stops all
transmission while remote keys still arrive. Power, volume and active-source commands are authenticated dashboard/API
presses, never agent actions on a real device.

`cadence-screen` needs `/dev/dri` and the input/video/render groups supplied by its unit. A running Wayland or DRM
compositor can hold DRM master and keep KMS/DRM from starting; Lite needs none. The unit is intentionally separate
from `cadenced`, which only starts and stops it (`systemctl reset-failed`, then `start --no-block`; `stop --no-block`):
a screen crash does not restart the device connection, and a restarted daemon starts or stops the screen again after
its first HDMI read.
