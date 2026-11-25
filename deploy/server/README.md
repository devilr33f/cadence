# Cadence v2 server

> New here? Use the [quickstart](../quickstart/README.md) instead: one host, one command, HTTPS included. This page is
> the full reference, and its stack sits behind the owner's own Traefik and Cloudflare.

Owner-gated: nothing here is deployed by CI or by an agent. The server runs as its own Compose project `cadence-v2`
(server, dashboard + Postgres) on its own Traefik host, so it can run beside a v1 stack without touching it. The
examples use `<server>` for the ssh host and `~/cadence-v2` for the stack's directory; use your own.

```text
device, browser --wss/https--> Cloudflare --> Traefik (web-secure, tls) -+-> cadence-v2:8080 (/api/, /v2/, /healthz: plain http + websocket)
                                                                        |     └── postgres:5432 (project network only)
                                                                        +-> cadence-dashboard:3000 (every other path: node ssr)
                                                                              └── its ssr calls: http://cadence-v2:8080 (project network)
```

- Device link: `wss://<host>/v2/device` (device token); enroll: `POST https://<host>/v2/enroll` (one-time code).
- Admin API: `https://<host>/api/v1/…` with `Authorization: Bearer cdn_…` (a token of the scopes the call needs, see
  [API tokens and scopes](#api-tokens-and-scopes)), or the owner's session cookie (see [Owner login](#owner-login)).
  Its OpenAPI 3.1 document is committed at `server/openapi/openapi.json` (`make openapi` regenerates it, `make openapi-check` fails on drift) and served at `/api/v1/openapi.json` behind the same auth (admin scope; its yaml, 3.0 and schema siblings are not served). Each operation's `x-cadence-scope` names the scope it needs. Every admin error, fiber's own too (an unknown route 404 `not_found`, a method 405 `method_not_allowed`, a body over 4 MiB 413 `too_large`, the live feed without an upgrade 426 `upgrade_required`), is `{"error": <code>, "path"?, "detail"?}`; the device API and `/healthz` keep fiber's plain text. A list answers `[]` for none, never `null`. Dashboard note (M7b): a missing required field is pointed at its parent object (Huma's location), and pointers are best-effort for keys containing `.` or `[`.
- Health: `GET /healthz` → `{"status":"ok","db":"ok",…}` (no token; `503` while the database is down).
- Dashboard: `https://<host>/` (see [Dashboard](#dashboard)): every path the server does not take.

```text
~/cadence-v2/
  compose.yml          copied from deploy/server/compose.yml
  .env                 CADENCE_V2_PUBLIC_HOST=<host>, CADENCE_REVISION=<git sha>; no secrets
  config/cadence.env   DATABASE_URL (holds the database password), mode 600
  config/postgres.env  POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_DB, mode 600
  config/master.key    base64 of 32 random bytes, mode 600, owned by 65532 (the image's nonroot user)
  src/                 git archive of the deployed revision (both images' build context)
```

## Environment

Configuration lives in the database and is changed through the API; the environment holds bootstrap values only.
Errors name a variable, never its value.

| Variable | Where | Notes |
| --- | --- | --- |
| `DATABASE_URL` | `config/cadence.env` | required; `postgres://cadence:<password>@postgres:5432/cadence?sslmode=disable` with the password of `config/postgres.env` |
| `CADENCE_MASTER_KEY_FILE` | `compose.yml` | required; `/run/secrets/master.key`, the read-only mount of `config/master.key`. It seals provider keys in the database: the server refuses a key file readable by group or others, and losing the file loses every stored provider key |
| `CADENCE_LISTEN` | `config/cadence.env` | optional, default `:8080`; must match the Traefik service port label |
| `CADENCE_DEV_FAKES` | never in production | `1` registers the `fake-stt`, `fake-llm` and `fake-tts` modules; dev compose and CI only |
| `CADENCE_PUBLIC_ORIGIN` | `compose.yml` | `https://${CADENCE_V2_PUBLIC_HOST}`: the origin (`scheme://host[:port]`, lowercase, no default port or path) a cookie request's `Origin` or `Referer` must have. Unset, the request's own scheme and `Host` count, which behind Traefik is `http://…` and refuses the dashboard |
| `CADENCE_TRUSTED_PROXY` | `config/cadence.env` | set in [One-time setup](#one-time-setup); comma-separated addresses or CIDR ranges of the proxies in front of the server: the Docker network Traefik reaches it on and Cloudflare's published ranges. The login rate limit takes the rightmost `X-Forwarded-For` entry that is none of them as the client; unset, the socket's address (Traefik) counts, so every login shares one per-ip limit (see [Owner login](#owner-login)) |
| `CADENCE_YTDLP` | the image | `/opt/yt-dlp/bin/yt-dlp`, the yt-dlp the `yt-dlp` music player runs (see [Music](#music)); unset, no `yt-dlp` instance builds |
| `CADENCE_WAKEWORD_NEGATIVES` | compose.yml | `/var/lib/cadence/wakeword/negatives.bin` on the `cadence-v2-wakeword` volume: the negative features the custom wake word's training reads, which the dashboard's Wake words page fetches there once (see [Custom wake words](#custom-wake-words-admin-api)); unset, training and the fetch are refused |
| `CADENCE_FFMPEG` | the image | `/usr/local/bin/ffmpeg`, the transcoder of an alarm's music (see [Ring sounds](#ring-sounds-an-alarms-music-and-a-reminders-speech)); unset, ffmpeg on the `PATH`, and without one music alarms ring their tone |
| `CADENCE_YANDEX_PROXY_LISTEN` | `compose.yandex.yml` | optional, disabled by default; `:7443` enables the owner-managed station provisioning listener. See [Yandex provisioning](../yandex/README.md) |
| `CADENCE_YANDEX_PROXY_CA_CERT`, `CADENCE_YANDEX_PROXY_CA_KEY` | `compose.yandex.yml` | required with the listener; owner-supplied signing chain and mode-600 key, never committed |
| `CADENCE_YANDEX_PROXY_CLIENTS` | `config/cadence.env` | required with the listener; comma-separated station IP addresses or CIDR ranges, all others refused |
| `CADENCE_YANDEX_PROXY_DEVICES` | `config/cadence.env` | optional comma-separated `vendor-device-id=cadence-device-uuid` mappings for captured vendor log rows |
| `CADENCE_YANDEX_PROXY_UPSTREAM_DNS` | `config/cadence.env` | optional resolver `ip:port` for upstream connections when the server's own DNS maps Yandex back to Cadence |
| `CADENCE_COOKIE_INSECURE` | never in production | `1` leaves `Secure` (and with it the `__Host-` and `__Secure-` name prefixes) off the login's cookies, for a dev stack on plain http only |
| `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` | `config/postgres.env` | read by the Postgres image at the first start of the `cadence-v2-db` volume |
| `CADENCE_V2_PUBLIC_HOST` | `.env` | the owner's chosen hostname for the Traefik `Host(...)` rule; Compose refuses to start without it |
| `CADENCE_REVISION` | `.env` | git sha of the image tags `cadence-server:<sha>` and `cadence-dashboard:<sha>`; Compose refuses to start without it |
| `CADENCE_API_INTERNAL` | `compose.yml` (`cadence-dashboard`) | `http://cadence-v2:8080`: the server's origin on the project network, for the dashboard's SSR calls (an `http(s)` origin, no path); the dashboard exits at start without it, naming the variable only |

v1's images are tagged `cadence-server:<v1 sha>` too: v2 tags use v2 shas, so the two never collide.

## One-time setup

Run on the server (`ssh <server>`). No value is printed.

```sh
mkdir -p ~/cadence-v2/config && cd ~/cadence-v2
umask 077
head -c 32 /dev/urandom | base64 > config/master.key; chmod 600 config/master.key
sudo chown 65532:65532 config/master.key
pw=$(openssl rand -hex 24)
printf 'POSTGRES_USER=cadence\nPOSTGRES_PASSWORD=%s\nPOSTGRES_DB=cadence\n' "$pw" > config/postgres.env
printf 'DATABASE_URL=postgres://cadence:%s@postgres:5432/cadence?sslmode=disable\n' "$pw" > config/cadence.env
unset pw
printf 'CADENCE_V2_PUBLIC_HOST=%s\n' '<host>' > .env
# the subnet of the docker network traefik reaches cadence-v2 on (see below), e.g. traefik_cidr=172.18.0.0/16
traefik_cidr=
v4=$(curl -fsS https://www.cloudflare.com/ips-v4 | grep . | paste -sd, -)
v6=$(curl -fsS https://www.cloudflare.com/ips-v6 | grep . | paste -sd, -)
if [ -n "$traefik_cidr" ] && [ -n "$v4" ] && [ -n "$v6" ]; then
  printf 'CADENCE_TRUSTED_PROXY=%s,%s,%s\n' "$traefik_cidr" "$v4" "$v6" >> config/cadence.env
else
  echo 'CADENCE_TRUSTED_PROXY not written: traefik_cidr empty or cloudflare ranges not fetched; set traefik_cidr and run the traefik_cidr= to fi lines again' >&2
fi
unset traefik_cidr v4 v6
```

Keep a copy of `config/master.key` off the server (mode 600). The compose file declares no network: Traefik reaches
`cadence-v2` on the project's default network; if your Traefik needs a shared network, add it here.

`CADENCE_TRUSTED_PROXY` is not optional behind Cloudflare: the login rate limit counts failures per client address.
Cloudflare puts the client in `X-Forwarded-For`, Traefik appends its own peer (a Cloudflare edge), and the server
walks the list from the right past every trusted entry. So it trusts the Docker network Traefik reaches `cadence-v2`
on (`traefik_cidr` above: `docker network inspect <that network> -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}'`, comma
separated when it prints two) and Cloudflare's published ranges ([cloudflare.com/ips](https://www.cloudflare.com/ips/), the two lists the step above
fetches; refresh the line when Cloudflare changes them). Traefik must keep Cloudflare's header too: it deletes
`X-Forwarded-*` from a peer outside its entrypoint's `forwardedHeaders.trustedIPs`, so that list on `web-secure` holds
Cloudflare's ranges as well. Otherwise the only entry is the edge, which every client shares. Check it with
[the login check](#owner-login).

## Build and start

From the owner's machine, for revision `<sha>` (a commit on `main` or the branch under test):

```sh
git archive <sha> | ssh <server> 'rm -rf ~/cadence-v2/src && mkdir -p ~/cadence-v2/src && tar -x -C ~/cadence-v2/src'
scp deploy/server/compose.yml <server>:cadence-v2/compose.yml
```

Then on the server:

```sh
cd ~/cadence-v2
docker build -f src/server/Dockerfile --build-arg VERSION=<sha> -t cadence-server:<sha> src
docker build -f src/deploy/dashboard/Dockerfile -t cadence-dashboard:<sha> src
sed -i '/^CADENCE_REVISION=/d' .env && echo 'CADENCE_REVISION=<sha>' >> .env
docker compose up -d
docker compose logs --since 10m cadence-v2 | grep server_listening
docker compose logs --since 10m cadence-dashboard | grep dashboard_listening
curl -fsS https://<host>/healthz
curl -fsS -o /dev/null -w '%{http_code}\n' https://<host>/login
```

Both images carry the revision in their tag (`CADENCE_REVISION`), so they deploy and roll back together. An older
revision without `deploy/dashboard/` predates the dashboard: its compose file has no `cadence-dashboard`, so roll back
to one with `docker compose up -d --remove-orphans`, which stops the dashboard left from the newer one.

Never run `docker compose config` here: it prints the env files.

## First admin token

The CLI prints the token once on stdout; send it straight into a mode-600 file on the owner's machine:

```sh
(umask 077; ssh <server> 'cd ~/cadence-v2 && docker compose exec -T cadence-v2 /cadence-server admin token create --name owner' > ~/.cadence/v2-admin-token)
```

## API tokens and scopes

A token holds one or more scopes; tokens made before scopes are `admin` ones. The owner's session cookie is `admin`.
Every answer under `/api/v1`, refusals included, carries `Cache-Control: no-store` unless its route sets its own.

| Scope | May call |
| --- | --- |
| `admin` | everything |
| `ha` | `list-devices`, `get-device`, `get-device-media`, `get-device-artwork`, `get-device-vitals`, `send-device-command` for `audio.output` (`set_level`, `change_level`), `media.player` (`pause`, `resume`, `stop`, `seek`, `next`, `previous`), `media.url` (`play`), `media.video` (`play`) and `alarms` (`stop_ring`, `snooze`, `set_timer`, and `set_reminder` without a `level`) only (any other command, setting an alarm for one, is 403 `forbidden`, log `api_forbidden op=send-device-command scope=admin`), `announce-to-device`, the catalog's reads (`list-catalog-sources`, `search-catalog`, `browse-catalog`, `get-catalog-item`, `get-catalog-options`, `get-catalog-alternatives`), `get-catalog-image`, `play-catalog`, `get-device-catalog-session` and `select-device-catalog` (the same play a screen's own remote starts; no stream url leaves the server), and the live feed's `devices`, `media`, `vitals`, `groups` and `alarms` topics |
| `metrics` | the Prometheus exposition `GET /api/v1/metrics` (a plain admin route, see [Telemetry](#telemetry)); nothing else |
| `presence` | `get-busy`, `put-busy` and `delete-busy` (see [Busy mode](#busy-mode-admin-api)); nothing else, no live feed topic |
| `mcp` | the MCP endpoint (see [MCP endpoint](#mcp-endpoint)) with the tools the token names — none named, every tool; nothing else |

An operation that names no scope is `admin`'s alone, so a new route stays admin-only until its doc widens it; the
OpenAPI document's `x-cadence-scope` says which scope each operation needs. A token without it gets 403
`{"error":"forbidden"}` (log `api_forbidden op=<operation id> scope=<required>`, never the token). The admin routes
that are no Huma operation (the live feed, the Prometheus exposition) are `api.PlainRoute`s, which name their scopes
the same way: none named, admin only.

- CLI: `cadence-server admin token create --name <name> [--scope admin|ha|metrics|presence|mcp]... [--mcp-tool <tool>]...`
  (`--scope` repeats, default `admin`; `--mcp-tool` repeats and narrows an `mcp` token to the tools it names, none named:
  every tool) prints the token once on stdout; a bad name or scope exits 2 before the database is reached, a taken name
  exits 1 (`token_create_failed code=name_taken`). Home Assistant's, into a mode-600 file as above:

  ```sh
  (umask 077; ssh <server> 'cd ~/cadence-v2 && docker compose exec -T cadence-v2 /cadence-server admin token create --name home-assistant --scope ha' > ~/.cadence/v2-ha-token)
  ```

- `GET /api/v1/tokens` → `[{name, scopes, mcpTools?, createdAt, lastUsedAt?}]`, never a token or its hash; `lastUsedAt` is
  written at most once a minute.
- `POST /api/v1/tokens {name, scopes, mcpTools?}` → 201 `{name, scopes, token}` with `Cache-Control: no-store`, the only time the token is shown. A name is
  unique and matches `^[a-z0-9][a-z0-9._-]{0,63}$`; scopes are a non-empty list of known ones, each once (else 422
  `invalid` with `path`); `mcpTools` names MCP tools (each once, at most 128, each one the endpoint serves, else 422
  `unknown_mcp_tool`); a taken name is 409 `name_taken`.
- `PATCH /api/v1/tokens/{name} {mcpTools}` → 204: changes which MCP tools the token sees, empty for every tool, at once
  (the tool lists its clients see and every call follow); or 404 `not_found`, 422 `unknown_mcp_tool`/`invalid`.
- `DELETE /api/v1/tokens/{name}` → 204, or 404 `not_found` (a name from before the pattern, with a space or `/`, goes
  percent-escaped: `old%20token`). Every later request with the token is refused (401); a
  live feed it opened closes at its next check, within 20 s (see [Live feed topics](#live-feed-topics)).
- Only `admin` manages tokens, so an `ha` token cannot mint a wider one.

## MCP endpoint

`POST /api/v1/mcp` serves Cadence itself as an MCP server (streamable HTTP, stateless, JSON responses), so an outside
AI client — an editor, an agent harness — can read and steer the household the way the dashboard and Home Assistant
do. It needs an `mcp`-scoped (or `admin`) bearer token; the edge forwards `/api/v1` as for the admin API, so the URL is
`https://<host>/api/v1/mcp`. The tools are the voice agent's own (music, catalog, media control, volume, the home,
timers and reminders, tasks, screens, weather and search — run exactly as a turn runs them, sharing the voice turns'
music result ids and queues) beside the endpoint's own: `list_devices`, `get_device`, `get_playback`,
`playback_history`, `list_favorites`, `say`, `assistant_turn` (a typed turn that answers its reply),
`play_url`, `send_command` and `get_media_link` (a token-sealed, expiring relay url for a music result, a YouTube
video's audio or a catalog episode). `GET /api/v1/mcp/tools` (admin) lists them.

A token's tool list (see above) narrows what its clients see: each request serves exactly that list, so a tool outside
the policy does not exist for that token's client. Everything an agent may not do by rule 7 holds as for a typed
dashboard turn; the endpoint's own logs carry tool names and codes only.

Connecting Claude Code (its config holds the token; `claude mcp add` also takes `--header`):

```sh
claude mcp add --transport http cadence https://<host>/api/v1/mcp --header "Authorization: Bearer cdn_…"
```

## Live feed topics

`wss://<host>/api/v1/live` (a bearer of `admin` or `ha`, or the owner's cookie from the server's own `Origin`) writes
`{"type":"subscribed"}` first. The client may send `{"subscribe": [topics], "deviceId": "…"?}` at any time; each one
replaces the previous subscription and is answered `{"type":"subscribed","topics":[…],"deviceId"?}`, after which
every event of those topics reaches the client. Without one, the feed writes every topic the scope allows but `logs`
and `traces` (`admin`: the other nine, and any event type no topic maps yet; `ha`: `devices`, `media`, `vitals` and `groups`): `logs` and `traces` are opt-in, for their
volume, and reach only a client that subscribed to them (Ruling M7a-R5). The query's `?deviceId=` keeps
filtering; a subscribe's `deviceId` overrides it (`""`: every device), and a subscribe without one falls back to the
query's.

| Topic | Event types | Scope |
| --- | --- | --- |
| `devices` | `device_online`, `device_offline`, `device_added`, `device_renamed`, `device_deleted`, `device_volume` (below) | `ha` |
| `sessions` | `turn_start` (its `trigger`: `wake`, `button`, `follow_up`, `event` or `dashboard`, the names the conversation history stores; none when unspecified), `transcript`, `turn_end`, `assistant_speech` (a dashboard assistant turn's reply played on its device: `{code}`, `ok` or the announcement's refusal) | `admin` |
| `agent` | `llm_request`, `llm_text`, `tool_call`, `tool_result`, `mode_switch`, `budget_spent`, `memory_recall`, `memory_changed`, `compaction`, `learning_run`, `learning_op`, `mcp_connected`, `mcp_unavailable`, `mcp_tools_changed` | `admin` |
| `tasks` | `task_event` | `admin` |
| `media` | `media_state` | `ha` |
| `vitals` | `device_vitals` (below) | `ha` |
| `logs` | `log_line` (at most 50 a second to each feed, the rest counted in the feed's own `log_line_dropped {count}`); opt-in: only by subscription | `admin` |
| `presence` | `busy_changed` `data: {busy, reason?}`, once for each device whose busy state or reason changed (see [Busy mode](#busy-mode-admin-api)); never a source | `admin` |
| `subscriptions` | `subscription_update {id, topic, event, state}`, `subscription_changed {id, topic, action}` (`created`, `deleted`, `ended` by its next stop), `subscription_inbox {ids, action}` (`held`, `delivered`, `inbox`, `seen`, `answered`): ids, topics and codes, never an outside system's text | `admin` |
| `alarms` | `alarms_state` (a device's whole `alarms` state as it reported it, whenever it changed: alarms, timers, reminders with their labels, `ringing`, `snoozed`, `lastRing`), `alarm_ringing {kind, id, label?, time?, startedAt}` (a ring began; `startedAt` when the server heard it), `alarm_ring_ended {kind, id, outcome, stoppedBy?, snoozedUntil?, at}` (`outcome` `stopped`, `snoozed` or `missed`: it rang to its limit unanswered). A reminder's label is the owner's own setting, shown on screens and in Home Assistant, never logged | `ha` |
| `groups` | `group_changed {id, name, kind, leaderId, members}` (a multiroom group was created or changed, its members, config or elected leader; `{id, leaderId: "", members: [], deleted: true}` once deleted), `group_state {groupId, status}` (by the reporting device: its `media.group` state as it reported it, numbers and tokens only) | `ha` |
| `traces` | `span_start`, `span_update` (a streaming span's byte counters, at most one a second a span), `span_end`, each `data: {seq, span}` with the whole span (see [Tracing](#tracing)); at most 200 a second to each feed, the rest counted in the feed's own `span_dropped {count}`; opt-in: only by subscription | `admin` |
| `wakewords` | `wakeword_changed {id, status, code}` (a word added, trained, failed, renamed, its dataset dropped, or `status: deleted`), `wakeword_progress {id, stage, fraction}` (a training's `queued`, `embed`, `train`, `calibrate`, at most every 2 %), `wakeword_negatives {fraction, code, done}` (the negatives' fetch): ids, codes and numbers, never a word or audio | `admin` |

The `devices` and `vitals` events (M8-D2, M8-D3), each frame `{type, deviceId, at, data?}` (`at` RFC 3339, UTC):

- `device_online` and `device_offline`: the device's link opened (its hello) or closed; a link that replaces another
  publishes `device_online` again, and no `device_offline` for the one it replaced. `device_added`: its enrolment
  completed (`online: false`: its token opened no link yet). `device_renamed`: after a rename. Each carries
  `data: {name, model, online}`, the device as `GET /api/v1/devices` lists it, `online` whether it is linked at that
  moment. A link's events take the name and model its last-seen write read before the link was attached (or a later
  rename), so a store that fails or is slow loses no `device_offline` and holds up no hello or reconnect. Without them
  (that write failed, and the server has not seen the device since it started) the event reads the device after the
  link is attached or let go and is published only while the link still is (or is not) the device's; a read that fails
  within 3 s publishes nothing (log `device_presence_failed device_id=… type=device_online|device_offline`).
- `device_deleted`: once its delete committed, with no `data`; its link's close then publishes no `device_offline`.
- `device_volume` `data: {level}` (0–100): the `audio.output` level the device reported changed, or was reported for
  the first time since its capability was stored (Ruling M8-R1). No other capability state is published.
- `device_vitals` `data: {cpu, memoryUsedBytes, memoryLimitBytes, disk: [{mountpoint, freeBytes, limitBytes}],
  temperatureC?, at}`, also what `GET /api/v1/devices/{id}/vitals` answers (see [Telemetry](#telemetry)).

None of them is replayed: a client that connects or reconnects reads `GET /api/v1/devices` (and each device's
media and vitals) for the state now, then follows the events.

`dropped {count}` (events the client was too slow for) reaches every subscription. Refusals keep the connection and
the subscription before: an unknown topic is `{"error":"topic_unknown","detail":"<topic>"}` (the detail only for a
lowercase token), one the scope does not allow `{"error":"forbidden","detail":"<topic>"}` (log
`api_forbidden op=live scope=admin topic=<topic>`), anything else `{"error":"invalid"}`. A client frame over 4 KiB,
or more than 10 in a second (pings and pongs count), closes the feed with 1008 (log `feed_violation reason=too_large|rate_limited`). A token
whose scopes allow no topic (`metrics`) gets 403 `forbidden` at the upgrade.

The feed checks its credential again with every ping (20 s): a deleted token, one whose scopes changed, or a session
that ended (logout, expiry, 7 days unused, a password change) closes it with 1008 `unauthorized` (log
`feed_closed reason=revoked`). A check the store cannot answer within 2 s leaves the feed open until the next one
(log `feed_recheck_failed code=store|timeout`). A check counts as use of the session and the token. Other ends log
`feed_closed reason=peer|shutdown`.

## Devices (admin API)

- `POST /api/v1/devices {name, model}` and `PATCH /api/v1/devices/{id} {name}` (admin) take a name trimmed of white
  space, then 1–64 characters with no control or format character and no line or paragraph separator (else 422
  `invalid`, path `/body/name`), and no other device's in any case, Cyrillic too (409 `name_taken`; a unique index on
  `lower(name)`). A device's own name, in any case, is no clash. Upgrading a database whose names clash in case
  renames them as the index is made (migration 00027, ruling M7bs-R7): the oldest of each keeps its name, the later
  ones take ` 2`, ` 3`… (cut to 64 characters, a name no device has); a rollback leaves them renamed.
- `GET /api/v1/devices` → `[{id, name, model, status, online, lastSeenAt}]`; `online` is the device's link now.
- `GET /api/v1/devices/{id}`: the device with each capability's state, config and descriptor as the device last
  reported it: `configSchema` and `stateSchema` (JSON Schema objects as written, `null` for none or a boolean schema),
  `commands: [{name, argsSchema}]` by name, `secretSpecs: [{name, description?}]` in declared order, and `secrets`
  (`{name, description, set}`, never a value). `ha` reads both routes.
- `PATCH /api/v1/devices/{id}` → the device as `GET` answers it; 404 `not_found` for an unknown id. Prometheus labels,
  log reads and the OTLP export stamp the new name from the next scrape, read or batch on.
- A device's name is also the name its receivers advertise while their config sets none: AirPlay (`media.airplay`'s
  `name`, a screen's `media.screencast` `name`) and Spotify Connect (`media.soloist`'s `deviceName`). The device gets
  it in the `Welcome` and again at each rename, re-advertises at once (Soloist restarts only when the name it
  advertises changes), and keeps it beside its configs for the next boot, so it advertises the name before and without
  a link. It drops control and format characters and cuts the name to 32 bytes at a character's boundary (a Cyrillic
  name keeps 16 letters); a device with no name yet advertises `Cadence`. A name set in a config wins, and the
  capability's config and state never show the device's.
- `DELETE /api/v1/devices/{id}[?status=pending]` (admin) → 204, else 404 `not_found` (a second delete too). With
  `status=pending` it deletes the device only while it is pending: one enrolled by then (also by an enrolment that
  commits while the delete waits for its row) answers 409 `not_pending` and nothing is deleted, so an enrol wizard's
  Remove never takes a speaker that just enrolled. One transaction removes the device's row with its token, its
  settings overrides, enrol codes, capabilities with their sealed secrets, device-scope memories, conversation turns
  and messages, compactions and learning runs, task notices and told marks, and Home Assistant usage; a background
  task it started stays, without its origin. A settings write that races the delete
  waits for it and answers 404. A delete Postgres aborts to break a deadlock or a serialization conflict (40P01, 40001:
  a learning run or the conversation prune locking the same rows) runs once more (log `device_delete_retried code=…`),
  then answers 500 `internal`. Its log lines and metric points are not deleted (a per-device delete would walk every day partition):
  the lines stay readable in `GET /api/v1/logs` (`?device=<id>` still filters them, without `deviceName` or `board`)
  until their day partition is dropped (`logRetentionDays`), the points are out of the Prometheus exposition and the
  device routes at once and pruned within 24 h. An OTLP request let in before the commit and stored after it stores
  nothing (answered as usual). After the commit the device's live link closes with 1008 and any later one is refused;
  its token gets 401 at the next connect and at the OTLP ingest (its cadenced logs `link_unauthorized` once and keeps
  retrying). Log `device_deleted device_id=…` (failure `device_delete_failed`, 500 `internal`, nothing deleted). To use
  the hardware again, create a device and enrol it with the new code: it is a new device id. Household records keep
  the old id: the answer events of tasks it answered (`device`, and the speaker's words until their scrub), and the
  `evidence` message ids of global memories learned from its turns.
- Each completed enrolment, rename and delete, each link's open and close and each change of the reported
  `audio.output` level is an event of the live feed's `devices` topic (see [Live feed topics](#live-feed-topics)).
- `POST /api/v1/devices/{id}/capabilities/{cap}/configure {config, secrets?}` (admin) → `{generation}`: the config
  (validated as `PUT …/config` validates it) and each named secret (declared by the capability, 1–4096 bytes) are
  stored in one transaction under one new generation and pushed together, so the device never sees the config without
  its secrets; secrets the body does not name stay. The same body again still adds a generation (a retry). Refusals
  are codes alone, never a value: 404 `unknown_capability`/`unknown_secret`, 422 `config_invalid` (with its path),
  `secret_invalid` or `invalid` (a body that is not `{config, secrets}`), 413 `config_too_large`.

### Wi-Fi (`network.wifi`)

Midi, the VK Capsula Mini and the sim manage their Wi-Fi through `network.wifi` (wpa_supplicant's `wpa_cli`; the sim's
is a fake). The dashboard's device page has a Wi-Fi card: the link now, a Scan (the `scan` command) and a Connect that
sends `{config: {enabled: true, ssid}, secrets: {psk}}` through `configure`. The password reaches the supplicant alone:
the device keeps it in no file of its own and logs none of it (the supplicant's conf holds it, as it holds every saved
network's).

- A switch adds the network (nothing is ever removed) and selects it, and counts only when the new network holds the
  link with an address **and** cadenced's server link came up after that address. The address makes the link redial at
  once (whatever it held or waited for went over the old network).
- Otherwise it selects the previous network back and disables the new one (kept, so the supplicant never returns to it
  on its own): `no_ip` when no address came within 60 s of the switch (a wrong password, out of range), `no_server`
  when the link did not come back within 60 s of the address (a captive or isolated network, a VLAN without a route to
  the server, failing DNS). The rollback and the old network's address each make the link redial at once, so a failed
  switch costs the device its link for about two minutes, then the time to rejoin and dial. 60 s after the address
  covers the link's first dial (up to 15 s) and its retries 1, 2, 4 and 8 s apart: at least three whole dials.
- The state carries `target` (the network the config names, or the switch's), `code` (`switching`, `rolled_back`,
  `psk_missing`) and, after a rollback, `reason` (`no_ip`, `no_server`). A rollback holds for the generation it gave up
  under, also across restarts: a newer push naming the same network (Connect again) tries again.
- A switch in flight is kept in `<data>/state/wifi` (never the password): a restart in the middle of one resumes it
  with a fresh clock, so a device that came back on a network that cannot reach the server still rolls back.
  Turning the capability off (`enabled: false`) leaves a switch in flight to finish or roll back.
- Logs: `wifi_switch_started`, `wifi_associated`, `wifi_switched`, `wifi_rolled_back reason=…`,
  `wifi_switch_resumed`, `wifi_resettle`, with ids and timings alone, never a network's name or password.
- The sim fakes each failure by the password: `wrong-password` never associates (`no_ip`), `no-server-route`
  associates with an address but fails every dial of the link while it holds it (`no_server`); any other password of
  8–63 bytes joins at once.

## Media sessions (admin API)

- `GET /api/v1/devices/{id}/media` (`ha` too): the device's media session as it last reported it, `{sessionId,
  source, state, positionMs, positionAt?, rate, durationMs, volume?, metadata: {title, artist, album, artworkUrl,
  artworkHash}, actions}`, or `{"state":"idle"}` for none; each change is also the live feed's `media_state` (`media`
  topic) with the same body. A field past the device's bound is `""`. Metadata stays in memory (D11): never logged or
  stored. `artworkHash` is the hex SHA-256 of `artworkUrl` (`""` without one), so a client keys an image without
  fetching it.
- `GET /api/v1/devices/{id}/artwork?h=<artworkHash>` (`ha` too, M8-D4, ruling M8-R3): the image the session's
  `artworkUrl` names, fetched by the server, with its `Content-Type` (`image/jpeg`, `image/png`, `image/webp` or
  `image/gif`), `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`. `h`, optional, is the `artworkHash`
  the caller read: when the session's art is another by now, the answer is 409 `artwork_changed` and nothing is
  fetched, so a client never keeps one image under another's hash (a malformed `h` is 422 `invalid`); Home Assistant
  always passes it. A device or a cast sender names the url (an `ha` token too, through `media.url play`), so the
  fetch takes it as hostile: `https` on port 443 only (no port or `:443`; any other is refused with nothing
  resolved), without userinfo; its name is resolved once and refused whole when any address is loopback, private,
  link-local, CGNAT, multicast or unspecified (IPv4 and IPv6, IPv4 inside IPv6 too) or in another special-purpose
  block (this network, documentation, benchmarking, reserved, the 6to4 relay, NAT64, 6to4, Teredo, SRv6, site-local);
  only the checked addresses are dialed, never the name again (the dialer checks each once more at the socket), so
  DNS rebinding reaches nothing inside. At most 4 addresses of a name are tried, in order, each but the last with its
  share of the time left (at least 1.5 s), so one that hangs leaves time for the next. All of `64:ff9b::/96` is
  refused, so a host reachable only through DNS64/NAT64 shows no art. No proxy, no redirect, TLS
  verified against the system roots (`SSL_CERT_FILE`), 5 s for the whole fetch (the connect and the TLS handshake
  end on it too, even after the ask gave up), at most 5 MiB (a longer body is cut where it passes the cap, one that
  says it is longer is refused unread), no `Content-Encoding` but `identity` (nothing is decoded). Images are kept in
  memory only, by the url's SHA-256 (the `artworkHash`), the least recently used dropped beyond 32 images or 32 MiB;
  concurrent asks for one url fetch it once; a failure is kept 30 s (at most 64 of them), and asks for that url
  within it get the same 502 with nothing fetched; a restart forgets them all. 404 `no_artwork` without a session or
  its art (`not_found` for an unknown device), 502 `artwork_unavailable` when the fetch failed (log
  `artwork_unavailable device_id=… reason=url|address|dns|connect|tls|redirect|status|type|too_large|timeout|failed`,
  each ask, never the url, its host or an address). Art a device uploads by `sha256` is not served yet.

## Multiroom groups (admin API)

Groups and stereo pairs (`list-groups`, `create-group`, `calibrate-group`, `apply-group-calibration`, ...), their
calibration and auto-tune: `docs/multiroom.md`.

## Settings (admin API)

- `PUT /api/v1/settings/global` replaces the whole global set: a key it leaves out is unset, `timeZone` included
  (ruling M7bs-R5). To change one key, read the set, change it and send it whole (`host`, `jq` and `api` as in
  [Live turn](#live-turn-spec-9); `del(.key)` unsets one):

  ```sh
  api "https://$host/api/v1/settings/global" | $jq -c '. + {logRetentionDays: 14}' |
    api -X PUT --data-binary @- "https://$host/api/v1/settings/global"
  ```

- `PUT /api/v1/devices/{id}/settings` patches the device's own keys: a key sent replaces the device's, `null` removes
  it, a key left out stays.
- A key that names a provider instance (`stt`, `tts`, `llm`, `embed`, `weather`, `search`, `music`, `musicPlayer`,
  `xSearch`, `hermes`, `homeAssistant`, `telemetryExport`, and each name of `mcp`) may name one it did not name before only if that instance exists: 422
  `settings_invalid` with the key's path (`/body/llm`). What a key named before passes, so an instance deleted earlier
  blocks no other key. The mode keys follow the same rule (see [Agent modes](#agent-modes-admin-api)).
- `remoteTargets` (either scope, the device's wins) is whether other devices' turns may drive this one through a
  tool's `device` argument (play on it, control or switch what it plays, move its volume, set its alarms and timers,
  navigate its screen): `allow` (default), `room` (only devices of its own room: its `haArea` until devices carry a
  room) or `deny`. A device's own turns are never held back by it; reads (an alarm list, `screen_state`) are not
  either. Any write on another device also needs a session the user began with the wake word or a press.
- `persona` (either scope, the device's wins; `""` on a device turns a global one off there) is the owner's own
  instructions for the assistant, at most 2000 characters: who listens to that speaker and how to talk with them (a
  name, warmth, patience, jokes, longer answers). It goes last in every turn's system prompt, after Cadence's identity
  and rules, so it may change the tone, manner and length of replies but not the acting, tool and safety rules. The
  dashboard's settings edit it as Personality.
- `errorSound` (either scope, the device's wins) is what a device plays when a request fails: `prompt` (default; a
  spoken prompt in the primary subtag of its `language`, the error beep where the device lacks the file), `beep` or
  `off`. The server keeps each linked device's `session` capability config `{errorSound, language}` equal to them
  (checked at the link and every 60 s, log `session_config_set device_id generation`), so the device has them
  offline. The prompts and how to render them: [deploy/prompts/README.md](../prompts/README.md).
- Every settings write takes one advisory lock, the settings lock, from its reads to its write, and so do mode puts and
  deletes and provider deletes, so a delete never misses a setting written beside it.

## Agent modes (admin API)

A mode sets a turn's model, reasoning and budgets; `fast` and `smart` are seeded. All four routes are admin-only.

- `GET /api/v1/modes` → `[{name, model, reasoning, maxSteps, maxSeconds, maxTokens, firstTokenTimeoutMs, interim, tools,
  aliases}]` by
  name; `GET /api/v1/modes/{name}` → one, else 404 `not_found`.
- `PUT /api/v1/modes/{name}` creates or replaces a mode and answers it as `GET` does (200 both ways). The name is
  `^[a-z][a-z0-9_-]{0,31}$` (else 422 `invalid`, path `/path/name`). Required: `reasoning` (`none|low|medium|high`),
  `maxSteps` (1–50 model calls before the answer is forced), `maxSeconds` (5–600) and `interim` (`drop|speak`: whether
  text that comes with tool calls is spoken). Optional, as the table defaults them: `model`, a model id at the device's
  `llm` instance (not a provider instance), at most 200 bytes, `""` for the device's `model` setting; `maxTokens`
  (64–32768), the ceiling on the model's `max_tokens` every step of a turn in this mode allows, `null` for the built-in
  default (4096 with reasoning on, else 1024), which wins over the reasoning default only when set;
  `firstTokenTimeoutMs` (0 or 2000–120000), how long a step waits for the model's first token before the step fails
  as `timeout` (the turn's failure reports this limit), `null` or 0 for the built-in 10 s, never past the turn's
  `maxSeconds` or into the forced answer's reserve; `tools`, `null`
  for every built-in tool or up to 64 distinct names (`^[A-Za-z0-9_-]{1,64}$`, unknown names kept), which narrow the
  built-in tools within the device's `tools` setting (MCP tools follow the device's list alone); `aliases`
  `{en?, ru?}`, the words that name the mode by voice, up to 10 each, trimmed, then 1–40 characters with no control or
  format character, distinct in any case. `agent_mode` matches a word against every mode's name and every language's
  aliases, so an alias another mode has in any language, or another mode's name, in any case, is 409 `alias_taken` with
  its path (`/body/aliases/en/0`), and so is a new mode named as another mode's alias (path `/path/name`). Every other
  refusal is 422 `invalid` with the field's path. A voice session reads its mode when it opens, so a put lands from the
  next session; a text or event turn reads it at its start. `agent_mode`'s description, which lists the global allowed
  modes with their aliases, is loaded again after each put or delete and each global settings write, and a failed load
  is tried again after 1 s, doubling to 1 min, until one succeeds.
- `DELETE /api/v1/modes/{name}` → 204, else 404 `not_found`, or 409 `in_use` while a setting names the mode: the
  effective global `mode` or `allowedModes`, the defaults (`fast`, `["fast","smart"]`) included while no global
  override replaces them, or any device's own override of either.
- Settings: a `mode` or `allowedModes` that names a mode its scope did not name before needs that mode to exist (422
  `settings_invalid`, path `/body/mode` or `/body/allowedModes`). For the global scope that is the effective value, so
  dropping a global `allowedModes` brings the default list back and needs `smart` to exist. Mode puts and deletes and
  every settings write take turns under the settings lock (see [Settings](#settings-admin-api)), so a delete and a
  settings write naming its mode never both succeed.
- Logs: `mode_saved name=… created=…`, `mode_deleted name=…`; a store failure logs `mode_save_failed name=…`,
  `mode_delete_failed name=…` or `mode_read_failed` (500 `internal`). Never the model or an alias.

## Owner login

The dashboard logs in with the owner's password. Set it (or change it) from a mode-600 file on the owner's machine
whose first line is the password (at least 12 characters); it is read from stdin, never an argument, and never
printed. Setting it ends every session and forgets every known browser.

```sh
ssh <server> 'cd ~/cadence-v2 && docker compose exec -T cadence-v2 /cadence-server admin password set' < ~/.cadence/v2-owner-password
```

Typed instead (`ssh -t …` and `docker compose exec` without `-T`), the prompt does not echo it.

- `POST /api/v1/session {"password": …}` → 204 with two cookies, both HttpOnly, Secure and SameSite=Strict:
  `__Host-cadence_session` (Path=/, 30 days), the session, and `__Secure-cadence_browser` (Path=/api/v1/session, a
  year), the known browser. A wrong password is 401 `unauthorized`. `GET /api/v1/session` →
  `{authenticated, expiresAt?}` (also without credentials); `DELETE /api/v1/session` → 204, the session revoked and
  its cookie cleared (the known browser stays).
- A session ends 30 days after login, after 7 days unused, or at logout or a password change; requests with its cookie
  are refused at once, and a live feed it opened closes within 20 s. A known browser ends
  after a year, at the next login (which replaces it with a fresh one), after 5 failed logins with it over its life,
  or at a password change. Ended rows are pruned daily (log `admin_sessions_pruned count`).
- CSRF: a cookie request other than GET, HEAD or OPTIONS, and every login, sends `X-Cadence-CSRF: 1` and, when it
  carries `Origin` (else `Referer`), `CADENCE_PUBLIC_ORIGIN`; otherwise 403 `csrf`. The live feed's websocket
  upgrade on the cookie needs that `Origin`. Bearer requests are exempt.
- Rate limit: a login with a valid known-browser cookie counts only in that browser's own bucket (5 failures per 15
  minutes), so strangers' failures never lock the owner's browser out. Another host under the same domain can set a
  cookie of that name beside the owner's, so the login reads every value sent and takes the first live one (at most 4
  looked up). Any other login counts per client (an ipv4
  address, or an ipv6 /64: 5 failures per 15 minutes) and in all (20 per 15 minutes), then 429 `rate_limited` with
  `Retry-After`. A success clears its bucket; a restart forgets the counts. A new browser (or one whose cookie
  expired) logs in only while its client's and the global bucket have room.
- Logs carry no password, token or cookie:
  - `admin_login_failed reason=password|password_changed|rate_limited bucket=ip|browser|global ip_hash=…`, the
    refusal once per bucket and window;
  - `admin_login_refused bucket=… count=…` when that window ends;
  - `admin_login bucket=… ip_hash=…`;
  - `admin_browser_revoked reason=failures`;
  - `admin_login_error code=store|bad_hash|canceled` (a stored hash past m=256 MiB, t=10 or p=16 fails the login
    closed; `canceled`: the login's context ended before the store answered).
- `ip_hash` is the first 8 hex of an HMAC-SHA256 of the client's address (its /64 for ipv6) under a key the server
  makes at start and never writes anywhere: the same client has the same hash within one run, and nothing links it
  to the address or to another run.
- Check `CADENCE_TRUSTED_PROXY` after a deploy: log in from two networks (e.g. a phone on mobile data, then the
  laptop at home), then run `docker compose logs --since 10m cadence-v2 | grep '"msg":"admin_login"'`. The two
  lines must carry different `ip_hash` values, and two logins from one network the same one. One hash for every
  network means the server sees Traefik or a Cloudflare edge, not the client. Compare hashes only within one server
  run: a restart changes them all.

## Provider modules and tests

Weather has two keyless modules, `open-meteo` and `met-norway` (MET Norway Locationforecast 2.0). Neither takes
configuration, an API key or a secret. MET Norway sends Cadence's identifying User-Agent, truncates coordinates to four
decimals, caches until the response's `Expires`, then revalidates with `If-Modified-Since`; place names, including
Russian ones, are geocoded through Open-Meteo. Both modules answer in metric units with the same WMO condition codes
and summaries, current conditions and daily forecasts in the household's `timeZone`.

In the dashboard, open **Providers**, choose **Add a provider**, enter a name such as `weather`, select module
`met-norway` (or `open-meteo`) under Weather, and choose **Add the provider**: no configuration or secret fields appear.
Select that instance as the household or device's `weather` setting, and set `weatherLocation` (for example `Москва`)
and `timeZone` (for example `Europe/Moscow`). The weather tool tries the selected instance first; after a network,
timeout, HTTP or decode failure it tries every other configured weather instance in creation order (name breaks ties)
until one answers or the tool's deadline passes. An unknown place is not retried against the others.

`GET /api/v1/providers/modules` lists every module an instance can run (by kind, then id; the dev fakes only with
`CADENCE_DEV_FAKES`): its config JSON Schema as the module wrote it (compact, its keys and properties in the module's
order, so a form keeps it) and its secrets by name, never a value. Its path is
reserved: no instance may be named `modules` in any case (422 `config_invalid`, path `/name`). `POST
/api/v1/providers/{name}/test` builds the instance afresh (the one the runtime holds is left alone) and runs its
module's cheapest call that is not billed, within 10 s, at most once per instance per 5 s (429 with `Retry-After`):
`{ok, code?, ms}` with a code (`auth`, `unreachable`, `timeout`, `bad_response`, `unsupported`, `rate_limited`,
`build_failed`), never upstream text, and the log line `provider_test instance code ms`. Per module: `deepgram-flux`
and `deepgram-aura` read the key's own details (`GET /v1/auth/token`, Deepgram's documented key check, under no
project; no audio, no synthesis; a key whose scopes refuse it would answer `auth`, and that none does is unverified:
an owner check at the first live test); `openrouter` and `openrouter-embed` read the key's own record (`GET /key`;
the model list answers without a key, so it cannot tell a wrong one); `openai-compatible` lists the server's models;
`open-meteo` geocodes "Bishkek"; `met-norway` reads one compact Moscow forecast (no geocoding); `homeassistant` opens a connection of its own,
authenticates and reads `get_config` (a wrong token is one failed login, which Home Assistant counts toward its ban);
`yandex-uniproxy-stt` and `yandex-uniproxy-tts` open a socket with the instance's credentials and wait up to 5 s for SynchronizeState's answer (a refused upgrade or `InvalidAuth` is `auth`; a socket that opens and stays quiet passes, as UniProxy takes requests before it answers; nothing is recognized or synthesized); `hermes` reads `v1/capabilities` behind its key; `mcp-http` connects a session of its own and lists the tools (no call);
`otlp-http` posts an empty logs export (no data, nothing stored); `youtube-music` runs one search; `spotify` fetches
an app token (no search); `yt-dlp` asks the program its version (YouTube is not reached); `exa` answers `unsupported`, since its only calls are
billed searches; the fakes pass. None of these costs money; a test only spends the upstream's request quota.

`DELETE /api/v1/providers/{name}` → 204, else 404 `not_found`, or 409 `in_use` while a setting names the instance: the
global set or any device's own, under any key that names an instance (see [Settings](#settings-admin-api)). It takes
the settings lock, so a delete and a settings write naming the instance never both succeed. A store failure answers 500
`internal` and logs `provider_delete_failed`, `provider_test_failed` or `provider_catalog_failed` with the instance's
name (the module's for the catalog) and nothing else.

## Dashboard

`cadence-dashboard` (M7b) is the dashboard's Node SSR server, built from `deploy/dashboard/Dockerfile` on the Makefile's
pinned `NODE_IMAGE`: the build, its production dependencies and `dashboard/serve/` only, as `node` (uid 1000) on a
read-only root, `no-new-privileges`, all capabilities dropped, port 3000. It holds no secret: its only variable is
`CADENCE_API_INTERNAL=http://cadence-v2:8080` (`compose.yml`), where its SSR calls reach the server on the project network
with the reader's own cookie; the browser calls the public host.

- One origin: both routers take the same `Host(...)`. `cadence-v2`'s rule narrows it to the server's own paths,
  `PathPrefix(/api/)` (the admin API and its live feed), `PathPrefix(/v2/)` (device link, enroll, OTLP) and
  `Path(/healthz)`, at priority 100; `cadence-dashboard` takes every other path at priority 1. So
  `CADENCE_PUBLIC_ORIGIN` (`https://${CADENCE_V2_PUBLIC_HOST}`) is the dashboard's URL, which the `__Host-` session cookie
  and the CSRF `Origin` rule need. The dev stack's edge (`deploy/dev/edge.yml`) routes the same paths:
  `deploy/dev/dashboard-deploy.test.sh` (`make tools-test`) fails when they differ, or when a top-level route the server
  mounts is outside them (it would reach the dashboard).
- No other router on `web-secure` may match this host: one at Traefik's default priority (its rule's length) outranks
  the dashboard's 1 and takes every path the server does not.
- The live feed (`/api/v1/live`) and the device link (`/v2/device`) are long-lived websockets. Traefik v3's entrypoint
  `respondingTimeouts.readTimeout` (60 s by default) is a deadline on the whole request, which a websocket's pings do
  not extend, so it would cut both every minute. After the first deploy, check that the dashboard header's feed stays
  `Live` past a minute, and that `docker compose logs cadence-v2` shows a device's `link_open` once, not every minute.
  If not, set the entrypoint's `readTimeout` to 0 (as the dev edge does), or, from Traefik v3.8,
  `respondingTimeouts.readTimeout` 0 on the `cadence-v2` router alone.
- First login: set the owner password ([Owner login](#owner-login)), open `https://<host>/` (it sends you to
  `/login?redirect=%2F`) and sign in. Setting the password again signs every open dashboard out.
- Logs: one line per request with its method, path, status and time (`{"msg":"request",…}`), never its query, headers,
  cookies or body, plus `dashboard_listening` and `dashboard_stopping`. What a library logs through `console` becomes a
  line of its level and an error's class only (rule 8). `docker compose logs --since 10m cadence-dashboard`.
- Health: the image's `HEALTHCHECK` asks the container's own `/healthz` (`docker compose ps` shows `healthy`); the public
  `/healthz` is the server's.
- Answers carry `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`; pages
  `Cache-Control: private, no-store`, the hashed build files a year `immutable`. Only GET and HEAD are served (405 else).

## Live turn (spec §9)

From the owner's own shell, never through chat or the repo. Provider keys are read from files outside the repo
(here `~/.cadence/deepgram.key`, `~/.cadence/openrouter.key`) through stdin, so they are in no argument or history.

```sh
host=<host>
jq='docker run -i --rm ghcr.io/jqlang/jq:1.8.2@sha256:b9c68867e5766576263a222e91db3de422d802069c7af70440e667a95344e486'
api() { curl -fsS -H "Authorization: Bearer $(cat ~/.cadence/v2-admin-token)" -H 'Content-Type: application/json' "$@"; }
for p in deepgram-flux:deepgram deepgram-aura:deepgram openrouter:openrouter; do
  $jq -Rs --arg m "${p%%:*}" '{name: $m, module: $m, config: {}, secrets: {apiKey: rtrimstr("\n")}}' < ~/.cadence/"${p#*:}".key |
    api --data-binary @- "https://$host/api/v1/providers" >/dev/null
done
# a put replaces the whole global set (see Settings), so the keys are merged into it
api "https://$host/api/v1/settings/global" |
  $jq -c '. + {stt: "deepgram-flux", tts: "deepgram-aura", voice: "aura-2-thalia-en", llm: "openrouter",
    model: "deepseek/deepseek-v4.1-flash-1017", language: "en"}' |
  api -X PUT --data-binary @- "https://$host/api/v1/settings/global"
api --data-binary '{"name":"sim-live","model":"sim"}' "https://$host/api/v1/devices" | $jq -r .code
make live-turn HOST="$host" CODE=<code>
```

`make live-turn` builds the sim image, enrolls it with the code, says `what-time` once and prints
`live-turn: ok` when a reply stream of at least 10 packets arrived and the session ended without an error code.
Names are unique, so a second run deletes the earlier `sim-live` first (`DELETE /api/v1/devices/{id}`, its id from
`GET /api/v1/devices`) or names its device otherwise; a taken name answers 409 `name_taken`.
Then check the server's `turn` line: `docker compose logs --since 10m cadence-v2 | grep '"msg":"turn"'`.

## Follow-ups and command words

- `followUp` `{enabled, windowMs, limit}` (default `{true, 5000, 3}`; `windowMs` 2000–15000, `limit` 1–10, all three
  keys or `null`): after a spoken reply to a wake, press or follow-up session that ended without an error, the close
  offers a follow-up with that window, at most `limit` in a row after a wake or press. The device opens it only when
  its own facts hold (not muted, the reply heard whole, no ring). A follow-up turn keeps no woken rights.
- A final transcript of stop words alone (`ru` стоп/хватит/пауза, `en` stop) gets no answer: the device's media is
  paused first when it plays, and the session closes with `stop_requested` (log `stop_requested paused=…`).
- In a follow-up, a final transcript that is only the previous reply's words (its echo) closes the session with
  `follow_up_echo`. The reply's words are kept in memory only until the next session ends or the link closes.
- Everything sent to TTS (replies, spoken tool errors, announcements) passes the command-word guard, so no reply says
  стоп, хватит, пауза, тише or громче, also not in Latin look-alikes or, in `ru`, sound-alikes. The history keeps
  the model's own words.

## Devices and screens in turns

Every turn but an event turn gets a devices note: this device, then the household's other online devices (owner-set
name, room, speaker or screen, what matters of what it can do, a one-line now playing), and a screen note per screen
of its room whose context is fresh (what it shows, built from its refs through the catalog's cache, at most 1 KB;
titles framed as data). `device_volume`, `media_control`, `music_play`, `video_play`, `alarm`, `timer` and the screen
tools take a `device` argument (a name, a room or `here`): an exact name, then the room's device with the capability,
then a kind (`телевизор`, `tv`), then a name with an inflected ending; several answer `ambiguous_device` with their
names, and a screen-only tool from a speaker defaults to its room's screen (`no_screen` when there is none). A write on
another device needs a turn the user spoke (a follow-up too, never an event turn) and the target's `remoteTargets` (see
Settings), and the tool message stored with the turn names the device it acted on (`target`). Only rule 7's volume
raise and alarm set, Home Assistant's sensitive actions under `haSensitive`, approvals (Hermes tasks, followed work),
the t3 tools and MCP write tools not marked for any turn need a session the user began with the wake word or a press. The screen tools are
`screen_state`, `screen_navigate`, `catalog_item`, `catalog_play`, `media_switch` and `media_item_action`; they act on refs the catalog
issued to the household alone, never the volume, and event turns get none. Web search results carry the number of
their card on a screen (`n`); a reply cites them as `[n]`, and the voice path strips the marks before speech.

## Alarms and timers

Set `timeZone` (an IANA zone, e.g. `"Asia/Bishkek"`) in the global settings (the whole set: see
[Settings](#settings-admin-api)) or on the device before alarms can work: the device rings alarms on that calendar,
and the built-in default `UTC` is never pushed to it. Until a scope sets one, the `alarm` tool answers `unavailable`
(timers work regardless). The server sets the device's `alarms` config, the zone and the `alarms` setting's keys,
when the device links and checks it every 60 s, so a changed zone or setting reaches a linked device within a minute
(log `alarms_zone_set device_id generation`); removing the zone leaves the device on the zone it has. Setting an
alarm needs a session the user began with the wake word or a press; timers, cancels and lists work in a follow-up too.

The `alarms` setting (global or per device, the device's whole object over the global one; every key optional, the
device's default when left out) tunes how rings sound:

| Key | Range | Default | What |
| --- | --- | --- | --- |
| `rampSeconds` | 0–1800 | 30 | how long an alarm climbs to its level, in 5-point steps (0: at once) |
| `rampFrom` | `current`, `quiet` | `current` | where the ramp starts: the level in force, or level 5 (lower when the level in force or the alarm's is), climbing to the alarm's level, never above |
| `alarmLimitSeconds` | 60–3600 | 600 | how long an alarm rings when nothing stops it |
| `timerLimitSeconds` | 10–600 | 60 | the same for a timer |
| `reminderLimitSeconds` | 10–300 | 60 | how long a reminder rings when nothing stops it |
| `snoozeMinutes` | 1–60 | 9 | a snooze's length when none is asked for |
| `autoSnooze` | 0–5 | 0 | how many times an alarm nobody stopped rings again `snoozeMinutes` later |

An alarm may carry its own `rampSeconds`, `rampFrom` and `limitSeconds`; the agent sets the ramp only when asked
(«плавно, минут за пять»).

A snooze puts a ring off without touching its schedule: a repeating alarm keeps its days, a one-time alarm that rang
is gone but its snooze rings it once more, at its own level; cancelling the alarm or timer by id takes its snooze
too. It comes from the agent (`alarm` or `timer` with `action: "snooze"` and optional `minutes`), the device's
`snooze {kind?, id?, minutes?}` command, or the action button held down a second while it stops a ring. The device's
`stop_ring {kind?, id?}` command stops the ring that sounds. Neither needs a woken session (owner 2026-11-16: they
only end or put off a ring set in one), so a follow-up, a screen, the dashboard or Home Assistant may send them; with
no ring sounding, a snooze takes the one stopped in the last 10 minutes. When a wake stops a ring, its session start
carries `after_ring {kind, id, stopped_ms_ago}` and the turn gets a note of it, so «ещё 5 минут» is one snooze, never
a cancel or a new alarm. The device's `alarms` state adds `snoozed` (`{kind, id, until}`), `lastRing` (`{kind, id, at,
stoppedBy}`, `at` when it stopped) and each alarm's and timer's `next` (RFC 3339; an alarm's is absent until its zone
is known).

Timers, and reminders without a level, may be set and cancelled without a session at all: a command without
`sessionId` comes from the server's authenticated API (Home Assistant, a screen's press; owner 2026-11-16). Alarms,
and a reminder with a level, keep needing a woken session; a command that names a session is checked as before.

### Reminders

A reminder rings a soft two-note chime and speaks its text at a time of day (once, on `days`, or on a `date`) or in
some minutes. The agent's `reminder` tool (`set|cancel|list|snooze`; `text`, `time`, `days`, `date`, `inMinutes`,
`level`, `minutes`, `all`, `device`) speaks the text in the device's voice into a ring sound when it is set and then
sends the device `set_reminder {sessionId?, id, time + days|date | inSeconds, label, soundId?, level?}`: the label is
the text's first 80 characters, for screens. A reminder without a level rings at the level in force, never touching
the volume, so it may be set in a follow-up; with a `level` it ramps like an alarm and needs a woken session (rule 7).
If the speech cannot be made (no TTS, the provider failed) the reminder is set anyway and the tool says so: it then
rings its chime and the bundled prompt «Напоминание». The text is the user's setting, kept with the ring sound until
the reminder goes (not a transcript, D8 does not apply), shown on the dashboard and screens, never logged. A device
holds at most 20 reminders; `cancel_reminder {id | all}`, `snooze` and `stop_ring` take `kind: "reminder"`; its state
lists `reminders` (`{id, time?, days?, date?, at?, label, soundId?, level?, next?}`).

Home Assistant (or any API caller) sends `alarms.set_reminder` in the API's own shape: `{text, time, days?|date?}` or
`{text, inMinutes}`, optionally `id` ([a-z0-9]{1,16}; one is made when absent), never `level`. The server turns it
into the device's shape on its way (speaks `text`, labels it, `inMinutes` to seconds) and sends it without a session.
A command in the device's own shape (`label`, `inSeconds`) passes as it is, with no speech.

### Ring sounds: an alarm's music and a reminder's speech

Both stay on the server and are streamed at ring time; nothing is cached on the device. They are Ogg Opus, 48 kHz
mono, kept in the `ring_sounds` table per device and served by `GET /v2/ring-sound/{id}` with the device token in
`Authorization` (never in the url), whole or by byte range, to the device it was made for alone (another device's id
is 404). A ring that names a sound plays it through audiod's url input: an alarm's music looped (its ramp is the fade
in), a reminder's chime, speech and a 2 s pause looped up to `reminderLimitSeconds`. When the url gives no audio
within 3 s or fails while it plays (the server or Wi-Fi down), the ring falls back for the rest of it, logged
`ring_fallback code=` on both sides: an alarm to its synthesized tone, a reminder to its chime, the bundled
`reminder` prompt and a 3 s pause. A sound no alarm or reminder names in its device's reported state is deleted 7
days later (checked hourly, `ring_sounds_deleted count=`).

`alarm set` takes `sound`, a `music_search` result id (`r1`) or a query («разбуди под Queen»): the server resolves the
song through the music player as `music_play` would, fetches it itself (the signed url never leaves the server or
reaches a log or the table) and transcodes it once through ffmpeg (the first 10 minutes, `loudnorm`, a 3 s fade-in).
The server finds ffmpeg at `CADENCE_FFMPEG`, else on its `PATH`; without one music alarms are set with their tone and
the tool says `music: false`. The image ships one at `/usr/local/bin/ffmpeg` and sets `CADENCE_FFMPEG` to it: ffmpeg
9.0.2 with libopus 1.5.2, a static LGPL 2.1+ build (no `--enable-gpl`, no nonfree) cross-compiled with zig against
musl (`deploy/builder/build-server-ffmpeg.sh`, both tarballs checked by sha256). It holds only what the ring sounds
need: the `pipe` protocol (the song on stdin, the sound on stdout; no file or network protocol), the matroska/webm,
mp4/m4a, ogg, mp3, flac, aac and wav demuxers, the aac, alac, flac, mp3, opus, vorbis and pcm decoders, the
`loudnorm`, `afade`, `aresample`, `aformat`, `atrim` and `anull` filters, libopus and the ogg muxer. An mp4 whose
index sits at its end cannot be read from a pipe and fails like any other bad song. Its license texts, configure line
and source pointer are in the image's `/licenses/ffmpeg`. The build stage is in `server/Dockerfile`, so the plain
`docker build` below compiles it (a few minutes, on the build machine's own cpu); `make server-image` takes it from the
prebuilt cache instead (`ffmpeg-server-amd64`, `deploy/builder/prebuilt.sh`). The music's title is kept with the sound and
shown by the alarm's list and card.
Every linked screen gets the household's alarms (`alarms_summary`: each device's alarms, timers and reminders by next
ring, the screen first, then its room's) at its link and after any change, and the rings it shows: its own and those
of its room's online devices. A TV wakes its display for its own ring alone; another device's ring shows only on an
awake screen (owner 2026-11-16). The overlay's «Стоп» and «Ещё N мин» and the alarms page's cancel of a timer or a
reminder go through the screen's `alarms.act {deviceId, kind, id, action, minutes?}` (`stop` → `stop_ring`, `snooze`
→ `snooze`, `cancel` → `cancel_timer`/`cancel_reminder`; log `alarms_act device_id target_id action kind code`); a
screen never sets or cancels an alarm (rule 7).

## Music

For dashboard setup and step-by-step instructions for obtaining each provider's token, cookies, client id and matching
User-Agent, see [Streaming music providers](../../docs/music-providers.md).

"Включи <song>" runs two agent tools: `music_search` asks every provider instance the `music` setting lists (a
list, global or per device; one name alone is read as a list of it) at once and interleaves their answers under short
ids (`r1`, `r2`, …) with title, artist, album, length and provider (YouTube Music, Spotify), nothing else (`source`
narrows it to one); then `music_play` plays one by its id: `now` (a new queue of that song, which YouTube Music's radio
of it tops up at its end), `next` or `later` (into the queue).

`music_search`'s `kind` is `song` (the default), `album`, `playlist`, `artist` or `station`; a list's result has its
`kind`, title, `artist` (an album's) or `owner` (a playlist's), `trackCount` where the provider tells one (Spotify does,
YouTube Music's search does not) and provider. `station` is Yandex Music's personal Моя волна (My Wave, also called My
Vibe), found without an upstream search and played with the instance's account credential. `music_play` of an album or
a playlist reads its songs from the instance that found it, at most the queue's 200, and plays them as a new queue
without radio (`now`) or adds them (`next`, `later`); of an artist it plays the artist's radio, which tops itself up;
of the station it starts a new continuing queue of its personal batches (never `next` or `later`). `radio: true` starts
a radio now ("включи радио по Linkin Park", "радио этой песни"): of a song's id (the song, then 25 songs of its
YouTube Music radio), of an artist's or station's id, or of `id: "current"`, the queue's own song, which plays on (or
again, once it no longer plays) with the queue after it replaced by its radio; `current` answers `nothing_playing`
without a queue, and a Spotify seed is matched on YouTube Music first. YouTube Music finds lists with its albums,
playlists and artists search tabs (the params ytmusicapi documents), reads an album or a playlist through InnerTube
`browse` (100 rows a page, three pages at most), and an artist's songs are its radio ("Start mix", `RDEM…`). Spotify
reads an album's and a playlist's tracks with the app's client credentials, and an artist's songs are its own tracks a
search of its name ranks first (the endpoints Spotify keeps from new apps, such as recommendations, are not used).
Spotify keeps its own editorial playlists, and since 2026 may keep other playlists, from an app without a user: a 403
or 404 is `music_refused` (the model tries another result), never Spotify's text; a playlist whose `/tracks` path
answers 404 is read again from `/items`. The `musicPlayer` instance (yt-dlp) turns a song into an audio stream (AAC in
MP4) and the server sends it to the device as a `media.url` session with its metadata and cover.
Both tools are offered wherever `music` is set; a song that needs YouTube Music (from `youtube-music` or
`spotify`, or one a streaming provider below cannot play whole) answers `not_set_up` without a `musicPlayer`.

The server keeps each device's queue in memory (`server/internal/music`): next and previous, shuffle, repeat (`off`,
`all`: the queue, `one`: the song), radio at its end, and the next song once one plays to its end (its last position
within 8 s of its length; a stop or a replaced session does not advance). A device's URL session cannot skip, so the
server answers `media.player` `next` and `previous` itself while the device plays a song of the queue, whoever sends
them (the agent, the dashboard, Home Assistant), and adds them to the session's `actions` with its `provider` and
`webUrl` (the song's page); other sessions (Soloist, AirPlay) skip on the device as before. `media_control` is the
agent's playback tool (pause, resume, stop, seek, next, previous, shuffle, repeat; never the volume), and every
non-event turn gets a note of what plays (title, artist, provider, state, position, the next three songs and the modes):
its text is third parties', quoted, cut to 80 characters and framed as data, and it is sent to the model alone, never
stored with the turn. Admin routes: `GET /api/v1/devices/{id}/music` (the queue: current, upcoming, history, modes),
`PUT …/music/modes` (`shuffle`, `repeat`) and `POST …/music/jump` (`offset`: play a song further down the queue).
Each provider's search has its own 4 s budget within the tool's 8 s, so one slow or failed provider does not discard
another's matching results; only a search with no provider answer fails.

`music_feedback` likes, dislikes or clears the current song or a bounded search result (`current`/`rN`, never an id or
url the model names). A like adds the household favorite; a dislike or clear removes it. Yandex synchronizes these
through the account's `/users/{uid}/likes/tracks/...` and `/dislikes/tracks/...`; other providers keep the Cadence-local
state and answer `providerSync: "not_supported"`. A provider refusal changes nothing locally. `music_favorites` reads
the durable favorites as a Cadence collection, which `music_play` plays or queues through each song's original
instance, with the usual YouTube fallback. A Yandex radio also reports `radioStarted`, `trackStarted`,
`trackFinished` and `skip` with the station's `batch-id`, track id and actual played seconds, serialized per device and
best-effort (a report never delays or fails playback). This includes the initial `track:<id>` station seed and a skip
only once a replacement play succeeds.

Play history stores the canonical `providerSongId` and public `webUrl`, plus the current `feedback` and `favorite`
joined from durable song state; pruning it never removes a favorite. `GET /api/v1/music/favorites` lists the durable
collection (optional `query`, `limit` up to 200), and `PUT /api/v1/music/songs/{provider}/{songId}/feedback`
(`{feedback: "none|like|dislike"}`) rates it from its stored or latest-history metadata. Likes/dislikes/favorites stay
until cleared (owner 2026-11-18). Neither history nor favorites stores a stream or signed url; VK access keys and
SoundCloud query secrets are stripped, and a YouTube page keeps only its canonical `?v=<public id>`.

- Modules: `youtube-music` (kind `music`, no key: YouTube Music's own search of songs, albums, playlists and
  artists), `spotify` (kind `music`,
  secrets `clientId` and `clientSecret` of an app from the Spotify for Developers dashboard, config `{market}` an
  ISO country code; it searches and reads lists only: a Spotify song is played from YouTube Music, matched by
  artist, title and length within 4 s or 4 %, else `not_on_youtube`), and `yt-dlp` (kind `player`, optional secret
  `cookies`).
- Providers that stream their own songs (kind `music`; each also searches albums, playlists and artists and has its own
  radio, which tops a radio queue up instead of YouTube Music's). A song plays from the instance that found it (the
  queue remembers it); a preview, a song the account may not play (`sign_in_required`) or one gone (`unplayable`)
  falls back to YouTube Music's match of it when a `musicPlayer` is set, else the play answers the provider's reason.
  Their streams go through the relay like YouTube's (each provider's media hosts are on its list; an HLS playlist is
  rewritten, its AES-128 keys included). TVs play these songs from YouTube Music for now (the screen player takes a
  YouTube video alone).
  - `soundcloud`: no key. It reads soundcloud.com's own public client id from the site's scripts (again every 12 h,
    or at once when api-v2 refuses it) and plays the whole song's HLS AAC (or progressive mp3). Optional secrets:
    `clientId` (a fixed 32-character id, when the site's cannot be read) and `oauthToken` (a signed-in account's
    `oauth_token` cookie: a Go+ account plays the songs SoundCloud otherwise cuts to a 30 s preview).
  - `yandex-music`: no key needed to search (api.music.yandex.net); a whole song needs an account with Yandex Plus,
    as optional secret `token` (the music app's OAuth token: `https://oauth.yandex.ru/authorize?response_type=token&client_id=23cabbbdc6cd418abb4b39c32c41195d`,
    signed in, then `access_token` from the address it lands on) or `cookies` (a browser's `Session_id` and
    `sessionid2` of yandex.ru, as a cookie header, a `cookies.txt` or its base64), which the server exchanges for a
    token (cached 12 h, again on a 401). Config `{quality}`: `hq` (the default), `nq` or `lossless` (FLAC). Streams
    come from `get-file-info` (signed as the desktop app does, `raw` transport only: an encrypted-only answer is
    `unplayable`), else the older `download-info`. A `station` search returns Моя волна (`user:onyourwave`), whose
    batches need the account credential; its tracks carry the station internally, so the queue's refills stay on it
    even after a song's YouTube fallback, and a failed refill never turns it into YouTube radio.
  - `zvuk`: no key. A guest token (`/api/tiny/profile`) searches and plays whole songs at 128 kbps mp3 (`mid`);
    optional secret `token` (a signed-in session's `auth` cookie, or `result.token` of
    `https://zvuk.com/api/tiny/profile` opened while signed in) plays `high` (320 kbps) and `flac` with a
    subscription. Config `{quality}`: `high` (the default), `mid` or `flac`; it steps down to what the token gets.
    Zvuk answers from Russia only (and its anti-bot refuses some calls with 418): the server needs a Russian exit
    (`HTTPS_PROXY` reaches every provider) for it.
  - `vk-music`: secret `token` (required: VK's audio API answers nothing without one), an access token of VK's own
    Android client (client id 2274003, scope `audio,offline`; Kate Mobile's no longer reach audio), and config
    `{userAgent}`, the User-Agent it was issued with (default `VKAndroidApp/5.52-4543 …`, which VK checks on every
    audio call). Get one once on a desktop (vkpymusic's `TokenReceiver`, or `oauth.vk.com/token` with
    `grant_type=password` from your own shell: 2FA codes work, a captcha needs its browser page); the server never
    takes a password. Its songs are HLS with AES-128 segments, read again before each play (their urls live a short
    while); calls are paced at about 3 a second.
- `cookies` is a Netscape `cookies.txt` of a signed-in YouTube session (a browser extension exports one; keep only
  `.youtube.com` lines), for songs YouTube plays only to an account (age-gated, members-only) and for its "sign in to
  confirm you're not a bot" check. Send it as the file's text through the API, or as base64 (`base64 < cookies.txt`):
  the dashboard's one-line secret field drops line breaks, so its "Load from file…" button reads the file and fills
  the field in base64. It is sealed like every secret, and written for each yt-dlp run alone to a mode-600 file in
  the container's `/tmp` tmpfs, removed after it. A bad file fails the instance's build (`build_failed`). Secrets are
  set at create: to change the cookies, delete the instance (after clearing `musicPlayer`) and create it again.
- YouTube rotates a signed-in session's cookies (`__Secure-*PSIDTS`, `SIDCC`), and a jar left behind stops signing in
  (yt-dlp then fails, or its streams answer 403). yt-dlp writes the jar back to its file at exit: the server takes it
  as the next run's and, once a cookie's value changed, seals it back into the instance's `cookies` secret, so a
  restart keeps it too (`ytdlp_cookies_renewed`, then `ytdlp_cookies_saved` or `ytdlp_cookies_save_failed`). Runs
  with cookies take turns, one at a time, so none starts from a jar another is renewing. Export the cookies from a
  private window and close it without signing out: a browser that keeps the session rotates it away from the copy.
- The model never sees a stream URL and `music_play` takes no URL: only an id of a song, album, playlist or artist
  this server's search found for the same device in the last 30 min (at most 40 kept), or `current`, the queue's own
  song, so an instruction in a title can at most pick another result. A list's songs come from the provider that
  found it, by the provider's own id, which the model never sees. `music_search`'s output is untrusted (a turn that reads it keeps Read tools), and `music_play` is
  exempt from that taint by this bound; it never moves the volume, and event turns do not get it.
- yt-dlp runs as a child of the server (at most 2 at once, 25 s each) with no config file, IPv4 only, and its errors
  become codes: `unplayable`, `sign_in_required`, `player_failed`. Logs carry `ytdlp_resolved ms cookies` and
  `ytdlp_failed code exit ms cookies`; never a URL, a query, a title or yt-dlp's output.
- A stream URL is bound to the address that asked for it, and the server may reach the internet from another one than
  the device (a VPN exit). So with `CADENCE_PUBLIC_ORIGIN` set the device gets `<origin>/v2/stream/<token>` instead: the
  server relays the stream (ranges passed through) from YouTube's media hosts (`*.googlevideo.com`, https, IPv4, as
  yt-dlp asked), and the signed URL never leaves the server. The token is 32 random bytes, valid until the stream's
  own expiry (6 h at most), 64 kept; an unknown one is 404, an upstream refusal 502. Logs: `stream_relay device_id
  status ranged ms`, `stream_relay_failed device_id code [status]`. Without the origin, the device fetches the URL
  itself and must share the server's exit.
- Updating yt-dlp (YouTube changes break it every few weeks): bump `server/ytdlp/requirements.in`, run `uv pip compile
  requirements.in --universal --python-version 3.14 --generate-hashes --no-header -o requirements.txt` in
  `server/ytdlp/`, and rebuild the image. The image holds python-build-standalone (libedit, no GNU readline), the
  hash-locked wheels (yt-dlp and its default extras but `mutagen`, which is GPL-2.0 and only tags downloaded files)
  and deno for YouTube's JavaScript challenges.

## X search

The `x_search` tool searches posts on X as the account whose cookies an `x-web` instance holds (kind `xsearch`), through
X's own web app: it reads the signed-in home page and two of its scripts for what a search needs (the signing parts of
the `x-client-transaction-id` header, the `SearchTimeline` query id and its features, the web app's bearer), keeps
them 30 minutes, and reads them again once when X refuses a search with a 400, 403 or 404 (it deployed). Use a
throwaway account: X may suspend one its web api serves to a program, and nothing here is X's public api.

- Create an `x-web` instance with the secret `cookies`, a Netscape `cookies.txt` of a signed-in x.com session, as
  yt-dlp takes it (as text, or base64 for a one-line field). It needs the `auth_token` and `ct0` cookies of `.x.com`
  (a create without them is refused); cookies of other sites in the file are never sent. Its test reads the home
  page as the account and spends no search: `auth` means the cookies no longer sign in. To change the cookies, clear
  `xSearch`, delete the instance and create it again. A cookie X renews on its answers (`ct0` rotates) is sent from
  the next request on and sealed back into the secret (`x_cookies_renewed`, `x_cookies_saved`); X clearing
  `auth_token` is ignored.
- Set `xSearch` (global or per device) to its name: the tool is offered while it names one, a Read tool whose output
  is untrusted, as `web_search`'s. It takes a keyword query with X's operators (`from:`, `since:`, `min_faves:`,
  `-filter:replies`, `lang:`), `latest` (newest first) and `count` (1 to 20, default 8), and answers each post's url,
  author, time, counts, text (long posts whole, at most 800 characters), quoted post and community note.
- Logs carry `x_search_failed provider=x-web code=<code>` alone (`signed_out`, `rate_limited`, `status_<n>`,
  `page_status_<n>`, `no_signer`, `no_operation`, `decode`, `timeout`, `send`); never a query, a post or a cookie.

## Memory embeddings

One embed model per household: set `embed` in the global settings (`PUT /api/v1/settings/global`, read-modify-write:
see [Settings](#settings-admin-api)), not per device. The background re-embed job (every 60 s) gives every memory a
vector of the global instance's model only. A device that sets its own `embed` to an instance of another model
matches none of them: its recall is pinned-only and the
live feed shows `memory_recall` with `skipped_reason: model_mismatch` (logged once an hour as `memory_embed_skipped`).
A device that sets `embed` while the global one is unset gets no re-embedding at all (logged once an hour as
`reason=global_embed_unset`). An empty `embed` on a device switches input matching off; pinned memories still apply.

## Conversation history (admin API)

`GET /api/v1/devices/{id}/turns?limit=<1–100, default 20>&before=<cursor>` lists a device's turns newest first as
`{turns, next?}`: send `next` back as `before` for the page after it (a cursor the server did not make is 422
`invalid_cursor`). Each turn is `{turnId, at, trigger?, mode?, user?, reply?, tools: [{name, code?}], tokens?}`: what
started it (`wake`, `button`, `follow_up`, `event` or `dashboard`, the names the live feed's `turn_start` carries too, or `notice`:
an update the subscriptions hub spoke unasked, one assistant message and no user message), the mode its
last step ran in, its first user message and last reply cut to 2000 characters, its tool calls by name with the error
code a result of at most 4 KiB carries, and its tokens summed over its model calls. The list reads only that of each
message, never a message whole, so a turn with large tool results lists cheaply. `GET
/api/v1/devices/{id}/turns/{turnId}` answers one turn with its messages as stored, tool arguments and results
included, and each tool message with the `code` its result carries, read as the list reads it; both 404 `not_found`
for an unknown device or turn. A turn's row (`conversation_turns`) is written in the statement that writes its
messages and pruned with them after 14 days; turns stored before it came have no trigger, mode or tokens, and a code
rollback that keeps the schema stores turns with no row, which stay out of the list (until pruned) after the upgrade
again. Both routes are admin-only (`ha` and `metrics` get 403), answer `Cache-Control: no-store` and log no content,
only `conversation_read_failed op=list|get` on a store failure (500 `internal`): transcripts are read here and in the
database, never in a log (rule 8).

## Dashboard assistant (admin API)

`POST /api/v1/devices/{id}/assistant {text, language?: en|ru, speak?}` runs a turn the owner typed (1 to 1000
characters once trimmed) as the device's own agent turn and answers 202 `{turnId}` as it starts. The turn runs as a
press would (woken: its tools act for real, Write tools that need a woken session included, rule 7's explicit request
being the owner's typed one), joins the device's conversation and is stored and published with the trigger
`dashboard`. Its steps reach the live feed's `sessions` and `agent` topics under `turnId`; with `speak` its reply then
plays on the device as an announcement, and `assistant_speech {code}` says how that went. One typed turn runs per
device at a time (409 `busy`); a turn is bounded at 60 s; 404 `not_found`, 422 `text_invalid` or a schema refusal.
Admin only; the log gets `assistant_turn` and `assistant_speech` with ids and codes, never the text. The dashboard's
device page has it as its Assistant tab.

## Conversation compaction

A device's context carries its latest conversation summary and at most `historyTurns` (default 20) raw turns after
it. A conversation idle for `compactAfter` minutes (default 10), or whose raw turns pass 70 % of the turn model's
window (capped at 131072 tokens), is summarized in the background on `maintenanceModel` (empty: the device's
`model`) through the device's `llm` instance, at most 3 devices at a time. `compactionMode`, when set, is an agent mode that already exists (not the mode the speaker is in). The summary uses
that mode's model, reasoning effort, max tokens, first-token timeout and max seconds. `memoryMode` does the same
for the learning call; empty uses the summary's limits. With no compaction mode, `maintenanceModel`
and `compactionMaxTokens` (default 8192, 64–32768) apply, with no reasoning and a 60 s attempt: thinking counts
against the token ceiling even then, so a tight cap ends the run mid-thought with no summary. A failed run logs
`compaction_failed device_id=… code=…` and runs again once a newer turn idles, or for the same messages after 30 min.
The live feed shows `compaction` with counts only; summaries are in the database. The summary and learning input
label an event turn's framing (the server's own words, `trigger: event`) `Event:`, never `User:`.

Limits:

- A chat that runs past `historyTurns` turns with no idle gap, and below the size trigger, loses its oldest raw turns
  from the context until the next compaction takes them in.
- A run reads at most the newest 2000 messages since the last summary, and leaves messages younger than 10 s for the
  next run; older ones beyond the cap never reach a summary.
- Each device keeps its newest 50 summaries; pruning a summary also deletes its learning-log rows.
- Raw turns are no longer cut at an idle gap (the summary took that over): while compaction keeps failing, a device's
  context carries up to `historyTurns` raw turns since its last summary, however old.

## Memory learning

After each compaction the same model turns the summarized messages into memory operations (add, update, delete). The
memory exists so the assistant does not repeat a mistake: it keeps corrections the user made (as what to do instead),
fixes for a tool error that the user confirmed (the call that works, never "try again"), names the assistant did not
resolve (with their entity), and stated preferences, each as a short instruction; never what a tool can read again (a
playlist's songs, a state, search results), one-offs or the assistant's own story. The model is shown 20 existing
memories of the device's `memoryScopes`: those nearest to the summary, or without a global `embed` the pinned ones and
then the latest updated, so it updates a memory rather than adding it again (adds are then stored without vectors and
not deduplicated by them). A device with `memoryScopes: []` learns nothing (`learning_skipped`). An op is
dropped, and counted by reason, when it fails the schema (`schema`, `limit` past 10 ops), looks like a secret
(`secret`: key prefixes, long random runs, email addresses, phone numbers, a password/token/key word with a value),
adds to a scope the device does not use (`scope`), names an unknown memory (`alias`), touches a manual or pinned
memory (`manual`, `pinned`), cites no message the model was shown (`evidence`) or none of the user's own
(`untrusted`: tool results, an event turn's framing and the assistant's words never justify a change), finds no
room (`cap`) or its memory went or changed (edited, pinned or approved in the dashboard) after the model was shown it (`changed`). An add within
cosine 0.9 of an active learned memory of its scope updates that memory (with approval on, a pending one too, edited
in place). Adds to one scope take turns, as do the dashboard's creates, approvals and scope moves, so two devices
learning the same household fact store it once and the caps hold. Each scope keeps at most 300 active memories (per
device for `device`), evicting the non-pinned learned ones with the lowest `hits × 0.5^(age_days/14)`, and at most
100 pending ones (oldest dropped). The learning log (`memory_learning_runs`) has a row per run; the live feed shows
`learning_op` and `learning_run` with ids and counts only; logs `learned` / `learning_failed code=…` carry counts and
reasons. A failed run is not retried; the next compaction learns from its own messages. A memory's evidence ids name
conversation messages, which are deleted after 14 days: an older memory's evidence may point at messages that are gone.

With `memoryApproval: true` learning changes nothing active in that scope: global memories follow the global
setting, a device's memories its effective one (a device turning approval off loosens only its own). A new memory is
stored as `pending`; an update or delete of an active memory becomes a pending proposal row (`replaces` = that
memory, `proposal` = `update` or `delete`; the newest proposal for a memory replaces older ones, and a direct update
without approval deletes them); a pending memory is edited in place. Approving an `update` proposal writes it onto
the memory it replaces, a `delete` one deletes that memory, and a plain pending memory becomes active.

## Background tasks (admin API)

The `hermes` global setting names the Hermes provider instance (set with the rest of the global set, see
[Settings](#settings-admin-api)); it is read at startup only, so a change takes a restart. `GET /api/v1/tasks/kinds` lists `{name, instance, configured}`: `instance` is what the running kind was built
with (null: disabled), `configured` the setting now; when they differ the dashboard shows "restart to apply".

Routes (admin bearer, `:id` is the short id `t-<n>`):

- `GET /api/v1/tasks?status=<s>[,<s>…]&device=<uuid>&kind=<k>&limit=<1–200, default 50>`: newest first, no results.
- `GET /api/v1/tasks/:id`: the task with `result`, `events` (an `answered` event shows `decision`, `via`, `device`
  and `words`; the words go after 14 days) and `notices` (`eventSeq`, `text`, `createdAt`, `deliveredAt`).
- `GET /api/v1/tasks/:id/inspect?detail=summary|transcript`: the kind's live view; 502 `kind_unavailable` when the
  kind is off or silent.
- `POST /api/v1/tasks/:id/cancel`.
- `POST /api/v1/tasks/:id/reply {message, token}`: answers a clarify question only.
- `POST /api/v1/tasks/:id/approve {decision: once|deny, token}`: the dashboard may approve whatever
  `hermesApprovals` says; the answer is kept with `via: dashboard`.

`token` is the one the question carries; an answer to a question that changed is 409 `stale`. Other refusals are 409
`not_active`, `no_question`, `approval_pending`, `no_approval`, `busy`, `prompt_unshowable`, `secret_prompt`,
`finished`, `conflict`, `gone` or `capacity` (one name per condition, whichever side noticed it), 502 `kind_failed`, 422 with
`path` for bad input. Task text (titles, questions, results, words, notices) appears only in these bodies, the
database and the live feed's ids; logs carry short ids, kinds, statuses and codes. The live feed shows `task_*`
events (short id, kind, status, seq) as before.

Settings that shape task delivery:

- `quietHours` `{enabled, start, end}` (default `{true, "23:00", "08:00"}`, on the device's `timeZone`; an end before
  the start wraps past midnight) holds spoken task updates: they are said after the next woken session, or when the
  quiet hours end.
- `hermesApprovals` `voice` (default) or `dashboard_only`: who may answer an approval question. Voice needs a woken
  turn, a question the device's user was told before it (its update spoken there, or read in an earlier turn there),
  and the user's own words; `dashboard_only` leaves it to the routes above.

## Busy mode (admin API)

While the owner is busy (in a call, a meeting, screensharing, or not to be disturbed), the speakers hold what nobody
asked for: task updates wait, as in quiet hours, and go once busy ends (`afterBusy` below). Alarms, timers, sessions
the user begins and command words never look at it.

A busy signal is `{source, reason, devices, expiresAt}`, one per source; `reason` is `call`, `meeting`,
`screenshare` or `dnd` (several covering a device: the first of these wins), `devices` null covers every device. Every
stored signal expires, at most 24 h after its write (a source that keeps being busy sends it again, every 30–60 s for
a call); stored signals and their expiry survive a restart.

- `GET /api/v1/busy` → `{signals: [{source, reason, devices, expiresAt}], devices: [{deviceId, reason}]}`: the signals
  in force and the enrolled devices they make busy.
- `PUT /api/v1/busy/{source} {reason, devices?, ttlSeconds}` → 200 with the signal: it replaces the source's. A source
  matches `^[a-z0-9][a-z0-9._:-]{0,63}$` (the dashboard's toggle is `dashboard`, the voice tool's `voice`);
  `ttlSeconds` is 1–86400; `devices` omitted or null is every device, else 1–64 enrolled device ids. Bad input is 422
  `invalid` with `path`; a source starting `ha:` is 422 `source_reserved`.
- `DELETE /api/v1/busy/{source}` → 204, 404 `not_found`, or 422 `source_reserved` for `ha:`.

Sources besides the API:

- Home Assistant: the global `busyEntities` `[{entity, state, reason}]` (at most 32, each entity and state once)
  matches Home Assistant's live states; while an entity holds its state, every device is busy with its reason (source
  `ha:<entity>`, no expiry). Its signals go when Home Assistant disconnects, and a change to the setting applies
  without a restart. Examples: the companion app's `binary_sensor.<mac>_audio_input_in_use` `on` → `meeting`, a
  phone's `sensor.<phone>_phone_state` `offhook` → `call`.
- Voice: the `do_not_disturb {minutes | off}` tool, in any turn the user spoke (a follow-up too, never an event turn),
  sets `dnd` on every device for 1–1440 minutes (source `voice`), or clears it (`off` clears the `dashboard` source
  too: both are the owner's do-not-disturb).

Per device, `afterBusy` is `announce` (default: the held updates are spoken once busy ends) or `inbox` (they wait
for the next woken turn's unread block). The subscriptions hub holds its updates the same way. Logs carry sources, reasons and
counts only; a Home Assistant source is logged as `ha`, never its entity id (`busy_set`, `busy_cleared`,
`busy_expired`, `busy_ha_changed`, `busy_failed op=…`).

## Subscriptions (admin API)

A device follows work in outside systems through subscriptions to topics `<module>:<instance>:<path…>`: `module` is
the provider catalog's module, `instance` one of its provider instances, segments percent-encoded, `*` only as the last
segment (the subtree). On each update the most specific of a device's matching subscriptions decides alone (exact over
a subtree of the same literal prefix, the newest of equals): `mute` silences it, an event it does not list leaves it
out. `next_stop` ends a subscription with its first `stopped`. A provider instance a subscription names is refused
deletion (409 `in_use`). The modules: `t3code` (below), registered once the `t3code` setting first names an instance;
until then its topics are 422 `unknown_module`.

Routes (admin bearer only):

- `GET /api/v1/subscriptions?device=<uuid>`, `POST /api/v1/subscriptions {topic, events?, delivery?, until?, device}`
  (201; one per device and topic, a new one replaces it; 422 `invalid_topic`, `unknown_module`, `unknown_instance`,
  `unsupported_event` with `path`; 404 for an unknown device), `DELETE /api/v1/subscriptions/{id}` (204).
- `GET /api/v1/subscriptions/topics?prefix=<module>:<instance>[:path…]`: what the module lists there.
- `GET /api/v1/subscriptions/inbox?device=<uuid>`: pending updates, unread ones and open questions, `Cache-Control:
  no-store`.
- `POST /api/v1/subscriptions/reply {device?, token, answers? | message? | decision?}`: answers an open question by its
  token, with evidence `dashboard` (404 when none is open, 422 `answer_mismatch`, 502 `module_failed`).

Delivery waits for the device to be online, idle, outside quiet hours (unless the user is awake) and not busy, and
gathers the updates of about 5 s into one announcement: `speaker` speaks a fixed ru/en template (`«Тред „…“
остановился»`) and writes it into the history as a `notice` turn that the agent's history and memory learning never
read; `agent` runs an event turn with the updates as untrusted tool output, which may say nothing. The next woken turn
on the device reads an "Unread updates" block (delivered or inbox updates no woken turn saw, and every open question)
that carries Cadence's own record alone: each topic with its module, instance and kind, its updates counted with their
events and times, and each open question's kind, token and number of options; no title, text, question wording or
option label, so it taints nothing (owner ruling, as the tasks block). The Read tool `subscription_inspect {token |
topic}` returns the outside words (titles, texts, a question with its options) and taints the turn like an untrusted
tool result: the rest of it keeps Read tools and the taint-exempt `subscription_reply`, `subscribe` and `unsubscribe`.
Reading the block marks its updates seen; a question counts as told on the device once it was spoken there or
inspected by a turn there. Settings: `subscriptionDelivery` `speaker` (default) or `agent`, the
delivery of a subscription made without one; `subscriptionApprovals` `off` (default) or `voice`: an approval by voice
also needs a woken turn, a question told on the device before it and the user's own words. Outside text (titles,
texts, questions) lives in the database for 14 days and in these bodies only; logs carry ids, modules, counts and codes.

## Custom wake words (admin API)

`wake.custom` lets a device wake on a word the owner chose (`pkg/kws`: openWakeWord's front end and a small head per
word). Off by default; each device's capability config switches it: `{"enabled": true, "modelId": <id>, "sha256":
<the model's>, "sensitivity": 0.5}` (0 to 1, higher wakes more easily; the dashboard's device page sets all of it). The
device fetches the model once with its token (`GET /v2/wakeword/{id}`), checks its sha256 and caches it.

- `GET /api/v1/wakewords` lists the words (status `training`, `ready` or `failed` with its code, the calibration's
  threshold, recall and false detections per hour, whether a dataset is kept); `GET /api/v1/wakewords/{id}/model` is
  a ready word's model file.
- `POST /api/v1/wakewords/models?word=` stores a ready model file (JSON, at most 1 MiB).
- `POST /api/v1/wakewords/datasets?word=&keep=` trains one from a zip (at most 256 MiB): `takes/*.wav` (at least 3,
  6 to 15 is best, one utterance each, recorded where the device stands), `ambient.wav` (the room, 10 s or more,
  required) and `speech.wav` (the owner talking normally, optional). Any rate and channel count. The answer is the
  word in `training`; progress comes on the `wakewords` topic; a training takes a minute or two (one at a time,
  half the cores). The zip is held in memory and dropped when the training ends, unless `keep=true` stores it for
  `POST /api/v1/wakewords/{id}/retrain`; `DELETE /api/v1/wakewords/{id}/dataset` drops it. Its audio is never logged.
- Training needs the negative features: openWakeWord's precomputed features (CC BY-NC-SA 4.0, not ours to ship).
  `POST /api/v1/wakewords/negatives/fetch` fetches a sample (about 25 MiB from huggingface) into the file
  `CADENCE_WAKEWORD_NEGATIVES` names; `GET /api/v1/wakewords/negatives` says whether it is there. Without it a training
  fails `no_negatives`.

## T3 Code (admin API)

A T3 Code server (orchestration protocol 2) is a provider instance of module `t3code`: config `{baseUrl}`, secret
`token`, which the owner issues on the T3 host (`t3 auth session issue --scope orchestration:read --scope
orchestration:operate --ttl 365d --label cadence --token-only`) and pastes into the dashboard; agents never handle it.
The global `t3code` setting (a list of such instance names, at most 32, none: off) turns it on and applies without a
restart; a name of another kind is refused 422 `settings_invalid` at `t3code`, and an instance it names is refused
deletion (409 `in_use`).

For each named instance the server keeps one connection, follows the shell (every thread, new ones too) and a
thread's own stream while a subscription could match it and it is active (and 30 s after it went idle, at most 64 a
server). Topics: `t3code:<instance>:*`, `t3code:<instance>:project:<id>:*`, `t3code:<instance>:thread:<id>` (a thread's
update also matches its project's). Events: `stopped` (a run completed, was interrupted, failed or cancelled), `question`
(user input), `approval` and `error`; a request answered elsewhere withdraws its question. An answer is checked against
the thread's projection read again (the prompt the user heard, else 404): approvals `once`, `session`, `deny` become
`accept`, `acceptForSession`, `decline`; a question's options are `"1"`, `"2"`, … (several questions in one request
`"<question>.<option>"`, one each), a free answer where T3 allows one, and a request T3 wants answered by message gets
a message to the thread.

- `GET /api/v1/t3code/status` → `{instances: [{instance, running, connected, synced, lastError?, disabled?, threads,
  pending, followed}]}` in the setting's order: codes and counts, never a title.

Agent tools, offered only while `t3code` names an instance: `t3_threads {query?, project?, instance?}` (projects and
threads with ids, titles, status and what each waits on; its titles are outside text, so it taints the turn),
`t3_launch {project, text, title?, worktree?}` (a thread on the project's default model, runtime `approval-required`;
`worktree` starts from `HEAD`), `t3_send {thread, text, mode?: auto|queue|steer}` and `t3_stop {thread}`; the last three
need a session the user began. A thread or project is named by its id or its title; a name that fits several answers
their ids and does nothing. Logs carry instance names, kinds and codes: no title, text, question or token.

## TV catalog (admin API)

The catalog is one API over the content providers (TV stream D): torrplayer (anime and series from its sources),
TorrServer (the owner's torrents) and YouTube Music with YouTube videos. Each is a provider instance:

- module `torrplayer` (kind `catalog`): config `{baseUrl}`, the http(s) address of torrplayer's control API
  (`:8092`), secret `token` (its pairing's bearer token). torrplayer builds its play URLs on the host it was asked
  by, so `baseUrl` must be an address the TVs reach too (the Mac's LAN address, `serve --lan`); a TV then pulls
  `/play/{hash}/{index}` and `/resolve/{id}` from it directly;
- module `torrserver` (kind `catalog`): config `{baseUrl, publicUrl?, user?}`, optional secret `password` (basic
  auth). `publicUrl`, when the server calls TorrServer by another address than the TVs, is put on every play and
  subtitle URL handed out; those carry no credentials (TorrServer serves known torrents openly);
- a `youtube-music` music instance, with the `musicPlayer` (yt-dlp) instance resolving its tracks and videos; its
  streams go out behind the server's relay (`CADENCE_PUBLIC_ORIGIN` must be set).

YouTube itself (the `youtube-music` instance's `video` source) is read keylessly from `www.youtube.com`'s own web
API (InnerTube, `server/internal/providers/ytweb`), as YouTube Music's search is; yt-dlp still resolves what plays,
and finds videos when that API fails (then without channels, playlists, sorts or filters, named `unenforced`):

- search with `sort` `relevance` or `popular` (YouTube no longer sorts a search by date) and the filters
  `uploaded` (`today`, `week`, `month`, `year`), `duration` (`short` under 3 min, `medium` 3 to 20, `long`) and
  `live`, and one `kind` of `video`, `live`, `channel` or `playlist`, or YouTube's mix;
- a channel (`kind: channel`) opens on its videos, latest first; `tab` is `videos`, `shorts`, `streams` or
  `playlists`, `sort` `latest`, `popular` or `oldest` (a channel's playlists: `latest` or `updated`), as the tab
  offers them (`sorts`; `sort` names the one answered), and `q` searches inside the channel;
- a YouTube playlist reads in its own order a page at a time, or `latest`, `oldest` or by `views` over its first 500
  videos (`partial` past them); `latest` settles the videos tied on their newest relative time ("2 weeks ago") by
  their exact publish time; `q` keeps the titles holding it;
- a video (or a YouTube Music track) opens with its exact views, likes and publish time (the player's own answer,
  read even where it will not play to the server), and its description as a panel.

Every item carries the counts its listing showed as `stats` (`views`, `likes`, `plays`, `subscribers`,
`monthlyAudience`, `videoCount`, `publishedAt`, `publishedApprox` where the time was a relative one) and a video's or
a playlist's channel as `owner` (`{ref, title}`), opened as an item of its own. YouTube Music's rows carry their
plays, views, subscribers and monthly audience too, in `music_search` as in the catalog.

The voice agent reads it through `catalog_search` (`kind`: `title`, `video`, `channel`, `playlist`) and
`catalog_item` (`tab`, `sort`, `query`), and on any device through `youtube_search` (type, sort, uploaded,
duration, a cursor) and `youtube_browse` (a channel's tab and sort, a search inside it, a playlist's order, a video's
counts), which the `youtube` skill loads. The global `youtubeVotes` setting (off unless set) adds a video's likes
and estimated dislikes (`dislikesEstimated`) from [Return YouTube Dislike](https://returnyoutubedislike.com)'s open
API to those tools' answers, with its credit: a third party then learns the video ids the household looks at (only
ids; at most 8 a call, 60 a minute and 5 000 a day, under its terms' 100 and 10 000, backing off on a 429; cached
30 min). Logs carry an operation and a code (`ytweb_failed op code status`, `ryd_rate_limited`), never a query, an
id or a title.

The global `catalogSources` setting (a list of such instance names, at most 32, in the order Home and search show
them; none: an empty catalog) applies without a restart; a name of another kind is refused 422 `settings_invalid`
at `catalogSources`, and a named instance cannot be deleted (409 `in_use`).

Items reach screens and the dashboard as opaque refs (`c:…`, in memory, 12 h since last use; a restart forgets
them: `unknown_ref`), artwork as image ids. A title found on several torrplayer sources is one `title_group`
(merged by Shikimori, then MAL id) whose variants are its sources. The choices last played (a title group's source;
voiceover and quality in torrplayer's own sticky prefs; a TorrServer torrent's file) are remembered, so "next
episode, same voiceover" needs no choice; the server's own are in `catalog_choices`, keyed by a hash.

The voice agent's `catalog_play` never picks a voiceover at random: a choice the user named plays, else the title's
remembered one, else the first of the global `catalogVoiceovers.prefer` (dubs and sources, best first: `{"prefer":
["AniLibria", "anime365"], "avoid": ["animevost"]}`, at most 32 each, a name compared without case or separators,
none both preferred and avoided) that the title offers on any of its sources; `avoid` is never taken unasked. With
neither, the call answers `choose_voiceover` with the best two to four options and the assistant asks; the choice
then made is remembered for the title. A speaker's screen tools default to its room's screen; of several there,
the one the user is looking at (its assistant button opened the session, else the one whose drawer is open), then
the one on and playing, else the one touched last (its last screen context), then the one a screen tool used last;
only when none of these separates them does the call answer `ambiguous_device`.

A title's page (`catalog.item`, and the agent's `catalog_item`) carries Shikimori's and MyAnimeList's facts over
the source's: score, MyAnimeList rank and popularity, status, aired and released dates, studios, genres, episodes,
Shikimori's Russian description and the related titles (`show.rank`, `popularity`, `airedOn`, `releasedOn`,
`related`), read anonymously from `shikimori.io`'s and Jikan's (`api.jikan.moe`) public APIs by the title's ids and
kept in the server's memory for a day (a failed read ten minutes), at the pace each site allows; Home's items get
what the cache holds and warm it for its first ones. The owner's own list entry (`show.mine`: status, score,
episodes watched) comes from the Shikimori list torrplayer keeps linked (its `/mylist`, read at most every two
minutes): the server holds no Shikimori account or token. `catalog.alternatives {ref}` (screens) and the agent's
`catalog_alternatives` find the same title on the other providers and sources, by its Shikimori or MyAnimeList id,
else its names, each with its voiceovers, episodes and best quality.

Routes (`sources`, `search`, `browse`, `items`, `options` and `alternatives` also on the `ha` scope, for the Home Assistant media browser; codes only in errors):

- `GET /api/v1/catalog/sources` → `{providers: [{id, instance, capabilities, sources: [{provider, source, name,
  enabled, torrents}], failure?}]}`;
- `GET /api/v1/catalog/search?q=&provider=&source=&kind=&sort=&uploaded=&duration=&cursor=&limit=` → `{events: [...]}`, what a screen's
  streamed search gets at once: `{pending}` (the sources asked), one event per provider and source (`items`,
  `updated` title groups, `failure: {code}`, `next`, `unenforced` filters), then `{done: true}`;
- `GET /api/v1/catalog/browse?path=&cursor=&limit=`: an empty path is Home (every provider's root sections),
  `<provider>/<path>` a feed (`continue`, `my_list`, `subscriptions`, `catalogue`, `library`) or a tree node;
- `GET /api/v1/catalog/items/{ref}?cursor=&group=&choice=&tab=&sort=&q=` (`tabs`, `sorts`, `sort`, `partial` in the
  answer), `GET …/options?episode=&choice=`,
  `GET …/alternatives`, `POST …/act {action, on?, value?}` (an action id the item read issued),
  `GET …/panels/{panel}?cursor=`. `choice` repeats as `group=id` (an exploded query array): it selects a source for
  the item read and makes options consistent with choices already made, without remembering them before a play;
- `GET /api/v1/catalog/images/{id}`, and for screens `GET /v2/images/{id}` without credentials (an id is
  unguessable and issued by this process). Public images are fetched under the cover guard, a provider's own on
  its LAN host; both kept in memory, 5 MiB each at most. `/v2/images/{id}` answers in a type the request's `Accept`
  takes (a screen sends `image/jpeg, image/png`): a webp or a gif's first frame is converted to jpeg, or to png when
  it has transparency (4096 px a side and 4 MiB out at most, kept beside the original), 406 when nothing fits.
  torrplayer's signed `/api/v1/image` urls are fetched from its configured control API, also when its stream port
  (where it builds them) is another.

Screens call `catalog.search` (subscribed: events stream), `catalog.browse`, `catalog.item`, `catalog.options`,
`catalog.resolve` (subscribed: `{stage}` events, then `{playable}` or `{refused}`), `catalog.act` and
`catalog.panel` (a page of cadence cards) over the device link. Logs carry providers, sources and codes; never
a query, a title, a ref, an infohash or a URL. Dev fakes: `server/tools/faketorrplayer` and
`server/tools/faketorrserver`.

`POST /api/v1/devices/{id}/catalog/play {ref, episode?, choices?, from?}` (`ha` scope) is the Home Assistant media
browser's play: the same `media.play` a screen's own remote starts — the catalog's resolve with the exact choices the
browser selected (then remembered for the title), then `media.video/play` on the device — so a stream url never leaves
the server. `GET …/catalog/session` reads the playing title's resolved choices and its current source, voiceover and
quality groups; `POST …/catalog/select {group, choice}` switches one of them through `media.play` with `replaces` and
`reason=switch`, keeping the position and refusing a stale or unavailable choice (`422 invalid_choice`) or a newer
session (`409 superseded`). Both are `ha`-scoped and only sessions the server's screenplay tracker owns answer.
404 `unknown_ref`, 409 `refused` (`unplayable`, `sign_in_required`, …), 502 `unavailable`; nothing of the calls is
logged but the device and a status.

## Home Assistant (admin API)

Create a provider instance of kind `homeassistant` (`baseUrl`, secret `token`: a Home Assistant long-lived access
token), set the global `homeAssistant` setting to its name (with the rest of the global set, see
[Settings](#settings-admin-api)) and restart: the setting is read at startup only. A rejected token is not retried
until the next restart, so after fixing it restart the server (Home Assistant bans an IP after repeated failed
logins).

Settings: `haArea` (device only, an area id; narrows ambiguous names to that area; default unset), `haFavorites`
(global, up to 32 entity ids shown to the model first; default none), `haSensitive` (global, `woken` default or
`deny`).

The sensitive set is lock, alarm_control_panel, valve, siren, script, automation, update, button, input_button,
scene, group, conversation and garage, gate and door covers. With `woken` it needs a session a wake or press began (a
follow-up or an event turn refuses it); `deny` refuses it always. Not covered: a switch, input_boolean, select or
remote that wraps such a device, a cover without a device_class, and cameras: reclassify them in Home Assistant or use
`deny`. Through the `homeassistant` domain only `turn_on`, `turn_off` and `toggle` pass, and no service field that
names other devices (an entity, device, area, floor, label or target selector) is accepted.
`ha_control` refuses every entity of Cadence's own Home Assistant integration (platform `cadence`: each device's media
player and sensors) in every turn: those change only through Cadence's own tools. Other players' volume is
ordinary control. Not covered: a wrapper of a platform of its own, which `ha_control` can run: a script or scene
whose action sets a Cadence speaker's volume (in a woken turn; never with `haSensitive: deny`), or a media player of
the `universal` or `group` platform around one (in any turn). Build no such wrapper around a Cadence speaker. An
entity whose platform is not known is refused too (`unavailable`): one whose registry entry cannot be read (until
Home Assistant's entry changes; the model is told not to retry), and one with a state and no registry entry while an
entity re-list is pending, running or failed after a registry event that can name a new id (a `create`, an `update`
that changed an entity id, or one the server cannot read), and during each connection's load, since Home Assistant
lists a new entity in its registry before its first state. A failed re-list (`ha_reload_failed what=…`) is tried
again after 1 s, doubling to 30 s, without waiting for the next event.

Routes (admin bearer; 422 with `path` for bad input, unknown query parameters included). The entities, areas and usage
routes answer 404 `not_configured` when no client runs: `homeAssistant` was unset, or its instance could not be built,
at startup. After setting or fixing it, restart the server (the status route shows `restartRequired` until then).

- `GET /api/v1/integrations/homeassistant`: `{configured, instance, connected, connectedAt, lastError, entities,
  areas, restartRequired, version}`. `configured` is the setting now, `instance` what the running client was built with
  (null: none); `restartRequired` when they differ or `lastError` is `auth_failed`; `version` the Home Assistant
  version its latest authentication reported (`auth_ok`'s `ha_version`, kept across reconnects; null until one did,
  or when it is no version token).
- `GET /api/v1/integrations/homeassistant/entities?q=&domain=&area=&limit=<1–500, default 100>`: `[{id, name,
  domain, area, areaName, state}]` by id; `q` matches name, id or alias; `area` is an area id; names are sanitized and
  free-text states read `(text)`; 503 `unavailable` while there is no view.
- `GET /api/v1/integrations/homeassistant/areas`: `[{id, name, aliases}]`.
- `GET /api/v1/integrations/homeassistant/usage?device=<uuid>&limit=<1–200, default 50>`: `[{entityId, score}]`, the
  device's most used entities or, without `device`, the household's.

Bodies carry Home Assistant's names, states and areas (the dashboard's job); logs carry event names, codes, counts and
service names only, and the token is never in a body or a log.

## Home Assistant integration (M8)

The other direction: Home Assistant's `cadence` custom component (`integrations/home_assistant/`, install and use in
its README) connects to this server, shows each enrolled device as a Home Assistant device (a media player, online,
CPU, memory, disk and temperature) and adds the action `cadence.say`.

- Token: one of scope `ha` (see [API tokens and scopes](#api-tokens-and-scopes) for the CLI line into a mode-600 file),
  given once in Home Assistant's config flow, where it stays (never logged, redacted in diagnostics, but in the clear
  in Home Assistant's storage). The flow (setup, reauthentication, reconfigure) takes nothing wider: after
  `list-devices` it reads the admin-only `list-tokens`, and a 200 refuses the token (`token_too_broad`); an `ha`
  token's 403 there is the expected answer and logs `api_forbidden op=list-tokens scope=admin` once per check.
- URL: this server's public `https://` address (`https://$CADENCE_V2_PUBLIC_HOST`); the component refuses `http`,
  a login, a query or a fragment, and verifies TLS against Home Assistant's CA bundle (certifi's, or the file
  `REQUESTS_CA_BUNDLE` names; not the operating system's store). It needs a server with M8's routes (device presence
  on the feed, `get-device-vitals`, `get-device-artwork`): against an older one its setup fails on the vitals route's
  404 and Home Assistant retries it.
- What it calls: `list-devices` and, for each device, `get-device`, `get-device-media` and `get-device-vitals` at every
  (re)connect; the live feed's `devices`, `media` and `vitals` topics after it, nothing polled (no event is replayed, so
  every reconnect reads again; `dropped` too); `get-device-artwork` with `h` for a cover; `send-device-command` within
  the `ha` allow-list (volume, transport, `media.url play`); `announce-to-device` for `cadence.say`.
- Volume: Home Assistant is an authenticated caller and may set it (rule 7). The agent's `ha_control` refuses every
  entity of platform `cadence` (see [Home Assistant (admin API)](#home-assistant-admin-api)), so a turn never controls a
  Cadence entity directly; keep the component's domain `cadence`, and build no Home Assistant wrapper around a speaker
  (see the wrappers there).
- Revocation: `DELETE /api/v1/tokens/home-assistant` refuses its next request (401) and closes its feed with 1008
  within 20 s (`feed_closed reason=revoked`); Home Assistant then asks for a new token, and its feed stays closed until
  it has one (a `cadence.say` sent meanwhile still uses the old token and fails with 401, `invalid_auth`). A token
  without `ha` is refused at setup (403, `api_forbidden op=list-devices scope=ha`).
- `make e2e` drives the component's own client and feed modules against the dev stack (devices, a URL session and its
  pause, `say`, a device restart and vitals on the feed, the token revoked).

## MCP servers (admin API)

Create a provider instance of module `mcp-http` (`POST /api/v1/providers`): config `url` (https only, no userinfo,
query or fragment: a create with another is refused, 422 with path `/url`), `toolPrefix` (optional,
`^[a-z][a-z0-9_]{0,15}$`; default derived from the instance name), `timeoutMs` (1000–30000, default 8000: one tool
call) and `maxResultChars` (256–65536, default 8192); optional secret `authorization`, the whole `Authorization` header
value (`Bearer …`), sent only to `url`. The secret is sealed like every provider secret: the providers API shows
`{set: true}`, and no MCP route returns or accepts it. Then list the instance in the global `mcp` setting
(`PUT /api/v1/settings/global` with the rest of the set, see [Settings](#settings-admin-api); at most 32 names; a name
newly added must be an instance of kind mcp). Servers start and stop on a settings or instance change, without a
restart. Overrides are kept by instance name, so they survive a delete and re-create; the endpoint URL is part of each
tool's hash, so an instance re-pointed at another URL withdraws every tool until it is acknowledged (a new token
changes nothing).

Tools are opt-in: a tool is offered to a device whose `tools` setting names its local name
(`mcp_<prefix>_<remote name>`, shown by the tools route), or to every device without a `tools` list once the owner
sets `enabledDefault` (an explicit device list still narrows it). Mode lists never grant or hide an MCP tool. A
device's `tools` list names every tool that device gets, built-in ones too: `["mcp_x_y"]` alone leaves it that one
tool, without `weather`, `web_search`, `device_volume` or any other, so list those beside it. A local name keeps
characters `[A-Za-z0-9_-]` (others become `_`); one over 64 characters or held by another tool, of any server, is cut
and ends `_` + 6 hex of the remote name's sha256. A name stays with its tool while its row lives: every row, overrides
and last acknowledged hash with it, stays while a provider instance of its name exists, however long the tool is out
of the listing or the server down, so a tool that comes back is `changed` if it differs from what was acknowledged
(Ruling M5d-R11); the rows of an instance gone (deleted and not created again) go 30 days later. An instance created
again with another `toolPrefix` keeps its overrides but renames its tools at its next listing: the devices' `tools`
lists then no longer name them, and need the new names.

Overrides (per tool, `PATCH`): `effects` `read|write` (default `write`), `trust` `trusted|untrusted` (default
`untrusted`), `enabledDefault` and `anyTurn` (both default false). A Write tool needs a woken turn (a session a wake or
press began) unless `anyTurn`, and never runs in an event turn; a Read tool may. Only a tool marked both `read` and
`trusted` leaves the turn untainted (Ruling M5d-R8): after any other MCP result, the rest of the turn offers Read
tools only, and the two task tools whose own policy bounds them, `task_reply` and `task_cancel` (Ruling M5b-R4). A
Write tool that timed out, lost its server or sent an answer that could not be read (`mcp_protocol`) may
have run, so the model is not told to retry it (Ruling M5d-R9); a call that was never sent (no free slot of the 4 per
server, no session, a tool changed since the turn began) says so. The server's annotations are shown as hints and
never applied.

Rug pulls: a tool whose description, input schema or annotations change (or whose instance's URL does) is `changed`
and withdrawn, overrides kept, until the owner acknowledges its current hash.

An enabled tool's description reaches the model in the turn's first step, before any result could taint the turn, so a
hostile server can try to steer the model toward other tools there: enable only servers you trust. What the model reads
of a server is cleaned and capped:

- description: one line, control and format characters dropped, prefixed `[MCP <prefix>] `, at most 600 code points;
- input schema: at most 8 KiB as listed, `type: object`, nested at most 8 levels (the root is 1; a map of named
  subschemas such as `properties` and a subschema in it are one level together, every other object or array one
  more), no object over 64 members, only `#` references, no pattern over 512 bytes; the model reads the JSON Schema
  2020-12 vocabulary only (and draft-07's `dependencies`, `definitions`): `examples`, `$comment`, `x-*` and unknown
  keywords dropped, every `description` and `title` one line of at most 200 code points;
- result: structured content as `{"result": …}`, else its text parts as `{"text": …}` (other parts noted as omitted),
  in its text and every string and key of its structure the control characters but newline and tab (C0, DEL, C1), the
  bidi controls, the tag characters and U+FEFF stripped (the joiners emoji need stay), at most `maxResultChars` code
  points: a Read tool's is cut to fit (`truncated: true`), a Write tool's fails `mcp_too_large` (it ran). A structured
  number of 2^53 or more may have lost digits, so then the result's text is used instead, or, with none,
  `precisionLost: true` is added.

Each server offers at most 64 tools and all servers 128: `enabledDefault` tools first, then by local name. A server's
tools are re-listed every 10 min, 1 min after a failed listing, and when a call meets a tool the server no longer
knows; a server failing for 10 min has its tools withdrawn (`unavailable`) until it lists again.

Routes (admin bearer; `:name` an instance name in the `mcp` setting, else 404 `not_found`; 422 `{error: invalid,
path}` for bad input, unknown fields and query parameters included):

- `GET /api/v1/mcp/servers`: one entry per name in the setting, `[{instance, status: connected|connecting|unavailable,
  host, era: modern|legacy|"", lastCode, failingSince?, lastRefresh?, tools: {total, offered, changed, skipped}}]`.
  `host` is the URL's host (and port) only. A busy server keeps `connected` while its last listing failed: `lastCode`
  and `failingSince` say so; `lastCode` is the latest failure of a server still failing (or whose session is down),
  empty again once it lists. A rejected credential is `mcp_auth`. A name that is no
  instance of kind mcp shows `unavailable` and `not_configured`; one that could not be built, its build code.
- `POST /api/v1/mcp/servers/:name/refresh`: 202 `{accepted: true}`; the re-list's outcome shows in the list.
- `GET /api/v1/mcp/servers/:name/tools`: `[{remote, local, description, hints: {readOnly?, destructive?,
  idempotent?, openWorld?}, schemaBytes, effects, trust, enabledDefault, anyTurn, changed, hash, ackedHash?, firstSeen,
  lastSeen, skip?}]` by local name, those without one last. `description` is what the model reads; `ackedHash` shows
  while it differs from `hash`. `skip` says why a tool is not offered: `name` (an empty name, one over 128 code points or
  with control characters: shown cut to 128 code points with those escaped, and without overrides), `duplicate`,
  `schema_size`, `schema_type`, `schema_depth`, `schema_properties`, `schema_ref`, `schema`, `changed`, `cap`,
  `unavailable`, or the agent's refusal (`name`, `duplicate`, `run`, `schema`).
- `PATCH /api/v1/mcp/servers/:name/tools/:remote` (the remote name path-escaped) with any of `{effects, trust,
  enabledDefault, anyTurn, acknowledge: "<hash>"}`: the updated tool; 404 for a tool the server does not list under a
  local name; 409 `hash_mismatch` (nothing applied) when `acknowledge` is not the tool's current hash.
- `POST /api/v1/mcp/servers/:name/acknowledge` with `{hashes: {"<remote>": "<hash>", …}}` or `{all: true}`:
  acknowledges each named tool at its hash, or every changed one at its current hash, each or none (409
  `hash_mismatch` for a stale hash, 404 for a tool not listed); answers the tool list. `{all: true}` takes whatever
  is current, which may be newer than what the owner looked at: a dashboard sends `{hashes}` with the hashes it
  displayed, so a tool that changed again meanwhile is refused with 409 instead of approved unseen.

The live feed shows `mcp_connected {instance, era, tools}` (a server listed, first or again after a failure),
`mcp_unavailable {instance, code}` (code `removed` once a server stops because the setting no longer lists it or its
instance is gone; never at shutdown) and `mcp_tools_changed {instance, total, offered, changed, skipped}`, each on a
change only. Bodies carry tool names and the cleaned descriptions (the dashboard's job); logs carry instance names,
counts, local tool names, codes, ms and bytes only (`mcp_admin_failed op=… code=…` for a route that failed): never the
URL, a header, a description, a schema, arguments, results, a server's error text or a session id.

## Telemetry

Every device ships its logs (cadenced, audiod and its children, kmsg, the board's files) and system metrics to
`POST /v2/otlp/v1/logs` and `/v2/otlp/v1/metrics` with its device token; the server's own log lines join them as
source `server`, with no device. The server scrubs each device line again of the device's token and capability
secrets, stamps it with the device, and stores it. A request gets 5 s for its token lookup, 5 s for a decompression
slot and 10 s to store, each answered 503 when it runs out. A device gives a request 30 s; after a 429 or 503 it
sends nothing for the `Retry-After` and up to 20 % more (its logs wait, its metrics rounds meanwhile are dropped), and
after any other failure its logs back off 1 s, doubling to 60 s, each wait ±20 %. A token store that fails is logged
`otlp_ingest_failed code=auth` once a minute server-wide, the window's end counting the failures it held back
(`suppressed`).

Retention:

- Logs: one partition per UTC day, today's and the `logRetentionDays` days before it kept (global setting, default 7,
  1–30, sent with the rest of the global set: see [Settings](#settings-admin-api)). Old days go at the hourly
  maintenance round, and at once after a settings change.
- Metrics: 24 h, pruned hourly. Top processes: only the device's latest `system.metrics` state.
- A deleted device's lines and points are kept to these windows too (see [Devices](#devices-admin-api)).

What a device sends, every `intervalS` (its `system.metrics` config, 10–300, default 30):

- Gauges: `system.cpu.utilization` (0–1, total and per `cpu`), `system.cpu.load_average.{1m,5m,15m}`,
  `system.memory.{limit,available,used}` and `system.filesystem.{limit,free}` (bytes, per `mountpoint` the board
  declares), `system.temperature` (°C, where the board has a sensor) and `cadence.media_playing` (0 or 1).
- Sums, counted since cadenced started: `cadence.sessions{trigger}`, `cadence.wake_hits`, `cadence.commands`,
  `cadence.commands_ignored`, `cadence.rings{kind}`, `cadence.ring_stops{kind}`, `cadence.audiod_restarts`,
  `cadence.logs_{dropped,rejected,cut}` and `cadence.metrics_{errors,dropped}`.

Routes (scope `admin`, `get-device-vitals` `ha` too; the OpenAPI document's `telemetry` tag: `list-logs`,
`list-device-metrics`, `get-device-processes`, `get-device-vitals`; refusals are the error model: a bad parameter 422
`invalid` with its `path`, an unknown device 404 `not_found`):

- `GET /api/v1/logs?device=<uuid>&source=<s>&level=trace|debug|info|warn|error|fatal&since=<rfc3339>&until=<rfc3339>&q=<text>&limit=<1–1000, default 200>`:
  `{logs: [{ts, observedTs, deviceId?, deviceName?, board?, source, severity, body, truncated, continued}]}`, newest
  first. `level` is the lowest severity; `q` a case-insensitive substring of the body (at most 256 bytes; `source` at
  most 64); `since` is inclusive, `until` exclusive. Without `since` a read goes back 31 days, a search (`q` or
  `level`) 1 day, since it may walk every line in its window. A read gets 10 s, then 503 `timeout`. The server's own
  lines have no device fields.
- `GET /api/v1/devices/{id}/metrics?name=<n>&name=<n>…&since=<rfc3339>` (1–20 names): `{series: [{name, kind:
  gauge|sum, attrs, points: [{ts, value, start?, reset?}]}], truncated}`, by name, attributes and time, at most 24 h
  back (the default) and 50000 points (`truncated` then); 10 s, then 503 `timeout`. A sum's point carries its
  `start`; `reset: true` where its value fell or its start changed (the device restarted its count).
- `GET /api/v1/devices/{id}/processes`: `{topCpu, topRss}`, each at most 10 `{pid, comm, cpu, rss}` (cpu in % of
  one core, rss in bytes), empty until the device reports them.
- `GET /api/v1/devices/{id}/vitals` (`ha` too): the device's latest vitals, `{cpu, memoryUsedBytes,
  memoryLimitBytes, disk: [{mountpoint, freeBytes, limitBytes}], temperatureC?, at}`, from its last stored round that
  had them, in memory only; 404 `no_vitals` when it stored none in the last 10 minutes (a server restart forgets them
  until the next round). A round makes vitals when it holds `system.cpu.utilization` without a `cpu` (the total, 0–1;
  cadenced's first round after a start has none) and both `system.memory.used` and `.limit` (bytes); `disk` lists the
  mountpoints that sent both `system.filesystem.limit` and `.free` (bytes), by mountpoint, at most 16; `temperatureC`
  is `system.temperature` where the board has a sensor; `at` is the round's time. Only these gauges are read: never a
  process, a count or a load. Each one also reaches the live feed's `vitals` topic as `device_vitals`, at most one a
  device each 30 s: a round that comes sooner is held, and the device's latest goes out once its 30 s are up. A round
  older than the latest, stored within 30 s of it (a backlog), is left out; later it is taken (a device clock set back).
- The live feed (`/api/v1/live`) shows `log_line {deviceId?, source, severity, ts, body}` for every stored line to an
  admin feed subscribed to the `logs` topic (`{"subscribe":["logs"]}`; opt-in, see [Live feed topics](#live-feed-topics)),
  at most 50 a second to each; the rest are counted in one `log_line_dropped {count}` a second. The device filter
  (`?deviceId=` or the subscription's) applies before the limit. While no feed subscribed to `logs`, stored lines are
  not published at all.

Prometheus scrapes `GET /api/v1/metrics` (text exposition) with a token of scope `metrics`, which reaches nothing else
(an admin token may scrape too; an `ha` one is 403 `forbidden`, log `api_forbidden op=metrics scope=metrics`). Make it
into a mode-600 file, as the admin token above:

```sh
(umask 077; ssh <server> 'cd ~/cadence-v2 && docker compose exec -T cadence-v2 /cadence-server admin token create --name prometheus --scope metrics' > ~/.cadence/v2-metrics-token)
```


```yaml
scrape_configs:
  - job_name: cadence
    scheme: https
    metrics_path: /api/v1/metrics
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/cadence-token # the metrics token alone, mode 600
    # a device sends its own names and attributes: bound what one scrape may add
    sample_limit: 20000 # 1000 series a device at most, plus the server's own
    label_limit: 72 # 3 device labels, at most 64 attributes, the target's
    label_value_length_limit: 128
    static_configs:
      - targets: ["<host>"]
```

Raise `sample_limit` past about 18 devices sending their full 1000 series; a scrape over a limit fails whole.

- Device series: the latest point of each series of the last 10 min (older ones are left out), named `cadence_` and
  the metric's name, less a leading `cadence.`, with every character outside `[A-Za-z0-9_]` made `_`
  (`system.cpu.utilization` → `cadence_system_cpu_utilization`, `cadence.sessions` → `cadence_sessions_total`, so a
  device's `cadence.otlp.…` or `cadence.server.…` is left out as the server's own below); sums are counters ending in
  `_total`, gauges gauges (`system.memory.limit` → `cadence_system_memory_limit`), and a gauge whose name would end in
  `_total` gets `_gauge` after it (a gauge `x.total` → `cadence_x_total_gauge`), so a sum and a gauge never share a
  name. Labels: `device_id`, `device_name`, `board`, then the point's attributes (names made the same way, `_`
  before a leading digit; one starting `__` or taken by another left out), each value at most 128 bytes. At most 1000
  series of a device (by name and attributes); a name under `cadence_otlp_` or `cadence_server_` (the server's own),
  or labels another series of its name took, is left out and counted in `cadence_server_scrape_series_omitted{reason}`;
  `cadence_server_scrape_devices_capped` counts the devices cut at 1000 and `cadence_server_scrape_store_up` is 0
  when the store could not be read (the server's own metrics still serve).
- The server's own: `go_*`, `process_*`, `cadence_otlp_ingest_{accepted,rejected}_total{signal}` (records or points
  stored, and refused in a partial success), `cadence_otlp_ingest_{rate_limited,busy}_total{signal}` (requests
  answered 429, and 503 for want of a decompression slot), `cadence_otlp_ingest_failed_total{signal,code}`,
  `cadence_otlp_export_{sent,dropped}_total{signal}` (batches), `cadence_otlp_export_failures_total`,
  `cadence_otlp_export_queued` and `cadence_server_log_dropped_total` (the server's own lines lost before the store).

OTLP export: create a provider instance of module `otlp-http` (`POST /api/v1/providers`): config `url` (https only,
no userinfo, query or fragment, at most 2048 bytes: a create with another is refused, 422 with path `/url`; the
collector's base, under which `/v1/logs`, `/v1/metrics` and `/v1/traces` are posted) and `timeoutMs` (1000–30000, default 10000:
one request); optional secret `authorization`, the whole `Authorization` header value (`Bearer …`, `Basic …`), sent
only to those three URLs. The secret is sealed like every provider secret: the providers API shows `{set: true}`. Then
set the global `telemetryExport` setting to the instance's name, with the rest of the global set (see
[Settings](#settings-admin-api); `""` or null turns the export off); it applies without a restart, at a settings or
instance change and within a minute otherwise.

- Every stored line and point is forwarded, re-encoded as OTLP protobuf, gzipped, with the resource the server
  stamps: `service.name` `cadence-device` with `device.id`, `device.name` and `board`, or `cadence-server` for the
  server's own lines. A batch holds at most 1000 lines or 2000 points, and 512 KiB of their bodies, names and
  attributes.
- Best effort: at most 64 batches wait (besides the one being sent); past that the oldest are dropped and counted in
  `cadence_otlp_export_dropped_total`, as are batches the collector refuses (a 4xx but 408 and 429) and those waiting
  when the export is turned off. A failure waits 1 s, doubling to 60 s (a 429 or 503 `Retry-After` up to 60 s is
  honoured); ingest never waits for the export. TLS is verified against the system roots and redirects are refused.
- Logs: `telemetry_export_on|off|disabled instance=… code=…` at each change, `otlp_export_failed signal=… code=…
  status=…` once per outage and `otlp_export_recovered failures=…`: never the URL, the header or the collector's
  answer.

Privacy: the server's and cadenced's own lines carry ids, codes, counts and times only; third-party lines (Soloist,
Shairport Sync, Avahi, kmsg, board files) ship as written, scrubbed of known secrets by the device and again by the
server, and go to the export as stored, so the collector is trusted like the server. Process telemetry is pid,
`comm`, cpu and rss: never a command line (`/proc/<pid>/cmdline` is never opened). The scrub replaces each known secret
(the device token, every capability secret) in its exact, URL-escaped and base64 forms. A value a config rotates or
deletes, or one its provider refused, stays redacted on the device (the four latest per secret, until cadenced
restarts), as a child started with it may still log it; the server scrubs with the values it stores now. Known gaps:
a secret split across two lines, beyond the redacted ends of a line cut and its continuation, or nested in a larger
encoding (base64 of a document that holds it), passes; and up to 3 bytes of a secret at a cut may remain.

## Tracing

The server records spans of what it does (`internal/tracing`): an `/api` request is one server span named by method
and route template (`GET /api/v1/devices/:id`), an outbound provider request under a span is a client span named by
method and host (`POST openrouter.ai`), and the instrumented paths add their own. A caller's W3C `traceparent` joins
its trace (Home Assistant, a script). The spans live in memory: every running span and the last finished traces, at
most `ringSize` of them and about 32 MiB; a restart forgets them. Devices post their spans to
`POST /v2/otlp/v1/traces` with their token, as their logs (the same bucket; at most 4 MiB decompressed and 2048 spans a
request), stamped with the device (`device.id`, `device.name`, `board`) and scrubbed of its token and secrets; a
device's spans and the server's of one voice turn are one trace, from the wake word to the provider calls.

The global `tracing` setting, `{enabled, sampleRatio, ringSize}` (default `{"enabled":true,"sampleRatio":1,"ringSize":500}`;
`sampleRatio` 0–1, the share of new traces kept, while a trace a device or caller started follows its own decision;
`ringSize` 10–5000), sent with the rest of the global set, applies without a restart (with the export's reconcile, at
a settings change and within a minute). Off, nothing is recorded and devices' spans are taken and dropped.

Routes (scope `admin`, the OpenAPI document's `tracing` tag):

- `GET /api/v1/traces?limit=<1–500, default 50>&live=<bool>&device=<uuid>&since=<seq>`: `{traces: [{traceId, name,
  start, durationMs, live, error, spans, deviceId?, services, seq}], seq}`, newest first. With `since`, the traces
  changed after it, the oldest changes first up to `limit`, and `seq` the last one given: read again with it to miss
  nothing.
- `GET /api/v1/traces/{id}` (32 hex digits): the summary, `rootIds` and every span `{traceId, spanId, parentId?,
  remoteParent?, name, scope, kind, service, deviceId?, deviceName?, board?, start, end?, durationMs, status?,
  statusCode?, attrs, events, links, live, stream?, seq}` by start; 404 `not_found` once the ring let it go. The tree is
  built by `parentId`: a span whose parent is not in the trace (yet) is a root.
- `GET /api/v1/traces/stats`: live spans, traces, the ring, memory, the counts of dropped feed events, overflows,
  foreign spans, device spans taken and rejected, and the export's `{on, sent, dropped, failures, queued}` (spans).

Export to Jaeger or Tempo: traces go to the `telemetryExport` instance with logs and metrics (see
[Telemetry](#telemetry)), posted to its `/v1/traces`. Point its `url` at a collector's OTLP/HTTP receiver (an
OpenTelemetry Collector, Grafana Alloy, Tempo's distributor with `otlp: protocols: http`, or Jaeger 1.35+ with OTLP on,
its port 4318), over https: `https://otel.example:4318`, with `authorization` if the collector wants one. Spans are
batched (a 4096-span queue in the sdk, 8192 waiting to be sent, 512 a request); past that the oldest are dropped and
counted, a refusal (a 4xx but 408 and 429) drops its request, and other failures wait 1 s doubling to 60 s. Logs:
`trace_export_failed code=… status=…` once per outage and `trace_export_recovered`.

Privacy (rule 8): spans carry ids, codes, numbers, sizes and timings only: never a transcript, prompt, model message,
media metadata, URL path or query, header, token or error text (a failure is a code: `timeout`, `status_503`, …). A
guard drops attribute keys that name a URL, a query, a credential or a text, cuts strings to 256 bytes and keeps a
status only as a code, for the server's spans and the devices' alike, before the store and the export.
