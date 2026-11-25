// Package catalog is one API for every content provider (TV plan W0-C item 5,
// content findings section 0): torrplayer, TorrServer, YouTube Music (with
// YouTube videos) and Spotify implement the same Provider, each the subset its
// Capabilities name. The catalog service (wave 1, stream D) fans requests out
// to the configured providers, issues opaque refs for what they answer, and
// serves the TV (RPC catalog.search|browse|item|options|resolve|act), the
// dashboard (/api/v1/catalog/...) and the agent's catalog_search,
// catalog_item and catalog_play tools from the same calls.
//
// # Two levels: providers and sources
//
// A provider is one of the four. A source is a sub-provider inside one:
// torrplayer's AniLiberty, anime365, AnimeGo, YummyAnime, AnimeVost,
// Sameband, Nyaa, AnimeBytes and Torznab endpoints. Every item carries its
// provider and, where it has one, its source. Search streams a batch per
// provider and per source as each answers, failures included.
//
// # The tree
//
// title_group (one title merged across sources by its Shikimori or MAL id,
// see GroupKey) → variants (one series or movie per source: episode counts,
// voiceovers) → episodes (fractional ordinals, Label is the key) → choice
// groups for one episode (source, voiceover, quality, torrent) → Resolve
// (a stream URL or a torrent's file, external subtitles, skips, resume).
// TorrServer's tree is flat: torrent → files → Resolve. Music: album,
// playlist or artist → tracks → Resolve (stream, or handoff or match for
// Spotify).
//
// # Refs and keys
//
// A provider names things by Key, its own identity, and never sees a Ref.
// The service swaps every Key for a Ref (RefIssuer) before anything leaves the
// server; devices, the model and the dashboard echo refs back and the service
// looks the Key up again. A ref the server did not issue to the caller's
// household resolves to nothing (ErrUnknownRef), so the agent's catalog tools
// act only on what this server handed out, never on a name or URL a model
// made up.
//
// # torrplayer's REST API onto this contract (/api/v1, a bearer token)
//
//   - Search: GET /search?q= queries every enabled torrplayer source at once
//     and concatenates the titles in source order (not merged); its errors
//     name the sources that failed, page and has_more page it, unenforced
//     lists the filters some sources ignored. The provider emits one
//     SearchBatch per source (Failure set for the failed ones, Unenforced
//     copied) and the service merges titles into title groups by GroupKey.
//     Browse "catalogue" is the Shikimori-backed filter browse.
//   - Item on a variant (series or movie of one source): GET
//     /providers/{source}/titles/{id}/episodes → Children of kind episode,
//     Label "5.5" as the key, Ordinal for order, watched and resume_sec as
//     Progress; NextUnwatched → ItemDetail.Continue.
//   - Options for an episode: GET /providers/{source}/titles/{id}/sources?
//     episode=<label> → the voiceover, quality and torrent groups (a stream's
//     translator, trans_type voice|sub|raw, lang, quality; a torrent's
//     episode range, size_bytes, seeders, leechers, quality, codec, hardsub);
//     hidden_count → ChoiceGroup.Hidden. The source group itself lists the
//     title group's variants.
//   - Resolve: POST /resolve {provider, title_id, source_id, episode,
//     quality} → MediaOut{kind, url, headers, subtitles, skips, info_hash,
//     index} → Playable of kind stream. Its URLs are already behind
//     torrplayer's proxy (/play/{hash}/{index} for a torrent, /resolve/{id}
//     for a stream needing a Referer), reachable from whoever asked, so the
//     server must call torrplayer at an address the TV reaches too, and a
//     stream's headers are empty. A torrent waiting for peers reports
//     StageConnectingPeers; one with none fails ErrUnplayable.
//   - Remembered choices: torrplayer's sticky per-title prefs (provider
//     source, translator, quality; PUT /progress/{provider}/{title}/prefs)
//     are the provider's ChoiceMemory, so the TUI, the Kodi add-on and the
//     TV agree; SelectSource applies them when a Selection leaves a group
//     out.
//   - Report: POST /progress/{provider}/{title}/report, and PUT
//     .../watched for Act(ActionWatched).
//   - Act my_list: torrplayer's /mylist (Shikimori-synced statuses);
//     subscribe: its subscriptions.
//
// # Today's music contracts onto this one
//
// providers.Music.SearchMusic and providers.Collections.SearchCollections are
// Search with kinds track, album, playlist, artist; CollectionSongs is Item's
// Children; providers.Player.Resolve is Resolve of a track or video. A
// Spotify track with no stream here resolves as a match (the music service's
// YouTube Music match, music/match.go) or, on a device with Spotify Connect,
// a handoff. music_search and music_play become catalog_search and
// catalog_play.
//
// # Privacy (rule 8, D11)
//
// Titles, descriptions, comments and every other provider text are
// third-party: untrusted output for the model, never a log. Stream and
// subtitle URLs and headers are signed or secret: never logged, never stored,
// never shown to the model. Keys, refs and queries never reach a log either;
// a log line names a provider, a source and a code.
package catalog
