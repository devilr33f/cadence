// Package rpc names the server methods a device calls over the link's RpcRequest (D5: json args and results), and the
// status tokens of RpcResponse. A method's args and result types live with the server package that serves it.
package rpc

// Methods. A subscribed one (RpcRequest.subscribe) streams RpcEvent frames until the last.
const (
	// CatalogSearch {query, providers?} streams results per provider and per source, failures included.
	CatalogSearch = "catalog.search"
	// CatalogBrowse {path, cursor?}: a feed or a tree level.
	CatalogBrowse = "catalog.browse"
	// CatalogItem {ref}: children, variants, panels, actions.
	CatalogItem = "catalog.item"
	// CatalogOptions {ref, episode?}: the choice groups (variant, voiceover, quality, audio language, subtitles).
	CatalogOptions = "catalog.options"
	// CatalogResolve {ref, episode?, choices}: a playable (stream, handoff or match).
	CatalogResolve = "catalog.resolve"
	// CatalogAct {ref, action}: like, save, subscribe, my list, watched.
	CatalogAct = "catalog.act"
	// CatalogPanel {ref, panel, cursor?}: a page of an item's panel (description, comments, related) as cadence cards.
	CatalogPanel = "catalog.panel"
	// CatalogAlternatives {ref}: the same title on the other providers and sources (by its Shikimori or MyAnimeList
	// id, else its names) {items: [{ref, provider, source?, sourceName, title, match, episodesAired?, episodesTotal?,
	// voiceovers: [{label, kind?, quality?}], quality?, current?, unavailable?}], asked, failed}.
	CatalogAlternatives = "catalog.alternatives"
	// MediaQueue {sessionId, cursor?}: a page of the whole queue {items: [{ref, title, subtitle?, durationMs?,
	// artworkUrl?}], position?, next?}, each item's ref playable with media.control's jump; the up next a session
	// state carries is its first 20.
	MediaQueue = "media.queue"
	// MediaPlay {ref, episode?, choices?, from?, startMs?, replaces?, reason?}: a screen plays an item it chose (a
	// catalog playable; the choices are catalog.options' ids) as a session the server owns, as catalog_play does: the
	// server resolves it and sends the device media.video/play, so cadenced owns the session (its metadata, its
	// transport, its fields in media_session_state). replaces names the session it follows (a voiceover switch at
	// startMs, the next episode's auto-continue), and reason is user (default), switch or continue. Answers
	// {sessionId, episode?}, or the catalog's refusals.
	MediaPlay = "media.play"
	// MediaControl {sessionId, action, ref?}: the server's transport of a session it owns: next, previous, jump (to
	// ref, an up next or queue item), play_next (ref: put it after the playing one) or remove (ref). The player's own
	// transport (pause, seek) stays with the screen; an action the session state's actions do not list is invalid.
	MediaControl = "media.control"
	// MediaItemAction {sessionId, actionId}: run one of the session's item actions (like, save, subscribe); the new
	// toggled state comes with the next media_session_state.
	MediaItemAction = "media.item_action"
	// MediaVideo {sessionId, quality?, audio?, view?}: switch the video the server plays as sessionId (a link sent from
	// the dashboard, media.video/play's renditions, audioChoices and views) to another rendition (by name), audio
	// language (by id) or view, from where it plays. Answers {} once the new play is sent; not_found for another
	// session, invalid for a choice it does not hold.
	MediaVideo = "media.video"
	// MediaPanel {sessionId, panelId, cursor?}: a page of cadence cards.
	MediaPanel = "media.panel"
	// CardAction {cardId, revision, actionId, value?}, or {pageId, actionId} for a page's next: runs an issued card
	// action without the model and answers a typed CardActionResult (RpcResponse.card_action); subscribed, it pushes
	// the card's next revisions while it stays on screen.
	CardAction = "card.action"
	// CardsWatch {cardIds}: subscribed, pushes new revisions of cards on screen (live state) until cancelled.
	CardsWatch = "cards.watch"
	// CardsLease {leaseId, cardIds}: this screen keeps the live cards it shows after a turn's Home Assistant write
	// following their entity's state (Cards frames, context_id "live", at most 2 a second, a few minutes past the
	// write). Acquire and renew are the same call, sent as a heartbeat every heartbeatMs; a lease not renewed
	// within ttlMs lapses. Answers {following, ttlMs, heartbeatMs}; idempotent, so a screen back after a link drop
	// acquires again by its next heartbeat.
	CardsLease = "cards.lease"
	// CardsRelease {leaseId} or {all: true}: the panel closed, or (all, from cadenced) the screen's link dropped.
	CardsRelease = "cards.release"
	// TasksList {}: a page of task cards; subscribed, pushes revisions.
	TasksList = "tasks.list"
	// TasksItem {taskId}: a task panel; subscribed, pushes revisions.
	TasksItem = "tasks.item"
	// AmbientNext {kind}: the next clip or image {url, captions, category, duration}.
	AmbientNext = "ambient.next"
	// AssistantInvoke {}: a press on this screen; the server opens the room's speaker microphone (MicOpen).
	AssistantInvoke = "assistant.invoke"
	// AssistantStop {turnId}: stop listening now, or stop speaking; answers {} (the turn's next state says the rest).
	AssistantStop = "assistant.stop"
	// AssistantReset {}: reset the conversation this screen's room uses; the next AssistantTurn clears its chat.
	AssistantReset = "assistant.reset"
	// ToastAction {toastId, actionId}: a toast's RPC action (screen/v1 ToastAction kind RPC); answers {toast?}.
	ToastAction = "toast.action"
	// DevicesMedia {}: subscribed, what the household's other devices play, for a screen's home strip and its top
	// bar's indicator: {items: [{id, deviceId, name, room?, sameRoom, source, state, title?, artist?, art?, positionMs?,
	// positionAtMs?, durationMs?, here}]}, the whole list again on every change. A group is one item (deviceId its
	// leader's, name its own); the caller's own session is never an item, a group it is in is one with here.
	DevicesMedia = "devices.media"
	// DevicesPanel {focus?}: subscribed, the quick panel's page of cadence cards, one card per device (or group) with a
	// session, the caller's room first: its cover and title, the transport its session lists, a volume slider per
	// device and «play here too» (this screen joins its group) or «stop here» (it leaves); focus names the device
	// whose card the screen focuses. The cards' actions run through card.action; a volume moves only by such a press
	// (rule 7).
	DevicesPanel = "devices.panel"
	// SettingsSet {...}: a screen's settings page writes the device's own settings (capschema.SettingsSetArgs: the
	// language, the follow-up window, and display.screen's subtitles, overscan, autoSkipOpenings and reducedMotion).
	// The server validates them, keeps language and the window as the device's setting overrides, and language and
	// the screen keys in its desired display.screen config, which the device sees through config sync (cadenced hands
	// the screen the language in Ready); answers {applied: [keys]}, or invalid not_a_screen from a device without one.
	SettingsSet = "settings.set"
	// AlarmsAct {deviceId, kind, id, action, minutes?}: a press on a ring's overlay or the alarms page. action stop
	// (stop_ring) or snooze (snooze, minutes or the device's own) ends or puts off a ring set in a woken session, which
	// a screen may do for any device (owner, 2026-11-16); cancel takes a timer or a reminder off its device. never sets
	// an alarm (rule 7). answers {} or refused {code}.
	AlarmsAct = "alarms.act"
)

// Statuses of RpcResponse.
const (
	StatusOK            = "ok"
	StatusInvalid       = "invalid"
	StatusUnknownMethod = "unknown_method"
	StatusNotFound      = "not_found"
	StatusRefused       = "refused"
	StatusExpired       = "expired"
	StatusUnavailable   = "unavailable"
	StatusBusy          = "busy"
	StatusInternal      = "internal"
)
