#pragma once

#include "cadence/screen/v1/media.pb.h"
#include "cadence/v2/media.pb.h"
#include "player/player.h"

#include <cstdint>
#include <optional>
#include <string>
#include <vector>

// the playing item as the playback routes show it: what media.video/play carried (metadata, skips, renditions, the
// server's transport actions) and what the catalog adds on request (catalog.item: item actions, panels, the
// episodes that make up next). memory-only, never logged (D11): titles, refs and labels are third-party or private
namespace cadence::screen::routes {

enum class Mode { Video, Audio };

struct Chapter {
  std::uint64_t startMs = 0;
  std::string title;
};

enum class SkipKind { Opening, Ending, Recap, Preview };
struct Skip {
  SkipKind kind = SkipKind::Opening;
  std::uint64_t startMs = 0;
  std::uint64_t endMs = 0;
};

enum class ActionKind { Like, Dislike, Save, Subscribe, Queue, Custom };
struct ItemAction {
  std::string id;
  ActionKind kind = ActionKind::Custom;
  std::string label;
  std::string icon;
  bool toggled = false;
  bool enabled = true;
};

struct Panel {
  std::string id;
  // description, comments, channel, lyrics, chapters, related, custom
  std::string kind;
  std::string title;
  std::string countLabel;
};

struct QueueItem {
  std::string ref;
  std::string title;
  std::string subtitle;
  std::uint64_t durationMs = 0;
  // an episode's label, when the item is one, and its own name where the catalog has one
  std::string episode;
  std::string episodeName;
  // the art's texture source ("gen:art:<seed>" in the gallery; empty draws the art's fill)
  std::string art;
  // one of the server's own items (up next, its queue): played with media.control's jump by ref, never resolved here
  bool server = false;
};

struct AudioQueue {
  std::string name;
  std::string art;
  int total = 0;
  std::size_t position = 0;
  std::vector<QueueItem> items;
};

// media.queue {all:true}: the collection and its whole play order (at most 200), position the current item
std::optional<AudioQueue> audioQueueFrom(const std::string& json);

// how a server item shows (screen/v1 MediaView): the picture, or the audio page with the visualizer off or on
enum class View { Unspecified, Video, Audio, Visualizer };

// one audio language the server holds for the item (youtube's original and dubbings); picked with rpc media.video
struct AudioChoice {
  std::string id;
  std::string language;
  std::string label;
  bool original = false;
  bool dubbed = false;
  bool selected = false;
};

struct Session {
  std::string sessionId;
  std::string itemRef;
  Mode mode = Mode::Video;
  cadence_v2_MediaKind kind = cadence_v2_MediaKind_MEDIA_KIND_UNSPECIFIED;
  std::string title, artist, album, series, episode;
  // the episode's own name, when the server gave one
  std::string episodeName;
  std::uint32_t season = 0;
  std::string providerId, providerName, providerSource;
  std::string channelName, channelRef;
  std::vector<std::string> badges;
  bool explicitContent = false;
  bool live = false;
  std::string statsLabel;
  std::string art;
  // two dominant colours of the artwork, 0xRRGGBB (audio mode's background); none from the server yet
  std::uint32_t colorA = 0x2A1E3A, colorB = 0x14161E;
  std::vector<Chapter> chapters;
  std::vector<Skip> skips;
  // the server's transport: next, previous
  std::vector<std::string> actions;
  std::vector<player::Rendition> renditions;
  // a server item's view and the views it switches between, its audio languages (rpc media.video switches them)
  View view = View::Unspecified;
  std::vector<View> views;
  std::vector<AudioChoice> audioChoices;
  cadence_v2_PlayableKind playable = cadence_v2_PlayableKind_PLAYABLE_KIND_STREAM;
  // a handoff's target ("Колонка «Кухня»") and a match's provider
  std::string handoffTarget, matchedFrom;
  // the request that started it (a voiceover switch keeps its headers and subtitles out of it; never logged)
  player::PlayRequest request;
  // a session audiod renders (NowPlaying: an airplay cast, spotify connect), not the screen's own player: its
  // transport goes to cadenced (NowPlayingCommand), and the sending device's name is known when it gave one
  bool device = false;
  std::string sender;
  // a follower's play (multiroom P4): its picture follows the group's stream, and its pause, resume and seek go to
  // the group's session (groupSession, NowPlaying of source GROUP) as NowPlayingCommand; a stop is its own (it leaves)
  bool follow = false;
  std::string groupSession;
  // the cover's local file (NowPlaying.artwork_path), whose image id is art
  std::string artPath;

  // from catalog.item
  std::vector<ItemAction> itemActions;
  std::vector<Panel> panels;
  std::vector<QueueItem> episodes;
  // the current voiceover's or source's name (torrplayer), for the chip and auto-continue
  std::string voiceover;
  // what the next item is, once known
  std::optional<QueueItem> upNext;
  bool detailLoaded = false;

  // the server's own fields (screen/v1 media_session_state): once one came, its transport, item actions, up next and
  // panels are the server's, and catalog.item no longer fills them
  bool serverFields = false;
  std::vector<QueueItem> serverUpNext;
  std::optional<std::uint32_t> queuePosition;

  [[nodiscard]] bool has(const std::string& action) const;
  [[nodiscard]] bool torrplayer() const { return providerId == "torrplayer"; }
  // a video the server plays from a link (no catalog item): its choices are switched with rpc media.video
  [[nodiscard]] bool serverVideo() const { return itemRef.empty() && (!renditions.empty() || !views.empty() || !audioChoices.empty()); }
};

// a session from media.video/play
Session sessionFrom(const cadence_screen_v1_MediaPlay& play, const player::PlayRequest& request);
// a session audiod renders, from cadenced's NowPlaying: audio mode, its provider by its source (airplay, spotify),
// its art the image id of its cover file (coverId), its actions as media.player names them
Session sessionFrom(const cadence_screen_v1_NowPlaying& now);
// the image id a cover file goes by: 24 of its sha256's hex digits (the image cache's id pattern); empty for none
std::string coverId(const std::string& sha256);

// the server's fields of a session it owns (media_session_state for this session id): actions, item actions, up next
// (the first of it is the session's upNext), the queue position and panels, each replacing what was there whole
void applyServerState(Session& s, const cadence_v2_MediaSessionState& state);

// catalog.item's DetailView into the session: item actions, panels, the episodes and the one after this one, and what
// the play left out of the item (its title, channel, provider and art)
void applyDetail(Session& s, const std::string& json);
// catalog.act's answer {actions}: the new toggled states
void applyActions(Session& s, const std::string& json);

// one row of the voiceover switcher (design §18.6): this source's other voiceovers first, then other sources
struct SwitchChoice {
  // the choice group (voiceover, source) and the choice's id within it
  std::string group;
  std::string id;
  std::string label;
  std::string detail;
  bool current = false;
  // this episode exists there
  bool hasEpisode = true;
};
// catalog.options' OptionsView, with what is selected now
std::vector<SwitchChoice> switchChoices(const std::string& json);
// the selection to resolve with: the options' selected, the one group changed
std::string selectionJson(const std::string& optionsJson, const std::string& group, const std::string& id);

// a resolve's PlayableView as the player's request: the stream's url (the first rendition when there is one), its
// audio, subtitles and skips, starting at startMs; nullopt for anything but a stream with a url
std::optional<player::PlayRequest> requestFromPlayable(const std::string& json, const std::string& sessionId,
                                                      std::uint64_t startMs, std::vector<Skip>* skips);

// media.queue's page as the queue sheet takes it: the server's {items, position, next} (the items after the playing
// one, next the cursor), or an older {history, current, next, cursor}
struct QueuePage {
  std::vector<QueueItem> history;
  std::optional<QueueItem> current;
  std::vector<QueueItem> next;
  std::string cursor;
};
std::optional<QueuePage> queueFrom(const std::string& json);

// the provider badge's two letters: tp, TS, YT, YM, SP; else the id's first two
std::string monogram(const std::string& providerId);
// a provider token's display name, for a play that named only its token; "" for one the screen does not know
std::string providerName(const std::string& providerId);
// "1:47:30", "41:12", "0:07"
std::string clockTime(std::uint64_t ms);
// the chapter that holds positionMs, or -1
int chapterAt(const std::vector<Chapter>& chapters, std::uint64_t positionMs);
// the skippable segment playing at positionMs, if any
const Skip* skipAt(const std::vector<Skip>& skips, std::uint64_t positionMs);
const char* skipKey(SkipKind kind);

}  // namespace cadence::screen::routes
