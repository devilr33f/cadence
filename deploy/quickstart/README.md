# Cadence quickstart

Runs the server and dashboard on one machine with HTTPS, using one command. It takes about 20 minutes, mostly
waiting for the build.

> [!CAUTION]
> Cadence is built entirely with AI assistants for a small group of friends. It's messy, and **no support is
> provided**. If something breaks, read the logs, fix it, and [send a PR](../../README.md#contributing).

## What you need

- A Linux machine (amd64 or arm64; macOS or Windows with Docker Desktop also work for trying it out) with 2 GB RAM and
  about 15 GB of free disk.
- Docker with Compose v2, plus `git`, `bash` and `curl`.
- An address your speakers and your browser can reach: a LAN IP (`192.168.1.10`), a local name (`cadence.lan`) or a
  public domain.

## Install

1. Get the code:

   ```sh
   git clone https://github.com/devilr33f/cadence
   cd cadence/deploy/quickstart
   ```

2. Create your settings and set `CADENCE_HOST` to your address:

   ```sh
   cp .env.example .env
   nano .env
   ```

3. Run the setup:

   ```sh
   ./setup.sh
   ```

   It builds the images, starts everything, waits for the server, and then asks you to choose the dashboard password
   (at least 12 characters).

4. Open `https://<your host>` and log in.
   - With the default certificate (see below), your browser warns you until you trust `data/ca.crt`, or until you
     accept the warning once.

5. In the dashboard:
   1. Open **Providers** and add a speech-to-text, a language-model (LLM) and a text-to-speech provider.
      - Each provider has a **Test** button.
      - [Music providers](../../docs/music-providers.md) are optional.
   2. Open **Settings** and choose those providers as `stt`, `llm` and `tts`.
   3. In **Settings**, also set `timeZone` (for example `Europe/Moscow`). Alarms and timers stay off until it's set.

## HTTPS: pick one

| `CADENCE_TLS` | When | What to do |
| --- | --- | --- |
| `internal` (default) | Any address, LAN included | Caddy acts as its own certificate authority. Trust `data/ca.crt` in your browser or OS, and give it to speakers with `--ca-file` |
| your email | A public domain whose ports 80 and 443 reach this machine | Caddy gets a Let's Encrypt certificate. Nothing to trust, no `--ca-file` |

After changing it, run `./setup.sh` again.

## Add a speaker

Each speaker has its own guide:

| Speaker | Guide |
| --- | --- |
| Yandex Station Mini 3 (`yandex-pickle`) | [deploy/pickle](../pickle/README.md) |
| Yandex Station Midi (`yandex-midi`) | [deploy/midi](../midi/README.md) |
| Yandex Station Micro / Lite (`yandex-micro`) | [deploy/micro](../micro/README.md) |
| VK Capsula Mini (`vk-capsula-mini`) | [deploy/vk-capsula-mini](../vk-capsula-mini/README.md) |
| Raspberry Pi 4/5 | [deploy/rpi](../rpi/README.md) |
| Android TV stick | [android](../../android/README.md) |

The guides all follow the same flow:
1. Build the speaker's tarball.
2. Get an enroll code from the dashboard (**Devices → Enroll**).
3. Run `cadence-install`, adding `--ca-file deploy/quickstart/data/ca.crt` when `CADENCE_TLS=internal`.

Two devices don't use `cadence-install`:
- **Raspberry Pi:** it trusts `data/ca.crt` system-wide instead (see its quick path).
- **Android TV:** a Let's Encrypt certificate is the simplest choice.

On a VK Capsula running [capsulad](https://github.com/m4l3vich/capsulad):
1. In capsulad's Web UI, upload `data/ca.crt` to `/assets/cadence/ca.crt`.
2. Put capsulad's own CA (**Access → Download CA**) into `data/ca-certs/`.
3. Run `docker compose restart server`.

## Update

```sh
git pull
./setup.sh
```

The master key, the database and your password are kept.

## Everyday commands

Run them from `deploy/quickstart/`.

| What | Command |
| --- | --- |
| Server logs | `docker compose logs -f server` |
| Stop / start | `docker compose stop` / `docker compose start` |
| Set the password again | `./setup.sh password` |
| Export the CA again | `./setup.sh ca` |
| Trust another CA for the server's own calls (Home Assistant, a speaker) | copy its `.crt` into `data/ca-certs/`, then `docker compose restart server` |

## Back up

- `.env`: its `POSTGRES_PASSWORD` opens the database.
- The Docker volumes:
  - `cadence_db`: everything you configured.
  - `cadence_keys`: the master key.
  - `cadence_caddy-data`: the CA's private key.
- **If you lose the master key, every provider key stored in the database is lost too.**
- **If you lose the CA, Caddy makes a new one.** Every speaker given the old `data/ca.crt` then stops connecting until
  you install again with the new one, then reboot the speaker.

## Troubleshooting

| Problem | Fix |
| --- | --- |
| A port is already in use | Change `CADENCE_HTTPS_PORT` or `CADENCE_HTTP_PORT` in `.env` and run `./setup.sh` again. The address then includes the port: `https://<host>:8443` |
| `the server did not come up` | Check `docker compose logs server` |
| A speaker's enroll fails with a certificate error | Run the install again with `--ca-file`. Keep `CADENCE_HOST` as the address the speaker uses |
| Let's Encrypt fails | Check `docker compose logs caddy`. Ports 80 and 443 must reach this machine from the internet |
| You forgot the password | Run `./setup.sh password` |

## Remove

- `docker compose down` stops everything and keeps your data.
- `docker compose down -v` deletes everything, the database and the master key included. There is no undo.

Running behind your own Traefik and Cloudflare instead: see [deploy/server](../server/README.md), the full reference.
