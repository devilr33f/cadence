---
name: music
description: Music on a speaker or a TV: play a song, artist, album, playlist, station or favorites, rate songs and control playback.
title.ru: Музыка
title.en: Music
tools: music_search, music_play, music_feedback, music_favorites, media_control
triggers: саундтрек, саундтреки, soundtrack, музыка, музыку, песня, песню, трек, альбом, плейлист, исполнитель, группа, радио, волну, моя волна, вайб, любимые, избранное, лайк, дизлайк, нравится, не нравится, послушать, поставь, включи песню, следующая, следующий трек, предыдущая, пауза, продолжи, перемешай, повтори, music, song, track, album, playlist, artist, radio, my wave, my vibe, favorites, liked songs, like this, dislike, resume, shuffle
---
- Never invent a music id: music_search first, then music_play with the id of the best result.
- Every device whose line lists music plays it, TVs included ("на телевизоре": music_play with device naming the TV; it shows the song on its audio page). Never tell the user a device cannot play music without calling music_play: only its answer says so.
- Search names in their original language and script ("Кино Группа крови", "Linkin Park Numb"); fix misheard artist names to the likely real one.
- "Включи <artist>" with no song: kind artist (its radio), only when the user named a performer. "Альбом <name>": kind album. "Что-нибудь спокойное": a playlist search in the user's words.
- "Моя волна", "my wave" or "my vibe": music_search kind station with source yandex, then music_play; it needs the Yandex account token or cookies and continues as that personal station.
- "Мои любимые", "my favorites" or "my liked songs": music_favorites, then music_play with its Cadence collection id to play or queue them all.
- "Нравится", "like this", "не нравится" or "dislike this": music_feedback with id current; for an earlier search result use its rN id, never a provider id or URL. like also adds a Cadence favorite, dislike removes it; providerSync tells whether the provider accepted it.
- A provider that fails or hangs does not invalidate other providers' search results; use a matching result that did arrive.
- Music from a game, film, series or anime ("музыку из Escape from Tarkov", "саундтрек Интерстеллара"): search "<title> soundtrack" or "<title> OST" as a playlist or album (or songs), never as an artist.
- Results that do not match the request (unrelated artists or titles) are no match: search once more, broader (another kind, or soundtrack/OST added), before saying nothing was found, and never offer an unrelated result as what was found.
- An id from an earlier turn ("включи первый") may be gone: music_play finds it again itself; if it still answers unknown_result, call music_search again with the same request and play its best result in the same turn, never give up or ask.
- Prefer the original studio version unless the user asked for a live, cover or remix.
- Pause, resume, next, previous, shuffle and repeat go to media_control; the volume never does (device_volume does).
- If music_play answers `pending: true`, the device accepted the play and is still resolving it. `startingOn` names the device. Say it is starting there ("Включаю на Bedroom TV…"), never that it played or failed, and do not retry. The final outcome continues onto the live card and into the conversation history, not as an automatic spoken reply.
- After an immediate (not pending) play or a control succeeds, say nothing; if nothing was found, say so in a few words.
