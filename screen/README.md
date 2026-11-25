# cadence-screen

The TV's UI process: SDL3, GLES 3.0 and libmpv, talking `cadence.screen.v1` to cadenced over one SOCK_SEQPACKET
socket (an abstract `@name` on Android). C++20, its own CMake project beside `media/`. From `media/` it takes only the
vendored nanopb and `media/gen` (built here with `PB_ENABLE_MALLOC`).

| Dir | What |
|---|---|
| `platform/` | SDL init (driver hint: `kmsdrm`, `wayland`, `android`, `offscreen`), the GLES 3.0 context without MSAA, vsync, display info, overscan safe area and dp ratio, lifecycle, the remote's keys (long BACK is the assistant) |
| `render/` | `RenderBackend` (below) on GLES, render-when-dirty `FrameScheduler` with S1's startup hold, `FrameStats`, readback and hash for tests |
| `player/` | `Player` over libmpv: the render API into our texture (Linux) or mpv on its own SurfaceView (`vo=mediacodec_embed`, Android), tracks, `MediaState`, the playback gate, level, duck, mute and EQ for the DIRECT route, the `ao_cadence` ring, the AirPlay mirror presenter (`mirror-schedule`: J's five rules; `mirror-presenter`: its thread over a decoder seam; MediaCodec on Android, none yet on Linux, so a Linux screen's Hello says `mirroring: false`), `video-fit` (the picture's shape fitted into the safe area: Android's video plane is placed to it, never stretched) |
| `ipc/` | the screen/v1 client (reconnect with backoff, Hello first), the nanopb codec, the privacy-safe log |
| `app/` | the main loop: link messages to the player and the `UiLayer`, frames when something changed |
| `ui/` | stream b: RmlUi 6.3 on the `RenderBackend`, Afterglow's tokens and components, focus, Cadence Cards, the gallery (`ui/README.md`) |
| `i18n/`, `render/viz/`, `render/kenburns/` | stream p: the translator, the visualizer (its `Canvas` on the backend: `render/viz/screen_backend.h`) and Ken Burns |
| `tests/` | Catch2: `unit`, `ipc` (golden frames in `proto/fixtures/screen/v1`), `gl` (SDL offscreen on Mesa llvmpipe), `mpv` (a generated clip, `ao=null`), `app` (the whole screen against a fake cadenced) |
| `sim/` | the simulator (below): the real screen against a fake cadenced in-process, driven by a script |

`make screen-test` runs everything in `docker/test.Dockerfile` (Debian trixie's `libsdl3-dev`, `libmpv-dev` and Mesa
from the pinned snapshot), then builds for linux/amd64 and linux/arm64. Nothing ever plays sound: every test player
runs `ao=null`.

## The simulator (`sim/`)

`make screen-sim` builds the whole screen natively (no docker): on macos, homebrew's `cmake`, `ninja`, `sdl3` and
`freetype`, with RmlUi 6.3 and the two stb headers fetched at pinned SHA-256 into `build/screen-sim/deps`, and
desktop GL 4.1 core in place of GLES (the same calls, the version word of every shader changed — `render/gl.h`,
`RMLUI_GL3_APPLE`); on linux the same flow over its own packages. mpv is optional: without it the sim brings its own
placeholder player (a clock-true session and a painted video plane), so the player OSD, resolving and the routes all
run with nothing to decode. The wrapper is `build/screen-sim/sim` (`cadence-screen-sim`), and its own tests run with
`make screen-sim-test`.

The sim runs the real screen — the app's loop, the routed UI, the screen/v1 link with its codec and backoff —
against a fake cadenced in its own process: it listens on the socket, answers Hello with the boot sequence
(`ready`, `link_status`, `claim_state`), and serves the catalog's rpcs from the preview fake catalog's data
(`app/routes/preview/fake-catalog.cpp`), streamed on a wall clock. macos has no `SOCK_SEQPACKET` for AF_UNIX, so
`ipc/seqpacket.cpp` carries a stream framing (a 4-byte length a frame) behind the same interface, apple-only; the
linux and android paths stay on the kernel's seqpacket.

```
make screen-sim-run SCRIPT=screen/sim/examples/walkthrough.txt   # headless; screenshots land in sim-out/
build/screen-sim/sim run script.txt                              # the same, by hand
build/screen-sim/sim --windowed run script.txt                   # watch it live
build/screen-sim/sim                                             # commands on stdin, one a line
```

A script is one command a line (`#` comments; `screen/sim/examples/walkthrough.txt` walks them all):

| Command | |
|---|---|
| `start [--size WxH] [--lang ru\|en] [--headless\|--windowed] [--vsync] [--async-images] [--hud]` | boots the screen and the fake, waits for the first frame; `--vsync` paces swaps as a window would, `--async-images` decodes on the cache's workers as the device does, `--hud` draws the frame graph |
| `press KEY` | a remote press (the fake cadenced steps its level by 5 on VOLUME_UP/DOWN and toggles a mute on VOLUME_MUTE, sending the level back as `volume_state`, so the volume overlay shows; `screen/sim/examples/volume.txt`): UP DOWN LEFT RIGHT OK BACK HOME MENU G (the assistant), PLAY_PAUSE PLAY PAUSE STOP NEXT PREVIOUS FAST_FORWARD REWIND VOLUME_UP VOLUME_DOWN VOLUME_MUTE POWER — the app's own key tracker makes the press, so `hold BACK 800` is the assistant |
| `hold KEY MS` | a held key (the long press at 500 ms) |
| `wait MS` / `wait-for SELECTOR [TIMEOUT_MS]` | selectors: `ready`, `link`, `drawer`, `route:home`, `focus:ref`, `pageref:ref`, `query:…`, `media:playing\|paused\|gated\|idle` (the screen's own player), `cast:pause\|resume\|next\|…` (the last transport the screen asked of a cast) |
| `screenshot PATH.png` | one frame read back, the video plane included |
| `route` / `focus` / `context` | print the route, the focused ref, the screen context (refs only, as the server sees it; `position_ms`, where the bar is, while something plays) |
| `send {MESSAGE: {...}}` | any cadenced→screen message: `toast`, `route`, `link_status`, `claim_state`, `voice_state`, `assistant_turn` (transcript, reply, `cards`), `cards`, `airplay_pin`, `mirror_start`/`mirror_stop`, `display_power`, `capability_config`, `duck`, `playback_gate`, `tasks_summary`, `alarms_summary`, `volume_state` (`level`, `muted`, `show`), `media_preparing` (a server resolve's step), `media_play` (a server video: metadata, renditions, `view`, `views`, `audioChoices`), `media_session_state` (the server-owned `actions` and `upNext`, applied to a matching session or held for its play), `now_playing` (the session audiod renders: `sessionId`, `source` airplay/spotify/url, `state`, `title`, `artist`, `album`, `sender`, `actions`, `positionMs`, `durationMs`, `artworkPath` with `artworkSha256`, `colorA`/`colorB`; `{}` is none; `examples/cast.txt`); a `cards` array names the golden fixtures (`card_light_living.binpb`, …) |
| `send-file PATH` | a whole frame from a `.binpb` (the golden fixtures in `proto/fixtures/screen/v1` are such frames) |
| `send-json PATH` | what `send` takes, from a file: `alarms_summary` (`devices` with their `items`, `ringing`, `revision`; `nextInS` is a time that many seconds after the send) is the household's alarms, and the fake answers `alarms.act` by sending it again without the ring it stopped or snoozed, or the timer or reminder it cancelled (`examples/alarms.txt`: home's strip, the ring overlay, the page, the display rule) |
| `bands B0 ... B15` | one audiod-style classic-visualizer analysis frame, 16 integers 0–255 (`examples/audio-bars.txt`); on macos this stands in for the tap, whose production path is unchanged |
| `pcm [MONO_NS\|now]` | one audiod-style stereo PCM snapshot, 512 frames of a moving tone; `now` stamps its first frame from the mac clock, `0` exercises the output-latency-unknown path, and an integer is an explicit monotonic timestamp |
| `rpc-answer METHOD FILE` | overrides one method's answer from a json file: `{"status", "json", "delayMs", "page", "events": [{"atMs", "json", "last", "page"}], "withMediaPlay"}` (`{"delayMs": 2000}` alone: the built-in answer, that late, to see a skeleton; `page`: card fixtures by file name, or `"devices"` for the fake devices panel, whose page `devices.panel` answers by default); before `start` it answers the calls the screen makes at boot (`devices.media`'s subscription: `examples/devices.txt`); `--fixtures DIR` loads `<method>.json` for every method at boot; `METHOD#path` (`catalog.browse#ytmusic`) answers only the calls with that path |
| `rpc-calls METHOD` | print how often the screen called the method (or `METHOD#path`, the calls with that path) and its last args (`screen/sim/examples/ha-live.txt` checks the drawer's lease heartbeat and release) |
| `drop-link` | cadenced goes, as a restart would; the screen reconnects |
| `perf [reset\|LABEL]` | the frame profile since the last reset: one `perf` line (fps, turns a second, cpu and phase percentiles in ms, uploads, draws, clip masks, fill) and the three worst frames phase by phase (`screen/sim/examples/perf.txt` walks home and a title, 1080p, then idles) |
| `quit` | |

Known gaps: the fake's catalog answers are the preview's made-up one (override per method with fixtures for other
shapes); there is no mirroring decoder (a Linux screen has none either; the badge still shows) and no tap, so scripts feed
its classic bands with `bands` and its pcm-driven scenes with `pcm`. On a link flap the UI falls to the enroll page and
stays until a route is sent, as the real cadenced's messages would move it.


## Frame profile (`render/perf.h`)

Every loop turn is timed by phase (`events`, `link`, `update`, `draw`, `gpu`, `swap`) with the ui library's own spans
inside (`rml_update`, `rml_render`, `upload`, `decode`, `image_scan`) and what the frame drew (draws, clip masks,
layers, fill: the pixels the draws' bounds cover, the stencil the clip masks cleared, and the gradients: rmlui's
shader decorators, a per-pixel program, counted with their pixels as `gradients` and `gradient_mpx`). Numbers only
(rule 8):

- `screen_perf` every 60 s (5 s with `CADENCE_SCREEN_PERF=1`): frames and fps, loop turns a second (the idle cost),
  animation frames and the late ones (over 1.5 refresh periods), cpu work p50/p95/max (a frame's phases without the
  swap), update/rml/draw/swap p95, draws, clip masks, fill in Mpx, uploads and their ms. A window with no frame logs
  its turns alone.
- `CADENCE_SCREEN_PERF=gpu` adds a `glFinish` before each swap, so `gpu_p95_ms` is the gpu's own time and
  `swap_p95_ms` the vsync wait alone (it costs throughput: measure with it, run without).
- `CADENCE_SCREEN_HUD=1`: a frame graph in the top-left corner (a bar a frame: cpu in green, red when late, the swap's
  wait in grey, the refresh period in yellow) with the window's numbers, drawn only in frames the loop presents anyway.
- Android has no env for the app: `adb shell setprop debug.cadence.screen.perf 1` (or `gpu`) and
  `debug.cadence.screen.hud 1`, then restart the app.
- Where a slow swap comes from, on the device: `swap_p95_ms` near a whole number of refresh periods with
  `CADENCE_SCREEN_PERF=gpu` moving it into `gpu_p95_ms` is the gpu's own work (fill, gradients: the counters say
  which); a swap that stays long with `gpu_p95_ms` small is the compositor's back-pressure, which
  `adb shell dumpsys SurfaceFlinger` shows (the screen's SurfaceView composed `CLIENT`/GLES rather than `DEVICE`
  costs a gpu pass a frame of its own).

## RenderBackend (`render/render-backend.h`)

The one drawing surface for every layer: the UI (stream B implements RmlUi's render interface on it), Ken Burns and
the visualizers (stream P), the video. It runs on the main thread with the context current.

- Rects are pixels with a top-left origin, on whatever is bound.
- `Texture` values carry `flipped` for render targets and mpv's frame. The quad helpers undo it, so callers never
  flip.
- `beginFrame`, `resetState` (after mpv or raw GL), `createTexture`/`updateTexture` (RGBA8, R8)
- `createRenderTarget(size, stencil)` and `bindRenderTarget` (null is the screen)
- `setScissor`, `setBlend` (None, Premultiplied, Alpha), `compileProgram` and `setUniform`
- `drawTexturedQuad(texture, {dst, uv Mat3, opacity, blend})`
- `drawFullscreen(program, textures, uv, opacity, blend)` with `kFullscreenVertexShader`. The fragment shader gets
  `v_uv`, `u_tex0..`, `u_opacity`; a Ken Burns frame is `Mat3::scaleOffset`.

## Player (`player/player.h`)

- `play(PlayRequest)`, `command(Pause|Resume|Stop|Seek|SelectTrack)`.
  `setGate`, `setLevel`, `setDuck`, `setMute`, `setEq` return `""` or screen/v1's code token.
- `pump(nowMs)` on every loop turn, then `frameDue`/`renderFrame`/`videoTexture`/`reportSwap` on the render API path.
- `state()` with `onStateChanged` (debounced: a steady play reports nothing); `positionSample()` is mpv's time-pos as
  it last came, which the bar runs on.
- A seek (absolute, or relative from the target of one still under way, so quick presses add up) is exact and counted
  (`seekSeq`); until it lands (its answer, then mpv's playback-restart) the player reports its target and drops the
  time-pos mpv read before it (`seekPending`). The routes' `PositionModel` holds the bar on its own target until a
  reading of that seek arrives, and a cadenced echo once one read after the seek lands near it.
- Rule 7: content waits paused (and at volume 0 on the stock AO) until the gate opens. Losing the link shuts it.
  Ambient is route NONE with no audio track. Until cadenced sends a level, the stock AO's gain is 0.
- Every mpv call that changes state is async. With the render API's advanced control the core may wait on the render
  thread, so a synchronous call from it can deadlock.
- `CADENCE_SCREEN_AO`: `stock` (default) is mpv's own AO, route DIRECT, levelled through mpv's `volume` and `af`.
  This is the stopgap. `cadence` is the patched mpv's `ao_cadence` into audiod, route MIXER, so level, duck and EQ
  answer `unsupported` here. the pcm ring's ABI is `media/lib/src/ring/cadence_ring.h`, the one copy; audiod offers each ring over its tap (`player/ring-offer.h`).

## UiLayer (`app/ui-layer.h`)

Stream B's entry point. The interface:

- `attach(backend, geometry, outbox)`, `detach`, `resize`
- `key`: true when used; unused keys go to cadenced as `InputEvent`
- `message` (every UI envelope), `playerState`, `update`, `dirty`, `ready`, `render`, `status`, `cardVersions`

Nothing is shown until `ready()` (S1's startup fix). `ui::ShellLayer` (`ui/core/shell.h`) is the one the binary runs;
`NullUi` stays for the tests.

Every page change cross-fades (`Ui::crossfade`, 220 ms eased): the layer as last drawn is copied once and fades out
over the new page, which is shown only once laid out, so no half-built frame is ever presented; a route calls it too
before it swaps a skeleton for its content. It costs one full-screen quad a frame while it runs, and the layer is not
drawn again for it. Skeletons shimmer in 16 class steps (10 redraws a second while something loads, never more).
On a retina mac the window asks for the display's own density (`SDL_WINDOW_HIGH_PIXEL_DENSITY`, not for headless
runs), so rounded edges are drawn at twice the pixels; the stick keeps 1x without msaa.

The shell keeps the volume overlay (`ui/core/volume-overlay.h`, `ui/assets/rcss/routes-volume.rcss`) over every page:
an upright pill at the right edge, shown for each `VolumeState` cadenced sends with `show` (the level in force after
cadenced applied it: the remote, a voice turn, the dashboard or Home Assistant, an AirPlay sender, android's own
volume) and gone 2 s after the last; a layer of its own (z 60) with nothing focusable, so focus stays put.
