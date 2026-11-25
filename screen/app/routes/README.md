# screen/app/routes

Wave 2's screens on stream B's library. `core/` is shared; each stream adds its routes in its own `<name>.cmake` and
`register<Name>Routes(Router&)` (i1: `browse.cmake`, `browse.cpp`), and its sheet as `ui/assets/rcss/routes-<name>.rcss`
(linked into every document after the library's).

| Part | What |
|---|---|
| `core/router` | `Router` (a `ui::Pages` of the `ShellLayer`): a stack of `Route`s by `ScreenRoute`, cadenced's `Route` message (`back`, home pops to the root), BACK, the opener's focus and each row's memory given back on return, the image cache over the server's proxy, `makeRoutedUi()` for `main.cpp`, and `makeRoutedUi(Options)` with the platform's image fetcher for `android/native/android-main.cpp` |
| `core/rpc` | `RpcClient`: screen/v1's `RpcRequest`/`RpcEvent`/`RpcCancel` as json calls, events in `seq` order, `failAll` when the link drops |
| `core/context` | `ContextPublisher`: `ScreenContext` 500 ms after the last change (2 s at most while a page keeps changing), at once at a turn's start (`assistant_turn`, `voice_state` invoked or listening), a revision one higher each time |
| `core/failure` | a failed call as it is shown: the rpc status, the server's refusal and failure codes, the provider and source, the upstream's http status (catalogscreen's refusal body), the time waited; codes alone ([a-z0-9_.-]), never an upstream url or text (rule 8). Every error state is `ui::rml::errorCard` over it (title, source, reason, the call; «Повторить» keeps the failure shown and spins until the retry is answered); a feed of many fails inline in its row |
| `core/catalog` | the catalog's json views as structs, the card a catalog item makes, provider badges, failure words |
| `core/play` | picker or resolving for an episode (a remembered voiceover skips the picker), the episode tile |
| `core/router`'s `Service` | a stream's part beside the stack: every message, player state and frame, cadenced's `Route` for a page it shows itself, and the remote's keys before the focus manager (`Ui::setKeyFilter`); `Ui::setUnderlay` draws under the layer |
| `home`, `search`, `title`, `episodes`, `picker`, `resolving` | i1's routes (design r2 §11, §18); home asks its providers again each minute while on top and on coming back to it, quietly (rows updated in place, focus and row scroll kept, an answer held while focus moves). The hero follows one rule (`home/hero.h`): a focused card's item in any row (its name at once, its details from `catalog.item` faded in once focus rests 150 ms, kept per ref), else the film playing behind the page, else the item the hero's buttons act on (the one to continue). Home's backdrop is the whole page's, the same item's art: the blurred backdrop of `core/backdrop.h` (decoded at 480 px, blurred once on the decode worker; resolving asks for the same cache entry), cross-faded only from a ready image to a ready one (an item without art, a failed image or one asked for mid-fade keeps the picture there is). A provider or feed the household has not got (`unsupported`, `not_found`, `unknown_ref`: `home/answer.h`) is no row and no error: one that cannot browse is not asked again, a missing one only every 10 minutes; a timeout, a 5xx, auth or the network keeps its inline error row. The feed puts the focused row's top under the hero's details (`home/feed.h`), rows above step aside. Opening a title is the page cross-fade alone. A film sent behind the page (BACK in the player) plays on under it: the hero offers «Продолжить», and BACK or that brings the player back (`Router::background`, `foreground`). Background audio (a local track or a cast) instead gets a focused cover chip in the top bar with its title, artist and pause/resume state; OK brings the audio page back, the remote's play/pause key reaches either kind of background session, and the entry goes when playback ends. What plays in this room on the household's other devices (devices.media, the system layer's feed) is a strip under the hero (`home/nearby-strip.h`, at most four, faded with the hero while focus is in the feed), and the top bar's devices indicator counts every room's; both open the quick panel |
| `core/router`'s `Layer` | what sits over the routes with documents of its own (`Ui::addDocument`): made at attach, sees each message before the routes, adds to the status and the context; `Router::refresh` builds the top route's page again with focus kept |
| `system/`, `system.cmake`, `system.cpp` | i3 (design r2 §16-17, §20-24): `SystemLayer` (the assistant drawer as a chat while it stays open: `chat.h` orders the rounds and ages them, `chat-view.h` moves each item by transform and opacity alone; the tasks indicator (`tasks_summary`'s counts) and panels, `devices.h` (devices.media's feed, parsed by the plain `nearby.h`, for home's strip of this room and the top bar's indicator of all rooms; the quick panel of devices.panel's cards, the tasks panel's sheet with `card-sheet.h`'s shared parts, opened from either or by route `DEVICES`), the microphone choice (`MicChoice`, after each Ready and from room and microphone), settings written with `settings.set` and shown from display.screen's config, airplay's pin and mirroring badge; the press paths G, long BACK, home's «Ассистент», the mic button, «Ассистент» in an actions sheet), the routes `ENROLL` (the claim and its fallbacks) and `SETTINGS` (with `data.section: room`, room and microphone); `cadence-system-preview` (ctest `system-gallery`, golden images in `system/golden`) and `system.test` |
| `system/alarms*`, `system/alarm-ring`, `home/alarms-strip` | the household's alarms (screen/v1 `alarms_summary`, the server's AlarmsSummary): the ring overlay over everything for the first ring the screen may show (its own always, and app.cpp lights the display for it; another device's only while the display is on), «Стоп» focused and «Ещё N мин», BACK leaves it; home's strip of the next alarm and the running timers ticking; route `ALARMS` (ref: a device to focus) listing each device's items, a timer or reminder cancelled behind a confirm, an alarm never (rule 7). stop, snooze and cancel are rpc `alarms.act`, a refusal a toast; `alarms-model.test` (also `sim-alarms-model`) |
| `playback/`, `player/`, `audio/`, `panels/`, `queue/`, `ambient/` | i2's routes (design r2 §12-15, §19; `playback.cmake`): `PlaybackRoutes` is the service (the item, idle, the tap's feed) and registers `PLAYER`, `AUDIO` and `AMBIENT`; panels, chapters, the queue and the voiceover switcher are their side sheets |
| `preview/` | `cadence-browse-preview` (ctest `browse-gallery`): every route on a fake catalog with painted artwork, golden images in `preview/golden`, as `cadence-ui-preview` |
| `tests/` | `browse.test`: the §18.7 focus walk by the d-pad, the picker skipped, resolving's BACK and failure, episodes, home's providers and their refresh, the screen context, streamed search over encoded frames, the image cache (its failures logged by code), its http fetch and its Accept |

## Screen context per route

The router adds `focused_ref` (the focused element's or its ancestor's `data-ref`) and `visible_refs` (on-screen
`.has-ref` elements in document order, at most 24). Refs only, never titles.

| Route | `page_ref` | `selection` | `query` |
|---|---|---|---|
| home | | | |
| enroll, settings | | | |
| tasks, task (i3's panel over any route) | the task's ref on `task` | | |
| devices (i3's quick panel over any route) | the device it was opened on | | |
| any, with the drawer open | `drawer_open` (i3's layer) | | |
| search | | | the query |
| title | the title's ref | source (the selected variant's), voiceover and quality the next play takes, episode the primary button plays | |
| episodes | the variant | episode: the focused one | |
| picker | the variant | voiceover and quality of the focused chip (a torrent's id as source), episode | |
| resolving | the variant | the choices resolving went with, episode | |
| player, audio | the item's ref | episode, the voiceover playing | |

The player and audio mode also fill `player`: the item's ref, the position when the context was taken (a seek or
any other change sends it again, never the clock alone) and the tracks; with a sheet open the route is its own
(`QUEUE`, `PANEL`, `PICKER` for the voiceover switcher).

## Playing

The screen never plays what it resolved itself: resolving calls rpc `media.play {ref, episode?, choices?, from?}`, the
server resolves it and sends the device `media.video/play`, and cadenced's `MediaPlay` (its session id, which the
answer names too) starts the player, so the session is cadenced's like any other. Resolving hands the screen to a
`PLAYER` route once the player reports that session (stream I2), else steps back; BACK cancels the call on the server.

## Playback (i2)

- A content `MediaPlay` (cadenced's, or the ui's own that the app echoes back) starts the item: the player, or audio
  mode for a track or podcast, over what shows; with resolving on top, resolving replaces itself once the play starts.
- The osd reads the remote first (§12's table): OK and ← → act with nothing shown, auto-hide after 5 s while playing.
  Subtitles are drawn from mpv's `sub-text` (mpv's own are off on every path). Skips, up next with the same voiceover
  (`catalog.options` for the next episode, then `catalog.resolve`), and a voiceover or quality switch keeping the
  position go to the server as `media.play` with `replaces` (the playing session) and `reason` (`continue`, `switch`,
  `user`); the new session's `MediaPlay` starts it.
- The server's own fields of a session (screen/v1 `media_session_state`: transport, item actions, up next, queue
  position, panels) merge into it by session id, each one replacing the last whole, with focus kept by element id;
  fields that come before their play wait for it. Once they came, `catalog.item` only adds episodes. Next and previous
  are `media.control` when the session lists them, a server item (up next, the queue's) is `media.control`'s `jump`,
  the queue's play next and remove are its `play_next` and `remove`; item actions flip optimistically and go as
  `media.item_action` (`catalog.act` for a session without the server's fields), panels as `media.panel`
  (`catalog.panel` likewise); the queue's page is `media.queue`.
- Idle opens `AMBIENT` after `idleAfterS` with nothing playing; the first key only wakes (swallowed, down and up), a
  turn, a toast, a cast or a pin yield at once. Aerial clips play muted through the player; over a paused item no clip
  plays (one player). Images draw with ken burns under the layer.
- While i3's drawer is open (`UiStatus.drawerOpen`) the osd hides and the player reads no key; a focused toast keeps
  the remote too. The playback service's key filter goes through `Ui::addKeyFilter`; a page passing a key on calls
  `Ui::key` again, which the service lets through.
- `cadence-playback-preview` (ctest `playback-gallery`), `playback.test`: the osd's walk and keys, skips, up next,
  the switcher, item actions, sheets, audio's lean-back, idle, the context; the session parsing, bars, drift, idle.
