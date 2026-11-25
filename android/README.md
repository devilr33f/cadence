# Cadence on Android TV

The `android-tv` distro (`deploy/distros/android-tv.json`): one APK for the Xiaomi TV Stick 4K (Android 11,
armeabi-v7a, Mali-G31). Build it with:

```sh
make dist DISTRO=android-tv
# -> build/dist/cadence-<version>-android-tv-armeabi-v7a.apk
```

`deploy/builder/dist.sh` builds libairplay (`make libairplay`) and our `libmpv.so` with `ao_cadence` (`make mpv
MPV_TARGET=android-armeabi-v7a`) first. Then it runs `deploy/android/Dockerfile`, which is pinned throughout: NDK r29,
SDK cmdline-tools 19.0, platform 36, build-tools 36.0.0, Gradle 8.14.7, AGP 8.13.2 (`android/build.gradle`), Temurin
JDK 17 and Go 1.27. Gradle only compiles Java and packages; every native library is built before it runs. The APK is
signed with a debug key kept outside the repo: `~/.config/cadence/android-debug.keystore`, or the path in
`CADENCE_ANDROID_KEYSTORE`. `deploy/android/keystore.sh` makes it the first time and keeps it, so each new APK installs
over the last.

## What runs where

| Process | What |
|---|---|
| `dev.cadence.tv` (default) | `CadenceActivity` (SDLActivity, `libmain.so`): cadence-screen with the routed UI (`makeRoutedUi()`, the same routes as Linux). Its UI is on SDL's translucent surface; video is on a `SurfaceView` below it. mpv draws there (`vo=mediacodec_embed`) and plays its audio through `ao_cadence` into audiod's mixer (route C): the ring arrives as a `RingOffer` on audiod's tap `@cadence-tap`. AirPlay mirroring uses the same surface: the mirror presenter decodes the tap's `MirrorFrame`s with MediaCodec and releases each frame at its presentation time (`releaseOutputBufferAtTime`). Both scale to fit the surface, so the screen sizes the `SurfaceView` to the picture's shape (`screen/player/video-fit.h`: mpv's `video-params`, the codec's output format; fitted to the overscan's safe area and centred, black bars around it) through `CadenceActivity.videoRect`. The UI's rcss and fonts ship in `assets/ui` and are copied to `files/ui` when the APK's `MANIFEST` changes (`UiAssets`). Images come from the server's proxy: plain http through the screen's own client, https through `ImageFetch` (HttpURLConnection, the system's CA store). The server's address is `LinkStatus.server_url`. Before the first one arrives, it is `serverUrl` from cadenced's `files/cadence/config.json`. `AssistService`/`AssistSessionService`: the remote's G button becomes the screen's assistant key. |
| `dev.cadence.tv:daemon` | `DaemonService`, a foreground service. It hosts audiod's engine on a native thread (`libcadence-daemon.so`: AAudio low-latency output, a software gain, a PCM ring per MIXER session, the libairplay receiver with AMediaCodec AAC and Apple's ALAC, no URL source, and Soloist run plain: Spotify's arm64 build exec'd through its own runtime's loader as another child of the app's uid, its audio over the pulse shim's socket into audiod's mixer). It listens on `@cadence-audiod` and serves the tap on `@cadence-tap`. libairplay's records and `_cadence._tcp` go to NsdManager (`cadence_audiod_airplay_dnssd(cadence_android_dnssd_backend())`, set before audiod starts). It holds the wake, Wi-Fi and multicast locks. Before audiod starts it also writes the CA bundle from the system's certificates (`CaBundle` → `files/cadence/ca-bundle.crt`) and unpacks soloist's arm64 runtime from the APK's assets (`SoloistRuntime`, `assets/soloist-runtime` → `files/cadence/soloist/runtime`). |
| child of `:daemon` | `cadenced` (`libcadenced.so`, a `GOOS=android` cgo build exec'd from the native library dir), restarted with a backoff. It dials audiod at `@cadence-audiod` and listens for the screen at `@cadence-screen`. Before it is enrolled it runs `cadenced claim` once `files/cadence/server` holds the server's URL. It downloads soloist's build from Spotify's CDN into `files/cadence/soloist` as the speakers do, once a config enables `media.soloist` with its API key. |
| child of audiod | soloist, only while `media.soloist` is enabled on a kernel that runs arm64: `<runtime>/lib/ld-linux-aarch64.so.1 --library-path <runtime>/lib/aarch64-linux-gnu:<runtime>/usr/lib/aarch64-linux-gnu files/cadence/soloist/soloist`, preloaded with the runtime's `libcadence-resolv.so`, which answers its name lookups over `files/cadence/run/soloist/resolv.sock` through audiod's (bionic's) resolver — a glibc binary on Android has no `resolv.conf` and may not use `127.0.0.1:53`. Its keys and volumes follow the speakers' rules: never logged, never in a status. |

Both processes run as the app's one uid. Abstract socket names have no file mode, so audiod (control socket and tap)
refuses a peer of any other uid (`SO_PEERCRED`, `tap_client_refused`). SELinux's per-app categories keep other apps
out as well.

`BootReceiver` starts the daemon service at boot, but only once the app has been opened at least once. The app moves
one system value, the media volume (STREAM_MUSIC), which is the device's level (below); it changes no system setting
otherwise.

## The level is the media volume

There is one level. cadenced owns it (stored in `files/cadence/state/volume`, set by voice, the dashboard, Home
Assistant, AirPlay senders and the remote), and on this board it *is* android's STREAM_MUSIC volume
(`OutputConfig.system_volume`, `device/boards/android-tv`):

- cadenced's level 0-100 maps to the nearest stream index (`media/audiod/system-volume.h`; on a stream of 15 steps 30
  is index 5, and index 5 stays 30, never 33). audiod's engine writes it with `setStreamVolume` (no flag: android
  shows no panel, the screen shows Cadence's overlay) through `SystemVolume.java` and `android/native/volume-jni.cpp`,
  and reads it back; the playback gate opens only once it holds (rule 7). audiod's own gain stays at unity, so the
  mix leaves audiod whole and android's mixer applies the one attenuation.
- a change the system makes itself (the remote's keys while another app is in front, another app, the TV's HDMI-CEC
  volume) comes as android's volume broadcast (and the volume settings' observer as a fallback); audiod reads the
  stream again and reports it (`SystemVolume`), and cadenced stores it as the level, so the server, the dashboard and
  the overlay see what the TV plays at. cadenced's echo of it writes nothing back, so a held key is never pulled back.
- while Cadence is in front, the remote's volume keys are android's own one-index step (`adjustStreamVolume` from
  `CadenceActivity`, without android's panel), read back as above; mute toggles the stream's mute, which the overlay
  shows and which never moves the level.
- at each start audiod tells cadenced where the stream stands, and cadenced reconciles once without a raise: a stored
  level above the stream's comes down to it, one below it stays and lowers the stream; a fresh install takes the
  stream's level.
- a fixed-volume stream (HDMI-CEC passthrough, `isVolumeFixed`) has no index to move: the keys stay android's (sent to
  the TV), and the level is audiod's software gain over the mix, as before.

## Install (owner-attended, rule 7)

Agents never install, play audio, cast, pair or use adb on the stick. Before you start, set the TV to a low volume.

```sh
adb connect <device-ip>:5555
# over the previous apk: same package and debug key, so files/cadence (config, token) stays
adb install -r build/dist/cadence-<version>-android-tv-armeabi-v7a.apk
adb shell am start -n dev.cadence.tv/.CadenceActivity        # starts the daemon, out of the stopped state
```

If `adb install -r` answers `INSTALL_FAILED_UPDATE_INCOMPATIBLE`, the old APK was signed with another key. Uninstall it
first (this removes the enrollment) and claim again.

### Enroll: claim on the first run

An APK that is not enrolled yet shows the claim (a code and a QR) once it knows the server. Give it the server's URL.
This is one file in the app's own data, written with `run-as` (the APK is debuggable), never a code in argv:

```sh
adb shell run-as dev.cadence.tv sh -c 'mkdir -p files/cadence && echo https://<cadence-v2-host> > files/cadence/server'
```

Within about 2 s the daemon stops its waiting cadenced and runs `cadenced claim` (logcat: `cadence-daemon:
cadenced_claim`). The TV shows the code. Approve it on the dashboard's claim page, or type a dashboard enroll code
on the TV. cadenced writes `config.json`, exits, and starts as the enrolled daemon. The file `server` is read only
while no `config.json` exists.

The old route still works: `cadenced enroll --code` over `run-as`, as before. The code then sits in argv inside the
app's own uid, an open item in AGENTS.md.

```sh
adb shell run-as dev.cadence.tv sh -c \
  'lib=$(dirname "$(pm path dev.cadence.tv | cut -d: -f2)")/lib/arm; \
   "$lib/libcadenced.so" enroll --server https://<cadence-v2-host> --code <code> --board android-tv \
     --config files/cadence/config.json --data-dir files/cadence'
```

Logs (ids, codes and timings only): `adb logcat -s cadence-daemon cadenced cadence-audiod cadence-screen cadence-nsd`.

### The remote's G button: Cadence as the default assistant

First save the current values, so you can put them back. `settings get` answers `null` for an unset one and adb shell
ends its line in `\r`: both are kept out of the file, and the restore deletes what was unset instead of writing the
word `null` into the setting:

```sh
for k in assistant voice_interaction_service voice_recognition_service; do
  printf '%s=%s\n' "$k" "$(adb shell settings get secure $k | tr -d '\r')"
done | tee tv-assistant-settings.before
```

Then select Cadence:

```sh
adb shell settings put secure assistant dev.cadence.tv/.AssistService
adb shell settings put secure voice_interaction_service dev.cadence.tv/.AssistService
adb shell settings put secure voice_recognition_service dev.cadence.tv/.AssistRecognitionService
adb shell dumpsys voiceinteraction | grep -i ComponentName     # shows dev.cadence.tv/.AssistService
```

Android 10+ may put the role holder (Google's Katniss) back on its own. If the settings do not stick, also move the
role: `adb shell cmd role add-role-holder android.app.role.ASSISTANT dev.cadence.tv`.

Revert (each saved value goes back exactly; an unset one is deleted again):

```sh
adb shell cmd role remove-role-holder android.app.role.ASSISTANT dev.cadence.tv   # only if you moved it
adb shell cmd role add-role-holder android.app.role.ASSISTANT com.google.android.katniss
while IFS='=' read -r k v; do
  case "$v" in null|'') adb shell settings delete secure "$k" ;; *) adb shell settings put secure "$k" "$v" ;; esac
done < tv-assistant-settings.before
```

### Uninstall

```sh
adb uninstall dev.cadence.tv
```

This removes the app and its data (cadenced's config and token). Revert the assistant settings first: Android falls
back to its own if the component is gone, but the saved values are the exact ones.

## What the owner checks on the stick

Set a low level on the TV first. Agents never do any of this (rule 7).

1. **UI**: Home with its rows, Search, a title page, the drawer (G button), settings. Text is set in Inter, with icons.
   Logcat has no `ui_assets_failed`, and `files/ui/.manifest` matches the APK's `assets/MANIFEST`.
2. **Images**: posters and backdrops load over https (logcat has no `tls` and no `no_server` codes in the image
   lines). Before cadenced sends `server_url`, they come from the server in `config.json`.
3. **A catalog play with route C sound**: a title from the catalog plays, and its sound comes through audiod's mixer.
   Logcat shows `tap_client_connected`, `tap_ring_offered`, and no `tap_ring_unused` at the start.
4. **G end-to-end**: with Cadence in the TV's room with a Cadence speaker (the TV and the speaker in one room on the
   dashboard), press G. The drawer opens on the TV at once, says «Слушает: <the room's speaker>», and the speaker's
   microphone opens (its LED degree dot): speak, the transcript and the steps appear on the TV, the reply sounds from
   the speaker and is shown on the TV word by word. BACK on the remote during listening stops the turn. Pressing G
   while the app was killed (or right after the TV's boot, before Cadence was opened) starts the app and still opens
   the assistant: the press is held until the screen's link is up. Logcat shows `assistant_invoke code=ok` on the
   server's log and no `mic_open` refusal on the speaker's.
5. **Ducking**: with a film playing, press G and speak a turn. The film ducks under the reply and comes back after.
6. **AirPlay audio from an iPhone**: with a low level set on the TV, pick the stick in Control Center. The PIN shows
   if the server asks for one. Music plays through audiod (`airplay` lines in `cadence-audiod`), and a voice turn
   displaces or ducks it.
7. **AirPlay mirroring**: start Screen Mirroring. The badge shows, and the iPhone's screen appears. Logcat shows
   `mirror_decoder_configured codec=h264` (or `h265`), `mirror_first_output` (`ahead_ms` within a second; `unsynced=true`
   means the presentation clock did not fit the video, so frames are shown as they come), `mirror_stats` every 10 s,
   and on its end `mirror_session_ended` with `rendered` near `decoded` and few `late`, `unsynced` or `dropped` frames
   (`dropped`: the surface took the codec away, `mirror_surface codec_lost=true`, until the next keyframe). Lip-sync
   is ≤ 40 ms with a video playing on the phone. Stop it from the phone: the surface goes back to mpv (play a film
   after it). A film started while mirroring ends the mirror (`mirror_displaced_by_play`).
8. **G button settings**: saved and restored exactly, as below.
9. **Boot**: after a reboot, the daemon is running (`daemon_started`) and the screen opens on launch.
10. **One level** (set the TV low first): logcat shows `system_volume_started fixed=false` and audiod's
    `system_volume level=N` and `playback_opened ... "volume":"system"`. The first start after this update reconciles
    without a raise: the dashboard's level becomes the lower of the stored one and the stream's. The remote's volume keys
    with Cadence in front move the stream one step each, with Cadence's overlay and no android panel; the dashboard's
    level follows (`system_volume_changed`). A dashboard or voice level moves android's media volume (Settings → Sound
    shows it), and the overlay shows. Mute shows the muted overlay, and unmute brings the same level back. With another
    app in front, its volume keys move the level too. A held key never bounces back.

### Spotify Connect (Soloist, owner-attended; agents never play or pair on it, rule 7)

Soloist needs the kernel to be 64-bit: Spotify builds it for arm64 alone, and the stick's userland is armv7 (no
64-bit abi, no `/system/bin/linker64`), so the arm64 build runs beside the app as on midi. **`uname -m` cannot answer
this on the stick**: every shell and cadenced itself are 32-bit processes, and the compat personality renames
`aarch64` to `armv8l` for them. cadenced asks the kernel four ways (uname, uname with the personality cleared,
`/proc/sys/kernel/arch`, and exec'ing the runtime's own aarch64 loader, whose ENOEXEC is a 32-bit kernel's answer),
trusts each only for a yes, and logs which one answered:

```sh
adb shell uname -m          # armv8l: the 32-bit shell's view, the same on either kernel
adb logcat -s cadenced | grep soloist_kernel      # how=personality|probe says the kernel runs arm64; how=refused says it cannot
adb shell run-as dev.cadence.tv ls files/cadence/soloist/runtime/lib   # the runtime the app unpacked
```

A static arm64 binary run from `/data/local/tmp` also answers it (the owner checked: it runs).

Then, with a low level set on the TV:

1. **The capability is there**: the dashboard's device page lists `media.soloist` (state `off`). Set the API key and
   enable it. cadenced downloads the build from Spotify's CDN (state `no_build` → `starting`); the first download is
   about 30 MB and needs the network up.
2. **The receiver appears**: a Spotify app on the phone lists the TV under the device's dashboard name (a Cyrillic
   one included). Pick it, play: sound comes through audiod's mixer (logcat `soloist_start`, `soloist_answered`,
   `tap`-side `airplay`-style lines, and `media_player` sessions on the server), and the TV screen shows the track's
   title and cover as other media sessions do. The speaker-level slider in the Spotify app moves the device's level
   (rule 7's sender policy), and the device volume changes what the Spotify app's slider shows.
3. **Its line is watched**: soloist's own output ships to the server's log store as the speakers' does
   (`soloist` source), scrubbed; a wrong key shows `soloist_auth_failed`.
4. **A voice turn over it**: speaking a turn while Spotify plays ducks it and gives the reply the mixer, then Spotify
   comes back.
5. **Name changes**: rename the device in the dashboard; the Spotify app sees the new receiver name within a few
   seconds (audiod restarts soloist for it).
6. **DNS**: soloist's lookups go through the preload → audiod → netd. If playback stalls at `starting` with
   `soloist_silent` in the log, and soloist's own lines show name-resolution errors, that path is the suspect
   (logcat: `cadence-audiod`'s `soloist_resolv` lines are absent on failure). On a network with Private DNS in
   strict mode, resolution still goes through netd, so it should hold.

### HDMI-CEC (`display.cec`, owner-attended; agents never move it on a real tv, rule 7)

The framework owns HDMI-CEC on the stick and every `HdmiControlService` call is walled behind the
signature-level `HDMI_CEC` permission (checked on the calling uid at the binder, so no reflection or hidden-api
exemption reaches it), so the app serves what an app may do over the `cadence-cec` socket for cadenced's
`display.cec` (DaemonService's `CecBridge`):

- **Volume** rides the one public API on the CEC path: while the TV holds the volume (the stick's
  `STREAM_MUSIC` is fixed — `SystemVolume`'s `isVolumeFixed()`), `AudioManager.adjustStreamVolume` itself
  becomes the CEC volume key pair at the AVR (while system audio is on) or the TV. When the stream is not
  fixed, the volume command answers `not_fixed`: that volume is cadence's own level (`audio.output`), not
  CEC's, and is not moved twice.
- **Power** needs a caller the system signs for: an app may not inject input, and the framework only sends
  One Touch Play on the HOME key and `<Standby>` on SLEEP (auto-device-off). `AdbShell` is a minimal adb
  client for the stick's own adbd on `127.0.0.1:5555` (on tcpip, as the install above needs): `screen on`
  is `input keyevent 224` + `input keyevent 3`, `screen off` is `input keyevent 223`.
- **The device list** is what the framework knows: a TV at 0.0.0.0 whose power comes from
  `dumpsys hdmi_control`'s `mPowerStatus` (over the same adbd pairing, refreshed at the cadence cadenced
  asks). No vendor or OSD name (the dump holds none at this level), no AVR, no system-audio switch and no
  audio-status readback: on this Android level those are the framework's alone (`cmd hdmi_control setsam`
  and the network dump arrive with 12).

One-time, owner-attended (the TV must be on):

1. `adb shell getprop ro.build.version.release` — note the level (10/11 behave the same here; 12+ could take
   the fuller route above).
2. The first `power` press (dashboard → the device page's HDMI outputs card) makes the app connect to its own
   adbd with a key it generated (`files/cadence/adb.key`); the TV shows the USB-debugging confirm — allow it
   once with *always allow* (the key is sent in adb's own format, so the one confirm persists across reboots;
   an unanswered dialog backs off and re-asks at most every half hour, not every poll). Until then, and
   whenever adbd is off, `power` answers `no_target` and the state's `tvPower` stays `unknown`; volume works
   either way.

Then check: the dashboard's card lists the TV with its power; volume keys move the TV's own volume (and the
TV's remote still does); power off stands the TV by and a power on brings it back on the stick's input; the
state's power follows the TV's own remote within about half a minute; with the TV's volume handling off (the
stream not fixed), volume answers `not_fixed` and the level is the stream's as before.

## Gaps

- `volume.mode` is not consulted on this board: its level is always STREAM_MUSIC (above). A step of 5 from voice or
  the dashboard lands on the nearest of the stream's indexes, so on a 15-step stream some steps move nothing audible.
- Level 0 is the stream's minimum index (`getStreamMinVolume`), which android may hold above silence.
- `server_url`: cadenced fills `LinkStatus.server_url` (contract batch 4). The screen uses `config.json`'s
  `serverUrl` only until the first LinkStatus arrives.
- A mirror unit the tap client cannot join (a lost or out-of-order fragment) is dropped without telling the presenter.
  The decoder then gets the next P-frame, so the picture can smear until the next keyframe. `MirrorJoin.dropped()`
  could feed `MirrorSchedule::lost()`.
- HTTPS images trust the system's CA store only (targetSdk 28: user CAs are not trusted). A server behind a private
  CA needs a `network_security_config` that trusts user CAs. cadenced's own link has the same limit.
- The mirror presenter has no drift correction beyond the clock's 4 updates a second, and no sender remote control.
- No capture is configured: the remote's microphone gives an app nothing (S4b). `AAudioCapture` is built and tested
  for a USB microphone later, and the app does not ask for `RECORD_AUDIO`.
- arm64-v8a is in the manifest, but only armeabi-v7a is built.
- **Soloist's kernel check**: cadenced gates `media.soloist` on the kernel's ability to exec aarch64, asked four
  ways (uname as the process sees it, uname with PER_LINUX32 cleared, `/proc/sys/kernel/arch`, and the runtime's
  loader exec'd with `--version`), each trusted only for a yes: the compat personality renames the machine to
  `armv8l` for the 32-bit process, so uname alone can never say it. The app may pass
  `CADENCE_ANDROID_KERNEL_MACHINE` as its own word. If the stick's kernel is 32-bit, soloist cannot run at all —
  Spotify ships no armhf build — and the capability stays away with one `soloist_kernel_refused` line (and
  `how=unknown` when nothing could answer, a missing runtime among them).
- **Soloist's resolver is an interposition**: the preload answers `getaddrinfo` by asking audiod over a unix socket.
  A soloist that resolved names by its own means (a statically linked resolver reading `/etc/resolv.conf`, UDP DNS
  to `127.0.0.1:53`) would bypass it and fail to resolve; nothing on Android can serve that port for an app. If
  soloist stays silent with its own DNS errors in the stored log, this is why, and the owner reports it back.
- **Soloist's runtime rides the APK**: `assets/soloist-runtime` adds about 6 MB to the APK, unpacked once into
  `files/cadence/soloist/runtime` (about 15 MB on `/data`), replaced on an app update. It is Debian's trixie glibc
  plus our shim and preload, with their licences, as the speakers' tarballs ship it.
