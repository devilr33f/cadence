# Spoken prompts

A device tells its owner a request failed with a short spoken prompt: `offline` («Нет связи с сервером»), `failed`
(«Не получилось, попробуйте ещё раз»), `not_heard` («Не расслышала»), and `reminder` («Напоминание», for reminders
later). The phrases, in `ru` and `en`, are the table `server/internal/prompts/prompts.tsv` (tab separated `id`, `lang`,
`text`), embedded in `cadence-server`.

The audio files are never committed. Without them everything works: audiod plays its short error beep in place of a
prompt it lacks (`prompt_fallback id=… reason=missing`).

## What the device does

| Situation | Prompt |
|---|---|
| a wake or press with no link (at most once in 10 s; the lights blink offline every time) | `offline` |
| the link failed while the session started or streamed | `offline` |
| the microphone failed | `failed` |
| the server closed the session with `stt_*` | `not_heard` |
| `tts_*`, `llm_failed`, `provider_error`, `rate_limited`, `auth`, `timeout`, `internal`, `not_configured` | `failed` |
| `ok`, `no_speech`, a follow-up's own ends, `stopped`, `cancelled`, `superseded`, a command word, the switch, a ring | none |

Never during a ring, never for test audio, at most one per session, after the session's audio was cut. `failed` and
`not_heard` also pulse the lights red for 1.5 s. The `errorSound` setting (global or per device: `prompt`, the
default; `beep`, the error beep alone; `off`) and the `language` setting's primary subtag reach the device as its
`session` capability config, which it keeps offline. Nothing here touches the volume (rule 7).

## Rendering them (owner)

The files are made once with the server's own TTS provider instance, so the provider's key never leaves the server's
database (rule 6); agents never run this. Pick an instance whose output you may ship to your devices, and a voice
(`--voice`; without it the global `voice` setting). From the repo root on your machine, with the server at `<server>`
(ssh) and its stack in `~/cadence-v2` (`deploy/server/README.md`):

```sh
mkdir -p build/prompts
for lang in ru en; do
  ssh <server> "cd ~/cadence-v2 && docker compose exec -T cadence-v2 /cadence-server admin prompts render --tts <instance> --lang $lang --out -" \
    | tar -x -C build/prompts
done
ls -l build/prompts/*/
```

`--out -` writes a tar of `<lang>/<id>.opus` on stdout (the server's container has no shell to copy files with); the
log on stderr names each prompt with its size (`prompt_rendered`) or a code (`prompt_render_failed`: `not_found`,
`not_tts`, `too_long`, `no_audio`, `tts_failed`…). `--out <dir>` writes the same files into a directory instead, and
`--table <file>` takes another table of the same shape.

Each file is Ogg Opus, mono, 24 kbps, the speech with 50 ms of silence each side and at most 2.9 s (audiod plays at
most 3 s and 256 KB). Listen to them before shipping (`ffplay build/prompts/ru/failed.opus`).

## Shipping them

`make dist DISTRO=<board>` passes `build/prompts` (when it exists) to the speakers' builders, which copy
`<lang>/<id>.opus` into the tarball's `share/prompts/` (`deploy/builder/add-prompts.sh` refuses any other name, a
link, an empty file or one over 256 KB). audiod reads `share/prompts/<lang>/<id>.opus` beside its `bin/` the first
time a prompt is asked for. `check-device-tree.sh` does not require them. The android-tv APK and the screens' packages
do not carry them yet: there the beep plays.
