# Cadence

![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=FFF)
![C++](https://img.shields.io/badge/C%2B%2B-00599C?logo=cplusplus&logoColor=FFF)
![Self-hosted](https://img.shields.io/badge/Self--hosted-2E7D32)
![Heavily vibecoded](https://img.shields.io/badge/Heavily_vibecoded-FF5555?logo=claude&logoColor=FFF)
![License: WTFPL](https://img.shields.io/badge/License-WTFPL-brightgreen)

> [!CAUTION]
> **Cadence is built entirely with AI assistants. It's sloppy and messy, and it was made for a small group of people.
> No support is provided.**
>
> **There's also no warranty. If it breaks your speaker, your server or your evening, you get to keep both pieces.**

Cadence is a self-hosted voice assistant for smart speakers you've rooted.
- One server does the thinking: speech-to-text, an LLM agent with tools, text-to-speech, memory, Home Assistant and
  music.
- Each speaker runs a small daemon that streams its microphone to the server and plays the answers back.
- A web dashboard sets everything up and shows what's happening.

It replaces the stock assistant on Yandex and VK speakers, and also runs on a Raspberry Pi or an Android TV stick.

## What it does

- **Voice assistant:** wake word, follow-up questions without the wake word again, and the speech and language
  models you pick (Deepgram, Yandex UniProxy, OpenRouter or any OpenAI-compatible API).
- **Smart home:**
  - Controls Home Assistant ("выключи свет").
  - A Home Assistant integration puts every speaker into Home Assistant.
- **Music:**
  - YouTube Music, SoundCloud, Yandex Music, Zvuk, VK Music and URL playback.
  - Most speakers are also Spotify Connect and AirPlay receivers.
- **Alarms, timers and reminders** that ring even when the server is down.
- **Multiroom:** speaker groups and stereo pairs, played in sync.
- **Memory:** it learns facts about the household and keeps a conversation history.
- **More tools:** weather, web search (Exa), background tasks (Hermes), MCP servers as tools, and Cadence itself as
  an MCP server.
- **Custom wake words:** trained on your own voice.
- **Dashboard:** devices, providers, agent modes, memory, logs, metrics and traces.

## Supported devices

| Device | Board | State |
| --- | --- | --- |
| Yandex Station Mini 3 | `yandex-pickle` | daily use |
| Yandex Station Midi | `yandex-midi` | works, some parts still unchecked |
| VK Capsula Mini | `vk-capsula-mini` | works, but its wake word doesn't yet: use the talk key |
| Yandex Station Micro / Lite | `yandex-micro` | works (one firmware only) |
| Raspberry Pi 4/5 (HDMI screen, Bluetooth speaker) | `rpi` | works; screen hot-plug is untested |
| Android TV stick | `android-tv` | works |
| Simulated speaker and screen | `sim`, `sim-tv` | for development |

### Features by device

| Feature | Mini 3 | Midi | Micro / Lite | Capsula Mini | Raspberry Pi | Android TV |
| --- | :-: | :-: | :-: | :-: | :-: | :-: |
| Voice with the stock wake word | ✅ | ✅ | ✅ | talk key only | via a room speaker¹ | via a room speaker¹ |
| Custom wake word² | ✅ | ✅ | ✅ | ✅ | — | — |
| Spotify Connect | ✅ | ✅ | — | — | — | ✅ |
| AirPlay | ✅ | ✅ | — | — | ✅ | ✅ |
| Bluetooth receive | — | ✅ | ✅ | — | ✅ | — |
| Multiroom groups | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Screen, video, HDMI-CEC | — | — | — | — | ✅ | ✅ |

1. These devices have no microphone of their own: a speaker in the same room listens for them.
2. New, and not yet tried on real hardware.

What each device has and hasn't been tested on yet is listed under *Open items* in [AGENTS.md](AGENTS.md).

## Quick start

1. **Server.** [deploy/quickstart](deploy/quickstart/README.md):

   ```sh
   cd deploy/quickstart
   cp .env.example .env
   ./setup.sh
   ```

   It needs Docker and takes about 20 minutes, mostly waiting for the build.
2. **Speaker.** Follow your device's guide:

   | Device | Guide |
   | --- | --- |
   | Yandex Station Mini 3 | [deploy/pickle](deploy/pickle/README.md) |
   | Yandex Station Midi | [deploy/midi](deploy/midi/README.md) |
   | Yandex Station Micro / Lite | [deploy/micro](deploy/micro/README.md) |
   | VK Capsula Mini | [deploy/vk-capsula-mini](deploy/vk-capsula-mini/README.md) |
   | Raspberry Pi | [deploy/rpi](deploy/rpi/README.md) |
   | Android TV | [android](android/README.md) |

3. **Home Assistant (optional).** [integrations/home_assistant](integrations/home_assistant/README.md).

## Contributing

This project runs on do-ocracy: whoever does the work decides. Want it simpler, or want a feature? Go ahead and
send a pull request.

- **Where to send it:** pull requests go to GitHub at
  [devilr33f/cadence](https://github.com/devilr33f/cadence).
- **How the code moves:** day-to-day development happens on an internal GitLab. GitHub gets one
  `sync: mirror gitlab source code to github` commit per sync. A merged PR is carried back to GitLab before the next
  sync.
- **Issues are off.** See the warning above.
- **Before you start:** read [AGENTS.md](AGENTS.md), whether you're a human or an AI agent. It has the layout, every
  command and the hard rules: no vendor OTA, keep recovery access, never raise the volume on your own, privacy,
  secrets.
- **Commits:** follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/), and `make ci` is the
  gate.

## Develop

Needs Go 1.27, make, curl and Docker (Docker Desktop on Windows; run commands from Git Bash).

```sh
make dev-up     # postgres, server, dashboard, a sim speaker and fake providers on http://127.0.0.1:8080
make e2e        # the end-to-end suite on its own stack
make ci         # every gate side by side, then e2e
make dev-down
```

The dashboard's own dev server is documented in [dashboard/README.md](dashboard/README.md).

## Docs

| What | Where |
| --- | --- |
| Server behind your own Traefik and Cloudflare (the full reference) | [deploy/server](deploy/server/README.md) |
| Streaming music providers (tokens, cookies, client ids) | [docs/music-providers.md](docs/music-providers.md) |
| Multiroom and calibration | [docs/multiroom.md](docs/multiroom.md) |
| Yandex station provisioning proxy | [deploy/yandex](deploy/yandex/README.md) |
| The spoken error prompts | [deploy/prompts](deploy/prompts/README.md) |
| Every command, gate, convention and open item | [AGENTS.md](AGENTS.md) |

## Thanks

- [capsulad](https://github.com/m4l3vich/capsulad): a framework for VK Capsula speakers. Its Cadence Client app
  connects to this server, and its server guide inspired the quickstart.
- [hackalice.me](https://hackalice.me): root and hardening guides for Yandex stations.
- [UxPlay](https://github.com/FDH2/UxPlay) and
  [android-airplay-server](https://github.com/jqssun/android-airplay-server): AirPlay on the TV stick.
- [Shairport Sync](https://github.com/mikebrady/shairport-sync), [nqptp](https://github.com/mikebrady/nqptp) and
  [Avahi](https://avahi.org): AirPlay on the speakers.
- [openWakeWord](https://github.com/dscripka/openWakeWord): the feature models behind custom wake words.
- [FFmpeg](https://ffmpeg.org), [yt-dlp](https://github.com/yt-dlp/yt-dlp),
  [pgvector](https://github.com/pgvector/pgvector) and [Caddy](https://caddyserver.com), among many others.

## License

[WTFPL](LICENSE): do what you want. Third-party parts keep their own licenses:
- each speaker tarball ships them in `licenses/`;
- `pkg/kws/assets/NOTICE` covers the wake word models;
- the submodules under `media/third_party/` keep theirs.
