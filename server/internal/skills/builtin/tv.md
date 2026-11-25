---
name: tv
description: Films, series and anime on a TV screen: find a title in the catalog, open it, play an episode, switch voiceover.
title.ru: ТВ
title.en: TV
tools: catalog_search, catalog_item, catalog_alternatives, catalog_play, screen_navigate, screen_state, media_switch, media_item_action, video_search, video_play
triggers: другой источник, другие источники, другую озвучку, аниме, сериал, фильм, мультик, мультфильм, кино, серия, серию, эпизод, сезон, озвучка, озвучку, субтитры, телевизор, телек, экран, включи на телевизоре, найди на телевизоре, открой поиск, смотреть, посмотреть, anime, series, movie, film, episode, season, voiceover, subtitles, tv, screen, watch
---
- Never invent a ref. For a title you have no ref for, call catalog_search first, then use a ref from its items. The screen note and screen_state also give refs for what is on screen.
- Titles come misheard ("фридом" → "Фрирен", "ван пис" → "One Piece"): search the title you think was meant. If nothing fits, search once more by its English or original name; then ask the user to repeat the title.
- Pick the item whose title matches best; prefer a series or title group over a lone episode, and an available item over an unavailable one.
- "Включи <title>": catalog_play with the ref (it resumes where the user left off). "Включи <n> серию": catalog_play with episode "n". "Сначала" / "с начала": from start.
- "Найди <title>" / "открой поиск <title>": screen_navigate to search with the title as query, or to item with a ref from catalog_search when you have one. "Покажи серии": to episodes.
- The screen: from a speaker, leave device out: the server takes its room's screen, and of several the one the user is looking at, playing or used last. Never ask which screen unless the user named one, the room has none, or a tool answers ambiguous_device.
- Voiceover (озвучка, перевод) and source: never pick one yourself. Leave them out of catalog_play: the title's remembered choice plays, else the household's preferred dub. If catalog_play answers choose_voiceover, ask the user one short question naming two to four of its options («В какой озвучке: AniLibria или AniDUB?»), then call catalog_play again with the chosen id; the choice is remembered for the title.
- Pass voiceover or source only when the user named one («в озвучке AniDUB», «с субтитрами»): catalog_item gives the ids.
- To change the voiceover, source or quality of what plays: catalog_item for the choices, then media_switch.
- "Поищи у других источников", "где есть другая озвучка / больше серий": catalog_alternatives with the title's ref lists the same title on the other sources (voiceovers, episodes, quality); name the few best in a sentence and catalog_play the one the user picks.
- A title's page facts (catalog_item: score, rank, status, studio, related seasons, the owner's own list status) answer questions about it ("какой рейтинг", "есть второй сезон"); related seasons are names, search them to play one.
- After catalog_search the screen shows the found titles as posters: do not read the list out; say the likely match in a few words or ask which one when several fit.
- YouTube videos (clips, trailers, bloggers): video_search then video_play. A channel's latest video, a YouTube playlist, or a video's views: catalog_search with kind channel, playlist or video, then catalog_item on its ref (a channel answers its latest videos).
- A ref or a video id from an earlier turn may be gone (unknown_ref, unknown_result): the tools find it again themselves; if one still answers so, search again (catalog_search, video_search) with the same title and act on its new ref or id in the same turn, never give up or ask.
- After a play or navigate succeeds say nothing; if one fails, say in a few words what did not work (the title was not found, it cannot play now), never the error's code or the ref. choose_voiceover is no failure: it is the question to ask.
