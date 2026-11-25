# AGENTS.md

Context for coding agents and new contributors. Read it before changing anything. [README.md](README.md) has the
overview. `docs/superpowers/`, `docs/plans/` and `.superpowers/` are local, git-ignored working notes: they may be
missing, so this file carries the essentials. `docs.local/` (git-ignored, may be missing) holds a maintainer's own build
and deploy steps for their own servers and devices: paths and commands, never secret values. Committed docs name no
personal host, address, key path or registry: use placeholders (`<host>`, `<device-ip>`, `<registry>`). `main` is v2; v1 lives only in the signed tag `v1-final` (its knowledge in
`git show v1-final:AGENTS.md`).

## Purpose

Cadence v2 is a self-hosted voice assistant platform for many devices:

- one Go server (`cadence-server`) serves N devices;
- each device runs a device-agnostic Go daemon (`cadenced`) with a board package, plus a C++ audio engine
  (`cadence-audiod` on `libcadence-media`);
- a dashboard (TanStack Start + shadcn) configures providers, devices, capabilities, agent modes and memory, and
  shows live sessions, agent steps, tasks, logs and metrics.

Success:

1. A `yandex-pickle` runs v2 and passes the cutover gate (§1.1), so v1 can be switched off.
2. A second board (VK Capsula Mini or an RPi) enrolls with the one-line command, and the only new code is its board
   package: no change to core, the server, the protocol or the dashboard.

Cutover gate (spec §1.1, owner, Q1):

v2 must have, on a pickle: voice loop with follow-up; Home Assistant; Hermes and tasks; Soloist; AirPlay; URL
playback; LEDs; buttons and volume; alarms and timers that ring offline; weather and Exa web search; command words
(`тише`, and `стоп`/`хватит`/`пауза` during a ring).

After the gate: Bluetooth A2DP, the dashboard "update daemon" button, more boards (midi, VK Capsula Mini, RPi),
display/touch.

`§` references and `owner, Q1–Q3` sources in this file point to the local, git-ignored v2 spec
`docs/superpowers/specs/2026-11-07-cadence-v2-design.md`, which may be missing; this file carries the essentials.

## Hard rules

1. **No vendor OTA.** Each board's hardening disables it; never touch bootloader, radio, eFuse, keys or raw flash.
   Updating our own daemon or Soloist is not OTA.
2. **Recovery access stays.** adb and/or ssh is verified before and after hardening on every board; the installer
   aborts rather than leave a device without it.
3. **Rootfs writes only in hardening steps**, each idempotent, backed up and reversible; where root is read-only by
   default it is remounted read-only on every exit path. (A pickle's root may be left rw by its owner's choice;
   the scripts still restore read-only.)
4. **The vendor assistant stays off** (Quasar on Yandex, `voice-agent` and `fwupdater` on VK).
5. **Vendor material:** vendor libraries and models are loaded from the device at runtime and never committed or
   redistributed; the owner's command model is pushed from outside the repo; Soloist is downloaded by the device and
   run unmodified; the trixie runtime packages are fetched with pinned hashes; never upload firmware, `/data` secrets
   or confidential artifacts. Open-source code under any licence, copyleft included, may be used where it is better
   or scales better (owner 2026-11-21): keep its licence text with what ships (`licenses/`) and note it in the
   change.
6. **Secrets:** provider keys and vendor secrets encrypted in the server DB; the device holds its token and the
   secrets pushed to it (mode 600); never printed, logged or asked for in chat.
7. **Owner silence:** the agent raises volume only on an explicit voice request in a wake/press session; alarms are
   set only in such a session, but a ring may be stopped or snoozed from anywhere (a follow-up, a screen, the
   dashboard, Home Assistant), as that only ends or puts off what such a session set, and timers and reminders without
   a level, which never touch the volume, may be set from a follow-up or the authenticated API (owner 2026-11-16); media senders move volume as in v1; authenticated API callers (HA, dashboard) may set
   volume, and so may a screen's explicit press on another device's card in its quick panel (`devices.panel`, owner
   2026-11-16), never on its own. Agents and tests never raise volume, set alarms, cast or pair on a real device: they use the `sim` board
   and fakes. audiod keeps playback and media children gated until the persisted volume holds (§7.5).
8. **Privacy:** own logs carry ids, codes, numbers and timings only; transcripts, agent messages and play-history
   metadata live in the DB for 14 days, readable only in the dashboard; music likes, dislikes and favorites are durable
   until cleared (owner 2026-11-18); other media metadata is memory-only; play history and favorites never store stream
   or signed urls, and durable song ids carry no access keys; third-party logs ship with secret scrubbing; process telemetry never
   includes command lines; audio is never stored except under an opt-in debug flag that is turned off afterwards.

## Architecture

```text
dashboard (TanStack Start) ─┐ REST (OpenAPI) + WS live feed
HA custom integration ──────┤
                            ▼
              cadence-server (Go, Fiber v3) ── Postgres 17 + pgvector
               providers: STT/TTS/LLM/embed/weather/search
               integrations: HA, MCP, Hermes
               telemetry: logs, metrics -> store + OTLP export
                            │ WS /v2/device: protobuf frames, device token, TLS verified
                            │ HTTPS: /v2/otlp/v1/{logs,metrics,traces}, PUT /v2/artwork/{sha256}, GET /v2/session-artwork
                            ▼
  cadenced (Go, static) ══ unix SOCK_SEQPACKET, protobuf ══ cadence-audiod (C++, libcadence-media)
   board package, config sync, session,                      audio I/O, VQE, wake, Opus, PCM backend,
   policy, alarms, LEDs, buttons, telemetry                  URL, Soloist (jail), AirPlay, child logs
```

Layout:

```text
proto/                          protobuf: cadence/v2 (device<->server), cadence/audiod/v1 (cadenced<->audiod),
                                cadence/sync/v1 (audiod<->audiod: a multiroom group's stream)
                                buf generate -> Go (pkg/), C++ (media/), golden fixtures (proto/fixtures/)
pkg/                            Go shared by server and device: generated proto, capability schemas, privacy-safe logging,
                                tracectx (the W3C trace context a turn's frames carry), kws (the custom wake word: openWakeWord's
                                front end and a per-word head in pure Go, a neon kernel on arm64; inference and training;
                                assets/: the converted feature models with their NOTICE)
server/                         cmd/cadence-server, internal/{gateway,devices,enroll,voice,prompts (the spoken error prompts' table and renderer),alarmview (the alarms topic's events, the screens' alarms summary and alarms.act),providers,agent,tasks,subscriptions,memory,history,favorites (durable music likes, dislikes and favorites),mcpserver (Cadence served as an MCP server at /api/v1/mcp: the agent's own tools run as a pressed call, beside devices, playback, links, say and typed turns; an mcp token names the tools it sees),
                                integrations/{ha,mcp,hermes,t3code,torrplayer,torrserver},catalog (the TV catalog, with
                                catalogscreen and ytcatalog),images (the image proxy),presence,telemetry,tracing (spans in memory, live
                                and recent, the devices' merged in, exported over otlp; its package doc is the
                                instrumentation and privacy contract),api,store,events,
                                groups (multiroom groups and stereo pairs: clock master and stream source election, each
                                member's media.group config),follow (a group's other screens opening its video),yandex (the optional owner-managed station provisioning proxy and vendor log capture),
                                wakewords (the custom wake words: their models, training jobs, the negatives' fetch, the
                                device's model route)}
device/                         cmd/cadenced, cmd/cadence-install, internal/{core,link,config,session,policy,alarms,leds,
                                telemetry,install,discovery,group (media.group: a group's config and key to audiod, its status back),
                                cec (display.cec: hdmi-cec's wire, scan and flows behind a Bus seam; linux /dev/cec, a fake),
                                screen (the screen/v1 plane, tvboard, screentest), wifi (network.wifi: joins the network the
                                config names through a board's wpa_supplicant, never removing one, a switch counting only once
                                the server link is back over it, else rolled back with no_ip or no_server),
                                wakeword (wake.custom: the model fetched and cached, pkg/kws on audiod's MicPcm frames)},
                                boards/{sim,yandex-pickle,yandex-midi,
                                yandex-micro,vk-capsula-mini,sim-tv,rpi,android-tv}
media/                          C++: lib/ (libcadence-media; aaudio/: android's playback and capture over a stream seam,
                                tested against a fake; sync/: multiroom's group clock, stream, leader, member, playout
                                and calibration), audiod/ (embedded.h: audiod in a host's process), pulse-shim/
                                (clean-room libpulse.so.0), tests/
tools/kws/                      export.py: openWakeWord's released feature models (sha256-pinned) to pkg/kws's tensor file,
                                and onnxruntime's golden vectors for its tests (make kws-assets)
android/                        the android-tv app (Gradle, Java; README.md: what runs where, the owner's install steps):
                                app/ (CadenceActivity on SDL3, DaemonService with audiod in process and cadenced as its
                                child, NsdBridge, the assistant hooks, BootReceiver), native/ (libmain.so: cadence-screen;
                                libcadence-daemon.so: audiod's engine and libairplay's NsdManager dnssd backend)
dashboard/                      TanStack Start + shadcn (Base UI), own pnpm package: DESIGN.md (the visual system every page
                                follows), design-preview/, src/{routes (login, _authed: the session guard, devices/,
                                providers/, modes/, memory/, tasks/, integrations/, history/, logs/, traces/, settings/, tokens/), components
                                (shell/, data/, history/, ui/ vendored, login/, overview/, activity/, forms/: the json schema
                                renderer and raw json fallback, devices/: list, enroll/, detail/ (with health/,
                                conversations/ and inbox/), logs/: the stored lines and the live tail, traces/: the trace list, streams and a trace's waterfall, settings/: the
                                editor both scopes share, providers/: from the module catalog, with each instance's
                                test, modes/, memory/: suggestions, memories, learning log, tasks/: list and a task's
                                page, integrations/: home-assistant/, mcp/, hermes, subscriptions/, busy/, tokens/),
                                styles (tokens), api (fetch wrapper, ssr client, shared query keys, gen/: hey-api
                                client from server/openapi), lib (session, sign-out, household time zone, devices',
                                conversations', logs', metrics', providers', modes', tokens', settings', memory, tasks,
                                home assistant, mcp, subscriptions and busy queries and actions, history queries and source labels, live/: the live feed
                                client, its invalidation map and device deletions)},
                                serve/ (the image's node server: health, the build's files, start's ssr, a log line per
                                request), e2e/ (playwright specs against the dev stack)
integrations/home_assistant/    v2 custom component (Python): custom_components/cadence (the component), scripts/ (make
                                ha-package's zip, the e2e smoke), tests/ (pytest, the server's contract fixtures), uv.lock
deploy/                         server compose (server and dashboard on one host), the quickstart (quickstart/: the same
                                behind caddy, one setup script), the dashboard's image, per-board cross builders,
                                bootstrap script, CI helpers, the dev stack (its edge: deploy/dev/edge.yml), the github
                                mirror's sync (github/, gitlab only)
go.work                         server, device, pkg
```

## Decisions already made

Don't relitigate these without the owner:

| # | Decision | Source |
|---|---|---|
| D1 | Cutover gate as in §1.1 | owner, Q1 |
| D2 | UniProxy runs **server-side**, with credentials the device owns (a device-scoped secret); every device sends mic Opus to the server | owner, Q2 |
| D3 | Device = Go control plane (`cadenced`, static, `CGO_ENABLED=0`) + C++ audio plane (`cadence-audiod`), two processes over a unix socket | owner, Q3 |
| D4 | v1 archived as the signed tag `v1-final`; `main` is v2 (fast-forwarded on 2026-11-12, the `v1` and `v2` branches deleted) | approved §4, owner 2026-11-12 |
| D5 | Hybrid protocol: typed protobuf on the hot path, JSON + JSON Schema for every other capability payload | approved §5 |
| D6 | Server: Go, Fiber v3, `gofiber/contrib/v3/websocket`, Huma v2 (`humafiber.New`, OpenAPI 3.1), Postgres 17 + pgvector | approved §8 |
| D7 | Configuration lives in the DB (dashboard-editable); env holds bootstrap values only | approved §8 |
| D8 | Transcripts and agent messages are stored in the DB for 14 days, then only summaries and memories remain | owner, §8 |
| D9 | Unified telemetry: every device log (own, third-party, kernel) and system metrics go to the server over OTLP/HTTP; the server stores them and can export over OTLP | owner, §9 |
| D10 | Agent budgets are part of named agent modes (`fast`, `smart`, …), per-device defaults, switchable by voice ("Включи умный режим") | owner, §10 |
| D11 | Media metadata reaches the server and HA, kept in memory only and never logged, except play-history metadata stored for 14 days (D8) and music likes, dislikes and favorites kept until cleared, all without stream, signed or access-key-bearing urls/ids | owner 2026-11-16; favorites owner 2026-11-18 |
| D12 | Opus downlink is always 48 kHz fullband; no server-side resampling; bitrate and channels come from the device's `audio.output` | owner, §6 |
| D13 | A ring's stop and snooze need no woken session (follow-ups, screens, dashboard, HA); a snooze keeps the schedule and re-rings at the alarm's own level; ramp length, ramp start and ring limits are configurable globally, per device and per alarm | owner, 2026-11-16 |

## Commands

Run everything from the repo root (Git Bash on Windows). Host needs Go 1.27, make, curl and Docker (Docker Desktop on Windows).

| Task | Command |
| --- | --- |
| Regenerate proto (buf, pinned image) | `make proto` |
| Proto lint, format, generated-code drift | `make proto-check` |
| Rewrite golden frames after an intended proto change | `go test ./pkg/protofixtures -update` |
| Regenerate sqlc code (pinned image) | `make sqlc` |
| sqlc drift check | `make sqlc-check` |
| Regenerate the admin OpenAPI document `server/openapi/openapi.json` (`cadence-server admin openapi [-o path]`, no database) | `make openapi` |
| OpenAPI drift check (a gate of `make ci`; `make test` checks it too) | `make openapi-check` |
| The custom wake word's feature models and golden vectors (`pkg/kws/assets/features.bin`, `pkg/kws/testdata/golden.bin`): openWakeWord v0.5.1's `melspectrogram.onnx` and `embedding_model.onnx`, sha256-pinned and cached in `build/kws`, converted by `tools/kws/export.py` with numpy, onnx and onnxruntime pinned, in the pinned python image; only after a change to the script | `make kws-assets` |
| Go tests (needs Docker for testcontainers and the docker cli for the installer's busybox tests, which skip without one, or fail when `CI` is set; with a C compiler also `libopus-dev` + `pkg-config`, else the audio package builds its no-cgo stub), then the log pipeline's tests again as 386 (a 32-bit int, as midi's armv7) | `make test` |
| Device tests with `-race`, in the pinned golang image: the linux-only ones (audiod, sim; `make test` skips the audiod ones on Windows) and alarms, session, media, core, volume, soloist, telemetry, discovery (its real-multicast test), the screen plane (its seqpacket socket) and the screen boards; `-short` leaves the busybox tests (and the telemetry grpc dependency guard) to `make test`; go's caches in the docker volume `cadence-go-cache` (shared with `make lint` and other worktrees), so unchanged packages are not built or run again | `make test-linux` |
| Regenerate the dashboard's API client `dashboard/src/api/gen` from the committed OpenAPI document (pinned node image) | `make dashboard-client` |
| Dashboard gate (a gate of `make ci`, pinned node image, on a copy of `dashboard/`; pnpm's store and corepack's pnpm in the docker volume `cadence-pnpm-cache`): the image's node equal to `.nvmrc` and `engines`, frozen install, lint (`@antfu/eslint-config`), typecheck, unit tests (a `console.error` fails one; the enroll hint's test reads the pickle and midi readmes, copied beside the package), the build (its route tree equal to the committed one) and the client regenerated beside the committed one | `make dashboard-check` |
| Dashboard dev server on http://localhost:3000 (Node 24 and pnpm on the host; `/api` proxied to the dev stack with the browser's Origin, which needs `CADENCE_DEV_ORIGIN=http://localhost:3000 make dev-up`; `dashboard/README.md`) | `cd dashboard && pnpm install --frozen-lockfile && pnpm dev` |
| The dashboard's image (`deploy/dashboard/Dockerfile` on the Makefile's `NODE_IMAGE`: frozen install and build, then the build, its production dependencies and `dashboard/serve/` as `node` on a read-only root, port 3000; `CADENCE_API_INTERNAL` names the API's origin for its SSR calls; a server's `cadence-dashboard`, `deploy/server/README.md`) | `docker build -f deploy/dashboard/Dockerfile -t cadence-dashboard:dev .` |
| libopus tests (cgo, in the server image build stage: the encoder and decoder, and UniProxy's TTS decoding a fake's Opus; BuildKit runs them again when anything the stage copies changed, else takes the passed run from its cache) | `make test-cgo` |
| Go lint (golangci-lint v2, pinned image; its caches in the docker volume `cadence-go-cache`) | `make lint` |
| One Go package | `go test ./server/internal/api -run TestHealth -v` |
| The tv screen's simulator, built natively (macos or linux, no docker): homebrew cmake, ninja, sdl3, freetype; rmlui and stb fetched pinned into build/screen-sim/deps; desktop gl 4.1 core and a stream-framed unix socket on macos, a placeholder player without mpv. `build/screen-sim/sim` (the screen/README.md simulator section: the script commands, `screen-sim-run SCRIPT=...`, examples in screen/sim/examples) | `make screen-sim` |
| The simulator's own tests: the script parser's unit test, and a headless walkthrough whose screenshots are checked (the real ui booted against the fake cadenced) | `make screen-sim-test` |
| This machine as a screen device of a real server (macos or linux, native): cadenced on the `sim-tv` board and the real `cadence-screen` with mpv (`brew install mpv`); its config, token and data in `~/.config/cadence-sim` (`CADENCE_SIM_HOME`), never the repo; the first run pairs with `SERVER` (approve the code it shows in the dashboard), later runs reuse it; delete the directory to pair again; `SIZE` sets the window (`deploy/dev/sim-device.sh`) | `make sim [SERVER=https://...]` |
| Device cross-build check (cadenced for linux/arm64 and arm v7, cadence-install for windows and linux amd64, no cgo; compile-time asserts hold the kernel struct layouts midi's framebuffer and evdev read to each word size) | `make cross` |
| Tests of the dev scripts: the in-docker wrapper, the e2e's native jq (`deploy/dev/jq-native.sh` refuses a build with another SHA-256), `make ci` with fake gates (their order, side by side, whole output, a log that fills while the gate runs, the summary, a failure named), and the one origin's path split (`deploy/dev/dashboard-deploy.test.sh`: the server compose's Traefik labels and the dev edge give the server the same paths, every top-level route the server mounts among them (`server/internal/{api,app}`, `server/cmd`), each router on its own service and port and both on one entrypoint, the dashboard's image and the e2e's Playwright install build on `NODE_IMAGE`, and the official Playwright image is pinned by digest at the dashboard's `@playwright/test` version), the quickstart's `setup.sh` against a fake docker (`deploy/quickstart/setup.test.sh`: `.env`'s generated half, the database password kept, the dashboard password asked until the server took one, `MSYS_NO_PATHCONV` on docker's calls), plus the prebuilt cache's script (`deploy/builder/prebuilt.test.sh`, against a `file://` registry: key stability and sensitivity, a fetch's sha256 refusal, publish's token hygiene) | `make tools-test` |
| Home Assistant component (`integrations/home_assistant/`, M8-D10): ruff (lint and format check) and pytest with `pytest-homeassistant-custom-component` (no network: a fake server over TLS with a throwaway CA), in the pinned uv/Python 3.14 image (`HA_IMAGE`) with `uv.lock`'s exact pins; uv's cache in the docker volume `cadence-uv-cache` (shared with other worktrees), no Python on the host | `make ha-test` |
| Rewrite the Home Assistant contract fixtures (`integrations/home_assistant/tests/fixtures/*.json`: a device list, media sessions, each presence event, vitals, written from the real handlers and publishers) after an intended change to what the server answers; `make test` fails while they drift | `go test ./server/internal/app -run TestHAContractFixtures -update` |
| Home Assistant component as a zip for a manual install (M8-D11): `build/ha/cadence-ha-<manifest version>.zip` holding `custom_components/cadence/` (no bytecode or hidden file), which unpacks in Home Assistant's config directory; the same files make the same bytes | `make ha-package` |
| Home Assistant component as an init-container image: pinned BusyBox plus the component at `/cadence`, tagged `<HA_COMPONENT_IMAGE>:<HA_IMAGE_TAG>` (tag default `dev`; set `HA_COMPONENT_IMAGE` to your registry's repository), built natively and never pushed unless `HA_IMAGE_PUSH=1` | `make ha-image [HA_IMAGE_TAG=<tag>] [HA_IMAGE_PUSH=1]` |
| Regenerate the sim mic fixture and the URL test FLAC and e2e music (docker: espeak-ng, ffmpeg, libopus) | `make fixtures` |
| Device sim image (waits for `/var/lib/cadence/config.json`, which `cadenced enroll` writes) | `docker build -f device/Dockerfile.sim -t cadenced-sim:dev .` |
| C++ host build + ctest in the trixie builder (its ffmpeg ensured from the prebuilt cache, the named context `ffmpeg-mediahost`; `MEDIA_CTEST_ARGS` narrows the host and sim suites, e.g. `-R ^(trace\|capture):`), the host build again with audiod's trace marks off (`CADENCE_TRACING=OFF`, its trace suites alone), then Soloist's runtime for arm64 and amd64 (`deploy/builder/assemble-soloist-runtime.sh`: pinned trixie debs, the shim built for each cpu) and its script's tests | `make media-test [MEDIA_CTEST_ARGS=...]` |
| Soloist jail tests through the real jail (`ctest -L privileged`, `docker run --privileged`: root with CAP_SYS_ADMIN) | `make media-test-privileged` |
| AirPlay runtime (`deploy/builder/airplay.Dockerfile`): Shairport Sync and nqptp from pinned tarballs, Avahi from Debian's pinned source package (runs as root from `/tmp/cadence-airplay/avahi`), the rest from pinned trixie debs (`airplay-runtime.pins`), nqptp's tarball and Avahi's source package shipped in its `licenses/`, its script's tests and a smoke run of the four programs in an empty image, to `build/airplay/airplay-runtime-<arch>.tar`; fetched from the prebuilt registry by its recipe hash, built as before on a miss; amd64 in `make ci`, arm64 (pickle and midi) under emulation; armhf (`linux/arm/v7`, hard-float only) still builds, unused: an armhf glibc tries the time64 calls (`clock_gettime64`) first on every clock read, which midi's 4.9 kernel lacks | `make airplay-runtime [ARCH=arm64\|armhf]` |
| The prebuilt cache of the pinned third-party builds (`deploy/builder/prebuilt.sh`: our ffmpeg for pickle, airplay, the media host and the server image (`ffmpeg-server-<arch>`: one static LGPL program cross-built with zig, its stage in `server/Dockerfile`, which a plain image build compiles itself), midi's zig cross deps, the airplay and soloist runtimes, mpv): one upload per recipe in the project's generic package registry (git.shino.boo `devilreef/cadence`, anonymous downloads), keyed by the sha256 of every input; the make targets and `dist.sh` `ensure` what they need (fetch, else the same local build) and hand it to the dockerfiles as named build contexts (`ffmpeg-pickle`, `midi-deps`, `ffmpeg-airplay`, `ffmpeg-mediahost`, `ffmpeg-server`, `soloist`, `airplay`), so a cache hit compiles no ffmpeg and emulates nothing; mpv keeps its volume cache under it; `list` shows each key and whether the registry has it; `PREBUILT_PUBLISH=1` publishes what a build produced (the manual `prebuilt` ci job does all, on main); the first upload is the owner's: `GITLAB_TOKEN_FILE=<file> sh deploy/builder/prebuilt.sh publish <artifact>` (a project access token with api scope; never an argument, never printed) | `sh deploy/builder/prebuilt.sh [list\|key\|inputs\|fetch\|build\|publish\|ensure\|ensure-all] <artifact>` |
| The android-tv APK (`deploy/android/Dockerfile`, manual, not in `make ci`): libairplay and our `libmpv.so` first, then NDK r29 builds `android/native` (screen and audiod for armeabi-v7a, opus and speexdsp from pinned tarballs; audiod runs soloist plain, unjailed through the arm64 runtime's loader), cadenced as `GOOS=android` cgo, soloist's arm64 runtime ensured from the prebuilt cache and shipped as an asset the app unpacks into its data, libprojectM-4 built from `media/third_party/projectm`'s pins (shared, `lib/armeabi-v7a`) with its candidate milkdrop presets fetched into `assets/viz/milkdrop`, `deploy/android/check-libs.sh` checks every library's needs, and Gradle (AGP 8.13.2, JDK 17) packages, signed with the debug key outside the repo (`CADENCE_ANDROID_KEYSTORE`, else `~/.config/cadence/android-debug.keystore`, made once by `deploy/android/keystore.sh`), to `build/dist/cadence-<version>-android-tv-armeabi-v7a.apk`; installing it is owner-gated (`android/README.md`) | `make dist DISTRO=android-tv` |
| Pickle's tarball for the installer (`deploy/builder/device.Dockerfile`, manual, not in `make ci`): static cadenced, audiod built in Debian bookworm arm64 under emulation (pickle's glibc 2.36, OpenSSL 3.0), its FFmpeg ensured from the prebuilt cache (the named context `ffmpeg-pickle`), Soloist's and AirPlay's arm64 runtimes (also ensured), the CA bundle, the boot wrapper `share/cadence-run` with pickle's `share/board-hook` (it blanks the lights), `licenses/` and a `MANIFEST` (versions, SHA-256 per file), refused by `check-device-limits.sh` if anything run with pickle's loader needs a newer glibc or OpenSSL, libstdc++ or an unknown library, and by `check-device-tree.sh` if a file the install needs is missing, to `build/device/cadence-<version>-yandex-pickle-arm64.tar` | `make device-build BOARD=yandex-pickle` |
| Midi's tarball (same Dockerfile, manual): as pickle's, but audiod builds against cross deps ensured from the prebuilt cache (the named context `midi-deps`: FFmpeg, OpenSSL 3.0 for `bin/lib` from its release tarball, and the armhf prefixes), cross-built with a pinned zig for midi's glibc 2.30 (`deploy/builder/build-midi-audiod.sh`, `zig-armhf.sh`: no Debian with a C++20 gcc has a glibc that old; libc++ linked in, libasound linked against an unversioned stub of bookworm's), then checked under `linux/arm/v7` emulation: cadenced static `GOARM=7`, audiod with the device's `/lib/ld-linux-armhf.so.3` and the DT_RPATH `$ORIGIN/lib:/system/vendor/quasar/libs` (patchelf; the stock library it dlopens finds its own dependencies there), Soloist's arm64 runtime and AirPlay's arm64 one (midi's 64-bit kernel runs both; audiod, built for armhf, takes the runtime's aarch64 loader); `check-device-limits.sh --machine ARM` refuses anything needing a glibc newer than 2.30, and a 64-bit, big-endian or soft-float ELF, a dynamic one that is not hard-float, another loader, and the vendor directory anywhere but audiod's DT_RPATH or ahead of `bin/lib` there; midi's `share/board-hook` suspends the stock assistant before every start of cadenced and at every poll while it runs, and has the wrapper set the persisted level again after a re-suspend (M2m-D2, M2m-R5, M2m-R7); to `build/device/cadence-<version>-yandex-midi-armhf.tar` | `make device-build BOARD=yandex-midi` |
| VK Capsula Mini's tarball (same Dockerfile, manual, not in `make ci`): Amlogic A113X, Buildroot, kernel 4.9.113, arm64 on glibc 2.28 (the oldest firmware of the family); cadenced static, audiod cross-built with zig for aarch64 glibc 2.28 (`deploy/builder/build-vk-audiod.sh`, `zig-aarch64.sh`: no Debian with a C++20 gcc has a glibc that old) against cross deps ensured from the prebuilt cache (the named context `vk-deps`: FFmpeg and OpenSSL 3.0.22 for `bin/lib`, the firmware has 1.1), with the «Маруся» keyword engine (TFLM with CMSIS-NN; audiod about 2.55 MB; the model is the device's own, never shipped), `runtimes none` (no Soloist, no AirPlay), the board hook `share/board-hook` (stops the stock assistant and the platform daemons, owns the watchdog), checked by `check-device-limits.sh --board vk-capsula-mini --machine AArch64` and `check-device-tree.sh`, about 25 MiB, to `build/device/cadence-<version>-vk-capsula-mini-arm64.tar` | `make device-build BOARD=vk-capsula-mini` |
| The rpi deb (`deploy/builder/rpi.Dockerfile`, manual, not in `make ci`): one Raspberry Pi 4/5 on Debian 13 trixie arm64 with cadence-screen on HDMI through SDL's KMS/DRM driver (no Wayland or gamescope; optional: cadenced starts and stops `cadence-screen.service` as an HDMI sink comes and goes, the deb's `postinst` drops the old unit's enablement), HDMI-CEC, audiod on HDMI or an active Bluetooth speaker and libairplay; static cadenced and native audiod/screen, with the pinned mpv `ao_cadence` build's `libmpv2` installed beside it, libprojectM-4 in `/opt/cadence/lib` and the candidate milkdrop presets in `/opt/cadence/share/viz/milkdrop`, to `build/dist/cadence-<version>-rpi-arm64.deb`; installing and enrolling are owner-gated (`deploy/rpi/README.md`) | `make dist DISTRO=rpi` |
| List the Cadence devices on the local network (each enrolled cadenced advertises `_cadence._tcp` over mDNS with its id, board and version; `network.discovery` switches it per device; the query is a legacy one from a port of its own, so no privileges and no port 5353; Bonjour's `dns-sd -B _cadence._tcp` and `avahi-browse -r _cadence._tcp` see them too) | `go run ./device/cmd/cadence-install discover [--wait 3s] [--json]` |
| Swap a pickle from v1 to v2 with that tarball, or back with `--to-v1` (owner-gated: agents never run it; tests use a fake device and busybox) | `go build -o build/cadence-install.exe ./device/cmd/cadence-install`, then `deploy/pickle/README.md` |
| Swap a midi to v2 with its stock kept (`--board yandex-midi`, `--ota-blocked` always), back with `--to-v1`, or give it back to its stock with `--restore-stock --ota-blocked` (owner-gated, as pickle's) | the same `cadence-install`, then `deploy/midi/README.md` |
| Swap a VK Capsula Mini to v2 with its stock kept (`--board vk-capsula-mini --ssh root@<ip> --password-file <path>` over capsula-root's password-only ssh (askpass: the installer runs itself as `SSH_ASKPASS`), no `--ota-blocked`: its OTA stubs are applied), or give it back to its stock with `--restore-stock` (owner-gated, as pickle's; the stubs stay) | the same `cadence-install`, then `deploy/vk-capsula-mini/README.md` |
| The quickstart: the server, dashboard and Postgres on one host behind Caddy, built from the checkout (`deploy/quickstart/`, its README; the owner's or a newcomer's machine, never by an agent on a real server). `setup.sh` fills in `.env`'s generated half (origin, database password, arch, version, the trusted proxy), ensures the server's prebuilt ffmpeg, builds, starts, waits for `/healthz`, exports Caddy's root to `data/ca.crt` (`CADENCE_TLS=internal`, the default; an email takes Let's Encrypt) and asks for the dashboard password on the first run; the master key is made once in its volume by `keygen`; speakers trust that root with `cadence-install --ca-file data/ca.crt`, which adds it to their bundle before enroll; `deploy/dev/dashboard-deploy.test.sh` holds its Caddyfile's server paths equal to the prod ones | `cd deploy/quickstart && cp .env.example .env && ./setup.sh [--no-password\|password\|ca]` |
| Update the GitHub mirror (GitLab only, owner-gated: `deploy/github/sync.sh`, its test `deploy/github/sync.test.sh` in `make tools-test` while the file exists): GitLab's `main` tree minus `.gitlab-ci.yml` and `deploy/github/` as one signed `sync: mirror gitlab source code to github` commit on GitHub's main (none when the tree is unchanged); refused when a GitHub commit since the last sync changed a file the new tree does not hold as GitHub has it (a PR not yet cherry-picked into GitLab) unless `--force`; pushed only with `--push` | `sh deploy/github/sync.sh [--remote github] [--ref main] [--force] [--push]` |
| The server image for `ARCH` (default amd64) as `cadence-server:<version>` in the local docker (manual, not in `make ci`): its ffmpeg (alarm music) ensured from the prebuilt cache as `ffmpeg-server-<arch>` and handed in as the named context `ffmpeg-server`; `CADENCE_FFMPEG` points at it, its licenses and source pointer in `/licenses/ffmpeg` | `make server-image [ARCH=amd64\|arm64]` |
| Tests of the device build's limit check, packing and required files, natively with small ELF files and trees (in `make ci`) | `make device-limits-test` |
| Dev stack up / down (postgres, server, the dashboard's image, sim device, the fake Soloist CDN, a fake Hermes, a fake Home Assistant and a fake MCP server over https with a dev CA per stack, which the server trusts through `SSL_CERT_FILE`; one origin at `CADENCE_DEV_PORT`: its edge, Traefik with `deploy/dev/edge.yml`, sends `/api/`, `/v2/` and `/healthz` to the server and every other path to the dashboard, as the server compose's Traefik labels do) | `make dev-up` / `make dev-down` |
| End-to-end (compose with fake providers: enroll, the volume set by command, config sync, one voice turn, device restart, a scripted tool turn read back from the conversation history (newest in the list with its tool by name, its detail with the call as stored, an `ha` token refused on both), a URL session with an announcement over it, the music duck check, a fake Soloist from the fake CDN (its track, tone, slider by rule 7, `set_level` and pause), a fake AirPlay sender (the receiver on its private bus with a fake Shairport Sync in the runtime, no Avahi or nqptp in the sim's build: its cast displaces Soloist, then its track, tone, slider by rule 7, `set_level` and pause, then switched off), alarms (the `timeZone` set explicitly pushed as the alarms zone at the link, a stop transcript that ends a turn silently, a follow-up that ends with its window and one the sim's say file speaks into, where an alarm set is refused, an alarm a woken turn's scripted model sets ringing in the mix with its ramp until a press of the sim's press file stops it, and a timer that times out (`CADENCE_SIM_TIMER_SECONDS=5`: the sim alone rings a timer 5 s, not a minute, when nothing stops it); follow-ups stay off, as a device override, in the other blocks), memory (an alias learned after a compaction on a scripted maintenance model, recalled in the next turn, and one approved in approval mode; `CADENCE_DEV_COMPACT_SECONDS=1` makes compactAfter count seconds), background tasks on the fake Hermes (`server/tools/fakehermes`, runs scripted by their first word; one deliberate server restart applies the `hermes` setting: a task a scripted turn starts finishes and its event turn's update is announced on the idle sim, an approval answered in the dashboard API lets a run end, an update in quiet hours on the device's clock waits for the end of the next pressed session; `CADENCE_DEV_AWAKE_SECONDS=10` shortens the 2 min a pressed session keeps quiet hours from holding updates), Home Assistant on a fake (`server/tools/fakeha`: `ha/hatest` with a scripted home over wss; another deliberate restart applies the `homeAssistant` setting: the turn's home note lists the favorite with its live state (a fake LLM rule on the turn's notes), a scripted turn turns a light off by name, which the fake records, the view sees and the usage counts, with `haSensitive: deny` a turn's `lock.unlock` is refused with nothing sent, and with `woken` the same turn in a button session unlocks the lock, which the fake records and the view sees), MCP on a fake (`server/tools/fakemcp`: `mcp/mcptest` in the modern era with `echo` and `note_add` over https behind a bearer token; configured through the API alone, the `mcp` setting applies live: a tool no device lists is not offered and the fake gets no call, with the tools listed and `echo` marked `read` a turn's untrusted `echo` result leaves its later steps' `note_add` and `device_volume` (one step each) refused with nothing sent, a button session's `note_add` alone runs, and `echo`'s changed description withdraws it until its hash is acknowledged), the owner login and scoped tokens (M7a: the owner password set through the container cli from stdin, a wrong password, the login with its csrf and origin refusals, the cookie feed's origin rule, a media-only subscription that gets the media event and no turn event, the served OpenAPI document equal to the committed one, an `ha` token's reach, refusals and revocation, the logout; the dev stack sets `CADENCE_PUBLIC_ORIGIN` and `CADENCE_COOKIE_INSECURE=1`, dev only; no credential on an argument), telemetry (the sim's `system.metrics` config sets a round every 10 s; cadenced's, audiod's and the fake Soloist's lines are stored stamped with the device's id, name and board; a line of the sim's log probe (`<data>/logprobe`, read by the sim alone: each line ships as source `sim-probe`) holding the device token and the pushed Soloist key is stored with both redacted and seen on the live feed; the memory and session series, the top processes (pid, comm, cpu and rss alone) and the Prometheus exposition with the device's labels arrive, scraped with a `metrics` token that the logs route refuses; the probe's lines made while the server is stopped are stored once it is back, or the ring's `dropped` count says why not), the dashboard (M7b: its image as `node` on a read-only root and healthy; the edge's split, the server's paths answered by the server and the rest by the dashboard (the sim links and enrolls past the edge at `http://server:8080`, so the feed is the one websocket the edge carries); then the Playwright specs in `dashboard/e2e` (`deploy/dev/playwright.sh`: compose's `playwright` service, the pinned official image, in the edge's network namespace so the browser's origin is `CADENCE_DEV_ORIGIN`; the owner's password on its stdin): a login submitted with scripts off (a POST the dashboard's server answers 405, the password never in the URL), a wrong password, the devices list with the sim online, its page, `system.metrics`' interval through the config editor applied on the sim, the sim's Soloist key set (its next generation applied on the sim, one more `capability_secret` set in the server's log) and shown as set, never as its value, a token shown once, a refusal in a dialog that keeps the focus, a turn the sim's say file starts on the live timeline, the sign-out and a session ended elsewhere (the feed's 1008); then the newer pages (M7c): a `fake-stt` provider from the module catalog tested OK and deleted, a mode the global `allowedModes` names refused deletion (409 `in_use`) and deleted once the key is put back, the sim renamed and named back, a turn on the block's scripted model (`llm-dash`: `device_volume` to 10, nothing said) in its Conversations with its call and the transcript in neither URL nor storage (checked through booleans, so no matcher or `error-context.md` holds it), its Health CPU figure and top processes (no command line), and a line of the sim's log probe (`cadence-e2e:want-logprobe`, which `playwright.sh` writes into the probe file) on the Logs live tail; no trace, failure screenshots masked in `build/dashboard-e2e`, which the block greps for the run's secrets; what the specs saw (cookies, the token, the secret) joins the privacy greps, and the dashboard's log is a line per request of method, path, status and time), Home Assistant's client (`integrations/home_assistant/scripts/e2e_smoke.py`: the component's `client.py`, `feed.py` and `models.py` loaded without Home Assistant core, in `make ha-test`'s pinned image (read from the Makefile) on the stack's network with uv's `client` group, its files copied in, never mounted; an `ha` token from the server's CLI straight into a mode-600 file, on the smoke's stdin alone: the device listed with its commands, the feed subscribed, a URL session the client starts seen as `media_state` with its metadata, its `pause`, a `say` answered 202 that the sim plays, the sim's restart as `device_offline` and `device_online`, a `device_vitals` after it, then the token revoked: the feed closed with 1008 and the next read refused 401; the smoke prints codes alone; about 10 to 60 s: 10 s of work, then up to 30 s for the server's vitals cadence and up to 20 s for its feed recheck) and the privacy greps (the fake Hermes', the fake Home Assistant's and the fake MCP server's logs too; the Home Assistant client's token in no server log and no line of the smoke; entity ids, names, states and the token too; the MCP token, URL path, descriptions, arguments and results too, and the MCP admin bodies carry none of the token, path, arguments or results; the device token, the Soloist key and the probe's text too, and no `cmdline` in a metrics answer); refused while a previous stack still answers on its port; always torn down; parallel worktrees set `CADENCE_E2E_PROJECT` and `CADENCE_DEV_PORT` to run side by side; the checks run jq 1.8.2's release build for the host, SHA-256-pinned and fetched once into `build/tools` by `deploy/dev/jq-native.sh`, else the pinned jq image) | `make e2e` |
| Live turn against a deployed v2 server (owner-gated: sim on this machine, real providers) | `make live-turn HOST=<host> CODE=<enroll code>` |
| Render the device's spoken prompts with a TTS provider instance the server has (owner-gated: its key stays in the DB; `--out -` is a tar on stdout; `deploy/prompts/README.md`) | `cadence-server admin prompts render --tts <instance> --lang ru\|en --out <dir>\|- [--voice <voice>]` (needs `DATABASE_URL`, `CADENCE_MASTER_KEY_FILE`) |
| Create an API token (printed once on stdout; `--scope` repeats: `admin`, the default, `ha` or `metrics`; scopes and the tokens API in `deploy/server/README.md`) | `cadence-server admin token create --name <name> [--scope ha]...` (needs `DATABASE_URL`, `CADENCE_MASTER_KEY_FILE`) |
| Set the owner's dashboard password (one line on stdin, never an argument; at least 12 characters; ends every session and known browser) | `cadence-server admin password set` (needs `DATABASE_URL`, `CADENCE_MASTER_KEY_FILE`) |
| Everything CI runs, side by side: `proto-check` and `sqlc-check` first (they regenerate code, and `openapi-check` waits for them with the other gates; `tools-test`, `device-limits-test`, `airplay-runtime` and `ha-test` beside them), then every other gate at once, then `e2e` alone (its timed checks must not share the machine with the builds). Each gate's output prints whole when it ends (meanwhile in `build/ci/<gate>.log`), then a `ci_gate <gate> <seconds>s ok\|failed\|skipped` line per gate; a gate after a failed one is skipped, and ci fails naming each gate that is not ok (`deploy/dev/ci.sh`). On Ctrl-C, e2e's trap tears its stack down, but testcontainers' and the in-docker gates' containers can outlive the run (`docker ps --filter label=org.testcontainers=true`; remove only those whose own run has ended, as other worktrees' are listed too) | `make ci` |

## Conventions

- **TDD, at the boundary:** v1's test policy carries over (contract tests at module boundaries, guards for the hard rules
  never pruned, one regression test per real bug).
- **Small, focused files:** split by responsibility; no monoliths.
- **Comments:** only when needed, lowercase, explaining *why*.
- **Commits:**
  - Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) with a scope
    (`repo|proto|server|device|media|dashboard|deploy|ha`). The body says what changed and why, lowercase.
  - Commits are SSH-signed. Never rewrite history: `git filter-branch` and similar strip the signatures.
  - Work on feature branches, never on `main`. Merging and pushing happen only when the owner asks.
- **CI:** `.gitlab-ci.yml` runs the `make` targets on the internal GitLab (git.shino.boo) when a runner with Docker is
  registered for the project (owner check). It is not mirrored, and GitHub runs no CI: `make ci` is the gate.
- **v1 hotfixes:** for a device or server not yet cut over to v2. A v1 fix starts from the tag (`git switch -c v1-fix/<name>
  v1-final`), follows v1's own AGENTS.md, is deployed from that branch and tagged (`v1-final-<n>`); it never merges into
  `main`.
- **Remotes:** internal development happens on GitLab `https://git.shino.boo/devilreef/cadence` (`origin`), branch
  `main`. The public mirror `https://github.com/devilr33f/cadence` takes pull requests (do-ocracy; its issues are
  off). The mirror is updated by `deploy/github/sync.sh`, which lives on GitLab only and is not mirrored:
  - each sync puts GitLab's tree on GitHub as one `sync: mirror gitlab source code to github` commit, without
    `.gitlab-ci.yml` and `deploy/github/`;
  - a PR merged on GitHub must be cherry-picked into GitLab before the next sync. The script refuses a sync that
    would undo one, unless `--force`.

  Pushes to either remote happen only when the owner asks. Ignore `refs/t3/*`: those are tool checkpoints, not
  project history.

## Operations

- **Deployed servers (owner-gated):** compose project `cadence-v2` (`deploy/server/compose.yml`, steps in
  `deploy/server/README.md`) on its own Traefik host (`CADENCE_V2_PUBLIC_HOST`), with its own Postgres volume
  `cadence-v2-db` and master key file; a v1 stack beside it stays untouched. Agents never deploy one, create its admin
  token or enter provider keys: whoever runs the server does, from their own shell. Never run `docker compose config`
  on a server (it prints the env files). Each maintainer's own hosts and steps live in their `docs.local/`.

## Open items

- **Error prompts (owner):** the spoken prompts (`offline`, `failed`, `not_heard`, `reminder`, ru and en) are not
  rendered yet; until `cadence-server admin prompts render` runs with the owner's TTS instance and `build/prompts`
  ships in a tarball (`deploy/prompts/README.md`), devices play the error beep. On pickle, then: a press with the
  server stopped says `offline` once per 10 s, a fake STT failure `not_heard`, and the prompt's loudness sits with
  the earcons'. The android-tv APK and the screens' packages carry no prompts yet. No e2e block checks
  `error_prompt` on the sim yet.
- **Alarm and timer acceptance (owner-attended, M6a; agents never ring one, rule 7):** on pickle and midi (midi ring
  only): an audible ring, the ramp, a stop by button, by volume press and by word, one restore of the level, the
  degree dot while an alarm is set, the countdown and the ring pulse. Prerequisite: the `timeZone` setting must be set
  explicitly first; until then the alarm and timer tools answer `unavailable` (M6a-R6).
- **Snooze and ramp acceptance (owner-attended; agents never ring one, rule 7):** only the sim's tests and fakes have
  run them. On pickle and midi: the action button held a second over a ring snoozes it (`ring_snoozed`), a quiet ramp
  (`rampFrom: quiet`) starts low right behind the tone (audiod sets a ramp's level only once its tone started, so the
  first milliseconds may sound at the level in force) and climbs, and «ещё 5 минут» after a wake that stopped a ring
  snoozes it.
- **Reminders and ring sounds (owner-attended; agents never ring one, rule 7):** only fakes, audiod's ctests (a local
  http server) and unit tests have run them. On pickle and midi: a reminder's chime and speech from
  `/v2/ring-sound/{id}` (the device token in a header), its loop and limit; with the server stopped, `ring_fallback
  code=timeout` within 3 s and the chime with the bundled `reminder` prompt; an alarm with music, its ramp over the
  music and its fallback to the tone. The server image ships no ffmpeg yet: music alarms need `CADENCE_FFMPEG` or an
  ffmpeg on the server's path (an image change is the owner's call: a pinned LGPL build with libopus), else they ring
  their tone (`music: false`). Not built yet: the `alarms.defaultSound` setting, the dashboard's ring sounds list, and
  re-synthesis of a reminder's speech when the device's TTS voice changes (a new set speaks in the new voice).
- **Rings on screens and in Home Assistant (owner-attended; agents never ring one, rule 7):** only the sim, fakes and
  the screen simulator have shown them. On the TV stick: its own ring wakes the display and shows the overlay, a ring
  of a speaker in its room shows only while the TV is awake, «Стоп» and «Ещё N мин» end or put it off
  (`alarms_act ... code=ok`), and the home strip's next alarm and timer countdown. In Home Assistant: the ringing
  binary sensor and the alarm event entity follow a ring, its stop and snooze buttons work.
- **Echo final closes a follow-up (M6a):** an echo final in a follow-up (1-2 words of the reply's tail) closes it with
  `follow_up_echo`, so a real question a second later lands in a closed session; v1 dropped echo finals and kept
  listening. A fix needs STT partials or a server-sent window re-arm; watch at the owner's acceptance.
- **AirPlay clock drift (owner-attended, ruling M4d-R6):** a 30+ min cast on pickle; check that
  `airplay_output_closed` `dropped` and `underruns` stay ~0 (else implement fill-level resampler steering). Nothing
  corrects drift between the sender's clock and the device's until a real cast shows it.
- **AirPlay audio on the stick (owner-attended):** libairplay's audio (android-tv) is placed by the session's playhead
  position (no sampled latency), lost packets' gaps are filled with silence from the rtp timeline, the two clocks'
  drift is steered through the resampler (`steer_ppm`, at most 2000 ppm) and only an error past 60 ms is cut, spliced
  with a 2.9 ms fade; aaudio's ring holds three bursts, the playback thread runs at nice -16 and the rtp thread at -10
  (android only). Every 10 s of a cast `airplay_audio` says what happened. On the stick, check during a 10+ min cast:
  `hard_cuts`, `silence_ms`, `dropped_ms`, `out_underruns`, `in_underruns` and `xruns` stay 0; `steer_ppm` settles
  (tens to a few hundred ppm) with `error_ms` within a few ms; `fill_min_ms` above ~40 and `callback_p95_ms` near the
  burst (~43); `player_nice` -16 (else `player_nice_errno`); `lead_min_ms` above 0 and `late` 0; `lost` and
  `stall_max_ms` say what the network and libairplay's resend wait did (a lost packet whose resend never comes stalls
  libairplay's buffer up to its 256 packets, 2 s of alac: a patch to give up on time is not made yet).
- **Hermes orphan runs (M5b):** three windows can leave a Hermes run nobody watches: a server restart during
  `Start` (the task becomes `unknown`), one between a resume's run create and its `hermes_runs` row write, and an ack
  commit that fails after the kind started the run. A fix needs the submission body persisted before posting, so a
  restart can replay its idempotency key and adopt the run.
- **Event-turn framing in the history (M5b, M7c):** an event turn's fixed framing is committed as the turn's user
  message. Learning and summary input label it `Event:` and it is never a memory's user evidence (M8-D6; a turn from
  before migration 00028, which has no trigger, is known by the framing's fixed start, `tasks.EventFrame`), but the
  conversation history still shows it as the turn's `user` (`trigger: event`, none before 00028), so the dashboard
  (M7c) labels an event turn's input as the server's own.
- **Waiting tasks never expire (M5b, M9):** a `waiting_input` task nobody answers stays open, and in every turn's
  active-tasks block, until it is answered or cancelled: a prompt Hermes would time out is paused, not ended.
- **Pickle v2 install (owner-gated):** the swap to v2 and `--to-v1` (`deploy/pickle/README.md`) have run only against
  a fake device and busybox. Rebuild the tarball from a clean commit first (`make device-build BOARD=yandex-pickle`;
  a `-dirty` version means it was not).
- **Pickle audio baseline (spec §6.7, owner-attended):** Opus encodes on the VQE/spotter processing thread (its CPU
  share on pickle); `capture_errors` in `capture_engines_stopped` and whether the stream stays whole with the mute
  switch held; the time from `audiod_ready` through the `vendor_probe` lines to `vendor_status` against cadenced's
  15 s first-packet wait; when hardvold creates `Master` after boot (`volume_retry_failed`, then `volume_applied`
  with `retries`).
- **Midi ring (owner-attended, M2m-D4):** logical LED i is framebuffer pixel i, and each pixel is written R, G, B, 0;
  neither the ring's orientation nor its colour order has been seen on a midi.
- **Midi stock boot window (accepted, M2m-D2):** the stock assistant stays installed and its init script still starts it
  at boot, so it can listen, answer and set the volume until `cadence-run` first suspends it (its inittab line follows
  adbd's), seconds at most but unmeasured; the owner-attended bring-up measures it and confirms `stock_suspended
  count>0` at the first boot. Stock that starts later (`S99quasar` after cadence-run, a watchdog) is suspended at the
  wrapper's next poll, within about 2 s (`stock_resuspended`, ruling M2m-R5), and the wrapper then sets the persisted
  level again, since the stock may have set its own (ruling M2m-R7). The bring-up also shows whether `run.sh`, a shell
  the exe-based suspend leaves alone, restarts the launcher. A fast loop (the launcher back before a suspend ends)
  repeats `board_control_failed code=suspend`: cadenced does not start, or a running one has live stock beside it,
  which fails verify. A slow loop (back between polls) repeats `stock_resuspended`, and verify fails with
  `stock_keeps_returning` once it counts two since cadenced started.
- **Midi OTA hardening (owner-attended, M2m-R3):** the hackalice steps `ota-key`, `ota-script-stub` and
  `updatesd-launch` (`device/boards/yandex-midi/installer/profile.go`) are declared but not inspected, so none is
  applied and only the router blocks OTA: every midi install and `--restore-stock` needs `--ota-blocked`. Inspect a
  midi's files, fill in the three steps (backed up under `/data/cadence/backup/<step>/`), have Install apply them,
  then drop `RequireOTABlocked`. The steps write the rootfs as `SwapInittab` does (writable only for the write,
  read-only again on every exit path), and `--restore-stock` decides whether to revert them while it still requires
  `--ota-blocked`.
- **Midi v2 install (owner-gated):** the swap to v2, `--to-v1` and `--restore-stock` (`deploy/midi/README.md`) have
  run only against a fake device and busybox, and need a midi with v1's access phase (adbd and dropbear in init).
  Rebuild the tarball from a clean commit first (`make device-build BOARD=yandex-midi`). `--restore-stock` starts
  the stock with `start-stop-daemon -S -b -o -x /bin/sh -n run.sh -- /system/vendor/quasar/run.sh`: busybox's
  start-stop-daemon starts nothing while any process runs as `/bin/sh` (adbd's session shell does), so `-n` narrows
  the match to a `run.sh` its init script started; the bring-up confirms the stock comes back.
- **Midi vendor libraries (owner-attended):** audiod's DT_RPATH `$ORIGIN/lib:/system/vendor/quasar/libs` lets the
  vendor's copies win for every library `bin/lib` lacks (libasound, the ALSA plugins' dependencies); the bring-up
  confirms what `/system/vendor/quasar/libs` holds and that the stock library's OpenSSL needs fit bookworm's 3.0.x.
  An AArch64 stock library (another firmware's) is refused by dlopen in the probe child as `LibraryLoadFailed`
  (`vendor_probe result=failed`): v2 reads no vendor ELF header, so there is no v1-style `wrong_stock_abi`.
- **Midi media (owner-attended):** none of it has run on a midi: Soloist's arm64 runtime under the armhf userland
  (midi's 64-bit kernel runs it), its mount namespace on kernel 4.9.68, the AirPlay arm64 runtime (each program ran its
  `--version` there) and whether `/usr/share/alsa/alsa.conf` is midi's libasound root, and the `libpulse` shim against
  real Soloist.
- **Engine-mode read failures:** with the vendor engines on, audiod retries device read failures itself and sends no
  `CaptureFailed`; decide after the baseline whether a session should hear of them.
- **`Master` re-check (M6a-D8):** cadenced puts the level in force back on the hardware before each reply and each
  minute while music plays, never during a ring or while the level is unknown. Open: `volume_reapplied found=…` names
  only a difference cadenced knows (a ramp step not yet restored), so a level another writer left (hardvold, midi's
  stock) is put right silently; logging it needs audiod to read the control back before the re-apply. Midi's wrapper
  still sets the level again after a running re-suspend (M2m-R7), reading `state/volume` again after its `amixer`.
- **Command words (M6a-D6):** the command spotter's model ships in the stock root (`spotter_models/command`,
  version `…stations-shrd-bt-likes`: 25 phrases — the five below, the media words, navigation, `включи`/`выключи` +
  `свет`/`телевизор`/`блютуз`, `лайк`/`дизлайк`); the yandex boards load it when the owner has not pushed their own
  copy to `/data/cadence/stock/command`, and `input.CommandWords` lists every phrase the model can say — audiod
  matches the phrase by string, so a superset list is safe. `тише` and `громче` step the
  level within 2 s of the last reply frame (ending the session whose microphone carried it) and whenever no session
  streams — media playing or not, for nobody else hears the word then; громче raises under the same conditions by the
  owner's ruling of 2026-11-21, a reply's own echo of the word accepted as тише's always was; while
  a ring sounds `стоп`/`хватит`/`пауза` stop it, and with media playing and no session streaming they pause the
  speaker's own media and `дальше`/`следующий`/`назад` skip in it; `продолжи`/`играй` continue a player that stands
  (nothing playing, nothing streaming — which restarts a queue that ended); the home's phrases (включи/выключи
  свет/телевизор) are handed to the server as a `WordHeard` envelope when no session streams — it runs them as silent
  direct commands (the speaker's room's lights), storing the turn under trigger `word` — and with a session the
  streamed microphone carries them; navigation and the rest are counted and left to the server (offline: a song's own words can reach the
  spotter then, owner-accepted). Every word the spotter hears pulses the ring once (`leds.Heard`, a cyan flash over
  the clock and the music; a session or a ring shows over it); nothing acts while muted or for test audio, and the server's stop request ends a turn
  of stop words alone silently (`остановись`/`останови`/`приостанови` and en `pause` among them).
- **Direct commands (no model):** the server answers a fixed table of whole utterances without the LLM
  (`server/internal/voice/commands.go`, after the stop and reset requests): the volume words (`громче`/`погромче`/
  `громкость выше`, `тише`/`потише`/`громкость ниже`, `сделай/прибавь/убавь …`, en alike, a trailing «пожалуйста»
  forgiven) step `audio.output` `change_level` by 10 with the session id (a raise only in a woken session, rule 7,
  the device checks again); `продолжи`/`дальше`/`следующая`/`назад` drive `media.player` (a `resume` with no session
  falls to the model, which restarts an ended queue; a skip with nothing playing answers «Ничего не играет»); and
  `включи`/`выключи`/`turn on`/`turn off` + a name turn one home device through `tools.DirectHomeControl`, the same
  resolution, guards and policy as `ha_control`. The generic light word (`свет`/`освещение`/`лампочки`, en
  `light`/`the lights`/`lighting`) is apart: it means the speaker's whole room, so it turns every light of that room
  together (the light domain and what the home classes as a light; entities the home cannot see are skipped) instead
  of asking which one — the room is the device's `haArea` setting, else the HA area named as the device's own room.
  Anything the table cannot settle alone (no match, several matches, no room to name an area, a policy refusal, a
  failed command) is answered by the agent as before, and the turn is stored with its fixed
  reply. Success is silent (the lights show a volume, the action its own result) except the home's «Включаю X» line,
  and a direct command's turn ends without a follow-up offer — the command is the whole answer, the microphone stops
  with the turn.
  Extending the offline spotter vocabulary needs the phrases the model can say listed in `device/internal/input`'s
  `CommandWords` (string-matched, a superset is safe) and an action in `policy.Command`'s table; the model itself ships
  in the stock firmware's root (`spotter_models/command`), so nothing is pushed.
  Open (owner-attended on pickle): `тише` and `стоп` mid-reply, `стоп` over a ring, and the offline pause over real
  music.
- **Follow-ups (M6a-D5):** on v2, a pickle opens a follow-up after every spoken reply by default (v1
  parity); the `followUp` setting's `enabled: false` turns them off. Open: "heard whole" counts what cadenced handed
  audiod, so audiod-side drops (a gated stream's `StreamDone.played_ms` of 0) are not consulted; and an echo
  `SpeechStarted` followed only by empty finals keeps a follow-up open until the 30 s utterance limit (`stt_failed`),
  as the device clears its silence timer at `speech_started` (the fake STT never sends one, so the e2e cannot show it).
- **mDNS on the speakers (owner-attended):** cadenced advertises `_cadence._tcp` (port 0: cadenced listens on none; a
  multiroom group's leader audiod alone binds 7701, below) from boot,
  sharing UDP 5353 through `SO_REUSEADDR`/`SO_REUSEPORT`; it has run beside macOS's mDNSResponder and in Docker only.
  On pickle and midi, check that it starts beside the AirPlay runtime's Avahi and any vendor responder
  (`discovery_started interfaces=N`, no `discovery_failed`), that both keep answering (`avahi-browse -rt _cadence._tcp`
  and `_airplay._tcp`), and the CPU of the read loop. IPv6 (AAAA, ff02::fb) is not answered. Android advertises through
  `NsdManager` in the app instead (a multicast lock and the system's own responder).
- **Receiver names (owner-attended):** AirPlay and Spotify Connect advertise the device's own name while their config
  sets none (`deploy/server/README.md`, Devices), and a dashboard name may be Cyrillic, which the speakers' explicit
  `name`/`deviceName` patterns never let through. Only fakes have seen one: on pickle, check that a Cyrillic name shows
  in an iPhone's AirPlay list (shairport over Avahi) and in a Spotify app (Soloist), and that a rename shows within
  seconds.
- **Enroll code (optional):** `cadence-install --code` puts the code in the host's argv; a `--code-file` or stdin
  option would keep it out.
- **ha_control schema type unions (M5c):** `brightness`, `kelvin` and `temperature` are advertised as
  `["integer"|"number", "string"]`, so a quoted number passes `agent.Validate`. At the first live Home Assistant turn,
  check with each mode's model that it takes the union; if one refuses it, advertise integer/number only and unquote a
  quoted number before Validate.
- **HA sensitive-set gaps (M5c):** a switch, input_boolean, select or remote that wraps a lock, gate, siren or opener,
  a cover without a device_class, and cameras are outside the sensitive set. Reclassify them in Home Assistant or set
  `haSensitive: deny`; per-entity overrides come later.
- **HA live acceptance (M5c, owner-gated):** on a real Home Assistant: auth with a long-lived token, a
  reconnect after a Home Assistant restart, «выключи свет» with a name several devices share, a quoted temperature, a
  lock refused in a follow-up, and no device volume change through Home Assistant.
- **MCP live acceptance (M5d, owner-gated):** only the go-sdk 1.8.0 fake (`mcp/mcptest`, both protocol eras) has
  served Cadence. Check against real servers: a legacy-era one built on the Python or TypeScript SDK (initialize, its
  session id, its 404 for a lost session, its error bodies), and with each agent mode's model that it accepts the
  cleaned JSON Schema 2020-12 input schemas (the vocabulary allowlist, `examples`, `$comment` and `x-*` dropped).
  Accepted window (step 1): an enabled tool's description reaches the model in the turn's first step, before any
  result could taint the turn, so a hostile server can steer the model toward other tools there; Write tools still
  need a woken turn and the agent never raises volume on its own (rule 7). Enable only servers you trust.
- **Cadence's own HA entities (M8):** the v2 Home Assistant integration's entities (platform `cadence`: each device's
  media player, online and vitals sensors) stay out of `ha_control`. Keep its platform refusal (Ruling M5c-R8) and the
  refusal of an entity whose platform is unknown (M8-D6: an unreadable registry entry, or a state with no entry while an
  entity re-list is pending, running or failed after a registry `create`, an `update` that changed an entity id or an
  event the server cannot read, and during a load; retried 1 s → 30 s), and the integration's domain `cadence`. The
  guard rests on two things to confirm at the HA live acceptance: Home Assistant lists a new entity in its registry
  before its first state, and the integration gives every entity a unique id (one without never gets an entry, and at
  rest an entity without an entry is allowed). It is by platform alone: a wrapper of another platform around a Cadence
  speaker (a script or scene that sets its volume, a `universal` or `group` media player) can be run by `ha_control`
  (a script or scene in a woken turn unless `haSensitive: deny`), so the owner builds none.
- **Home Assistant integration acceptance (M8, owner-gated):** only pytest's fake server and the e2e (the component's
  client and feed modules on the dev stack, without Home Assistant core) have served the component. On the owner's
  Home Assistant (2026.10 or newer) against a deployed v2 server: the install (a copy, or `make ha-package`'s zip), an `ha` token in
  the config flow (a wrong one and an `admin` one refused), each of a pickle's entities (the speaker's state, metadata
  and cover, a volume set and a step, pause, play, next and previous as its sessions list them, `play_media` of a URL
  and of a TTS announcement; online, CPU, memory, disk and temperature), `cadence.say` in `en` and `ru`, a reconnect
  after the server restarts (everything read again, nothing replayed), a device enrolled, renamed and deleted while
  Home Assistant runs (its entities follow without a reload), a revoked token (reauthentication within 20 s, no
  retries), and the two assumptions of the platform guard above: a new `cadence` entity reaches Home Assistant's
  registry before its first state, and every entity has its unique id (`{deviceId}:{key}`).
- **HACS distribution (M8-D11):** HACS wants a component at its repository's root (or `content_in_root`), and this one
  lives under `integrations/home_assistant/`; until a layout, a repository of its own or a release zip is chosen, it
  installs by hand or from `make ha-package`'s zip.
- **AirPlay covers (owner-attended):** shairport's cover (`ssnc PICT`, jpeg or png, at most 1 MiB) is written by audiod
  to its AirPlay scratch (`run/artwork`) and named by an id in `MediaMetadata.artwork_id`; cadenced uploads it with
  its token (`PUT /v2/artwork/{sha256}`, kept in the server's memory per device, D11) and names it in the session's
  `artwork_sha256`, which the server shows as `artworkHash` and `get-device-artwork` serves. Only fakes have sent one:
  confirm on the midi that a cast's cover arrives (`airplay_artwork`, then `artwork_uploaded`), and that `prgr`'s
  position and length (rtp at 44.1 kHz) match the sender's.
- **Group covers (owner-attended):** a group member's screen asks the server for its session's cover
  (`GET /v2/session-artwork`, the stream source's AirPlay upload or artwork url, named by a hash of the title and
  artist in `X-Cadence-Item`; a few retries while the source's upload is not in yet), and a member's session runs at
  the position its playout hears. Only fakes and unit tests have run it: on the TV stick in a group with midi, check
  that the audio page shows the source's cover and tint for an AirPlay cast, Spotify Connect and a `music_play` song on
  midi, that the progress matches the source's after a join mid-song and a seek, and that a cover the TV casts reaches
  midi's card in the dashboard. The sender's name («с …») stays the source's own (D11) and does not show on members.
- **TV casts (owner-attended):** on the TV (libairplay in audiod) a cover comes as raop's `audio_set_coverart`, is
  written to `run/artwork` as the speakers' is, and the TV board uploads it (`media.UseArtwork`, which it never called
  before) and copies it to `<data>/run/screen` for the screen's audio page (`NowPlaying`, screen/v1); the sender's name
  rides on audiod's `MediaMetadata.sender` to the screen alone (never the server, D11). Only fakes have sent one:
  confirm with an iPhone that a cast's cover reaches the dashboard and the TV, that its name shows («на ТВ · с …»),
  and that a cast over a playing film pauses it (`external_action ... reason=displaced`, then `media_session_start
  source=airplay`; the screen also pauses its own player on a new cast, `player_displaced_by_cast`) and opens the
  audio page. The first report (a film playing on under a cast) was not reproduced off the device: its logs decide
  whether the displacement's pause was lost on its way or the screen simply stayed on the film's page.
- **Spotter score records (owner-attended):** the stock spotter built alone (`spotter_create_multichannel`, alice
  without command words) keeps a 12-byte score record per frame that only `spotter_destroy` frees (~10 MB an hour of
  audiod's memory on midi; `spotter_reset_logs` does not free them), so audiod rebuilds it every 10 min
  (`spotter_rebuilds` in `capture_engines_stopped`). The combined spotter (alice and command words) is not rebuilt:
  stock maind clears it per component (`spotter_reset_logs_for_component`). On a device with command words, watch
  audiod's RSS for an hour before deciding whether it needs the same.
- **Home Assistant pin (M8-D10):** `uv.lock` has `homeassistant==2026.10.4`, the version the newest
  `pytest-homeassistant-custom-component` (0.13.375) pins, while Home Assistant is at 2026.11; re-lock once a 2026.11
  pairing is out.
- **Audio xruns (M6b):** `system.metrics` has no `cadence.audio_xruns`: audiod sends no xrun count over its socket (its
  underrun counts stay in AirPlay's pipe and the PCM input). It needs an audiod IPC field (a proto change) that cadenced
  counts into its metrics.
- **Device log batches (M6b):** the device batches up to 4 MiB of encoded records and splits a request only by its
  compressed size, so short lines (kmsg, `dropped N lines`) can put about 70 Ki records in one request, which it acks
  whole on a 2xx; so the server takes up to 128 Ki rows a request. A per-batch record cap on the device would let the
  server cap rows tighter without losing lines.
- **Log scrub gaps (M6b, rule 6):** the device and again the server replace each known secret (the device token, every
  capability secret) in its exact, URL-escaped and base64 forms, whole, and at the ends of a cut line and of its
  continuation (4 bytes or more). Still shipped as written: a secret split across two lines its source ended, a segment
  cut at both ends inside one secret (a line flushed twice while it was written), one nested in a larger encoding
  (base64 of `user:pass` or of `Bearer <token>`, a JSON-escaped value), and up to 3 bytes of a secret at a cut.
  Third-party lines otherwise ship as written (owner accepted).
- **Telemetry acceptance (owner-attended, M6b):** only the sim, fakes and the e2e have shipped telemetry. On pickle and
  midi: the kernel log is read with no `kmsg_failed` and no `kmsg_stop_timeout`, also across a `logs` config that turns
  `kmsg` off and on again. With real Soloist on its two pipes (stdout and stderr): a wrong key is still detected
  (`soloist_auth_failed`, state `failed` `auth_failed`), and lines its buffered output splits at a 4 KiB flush arrive
  marked `truncated`, then `continued`. On midi: the `/data` and `/tmp` mounts its profile declares exist (their
  `system.filesystem.*` series arrive), and USER_HZ is 100 (the top processes' cpu agrees with `top`). On a deployed server:
  a full 4 MiB backlog, sent once the server is back after an outage, is stored inside the 10 s store timeout (no
  `otlp_ingest_failed code=store`, the device's `dropped` unchanged).
- **Tracing:** the server's plane (`server/internal/tracing`: store, `/api` and provider HTTP spans,
  `/v2/otlp/v1/traces`, the `traces` topic, the `tracing` setting, export through `telemetryExport`), the turn's
  voice, agent and link spans, cadenced's spans over the link's `TraceContext` fields and the dashboard's Traces page
  are in. A span's error is its class from the error's types (`Classify`: `ws_close` with its close status, `dns`,
  `tls`, `conn_refused`…), never its text; UniProxy's sockets have spans of their own (`uniproxy.stt`/`.tts`, with
  `cadence.outcome`, the close status and a token reason) and a failed voice.session names its stage and cause.
  Before that fix the traced HTTP transport hid a websocket's body, so every websocket dial through `httpx` failed
  with tracing on (a live `stt_unavailable` trace): confirm on a deployed server that a wake turn on UniProxy with tracing
  on now works. Only fakes have received the export: check a real Jaeger or Tempo takes `/v1/traces` from the
  `otlp-http` instance.
- **Telemetry limits per device (M6b, owner decision):** an enrolled device that turns hostile can slow every Prometheus
  scrape (`LatestMetricSeries` sorts all points of the last 10 min to find each series' latest) and fill the disk at the
  ingest rate (M6b-D8, 1 MiB/s a device, kept for the retention windows). A per-device latest-series map or row cap,
  and a storage quota, are owner decisions for later.
- **Dashboard server gaps (M7c):** the dashboard works around these server limits for now:
  - `list-logs` pages by timestamp alone, so lines of one equal timestamp across a page boundary can be skipped or
    repeated: it needs a cursor with a tie-breaker;
  - the live feed filters by topic only, with no per-topic device filter;
  - the metrics routes have no attribute filter or downsampling (a CPU total over a long range can hit the 50 000
    point cut), and the interval a query applied is not exposed;
  - modes take built-in tool names that no route lists, and a mode cannot be renamed (an owner call);
  - no route publishes the settings' schema, so the settings editor keeps its own copy of their types and bounds
    (`dashboard/src/components/settings/settings-catalog.ts`), which can drift from the server's.
- **Music live acceptance (owner-attended):** only fakes, and a dev machine's own yt-dlp against YouTube, have served
  `music_search` and `music_play` (`deploy/server/README.md`, Music). On a real device: a song plays through the
  server's stream relay (`/v2/stream/<token>`: ffmpeg's ranged reads through the edge, a long track whole); a pause
  or a seek past the stream's expiry (about 6 h) fails, as nothing resolves it again; whether YouTube's bot check meets a deployed server's address without `cookies`; and a Spotify app's
  client-credentials search (newer apps' Web API access is narrowed). No e2e block runs the tools on the dev stack yet.
- **Streaming music providers live acceptance (owner-attended):** `soundcloud` has searched and resolved whole songs
  live (anonymous, from a dev machine; FFmpeg decoded its HLS AAC); `yandex-music` searched live and its signed
  `get-file-info` answered a guest's preview; `zvuk` (geo-blocked from the dev machine) and `vk-music` (no token)
  have run against fakes only. On a deployed server with a device: a whole song of each through the relay (`stream_relay`, no
  `stream_relay_failed`), a Go+/Plus-less preview falling back to YouTube Music (`music_native_fallback`), each
  provider's radio topping a queue up, Yandex's cookie exchange and Моя волна (`user:onyourwave`, fakes only so far:
  its first batch, then another of the same station after a YouTube-matched song), its like/dislike library mutations
  and station `radioStarted`/`trackStarted`/`trackFinished`/`skip` reports (fakes only, including played seconds and
  batch ids), Cadence favorites surviving history pruning, whether Zvuk's stream hosts are all
  under `.zvuk.com`/`.zvq.me` (else the relay refuses them), Zvuk's GraphQL auth errors' shape, VK's master playlist and
  relative `key.pub` through the relay, and whether a flac-in-mp4 (Zvuk `flac`) plays on pickle.
- **YouTube discovery live acceptance (owner-attended):** `ytweb` (`server/internal/providers/ytweb`) was checked
  against live answers from a dev machine on 2026-11-16 (its trimmed testdata); no deployed server's address has called
  it yet. On a deployed server:
  a channel's latest, popular and oldest videos, a search inside a channel, a 165-video playlist read whole and its
  latest, a video's likes, and whether YouTube's bot check meets the InnerTube calls without cookies (`ytweb_failed
  code=failed status=`). Its shapes follow Yattee Server and YouTube.js (both MIT); a shape YouTube changes fails by
  code, and video search then falls back to yt-dlp. With `youtubeVotes` on: Return YouTube Dislike's counts arrive
  (no `ryd_rate_limited` in normal use). `video_search`/`video_play` stay until `catalog_play` matches them (a start
  offset, the play card, follow, recalling a lost ref), and `music_play` does not take a catalog ref yet.
- **Title facts live acceptance (owner-attended):** only fakes have served `titleinfo` (Shikimori's `/api/animes/{id}`
  and `/related`, Jikan's `/anime/{id}`) and torrplayer's `/mylist` entries with a score. On a deployed server: a title page shows
  its rank, studio, dates, Russian description and related titles (no `titleinfo_failed` codes for a known id), and
  the owner's list status and score match Shikimori's; `catalog.alternatives` from a single-source title lists its
  other sources with their voiceovers.
- **MCP endpoint acceptance (owner-attended):** only the rig's fakes and the dev stack have served `/api/v1/mcp`. On a deployed server: create an `mcp` token (`--scope mcp [--mcp-tool name]...`), connect a real client (Claude Code's `claude mcp add --transport http cadence https://<host>/api/v1/mcp --header "Authorization: Bearer …"`), then list devices, run an assistant turn, search and play music through `music_search`/`music_play` (ids shared with voice turns), play a url, read playback and history, mint a media link and let it expire, and check the Traefik edge passes the POSTs (plain JSON responses, no SSE). The tools that act on a device follow rule 7's typed-turn stance; nothing here rings or casts on a real device from a test.
- **Provider test on real keys (M7b-server, owner-attended):** only fakes have answered the provider tests. At the first
  live `POST /api/v1/providers/{name}/test` of a `deepgram-*` instance, confirm `GET /v1/auth/token` answers `ok` for the
  owner's key, including one limited to transcription scopes (if it answers `auth`, the probe needs another call).
- **Group calibration (owner-attended; agents never run one, rule 7; how it works: `docs/multiroom.md`):** on midi
  leading and the TV stick (2026-11-16), listener mode (a phone in the dashboard) measured the stick's HDMI
  output 98.7 ms and then 103.9 ms behind midi's, both chirps at 100 % confidence, and applied the trims. Mics mode now
  records midi's raw first microphone (before the vqe's echo canceller, which had taken its own chirp out: `no_self`), and
  midi heard its own chirp; the stick has no microphone, so that group can only answer `isolated`/`no_capture`. Still to
  see on two speakers with microphones (pickle and midi): a mics-mode `ok`, a same-model pair's offsets near 0, and a
  second run within ~100 us of the first. Applying a trim restarts each member's group link for a few seconds. Since then calibration
  chirps four rounds (800 Hz–7 kHz, 250 ms), keeps rounds within 1 ms of their median, records every microphone, has
  the speakers record a listener run too, and offers Retry louder (only fakes and unit tests have run it): on the stick
  and midi, check each member's spread (a small spread with runs still ~5 ms apart means the stick's output delay
  moves per start), the Path to you figures against the room, and audiod's analysis time on midi with all its
  microphones (`calibration_record channels=`).
- **Multiroom on real devices (owner-attended; agents never run a group on a device, rule 7):** only sims (one with a
  1234 ms, 80 ppm sim clock), fakes and loopback have run a group: there the two sims heard each frame within ~26 us
  (`group_heard`, the e2e's multiroom block). The P0 spike measured the clock on midi and the stick at ~25 us over Wi-Fi
  (midi leading; the stick leading only ~100-150 us, so prefer a speaker as leader). On pickle and midi: a stereo pair
  plays one track in step (`media.group` state synced, `placementErrorUs` within ~100), a pause, a resume from either
  speaker and a skip restart both on one sample, a member's Wi-Fi dropped for a few seconds rejoins without the other
  stopping, a wake on one ducks both while only it answers, and the leader's `group_threads` line. A leader binds 7701
  (tcp: the stream, udp: the clock) on every address while it leads, authenticated by the group key (hmac on every
  packet; the audio is not encrypted, accepted: music on the owner's lan). Every source and video (`docs/multiroom.md`,
  Video): Spotify Connect and AirPlay on midi heard on the TV, and an AirPlay cast to the TV heard on midi
  (`group_source target=feed`); an anime on the TV with midi as its pair: picture and sound in step, a pause, a seek and
  the next episode without the lead bouncing (`group_leader_moved` lines on the server, no `reason=preferred` within
  the 30 s grace); two screens in step (`video_follow error_ms` near 0), a next on the follower moving both to the
  next episode (`follow_control`, then `follow_play`, the lead kept) and a YouTube video followed
  (`video_follow_play`); the stick as stream source with midi as clock master (`group_started clock=dials` on the
  stick, its `clockErrorUs` ~25 us). Unverified: whether a real Soloist skip
  reaches the group as a metadata change or a clear (only a fake source has skipped), and each board's fixed output
  latency (dmix, the codec, a tv's hdmi) beyond what a calibration measures.
- **Multiroom e2e:** the full `make e2e` stopped before the multiroom block, in the t3 code block ("the held question
  not announced once busy ended", on an arm64 Mac and on ci alike): the tasks block left its quiet hours on, and
  with them gone the default's 23:00 to 08:00 on the device's clock (Asia/Bishkek's by then) held the question just
  as well at night, so it waited for the next pressed session. The tasks block now turns them off; the multiroom block
  has not run in a full `make e2e` since.
- **Bluetooth receive on midi (owner-attended; agents never pair, rule 7):** the `bluetooth` capability (basic tier:
  a PC or phone streams to the speaker) has run only against the sim's fake BlueZ and bluealsa. On midi: «включи
  блютус» opens the window and the PC pairs only inside it (one outside is refused, `bluetooth_pair_refused`); the
  board-hook keeps `bluealsa-aplay` and `just-works-bluez-agent` stopped (`--to-v1`/`--restore-stock` restart them);
  bluealsa 4.1.1's `PCM1.Running` turns true when the PC plays, the stream reaches audiod (`bluetooth_stream_open`) and
  stops on pause; the PC's volume moves the level and our level reaches it (`PCM1.Volume` with SoftVolume off; else
  `MediaTransport1.Volume`); AVRCP pause and next from the speaker's buttons; `RegisterAgent` and godbus' fd passing
  on midi's dbus-daemon. Midi's bluealsa offers SBC only. Send, BT outputs and BT in groups are the advanced tier
  (`bluetooth.output`, a modern Linux host), not built yet; the agent tool already routes them and answers
  `no_capability`.
- **Capability availability:** a board whose hardware comes and goes implements `board.Availabilities`; core
  answers an unavailable capability's commands `unavailable` and sends `StateDelta.unavailable` (an empty json when it
  has no state), and the server stores `device_capabilities.available`, hides that capability's `State` (so video,
  rooms, ambient and the remote find no screen) and shows `available` in the detail. Nothing is reported again, so
  config and secrets survive an unplug. The dashboard sees a flip at its next detail fetch: no live event carries it
  yet. Only rpi uses it.
- **RPi screen hot-plug (owner-attended):** only a fake sysfs and a fake unit control have run it. On a Pi with
  nothing on either HDMI port: after the deb upgrade, `cadence-screen.service` is `static`, inactive and no longer
  restarting, `cec_claimed` is not repeated, and the dashboard marks the five screen capabilities Unavailable; then a
  display plugged in starts the screen within ~4 s (`hdmi_sink connected=true`), CEC finds the tv, and unplugging
  stops it. Unchecked: whether vc4 holds `connected` through a tv's standby, and whether SDL's kmsdrm exits cleanly
  when its connector goes before cadenced stops it.
- **Micro (Lite) experimental bring-up (owner-attended):** `yandex-micro` supports only the probed Station Micro v1.2
  on firmware `0.369.1.48.4790768027.20261019.297`; its slim 24 MiB tarball fits the 87.5 MiB `/data` by omitting
  Soloist and AirPlay. Bluetooth receive uses the stock BlueZ/bluealsa services, and multiroom needs no extra runtime.
  Cadence enrolled, linked and started the stock wake/VQE engines on
  2026-11-16; an actual voice turn, microphone quality, URL playback, alarms, buttons and the three LEDs' colour order
  and orientation have not had physical acceptance. The mute key is deliberately unmapped. Root ADB uses the stock
  persistent markers and must stay available (the provisioning proxy keeps them refreshed); OTA hardening remains
  `not_inspected`, so every install or stock restore needs `--ota-blocked`. See `deploy/micro/README.md`.
- **VK Capsula Mini bring-up (owner-attended; agents never run these on the device):** one unit (the A113X, firmware
  44.90) has served the board; `--accept-firmware` and the AW21036 clock variant are untested
  (`deploy/vk-capsula-mini/README.md`).
  - Proven on that unit on 2026-11-18, with the M1 tarball and the boot_id unchanged throughout: the install over
    password ssh; two reboots back on cadence; a launcher restart handing the watchdog to the new feeder;
    `--restore-stock`, the stock holding the watchdog again with no reset; a reinstall over the running stock; the
    mute key (injected through evdev); `arecord` from `single_plugin` and `aplay` of silence; the clock rendering per
    its map; ambient brightness. Idle on M1: cadenced RSS 21 MB, audiod 7.5 MB, about 8 % CPU, `/data/cadence` 25 MB.
  - Still to see:
    - a talk-key turn, with audiod's own capture through `single_plugin` and its playback: audiod never opened
      `single_plugin` on the unit (M1 opens it only per session, and no turn ran), so the vendor plugin in audiod's
      process is unproven;
    - the M2 tarball on the A53, never run there: `vendor_status code=keyword`, `kws_started invoke_ms` (the shipped
      TFLM build measured 22.95/23.45 ms p50/p90 a call there in a standalone benchmark, not inside audiod), audiod's
      RSS and CPU with the vendor plugin held open, and a live `WakeEvent`;
    - the listening check: `TV-Set` loudness (an attenuator or an input to the amp's processing) and the level
      mapping; volume keys;
    - the ring's orientation and colour order; an alarm ringing offline (owner only);
    - `gpio446`: whether it is the hardware mic gate (watch it while toggling mute); if so the board drives it too;
    - magic close: whether `meson_wdt` honours it is unmeasured, and the design does not depend on it (the feeder
      never writes it: a feeder that ends leaves the watchdog running for the next holder, or for a reset).
  - M2 («Маруся», the stock model's wake) is implemented: audiod's keyword engine runs the stock model read from the
    device (only the probed model's sha256 is loaded) with the offline gate's policy (pos 0.999, sp 0.2, a 147-frame
    re-arm), and a unit without that model wakes by the talk key alone. The shipped int8 path (TFLM, run on the host
    over the gate's 2.06 h of public-domain speech and music, 92,570 calls) woke 0 times (its highest call 0.99826,
    the float reference's 0.99734), on 72/72 synthesized clips and 36/36 over background; the stock's 0.994 would
    wake 2.9 times an hour, and 0.997 with 2 calls in a row woke twice, so it is no fallback. Owner-attended: the live
    wake with real voices through `single_plugin` (TTS saturates near 1.0, real far-field voices may peak lower: tune
    0.999 from the scores) and the false-wake check (none in 60 min of speech and music in the room).
  Accepted: a factory reset (vol+ and vol- at boot) wipes `/data`, taking cadence and capsula-root with it; `/data/cadence/disabled` makes the next boot start the stock.
- **VK Capsula Mini 2021 firmware (owner-attended):** the Buildroot 2019.11.1 firmware is an installer variant (adbd
  recovery, `fwupdater` and the `swupdate` binary stubbed) and captures through `plug:uroc`; only `arecord` from
  `plug:uroc` and `aplay` of silence on `playback` have run on it. Its stock model (`model.msgpack`, `sp_model.msgpack`)
  is another format, so it wakes by the talk key alone. PulseAudio stays running (it has no ALSA sink there).
  `network.wifi` runs over its `/usr/sbin/wpa_cli` on `wlan0` (`/etc/wpa_supplicant.conf` on the overlay,
  `update_config=1`); only unit tests have run it, so the first switch from the dashboard is owner-attended. The
  server's device updates hot-swap its `bin/` as the yandex boards' (`yandex-hotswap`).
- **Wi-Fi switch from the dashboard (owner-attended; agents never change a real device's Wi-Fi, rule 7):** the Wi-Fi
  card's Connect (`configure-device-capability`, the ssid and psk as one generation), the link-backed success rule and
  the `no_ip`/`no_server` rollbacks (`deploy/server/README.md`, Wi-Fi) have run only against the sim's fake supplicant,
  the wpa_cli script fake and core's tests with a fake link gate. On midi and the Capsula, check: a switch to a good
  network ends `wifi_switched` with the link back within seconds; a wrong password rolls back `no_ip` and the device
  re-links; a guest or isolated network rolls back `no_server`; `select_network` leaves a disabled network disabled
  on that supplicant; and a restart mid-switch (cadenced killed after `wifi_switch_started`) resumes and finishes or
  rolls back. Open: changing the password of the network the device is on rewrites its saved psk in place with no
  switch, so a wrong one strands the device at its next reassociation; open and WPA3-only (SAE) networks are not
  offered (the device writes `key_mgmt WPA-PSK`); a server down during a switch rolls a good network back as
  `no_server`; a switch's ack may be lost with the old link (the next hello's push of the same generation is acked
  from the store).
- **Custom wake word (owner-attended; agents never wake a real device, rule 7):** `wake.custom` (pkg/kws, audiod's
  `WakeListen`/`MicPcm`, `server/internal/wakewords`, the dashboard's Wake words page) has run only in unit tests, the
  `listen` ctest suite (file capture), pkg/kws's golden test against onnxruntime and a synthetic word, and the server's
  tests with a fake negatives fetch. On the VK Capsula Mini (2021 firmware first: raw capture, so audiod holds a
  ListenCapture open), then pickle and midi (the vqe streams between sessions while listening) and the sim: fetch the
  negatives once, train a word from 6 to 15 takes recorded where the speaker stands plus the room, switch it on, and
  check the wake and «<word>, включи свет» without a pause (the pre-roll from `preroll_from_ms`), the false wakes over an
  hour of speech and music (`wakeword_detected` count), cadenced's CPU (the benchmark: 360 us a step on an M4 Pro with
  neon, about 5.6 M multiply-adds; not measured on an A53, where it should stay near 5-10 % of a core), and that muting
  stops it. Known limits: the detector warms up for about 2 s after every session or gap; on pickle and midi a stock
  wake and a custom wake share one vqe stream; the keyword engine's audiod (vk-kws) was not built with these changes
  here (its `KeywordCapture` edits compiled only by review); rpi has no capture configured, so no `wake.custom`; a
  calibration recording is refused while a ListenCapture holds raw capture. Licence (owner decision): openWakeWord's
  README calls its included pre-trained models CC BY-NC-SA; the two feature models shipped here are a fixed mel
  transform and Google's Apache-2.0 speech_embedding, shipped as Apache-2.0 (`pkg/kws/assets/NOTICE`); the negatives are
  CC BY-NC-SA and never leave the owner's server.
- **Media suites on an arm64 Mac:** 17 soloist and airplay-pipe tests (`soloist-resolv` hangs, then `soloist-output`,
  `-launch`, `-supervisor`, `-handlers`, `airplay-pipe`) fail in the media builder image on `main` as on the multiroom
  branch, so `make media-test` does not finish there; every other suite passes.
