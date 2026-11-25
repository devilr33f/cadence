# Cadence for Home Assistant

A custom integration (domain `cadence`) for one Cadence v2 server: one config entry per server, every device of it a
Home Assistant device that follows the server (added, renamed, deleted) without a reload. It reads the server with an
`ha`-scoped token and is pushed everything over the live feed; nothing polls.

## Install

Requires Home Assistant 2026.10 or newer (tested with 2026.10.4): the `cadence.say` schema uses `probatio`, Home
Assistant's validator in 2026.10, which older releases do not ship. The server must be a Cadence v2 server with M8's
routes (device presence on the live feed, `get-device-vitals`, `get-device-artwork`): against an older one the setup
fails on the vitals route's 404 and Home Assistant retries it. It must answer on `https://` with a certificate Home
Assistant trusts: Home Assistant verifies against certifi's CA bundle, or the file `REQUESTS_CA_BUNDLE` names, not the
operating system's store. The integration never sends the token over plain http and never skips TLS verification.

Manual install, either way, then restart Home Assistant:

- copy `custom_components/cadence` from this directory into Home Assistant's `config/custom_components/`;
- or build the zip at the repo root with `make ha-package` (in the pinned Python image, no Python on the host):
  `build/ha/cadence-ha-<version>.zip` holds `custom_components/cadence/`, so it unpacks in Home Assistant's `config/`
  directory: `cd /config && unzip cadence-ha-2.0.0.zip`.

For an init-container deployment, build the native image (BusyBox plus the component at `/cadence`):

```sh
make ha-image HA_COMPONENT_IMAGE=<registry>/cadence-home-assistant HA_IMAGE_TAG=2.0.0
# only an explicit push publishes it:
make ha-image HA_COMPONENT_IMAGE=<registry>/cadence-home-assistant HA_IMAGE_TAG=2.0.0 HA_IMAGE_PUSH=1
```

The image is `<HA_COMPONENT_IMAGE>:<tag>`; set `HA_COMPONENT_IMAGE` to your registry's repository. An
init container mounts Home Assistant's `/config` and cleanly replaces the old component while the HA pod is stopped:

```yaml
command: ["sh", "-c"]
args: ["rm -rf /config/custom_components/cadence && cp -a /cadence /config/custom_components/cadence"]
volumeMounts: [{name: home-assistant-config, mountPath: /config}]
```

Start Home Assistant only after that init container finishes. No build target pushes or changes a running installation by
default.
An update is the same install after deleting the old `config/custom_components/cadence` first (a file a release
removed would stay behind otherwise), then a restart. HACS cannot install it yet: HACS wants the component at a
repository's root, and here it lives under `integrations/home_assistant/` (an open item).

## Token

The integration needs a token of scope `ha` (see `deploy/server/README.md`, *API tokens and scopes*). The server's CLI
prints it once; send it straight into a mode-600 file, never into chat or shell history. With the server at `<server>`
(ssh) and its stack in `~/cadence-v2`:

```sh
(umask 077; ssh <server> 'cd ~/cadence-v2 && docker compose exec -T cadence-v2 /cadence-server admin token create --name home-assistant --scope ha' > ~/.cadence/v2-ha-token)
```

`ha` reads the devices, their media sessions, vitals and artwork, runs only `audio.output` `set_level` and
`change_level`, `media.player` `pause`, `resume`, `stop`, `seek`, `next` and `previous`, and `media.url` `play`,
announces, joins and leaves multiroom groups (`join-device-group`, `leave-device-group`), runs `alarms` `stop_ring`,
`snooze`, `set_timer` and `set_reminder` (never with a level), and follows the live feed's `devices`, `media`,
`vitals`, `groups` and `alarms` topics. On TVs it also reads and searches the
catalog (including alternatives and options), plays a selected ref and switches the current title's source, voiceover or
quality. It cannot set alarms, or manage tokens, settings, providers or the agent.

The config flow refuses a wider token (`token_too_broad`, also in reauthentication and *Reconfigure*): Home
Assistant keeps the token in the clear in its config entries (`.storage/core.config_entries`), so an `admin` one
would give whoever reads that file, or a backup of it, the whole server: its tokens, providers, settings and agent.
The check reads `GET /api/v1/tokens`, which only `admin` may: an `ha` token's 403 there is the expected answer, and the
server logs one `api_forbidden op=list-tokens scope=admin` line for it.

To take the access back, delete the token (`DELETE /api/v1/tokens/home-assistant` with an admin token, or in the
dashboard): its next request is refused, the live feed closes within 20 s, and Home Assistant asks for a new token.

## Set up

*Settings → Devices & services → Add integration → Cadence*: the server's URL (`https://` only, no login, query or
fragment; a path is kept) and the token. The URL is normalized (host lowercased, port 443 and a trailing slash
dropped) and names the entry, so the same server cannot be added twice.

- A refused token (deleted) starts *reauthentication*: the entry stops talking to the server until a new token is
  given. Nothing retries a refused token.
- *Reconfigure* changes the URL or the token. An empty token field keeps the stored one while the host stays the same
  (another port or path); a new host needs the token again, so the stored one never goes to a host it was not given
  for (a mistyped look-alike would get it).
- The token is kept in the config entry alone: never logged, and diagnostics redact it and the URL's userinfo.

## Entities

Every enrolled device becomes a Home Assistant device (manufacturer Cadence, model and name from the server). Its
entities have the unique id `{deviceId}:{key}` and follow the device without a reload: a rename renames them, a
deleted device takes its Home Assistant device and entities with it, a new device (or one that gains a capability)
gets its entities when the server reports it.

| Entity | Key | When | State |
| --- | --- | --- | --- |
| Media player (the device's name) | `speaker` | the device has `audio.output`, `media.player` or `media.url` | the session's state: `playing`, `paused`, `buffering`, `idle` without one |
| Online (connectivity, diagnostic) | `online` | every device | the device is linked to the server |
| CPU usage (%, diagnostic) | `cpu` | every device | the latest metrics round |
| Memory usage (%, diagnostic) | `memory` | every device | used / limit |
| Disk usage (%, diagnostic) | `disk` | every device | the largest filesystem the board reports |
| Temperature (°C, diagnostic) | `temperature` | once a round reports one | the board's sensor |
| Next alarm (timestamp) | `next_alarm` | the device has `alarms` | when its first alarm rings (a snoozed one: the snooze's end); attributes `id`, `days` (none: a one-off, on its `date`) |
| Next timer (timestamp) | `next_timer` | the device has `alarms` | when its first timer ends; attributes `id`, `seconds` |
| Next reminder (timestamp) | `next_reminder` | once the device lists reminders | when its first reminder rings; attributes `id`, `label`, `days` or `date` |
| Ringing | `ringing` | the device has `alarms` | an alarm, timer or reminder rings; attributes `kind`, `id`, a reminder's `label` |
| Alarm (event) | `alarm` | the device has `alarms` | `ring_started`, `stopped`, `snoozed` or `missed`, with `kind`, `id`, a reminder's `label`, `stopped_by`, `snoozed_until` |
| Stop ringing, Snooze (buttons) | `stop_ring`, `snooze` | the device lists the command | stops what rings, or snoozes it for the device's own default minutes |

Everything is unavailable while the live feed is down; the media player and the vitals also while the device is
offline (Online then shows off); the alarm entities stay available then, as the device rings its alarms offline. The vitals are unknown until the device's next metrics round (every 30 s by default,
the device's `system.metrics` setting), also after the server restarts.

The media player:

- shows the session's title, artist, album, duration, position and cover. The cover is read through the server by
  its hash (`get-device-artwork`); no image, url or metadata is stored, and the title, artist and album are kept out
  of the recorder. A video session names its show (`media_series_title`), its season and episode, the episode's own
  name as its title, and where it plays from — `torrplayer · anime365` — as its artist.
- volume is the device's `audio.output` level. A set is `set_level`, a step is `change_level` ±5 (the device resolves
  it against its own level), so the speaker's own buttons and Home Assistant never fight. Home Assistant is an
  authenticated caller and may raise the volume (rule 7); Cadence's agent never touches `cadence` entities.
- offers pause, play, stop, seek, next and previous only when the device has the command and the current session
  lists it (a radio stream has no seek, a URL session no next).
- plays `http` and `https` URLs and media sources (`play_media`, up to 2047 characters, no queue): a song as the
  device's session, an announcement or a TTS URL as an overlay that ducks the music. Browsing shows audio media
  sources.
- on a device with a screen (`media.video`, a TV), the media browser shows the server's TV catalog instead: its home
  rows, each feed with its next page, and a native search over every provider and source at once (anime, films and
  music). Search rows name their provider/source, year and kind. A title expands to **Available on** (every matching
  provider/sub-source, its episode count and best quality), then source, voiceover and quality nodes, seasons and
  episodes. The selected nodes carry those exact choice ids into play, and the server's successful resolve remembers
  them for the title, shared with the TV and voice agent. Unavailable choices show why and cannot be played. Item art
  rides the entity's own image path, and no stream url reaches Home Assistant.
- while a catalog title plays, `source` / `select_source` lists and switches its provider's sources, and `sound_mode` /
  `select_sound_mode` lists and switches its voiceover. Sources are inputs; a voiceover is an audio presentation mode,
  so it fits sound mode better than source. Video quality stays in the browse tree rather than masquerading as either.
  A switch resolves the same episode, keeps its position and remembers the choice. Both selector lists and their values
  are memory-only and excluded from the recorder.
- groups for multiroom (`GROUPING`, on a speaker with `media.url` `play`): `group_members` lists the speakers of the
  device's server-side group, its elected leader first, as this integration's entity ids (`[]` in no group).
  `media_player.join` on a speaker makes its device lead a group with the others (`join-device-group`: its own group
  extended, or a new one named after it; each joined device leaves the group it was in), and `media_player.unjoin`
  takes it out (`leave-device-group`; a group left with one member, or a stereo pair, is deleted). Only this server's
  Cadence speakers can join; a stereo pair takes no other speaker (`stereo_pair`). Groups changed in the dashboard
  show through the feed's `group_changed`, which also reads each affected device again; `group_state` (sync status)
  is not shown. Latency, channels and stereo pairs are set in the dashboard.

## Action `cadence.say`

Speaks a text on Cadence speakers with the server's text-to-speech: 1 to 500 characters, optional `language` (`en` or
`ru`, else the server's default). Name the speakers by their Home Assistant device (`device_id`), their media player
(`entity_id`), or both; each speaker is asked once, all at once, and the first refusal is raised after all were asked.

```yaml
action: cadence.say
data:
  device_id: 2f1c0a8e5b7d4c3a9e6f1b2d3c4a5e6f
  text: Dinner is ready.
  language: en
```

A refusal names the speaker as Home Assistant shows it: offline, busy, no sound output, no text-to-speech set up on
the server, replaced by a newer announcement, or text-to-speech failed or timed out.

## Alarms, timers and reminders

The alarm entities follow the `alarms` topic: the state the server keeps (`alarms_state`) and each ring
(`alarm_ringing`, `alarm_ring_ended`). A ring while the feed was down fires no event (the server replays nothing);
the sensors catch up at the reconnect. An outcome the integration does not know yet ends the ring without an event.
A reminder's `label` is the owner's own text: it shows as an attribute, is kept out of the recorder's database, and is
never logged or put in diagnostics (they count each kind alone).

Home Assistant never sets an alarm (rule 7: alarms are set only in a woken session on the speaker). It may:

- `cadence.set_timer`: a timer of 1 second to 24 hours, in whole seconds, on each named device (`device_id`), each
  with its own id; the device takes it without a voice session.
- `cadence.set_reminder`: a `text` (1 to 500 characters, which the server speaks when it rings; its first 80 runes
  become the reminder's `label`) and either a `time` (once at its next such time, on a `date`,
  or every week on `days`) or `in_minutes`. A device that does not list `set_reminder` is refused before anything is
  sent. No level is ever sent: the device rings it at its own.

```yaml
action: cadence.set_timer
data:
  device_id: 2f1c0a8e5b7d4c3a9e6f1b2d3c4a5e6f
  duration: "00:10:00"
```

## Troubleshooting

| What you see | Why | What to do |
| --- | --- | --- |
| *The server refused the token* (setup) or a reauthentication | the token is deleted, mistyped or of another scope | create a new `ha` token (above) and give it |
| *The token has no ha scope* | a `metrics` token | create one with `--scope ha` |
| *The token can do more than Home Assistant needs* | an `admin` token (see Token) | create one with `--scope ha` alone |
| *Cannot reach the server, or its certificate is not trusted* | wrong URL, the server down, or a certificate Home Assistant's CA bundle (certifi's, or `REQUESTS_CA_BUNDLE`) does not trust: a self-signed one, or a private CA added only to the operating system | check the URL from Home Assistant's host (`curl https://<host>/healthz`); give the server a publicly trusted certificate, or point `REQUESTS_CA_BUNDLE` at a bundle with your CA |
| *Enter the server's https URL…* | `http://`, a login, a query or a fragment | the plain `https://host[:port][/path]` |
| everything unavailable | the live feed is down; it reconnects by itself (1 s, doubling to 30 s) | the log's `cadence_feed_down code=…` says why (`connect`, `tls`, `timeout`, `closed`, …) |
| no media player for a device | the device reported none of the audio capabilities yet | wait for it to link; it appears without a reload |
| no next or previous, or no seek | the current session does not offer them | nothing to fix: they follow the session |
| no cover | the session has none, or the server could not fetch it (only a public `https` address on port 443, images up to 5 MiB; the server's log says why: `artwork_unavailable reason=…`) | nothing to fix |
| no catalog in the media browser | the device has no screen (`media.video`), or the server predates the catalog's `ha` routes (the log's `api_forbidden` or a 404) | nothing to do on a speaker: it shows audio media sources; on a TV, update the server |
| vitals unknown | no metrics round since the server started | wait for the next round |

The component logs ids and codes only. For more, turn on debug logging for it:

```yaml
logger:
  logs:
    custom_components.cadence: debug
```

The server side: `api_forbidden op=… scope=…` in the server's log names a call the token's scope refuses, and
`feed_closed reason=revoked` a feed closed for a deleted token.

## Privacy

- The component's own logs carry device ids and codes only: no token, URL, device name, media metadata or `say` text.
  The media player's title, artist and album are kept out of the recorder's database.
- Home Assistant itself stores what any action is called with, `cadence.say`'s text included. Core fires a
  `call_service` event with the call's data, which the recorder keeps for `purge_keep_days`. At DEBUG,
  `homeassistant.core` also logs the data of a call that fails validation. To keep `say` texts out of the database,
  exclude the event:

  ```yaml
  recorder:
    exclude:
      event_types:
        - call_service
  ```

## Layout

| Path | What |
| --- | --- |
| `custom_components/cadence/client.py` | the server's REST reads and calls (bearer token, 10 s each, codes only in errors) |
| `custom_components/cadence/feed.py` | the live feed: subscribe, read again after every connect or `dropped`, reconnect 1 → 30 s with jitter; a 1008 close or a 401 ends it |
| `custom_components/cadence/models.py` | the server's answers and feed frames, parsed; the art's URL is never kept |
| `custom_components/cadence/coordinator.py` | each device's state (name, model, online, capabilities, `audio.output` level, media session, vitals, alarms), moved by the feed's events, and the Home Assistant devices |
| `custom_components/cadence/entity.py` | the entities' base (unique id `{deviceId}:{key}`, unavailable while the feed is down) and how a platform adds a device's entities as it gains capabilities |
| `custom_components/cadence/media_player.py` | the speaker: session, volume, cover by `artworkHash`, its commands and `play_media` |
| `custom_components/cadence/binary_sensor.py`, `sensor.py` | online and ringing; CPU, memory and disk in use, temperature once reported |
| `custom_components/cadence/alarm_entity.py`, `alarm_sensor.py`, `event.py`, `button.py` | the alarm entities: next alarm, timer and reminder, the alarm event, stop and snooze |
| `custom_components/cadence/services.py`, `services.yaml` | the `cadence.say` action |
| `custom_components/cadence/alarm_services.py` | the `cadence.set_timer` and `cadence.set_reminder` actions |
| `custom_components/cadence/errors.py` | a refused call as a translated error, by its code |
| `custom_components/cadence/config_flow.py` | user, reauth and reconfigure steps; each takes an `ha` token alone (no wider one) |
| `custom_components/cadence/diagnostics.py` | diagnostics without the token, the URL's userinfo or media metadata |
| `scripts/package.py` | `make ha-package`'s zip |
| `scripts/e2e_smoke.py` | the e2e's smoke: the client against the dev stack (devices, feed, a URL session and its pause, `say`, a device restart, vitals, the token revoked) |
| `tests/fixtures/*.json` | the server's own answers and feed frames, written by `server/internal/app/hafixtures_test.go` |
| `tests/fake_backend.py` | a fake server over TLS with a throwaway CA, serving those fixtures |

`client.py`, `feed.py` and `models.py` import nothing from Home Assistant: the e2e smoke loads them as a package of
their own (a `cadence` module whose `__path__` is their directory, so `__init__.py` is not run).

## Development

- `make ha-test` (repo root): ruff (lint and format check) and pytest with `pytest-homeassistant-custom-component`, in
  the pinned Python image, with the exact pins of `uv.lock`; no Python on the host.
- `make e2e` drives `client.py`, `feed.py` and `models.py` against the dev stack (`scripts/e2e_smoke.py`, uv's
  `client` group: aiohttp alone, at the version Home Assistant pins).
- Pins: `uv lock` in that image after editing `pyproject.toml`, then commit `uv.lock`.
- Fixtures: after a change to what the server answers, `go test ./server/internal/app -run TestHAContractFixtures
  -update` rewrites them; `make test` fails while they drift.
