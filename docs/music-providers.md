# Streaming music providers

Cadence can search and play music through SoundCloud, Yandex Music, Zvuk (formerly SberZvuk) and VK Music. These
modules use the providers' own web or app APIs, not stable public developer APIs; an upstream change can require a
Cadence update or a new credential.

Use credentials from your own accounts. Tokens and cookies are session credentials: paste them only into the
dashboard's write-only secret fields, never into chat, a settings JSON field, a commit or a log. Cadence seals provider
secrets in its database. None of these modules needs your account password.

## At a glance

| Module | Search without an account | Whole songs from the provider | Credentials |
| --- | --- | --- | --- |
| `soundcloud` | Yes | Yes; a Go+ account also plays songs otherwise limited to previews | None normally; optional `clientId` and `oauthToken` |
| `yandex-music` | Yes | With a Yandex Plus account | Optional `token` or `cookies`; without either, playback uses the YouTube Music fallback |
| `zvuk` | Yes | Yes, at 128 kbps as a guest; higher quality needs a subscription | None normally; optional `token` |
| `vk-music` | No | Yes, with the account's access | Required `token`, plus the User-Agent it was issued with |

## Connect a provider

1. In the dashboard, open **Providers → Add a provider**.
2. Enter an instance name and select the module under **Music**. The name is what the `music` setting will reference;
   using the module id as the instance name is a convenient default.
3. Fill in **Configuration** and **Secrets** as described below. Leave optional secrets empty when you do not need them.
4. Save the instance and choose **Test**. The test checks search or account access; it does not play a song or prove a
   subscription's entitlement.
5. Open **Settings**. Add the instance names to **Music search** (`music`), either globally or in a device's overrides.
   For example: `["soundcloud", "yandex-music", "zvuk", "vk-music"]`.
6. Optionally set **Music player** (`musicPlayer`) to a `yt-dlp` instance. This lets Cadence play a YouTube Music match
   when the original provider cannot supply a whole song. It is needed for anonymous Yandex Music playback, Spotify
   results, and currently for these providers' songs on TVs.
7. Save and start a new turn, for example «Включи Linkin Park». No server restart is needed.

To replace a token or cookie later, edit the provider instance and enter the new value. An empty secret field keeps the
stored value; **Clear** removes an optional secret. The dashboard and API never return a stored secret's value.

## SoundCloud (`soundcloud`)

The normal setup needs no developer application, client id or account: Cadence reads SoundCloud's current public web
client id from the site's scripts and refreshes it when necessary. Use config `{}` and leave both secrets empty.

Optional secrets:

- `clientId`: a fixed 32-character alphanumeric web client id. Set this only if automatic discovery fails. To find the
  one your browser uses, open soundcloud.com with the browser's developer tools, open **Network**, reload or search,
  select a request to `api-v2.soundcloud.com`, and copy only its `client_id` query parameter's value. This is not a
  client secret or an OAuth token.
- `oauthToken`: the raw value of the `oauth_token` cookie from a signed-in soundcloud.com browser session. In Chrome or
  Edge it is under **Application → Storage → Cookies → https://soundcloud.com**; in Firefox, under **Storage →
  Cookies**. Paste only the value, without `oauth_token=` or an `OAuth` prefix. A Go+ account uses this to play songs
  that guests receive only as 30-second previews.

There is no configuration to set. The provider chooses a whole-song progressive MP3 when available, otherwise HLS AAC
or MP3.

## Yandex Music (`yandex-music`)

Search, albums, playlists, artists and radio work anonymously. Native whole-song playback requires an account with
Yandex Plus and either `token` or `cookies`. If both are set, `token` wins. Without credentials, configure `musicPlayer`
so Cadence can play a matching song from YouTube Music instead.

Configuration:

```json
{"quality":"hq"}
```

`quality` is `hq` (the default), `nq`, or `lossless` (FLAC, where the account and track allow it).

### Option A: OAuth token

Yandex Music's public app client id is `23cabbbdc6cd418abb4b39c32c41195d`; you do not need to register an application
or create a client secret.

1. Open [Yandex's authorization page for the Music app](https://oauth.yandex.ru/authorize?response_type=token&client_id=23cabbbdc6cd418abb4b39c32c41195d)
   in a browser signed in to the account you want Cadence to use.
2. Approve access if asked. The browser lands on a URL whose fragment contains
   `#access_token=<token>&token_type=…&expires_in=…`.
3. Copy only the `<token>` value, stopping before the next `&`. Paste it into the provider's `token` secret. Do not
   paste the whole redirect URL.

### Option B: browser session cookies

Use this when you prefer Cadence to exchange the browser session for a Music token itself. The exchanged token is
cached for 12 hours and exchanged again after a refusal.

1. Sign in to yandex.ru or music.yandex.ru in your browser.
2. In the browser's cookie storage for `yandex.ru`, find `Session_id` and `sessionid2`.
3. Put this two-cookie header in the `cookies` secret, replacing only the placeholder values:

   ```text
   Session_id=<Session_id value>; sessionid2=<sessionid2 value>
   ```

   Do not include the word `Cookie:`. `Session_id` is required; include `sessionid2` as well when the browser has it.

A Netscape `cookies.txt` export also works. Keep only the `.yandex.ru` entries and use **Load from file…** beside the
secret; the dashboard reads the multiline file locally. Cadence sends only the two session cookies to Yandex.

### Моя волна (My Wave / My Vibe)

Once the instance has a `token` or `cookies`, ask «Включи мою волну», “play my Yandex wave”, or “play my vibe”. No
additional configuration is needed. Cadence searches with `kind: "station"` and plays Yandex's personal
`user:onyourwave` station; starting it without an account credential answers `sign_in_required`.

Each queue refill asks for the personal station's next batch, rather than becoming a radio based on the last song. If an
individual track needs the YouTube Music fallback, the queue still continues Моя волна. Native whole-song streams have
the same Yandex Plus requirement as ordinary Yandex tracks. A station cannot be added with `when: "next"` or `"later"`;
it starts as a new, continuing queue.

## Likes, dislikes and Cadence favorites

Say «нравится» or “like this” for the current song, or use the heart/dislike buttons beside a song in the dashboard's
History page. A like creates a durable Cadence favorite; a dislike removes it and marks the song not to recommend.
Toggle the same mark again to clear it. For an earlier voice-search result, its short result id works too.

With a Yandex `token` or `cookies`, Cadence also updates the Yandex account's track likes/dislikes. Other providers
currently keep the rating in Cadence only, reported as `providerSync: "not_supported"`. A failed provider update does
not silently create a synchronized favorite.

Yandex radios report actual listening back to the recommendation station: station and track starts, natural finishes,
manual skips and replaced songs, including played seconds and the station batch id. Reports are sent in order and never
hold up playback.

Ask «включи мои любимые» or “play my Cadence favorites” to play the local collection. The dashboard's **History →
Favorites** view lists the same songs. Favorites survive the 14-day history cleanup and remain until cleared; only
canonical public song ids and pages are stored, never stream/signed URLs or VK access keys.

## Zvuk (`zvuk`)

The keyless setup obtains a guest token automatically and plays whole songs at 128 kbps MP3. Use config
`{"quality":"mid"}` and no secret for this mode.

For a signed-in account, set the optional `token` in either of these ways:

1. Sign in at [zvuk.com](https://zvuk.com/), then open
   [https://zvuk.com/api/tiny/profile](https://zvuk.com/api/tiny/profile) in the same browser profile. Copy only the
   JSON value `result.token`.
2. Alternatively, copy the raw value of the `auth` cookie for `zvuk.com` from the browser's cookie storage.

Configuration:

```json
{"quality":"high"}
```

`quality` is `high` (320 kbps, the default), `mid` (128 kbps), or `flac`. Cadence steps down to a quality the account
can actually receive; `high` and `flac` require the appropriate subscription.

Zvuk normally requires a Russian network exit and can refuse automated calls with HTTP 418. The **server's** egress
must satisfy this, not just your browser's; `HTTPS_PROXY` on the server applies to provider requests. A geo-block or
anti-bot refusal is not fixed by replacing the token.

## VK Music (`vk-music`)

VK requires an access token for audio search as well as playback. Do not create a regular VK application: its token
does not grant the audio methods Cadence uses. The token must belong to VK's official Android client (public client id
`2274003`, scopes `audio,offline`); Kate Mobile tokens no longer work. Cadence never receives your VK login or password.

The token and the User-Agent used to obtain it are a pair. If you use the default Android client flow below, copy both
values from its output file rather than relying on Cadence's default User-Agent.

### Obtain a token and matching User-Agent with vkpymusic

[vkpymusic](https://github.com/issamansur/vkpymusic) is a third-party Python helper whose `TokenReceiver` implements
the official Android client's login flow, including 2FA and captcha prompts. Run it yourself on your own machine, in a
disposable virtual environment:

```sh
python3 -m venv ~/.venvs/vkpymusic
~/.venvs/vkpymusic/bin/python -m pip install vkpymusic
umask 077
~/.venvs/vkpymusic/bin/python - <<'PY'
from getpass import getpass
from pathlib import Path
from vkpymusic import TokenReceiver

receiver = TokenReceiver(input("VK login: "), getpass("VK password: "))
if receiver.auth():
    receiver.save_to_config(str(Path.cwd() / "config_vk.ini"))
    print("Saved config_vk.ini in the current directory.")
PY
```

Follow the 2FA or captcha prompts. The script deliberately does not print the token. On success, `config_vk.ini` in the
current directory contains:

```ini
[VK]
user_agent=<matching User-Agent>
token_for_audio=<access token>
```

Create the `vk-music` instance with:

- secret `token`: the `token_for_audio` value;
- configuration `userAgent`: the exact `user_agent` value, for example
  `{"userAgent":"<matching User-Agent>"}`.

Keep the file mode 600 and remove it when it is no longer needed. If you obtain a token through another flow, set
`userAgent` to the exact User-Agent sent by that flow. A valid token with the wrong User-Agent fails VK's audio calls.
The instance test performs a one-song `audio.search`, so it checks both values.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Provider test `auth` | The token or session expired. For SoundCloud, also check a manually supplied `clientId`; for VK, check that `userAgent` exactly matches the token's issuance flow. |
| Provider test `build_failed` | Check the config's `quality`/`userAgent` JSON and the secret's format. Yandex cookies must contain a valid `Session_id`; SoundCloud's fixed id must be 32 letters or digits. |
| Provider test `unreachable` or `timeout` | Check DNS, TLS and outbound HTTPS from the server. For Zvuk, check the Russian exit and proxy. |
| SoundCloud test `bad_response` | Retry without a fixed `clientId`; if automatic site-script discovery is failing, supply the browser's current web `client_id`. |
| Zvuk test `bad_response` | This can be an upstream API change or its HTTP 418 anti-bot refusal; check the server exit before replacing credentials. |
| Playback says `sign_in_required` or returns a preview | The account lacks access to that track or quality. Add/renew the account credential, check the subscription, or configure `musicPlayer` for fallback. |
| Playback says `unplayable` | The track is unavailable, region-blocked, or offered only in a format the module cannot use. Configure `musicPlayer` to try a YouTube Music match. |
| Playback says `not_set_up` | A fallback was needed but no `yt-dlp` instance is selected in `musicPlayer`. |

The provider test is intentionally cheap. After it passes, verify one real voice turn per provider: search can succeed
while a subscription, regional restriction or expiring stream URL still prevents native playback.
