# Yandex provisioning proxy

Owner-gated, for the owner's stations and LAN only. The optional `cadence-server` listener terminates the station's
TLS with an owner-supplied CA, proxies registration and the other allowed API calls to Yandex, injects network ADB into
`get_sync_info`, and never offers a firmware update. It also captures the stock daemon-log batches into Cadence's log
store instead of forwarding them.

It does **not** replace the installer's on-device OTA hardening or recovery checks. Remove the network redirect only
after `cadence-install` has verified adb/ssh and the board's update blocks. Agents never run the real-device steps.

## Behavior

- `quasar.yandex.net`, `quasar.yandex.ru` and `scbh.yandex.net`:
  - `/get_sync_info`: the real JSON response, with `adbd.remoteActivation=true`, `forceDisable=false`, and
    `network=true` on Buildroot boards. A 429, 5xx, connection failure or malformed success response gets a minimal
    local config with the same switches. Other upstream errors pass through.
  - `yandexstation` and `yandexstation_2` (Station Max): `network=false` and
    `system_config.com.yandex.tv.services.platform.{adbEnabled,developmentSettingsEnabled}=true`. Production adbd on
    Max is not necessarily root.
  - `check_updates` and `allow_update`: local `200 {"hasUpdate":false}`, never forwarded.
  - `/ota/` and `.zip`: local 403. No firmware mirror or downgrade mode exists in Cadence.
  - `/generate_204`: local 204 with the clock header.
- `clck.yandex.net/quasar/write/json/batch`: gzip or plain JSON daemon-log batches are stored locally and acknowledged
  only after the store succeeds. Every other path on the clickdaemon and AppMetrica report/startup hosts, `log.strm`
  and Alice push-event batches is acknowledged without its body being decoded, stored or forwarded. No telemetry
  request on these hosts is relayed to Yandex.
- Other requests on the explicit host allowlist pass through over verified HTTPS, including protocol upgrades such as
  the Alice WebSocket. Unknown hosts and clients outside the configured address list are refused. Request queries,
  authorization headers and sync bodies are never logged.

The CA must be a signing certificate and key accepted by the station firmware, with any required intermediates in the
certificate file. The private key must be mode 600. Cadence mints short-lived, per-SNI leaves in memory; no generated
key or certificate is written to disk. The publicly documented eMudhra intermediates expire on 2028-12-31, and newer
firmware may not trust them. In particular, Mini 3 / Mini 3 Pro factory and retail-CA procedures are board-specific:
this proxy cannot make an untrusting firmware accept TLS or replace a factory-mode procedure.

## Configure the server

Use the normal owner build/deploy flow, then add `compose.yandex.yml` to the Compose command. Do not run
`docker compose config`: it prints the env files.

1. Put the chosen CA chain at `config/yandex-ca.pem` and its private key at `config/yandex-ca.key`, outside the git
   archive. Keep the key mode 600 and readable by uid 65532 (the server image's `nonroot` user). Never copy these files
   into the repository or print them.
2. Add the station addresses to `config/cadence.env` (one IP or CIDR per entry, comma-separated):

   ```sh
   CADENCE_YANDEX_PROXY_CLIENTS=<station-ip>/32,<other-station-ip>/32
   ```

   Optional settings:

   ```sh
   # attribute a vendor DeviceId in a telemetry batch to an existing cadence device uuid
   CADENCE_YANDEX_PROXY_DEVICES=vendor-device-id=cadence-device-uuid
   # needed when this server's own dns also maps quasar back to cadence; use a resolver returning the real addresses
   CADENCE_YANDEX_PROXY_UPSTREAM_DNS=1.1.1.1:53
   ```

   Without a device mapping, vendor lines remain server-level log lines with `vendor_device=` and `platform=` prefixes.
   A mapping must be updated or removed when its Cadence device is deleted.
3. Start with the override:

   ```sh
   docker compose -f compose.yml -f compose.yandex.yml up -d
   ```

   The listener is host port 7443 by default. `CADENCE_YANDEX_PROXY_BIND` and `CADENCE_YANDEX_PROXY_PORT` in `.env`
   change the published address and port; the container always listens on 7443. Do not expose it through Traefik,
   Cloudflare, a public firewall rule or port forwarding.

## Point one station at it

The preferred route is DNAT on the router: it covers the station's DNS fallback addresses and does not change DNS for
Cadence or other LAN clients. Redirect only the station's TCP port 443:

```sh
iptables -t nat -A PREROUTING -s <station-ip> -p tcp --dport 443 \
  -j DNAT --to-destination <cadence-ip>:7443
```

Use the router's equivalent nftables/UI rule where appropriate. Keep the rule only for the provisioning window. Roll
back with the corresponding `-D` rule or by removing the UI entry. A DNS override is also possible only when the host
can accept port 443 (set `CADENCE_YANDEX_PROXY_PORT=443`); port 53 is not provided by Cadence. DNS alone cannot select
port 7443 and can create an upstream DNS loop, so DNAT is less fragile.

The allowlist covers the provisioning, telemetry and Alice hosts documented in the sources below; an unlisted host is
refused rather than silently proxied. Restrict the redirect to the provisioning window, then remove it.

## Provision, harden, roll back

1. Follow the station-specific factory/pairing procedure in the sources. Watch for `yandex_proxy_request route=sync_adb`
   and `route=update_blocked`; `route=sync_fallback` says the real sync could not be used and only the minimal config
   was sent.
2. The owner verifies recovery access immediately (`adb connect <station-ip>:5555`, then the board's documented id
   check). ADB is a recovery path, not proof that Cadence is installed.
3. Complete the existing owner-gated installer and all its verification before removing the redirect:
   [pickle](../pickle/README.md) or [midi](../midi/README.md). Those steps, not this proxy, provide the persistent OTA
   blocks and adb/ssh recovery required by the project rules.
4. Remove the router rule and restart without `compose.yandex.yml` (or stop the listener by removing
   `CADENCE_YANDEX_PROXY_LISTEN`). Verify recovery access again. Restoring ordinary DNS while leaving the station
   unhardened can let a later cloud sync disable ADB or deliver an update.

## Captured telemetry

Each fluent-bit entry becomes a normal Cadence log row with source `yandex-<daemon-log-basename>`, severity info and a
body prefixed by the vendor id and platform when present. A configured `CADENCE_YANDEX_PROXY_DEVICES` mapping also
attaches the row to that Cadence device and applies its stored capability secrets to the scrubber. Request credentials,
query secrets, common bearer/OAuth/token forms and mapped capability secrets are redacted. Rows use the existing
`logRetentionDays`, dashboard Logs page/live tail and OTLP export.

These are third-party daemon lines, not audio capture. Cadence does not store request headers, URLs, sync payloads,
AppMetrica/clickdaemon protobuf bodies or firmware. The existing third-party log-scrub limitations in `AGENTS.md` still
apply; do not treat the captured text as secret-free for publication.

## Sources

- Local reverse-engineering prototype: the owner's separate `great-yandex` checkout (`README.md`, `docs/ADB.md`,
  `rules.json`, `logging.go`). Its CA material and captures are inputs for the owner, never files to commit.
- [DNS/root workflow](https://hackalice.me/start/root/), [retail CA](https://hackalice.me/start/retail-ca/),
  [Mini 3](https://hackalice.me/start/retail-ca-mini3/), [Station Max ADB](https://hackalice.me/start/adb-max/), and
  [SSH/update hardening](https://hackalice.me/start/ssh/).
- [lite-kit](https://github.com/krevetkqqq/lite-kit): real-sync ADB injection, Max's platform-specific flags and the
  separate update-server behavior deliberately not copied here.
- [Telemetry internals](https://hackalice.me/internals/telemetry/): fluent-bit's JSON batch format and the separate
  AppMetrica/clickdaemon channels.
