#include "app/routes/playback/session.h"

#include "ui/images/image-cache.h"

#include <nlohmann/json.hpp>

#include <algorithm>
#include <cstdio>

namespace cadence::screen::routes {

namespace {

using nlohmann::json;

std::string str(const char* s) { return s != nullptr ? s : ""; }

std::string text(const json& j, const char* key) {
  const auto it = j.find(key);
  return it != j.end() && it->is_string() ? it->get<std::string>() : std::string();
}

std::uint64_t ms(const json& j, const char* key) {
  const auto it = j.find(key);
  if (it == j.end() || !it->is_number()) return 0;
  const double v = it->get<double>();
  return v > 0 ? static_cast<std::uint64_t>(v) : 0;
}

ActionKind actionKind(const std::string& k) {
  if (k == "like") return ActionKind::Like;
  if (k == "dislike") return ActionKind::Dislike;
  if (k == "save" || k == "my_list") return ActionKind::Save;
  if (k == "subscribe") return ActionKind::Subscribe;
  if (k == "add_to_queue") return ActionKind::Queue;
  return ActionKind::Custom;
}

SkipKind skipKind(cadence_screen_v1_SkipKind k) {
  switch (k) {
    case cadence_screen_v1_SkipKind_SKIP_KIND_ENDING:
      return SkipKind::Ending;
    case cadence_screen_v1_SkipKind_SKIP_KIND_RECAP:
      return SkipKind::Recap;
    case cadence_screen_v1_SkipKind_SKIP_KIND_PREVIEW:
      return SkipKind::Preview;
    default:
      return SkipKind::Opening;
  }
}

std::optional<SkipKind> skipKind(const std::string& k) {
  if (k == "opening") return SkipKind::Opening;
  if (k == "ending") return SkipKind::Ending;
  if (k == "recap") return SkipKind::Recap;
  if (k == "preview") return SkipKind::Preview;
  return std::nullopt;
}

std::string imageIdOf(const std::string& url);

QueueItem queueItem(const json& j) {
  QueueItem q;
  q.ref = text(j, "ref");
  q.title = text(j, "title");
  q.subtitle = text(j, "subtitle");
  q.durationMs = ms(j, "durationMs");
  if (q.durationMs == 0) q.durationMs = ms(j, "durationS") * 1000;
  if (const auto e = j.find("episode"); e != j.end() && e->is_object()) {
    q.episode = text(*e, "label");
    q.episodeName = text(*e, "name");
  }
  if (const auto e = j.find("episode"); e != j.end() && e->is_string()) q.episode = e->get<std::string>();
  const auto sha = text(j, "artworkSha256");
  if (!sha.empty()) {
    q.art = "gen:art:" + sha.substr(0, 8);
  } else if (const auto a = j.find("artwork"); a != j.end() && a->is_object()) {
    // a catalog item's art: the image proxy's id
    if (const auto id = text(*a, "hash"); ui::images::validId(id)) q.art = "img:" + id;
  } else if (const auto id = imageIdOf(text(j, "artworkUrl")); !id.empty()) {
    q.art = "img:" + id;
  }
  return q;
}

// the image proxy's id in a play's artwork url (<origin>/v2/images/<id>): the screen fetches art from its proxy alone
std::string imageIdOf(const std::string& url) {
  constexpr std::string_view kPath = "/v2/images/";
  const auto at = url.rfind(kPath);
  if (at == std::string::npos || !url.starts_with("http")) return {};
  const auto id = url.substr(at + kPath.size());
  return ui::images::validId(id) ? id : std::string();
}

json parse(const std::string& s) {
  auto j = json::parse(s, nullptr, false);
  return j.is_discarded() ? json() : j;
}

}  // namespace

bool Session::has(const std::string& action) const {
  return std::find(actions.begin(), actions.end(), action) != actions.end();
}

Session sessionFrom(const cadence_screen_v1_MediaPlay& p, const player::PlayRequest& request) {
  Session s;
  s.sessionId = p.session_id;
  s.request = request;
  s.follow = !request.followGroupId.empty();
  s.live = p.live;
  s.playable = p.playable == cadence_v2_PlayableKind_PLAYABLE_KIND_UNSPECIFIED ? cadence_v2_PlayableKind_PLAYABLE_KIND_STREAM
                                                                              : p.playable;
  for (pb_size_t i = 0; i < p.actions_count; ++i) s.actions.emplace_back(p.actions[i]);
  for (pb_size_t i = 0; i < p.skips_count; ++i) {
    if (p.skips[i].end_ms > p.skips[i].start_ms)
      s.skips.push_back(Skip{skipKind(p.skips[i].kind), p.skips[i].start_ms, p.skips[i].end_ms});
  }
  s.renditions = request.renditions;
  const auto viewOf = [](cadence_screen_v1_MediaView v) {
    switch (v) {
      case cadence_screen_v1_MediaView_MEDIA_VIEW_VIDEO: return View::Video;
      case cadence_screen_v1_MediaView_MEDIA_VIEW_AUDIO: return View::Audio;
      case cadence_screen_v1_MediaView_MEDIA_VIEW_VISUALIZER: return View::Visualizer;
      default: return View::Unspecified;
    }
  };
  s.view = viewOf(p.view);
  for (pb_size_t i = 0; i < p.views_count; ++i) {
    if (const auto v = viewOf(p.views[i]); v != View::Unspecified) s.views.push_back(v);
  }
  for (pb_size_t i = 0; i < p.audio_choices_count; ++i) {
    const auto& a = p.audio_choices[i];
    s.audioChoices.push_back(AudioChoice{a.id, a.language, a.label, a.original, a.dubbed, a.selected});
  }
  if (p.has_metadata) {
    const auto& m = p.metadata;
    s.title = str(m.title);
    s.artist = str(m.artist);
    s.album = str(m.album);
    s.series = str(m.series);
    s.season = m.season;
    s.episode = m.episode;
    s.episodeName = str(m.episode_name);
    s.itemRef = m.item_ref;
    s.kind = m.kind;
    if (m.artwork_sha256[0] != '\0') {
      s.art = "gen:art:" + std::string(m.artwork_sha256).substr(0, 8);
    } else if (const auto id = imageIdOf(str(m.artwork_url)); !id.empty()) {
      s.art = "img:" + id;
    }
    if (m.has_provider) {
      s.providerId = m.provider.id;
      s.providerName = str(m.provider.name);
      if (s.providerName.empty()) s.providerName = providerName(s.providerId);
      s.providerSource = str(m.provider.source);
    }
    if (m.has_channel) {
      s.channelName = str(m.channel.name);
      s.channelRef = m.channel.ref;
    }
    for (pb_size_t i = 0; i < m.badges_count; ++i) {
      if (m.badges[i].kind == cadence_v2_MediaBadgeKind_MEDIA_BADGE_KIND_EXPLICIT) s.explicitContent = true;
      if (m.badges[i].kind == cadence_v2_MediaBadgeKind_MEDIA_BADGE_KIND_LIVE) s.live = true;
      const auto label = str(m.badges[i].label);
      if (!label.empty() && m.badges[i].kind != cadence_v2_MediaBadgeKind_MEDIA_BADGE_KIND_EXPLICIT) s.badges.push_back(label);
    }
    for (pb_size_t i = 0; i < m.chapters_count; ++i) s.chapters.push_back(Chapter{m.chapters[i].start_ms, str(m.chapters[i].title)});
    std::sort(s.chapters.begin(), s.chapters.end(), [](const Chapter& a, const Chapter& b) { return a.startMs < b.startMs; });
    if (m.stats_count > 0) s.statsLabel = str(m.stats[0].label);
  }
  // audio mode for what has no picture (design §13): tracks and podcasts, and a server item's audio views
  s.mode = s.kind == cadence_v2_MediaKind_MEDIA_KIND_TRACK || s.kind == cadence_v2_MediaKind_MEDIA_KIND_PODCAST ? Mode::Audio
                                                                                                              : Mode::Video;
  if (s.view == View::Video) s.mode = Mode::Video;
  if (s.view == View::Audio || s.view == View::Visualizer) s.mode = Mode::Audio;
  return s;
}

std::string coverId(const std::string& sha256) {
  if (sha256.size() < 24) return {};
  for (const char c : sha256.substr(0, 24))
    if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) return {};
  return sha256.substr(0, 24);
}

Session sessionFrom(const cadence_screen_v1_NowPlaying& n) {
  Session s;
  s.sessionId = n.session_id;
  s.device = true;
  s.mode = Mode::Audio;
  s.kind = cadence_v2_MediaKind_MEDIA_KIND_TRACK;
  s.title = str(n.title);
  s.artist = str(n.artist);
  s.album = str(n.album);
  s.sender = str(n.sender);
  switch (n.source) {
    case cadence_v2_MediaSource_MEDIA_SOURCE_AIRPLAY:
      s.providerId = "airplay";
      break;
    case cadence_v2_MediaSource_MEDIA_SOURCE_SOLOIST:
      s.providerId = "spotify";
      break;
    default:
      // a url or a stream: live when it has no length
      s.live = n.duration_ms == 0;
      break;
  }
  s.providerName = providerName(s.providerId);
  for (pb_size_t i = 0; i < n.actions_count; ++i) {
    switch (n.actions[i]) {
      case cadence_screen_v1_MediaAction_MEDIA_ACTION_PAUSE: s.actions.emplace_back("pause"); break;
      case cadence_screen_v1_MediaAction_MEDIA_ACTION_RESUME: s.actions.emplace_back("resume"); break;
      case cadence_screen_v1_MediaAction_MEDIA_ACTION_STOP: s.actions.emplace_back("stop"); break;
      case cadence_screen_v1_MediaAction_MEDIA_ACTION_SEEK: s.actions.emplace_back("seek"); break;
      case cadence_screen_v1_MediaAction_MEDIA_ACTION_NEXT: s.actions.emplace_back("next"); break;
      case cadence_screen_v1_MediaAction_MEDIA_ACTION_PREVIOUS: s.actions.emplace_back("previous"); break;
      default: break;
    }
  }
  if (const auto id = coverId(n.artwork_sha256); !id.empty() && n.artwork_path[0] != '\0') {
    s.art = "img:" + id;
    s.artPath = n.artwork_path;
    if (n.color_a != 0 || n.color_b != 0) {
      s.colorA = n.color_a;
      s.colorB = n.color_b;
    }
  }
  return s;
}

static ActionKind actionKind(cadence_v2_MediaItemActionKind k) {
  switch (k) {
    case cadence_v2_MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_LIKE:
      return ActionKind::Like;
    case cadence_v2_MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_DISLIKE:
      return ActionKind::Dislike;
    case cadence_v2_MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_SAVE:
      return ActionKind::Save;
    case cadence_v2_MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_SUBSCRIBE:
      return ActionKind::Subscribe;
    case cadence_v2_MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_ADD_TO_QUEUE:
      return ActionKind::Queue;
    default:
      return ActionKind::Custom;
  }
}

static const char* panelKind(cadence_v2_MediaPanelKind k) {
  switch (k) {
    case cadence_v2_MediaPanelKind_MEDIA_PANEL_KIND_DESCRIPTION:
      return "description";
    case cadence_v2_MediaPanelKind_MEDIA_PANEL_KIND_COMMENTS:
      return "comments";
    case cadence_v2_MediaPanelKind_MEDIA_PANEL_KIND_CHANNEL:
      return "channel";
    case cadence_v2_MediaPanelKind_MEDIA_PANEL_KIND_LYRICS:
      return "lyrics";
    case cadence_v2_MediaPanelKind_MEDIA_PANEL_KIND_RELATED:
      return "related";
    default:
      return "custom";
  }
}

void applyServerState(Session& s, const cadence_v2_MediaSessionState& st) {
  s.serverFields = true;
  s.actions.clear();
  for (pb_size_t i = 0; i < st.actions_count; ++i) s.actions.emplace_back(st.actions[i]);
  s.itemActions.clear();
  for (pb_size_t i = 0; i < st.item_actions_count && st.item_actions != nullptr; ++i) {
    const auto& a = st.item_actions[i];
    ItemAction ia{a.id, actionKind(a.kind), str(a.label), a.icon, a.toggled, a.enabled};
    if (!ia.id.empty()) s.itemActions.push_back(std::move(ia));
  }
  s.serverUpNext.clear();
  for (pb_size_t i = 0; i < st.up_next_count && st.up_next != nullptr; ++i) {
    const auto& u = st.up_next[i];
    QueueItem q;
    q.ref = u.ref;
    q.title = str(u.title);
    q.subtitle = str(u.subtitle);
    q.durationMs = u.duration_ms;
    if (u.artwork_sha256[0] != '\0') q.art = "gen:art:" + std::string(u.artwork_sha256).substr(0, 8);
    q.server = true;
    if (!q.ref.empty()) s.serverUpNext.push_back(std::move(q));
  }
  s.upNext.reset();
  if (!s.serverUpNext.empty()) s.upNext = s.serverUpNext.front();
  s.queuePosition.reset();
  if (st.has_queue_position) s.queuePosition = st.queue_position;
  s.panels.clear();
  for (pb_size_t i = 0; i < st.panels_count && st.panels != nullptr; ++i) {
    const auto& p = st.panels[i];
    Panel panel{p.id, panelKind(p.kind), str(p.title), str(p.count_label)};
    if (!panel.id.empty()) s.panels.push_back(std::move(panel));
  }
  if (st.playable != cadence_v2_PlayableKind_PLAYABLE_KIND_UNSPECIFIED) s.playable = st.playable;
}

void applyDetail(Session& s, const std::string& raw) {
  const auto j = parse(raw);
  if (!j.is_object()) return;
  s.detailLoaded = true;
  // what the play left out (an older server, a catalog read that timed out) the item itself names
  if (const auto it = j.find("item"); it != j.end() && it->is_object()) {
    const auto kind = text(*it, "kind");
    if (s.title.empty()) {
      std::string ru;
      if (const auto show = it->find("show"); show != it->end() && show->is_object()) {
        if (const auto names = show->find("names"); names != show->end() && names->is_object()) ru = text(*names, "ru");
      }
      s.title = !ru.empty() ? ru : text(*it, "title");
    }
    if (s.channelName.empty() && s.artist.empty() && (kind == "video" || kind == "live")) s.channelName = text(*it, "subtitle");
    if (s.providerId.empty()) {
      const auto provider = text(*it, "provider");
      if (provider == "ytmusic") {
        s.providerId = kind == "video" || kind == "live" ? "youtube" : "youtube_music";
      } else {
        s.providerId = provider;
      }
      s.providerName = providerName(s.providerId);
    }
    // the source a torrplayer item came from (anime365, aniliberty), when the play named none
    if (s.providerSource.empty() && s.providerId == text(*it, "provider")) s.providerSource = text(*it, "source");
    if (s.art.empty()) {
      if (const auto art = it->find("artwork"); art != it->end() && art->is_object()) {
        const auto id = text(*art, "hash");
        if (ui::images::validId(id)) s.art = "img:" + id;
      }
    }
  }
  // a title group's detail: the source is its selected variant's
  if (const auto v = j.find("variants"); s.providerSource.empty() && v != j.end() && v->is_array()) {
    const auto selected = text(j, "selected");
    for (const auto& x : *v) {
      if (x.is_object() && !selected.empty() && text(x, "ref") == selected && text(x, "provider") == s.providerId) {
        s.providerSource = text(x, "source");
      }
    }
  }
  // the server's own fields win: the catalog adds the episodes alone
  if (const auto a = j.find("actions"); !s.serverFields && a != j.end() && a->is_array()) {
    s.itemActions.clear();
    for (const auto& x : *a) {
      ItemAction ia;
      ia.id = text(x, "id");
      ia.kind = actionKind(text(x, "kind"));
      ia.label = text(x, "label");
      ia.icon = text(x, "icon");
      ia.toggled = x.value("toggled", false);
      ia.enabled = x.value("enabled", true);
      if (!ia.id.empty()) s.itemActions.push_back(std::move(ia));
    }
  }
  if (const auto p = j.find("panels"); !s.serverFields && p != j.end() && p->is_array()) {
    s.panels.clear();
    for (const auto& x : *p) {
      Panel panel{text(x, "id"), text(x, "kind"), text(x, "title"), text(x, "countLabel")};
      if (!panel.id.empty()) s.panels.push_back(std::move(panel));
    }
  }
  // the episodes, by their order; the one after this one is up next
  if (const auto c = j.find("children"); c != j.end() && c->is_array()) {
    struct Ordered {
      double ordinal;
      QueueItem item;
    };
    std::vector<Ordered> eps;
    for (const auto& x : *c) {
      double ordinal = 0;
      if (const auto e = x.find("episode"); e != x.end() && e->is_object()) ordinal = e->value("ordinal", 0.0);
      eps.push_back({ordinal, queueItem(x)});
    }
    std::stable_sort(eps.begin(), eps.end(), [](const Ordered& a, const Ordered& b) { return a.ordinal < b.ordinal; });
    s.episodes.clear();
    for (auto& e : eps) s.episodes.push_back(std::move(e.item));
    if (s.serverFields) return;
    s.upNext.reset();
    for (std::size_t i = 0; i + 1 < s.episodes.size(); ++i) {
      if (!s.episode.empty() && s.episodes[i].episode == s.episode) {
        s.upNext = s.episodes[i + 1];
        break;
      }
    }
  }
}

void applyActions(Session& s, const std::string& raw) {
  const auto j = parse(raw);
  const auto a = j.is_object() ? j.find("actions") : j.end();
  if (!j.is_object() || a == j.end() || !a->is_array()) return;
  for (const auto& x : *a) {
    const auto id = text(x, "id");
    for (auto& ia : s.itemActions) {
      if (ia.id != id) continue;
      ia.toggled = x.value("toggled", ia.toggled);
      ia.enabled = x.value("enabled", ia.enabled);
    }
  }
}

std::vector<SwitchChoice> switchChoices(const std::string& raw) {
  std::vector<SwitchChoice> out;
  const auto j = parse(raw);
  if (!j.is_object()) return out;
  const auto selected = j.value("selected", json::object());
  // this source's voiceovers first, then the other sources
  for (const char* group : {"voiceover", "source"}) {
    const auto groups = j.find("groups");
    if (groups == j.end() || !groups->is_array()) break;
    for (const auto& g : *groups) {
      if (text(g, "kind") != group) continue;
      const auto current = selected.is_object() ? text(selected, group) : std::string();
      for (const auto& c : g.value("choices", json::array())) {
        SwitchChoice sc;
        sc.group = group;
        sc.id = text(c, "id");
        sc.label = text(c, "label");
        sc.detail = text(c, "detail");
        sc.current = !current.empty() && sc.id == current;
        sc.hasEpisode = !c.value("unavailable", false);
        if (!sc.id.empty()) out.push_back(std::move(sc));
      }
    }
  }
  return out;
}

std::string selectionJson(const std::string& optionsJson, const std::string& group, const std::string& id) {
  const auto j = parse(optionsJson);
  json sel = j.is_object() ? j.value("selected", json::object()) : json::object();
  if (!sel.is_object()) sel = json::object();
  sel[group] = id;
  // another source picks its own voiceover
  if (group == "source") sel.erase("voiceover");
  return sel.dump();
}

std::optional<player::PlayRequest> requestFromPlayable(const std::string& raw, const std::string& sessionId,
                                                      std::uint64_t startMs, std::vector<Skip>* skips) {
  const auto j = parse(raw);
  if (!j.is_object()) return std::nullopt;
  const auto& pl = j.contains("playable") ? j["playable"] : j;
  const auto st = pl.find("stream");
  if (!pl.is_object() || text(pl, "kind") == "handoff" || st == pl.end() || !st->is_object()) return std::nullopt;
  player::PlayRequest r;
  r.sessionId = sessionId;
  r.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  r.url = text(*st, "url");
  r.audioUrl = text(*st, "audioUrl");
  if (const auto rs = st->find("renditions"); rs != st->end() && rs->is_array() && !rs->empty()) {
    // highest first, as the server lists them
    if (r.url.empty()) r.url = text((*rs)[0], "url");
    if (r.audioUrl.empty()) r.audioUrl = text((*rs)[0], "audioUrl");
    for (std::size_t i = 0; i < rs->size(); ++i) {
      const auto& x = (*rs)[i];
      player::Rendition rd;
      rd.height = x.value("height", 0u);
      rd.fps = x.value("fps", 0u);
      rd.name = std::to_string(rd.height) + "p" + (rd.fps > 30 ? std::to_string(rd.fps) : "");
      rd.selected = i == 0;
      r.renditions.push_back(rd);
    }
  }
  if (r.url.empty()) return std::nullopt;
  for (const auto& x : st->value("subtitles", json::array())) {
    r.subtitles.push_back(player::Subtitle{text(x, "url"), text(x, "language"), text(x, "label"), x.value("default", false)});
  }
  r.live = st->value("live", false);
  r.startMs = startMs;
  if (skips != nullptr) {
    skips->clear();
    for (const auto& x : st->value("skips", json::array())) {
      const auto k = skipKind(text(x, "kind"));
      const auto a = ms(x, "startMs"), b = ms(x, "endMs");
      if (k && b > a) skips->push_back(Skip{*k, a, b});
    }
  }
  return r;
}

std::optional<QueuePage> queueFrom(const std::string& raw) {
  const auto j = parse(raw);
  if (!j.is_object()) return std::nullopt;
  QueuePage q;
  if (const auto items = j.find("items"); items != j.end() && items->is_array()) {
    // the server's page: the songs after the playing one, each jumped to by its ref
    for (const auto& x : *items) {
      auto item = queueItem(x);
      item.server = true;
      q.next.push_back(std::move(item));
    }
    q.cursor = text(j, "next");
    return q;
  }
  for (const auto& x : j.value("history", json::array())) q.history.push_back(queueItem(x));
  if (const auto c = j.find("current"); c != j.end() && c->is_object()) q.current = queueItem(*c);
  for (const auto& x : j.value("next", json::array())) q.next.push_back(queueItem(x));
  q.cursor = text(j, "cursor");
  return q;
}

std::optional<AudioQueue> audioQueueFrom(const std::string& raw) {
  const auto j = parse(raw);
  const auto items = j.find("items");
  if (!j.is_object() || items == j.end() || !items->is_array() || items->empty()) return std::nullopt;
  AudioQueue q;
  q.name = text(j, "name");
  if (const auto id = imageIdOf(text(j, "artworkUrl")); !id.empty()) q.art = "img:" + id;
  const auto total = ms(j, "total");
  q.total = static_cast<int>(std::min<std::uint64_t>(total == 0 ? items->size() : total, 1000000));
  q.position = static_cast<std::size_t>(std::min<std::uint64_t>(ms(j, "position"), items->size() - 1));
  for (const auto& x : *items) {
    if (q.items.size() == 200) break;
    auto item = queueItem(x);
    item.server = true;
    q.items.push_back(std::move(item));
  }
  if (q.items.empty()) return std::nullopt;
  q.position = std::min(q.position, q.items.size() - 1);
  return q;
}

std::string providerName(const std::string& id) {
  if (id == "torrplayer") return "torrplayer";
  if (id == "torrserver") return "TorrServer";
  if (id == "youtube") return "YouTube";
  if (id == "youtube_music") return "YouTube Music";
  if (id == "spotify") return "Spotify";
  if (id == "airplay") return "AirPlay";
  return {};
}

std::string monogram(const std::string& id) {
  if (id == "torrplayer") return "tp";
  if (id == "torrserver") return "TS";
  if (id == "youtube") return "YT";
  if (id == "youtube_music") return "YM";
  if (id == "spotify") return "SP";
  if (id == "airplay") return "AP";
  return id.substr(0, 2);
}

std::string clockTime(std::uint64_t t) {
  const auto s = t / 1000;
  char buf[32];
  if (s >= 3600) {
    std::snprintf(buf, sizeof buf, "%u:%02u:%02u", static_cast<unsigned>(s / 3600), static_cast<unsigned>(s / 60 % 60),
                  static_cast<unsigned>(s % 60));
  } else {
    std::snprintf(buf, sizeof buf, "%u:%02u", static_cast<unsigned>(s / 60), static_cast<unsigned>(s % 60));
  }
  return buf;
}

int chapterAt(const std::vector<Chapter>& chapters, std::uint64_t positionMs) {
  int at = -1;
  for (std::size_t i = 0; i < chapters.size(); ++i) {
    if (chapters[i].startMs <= positionMs) at = static_cast<int>(i);
  }
  return at;
}

const Skip* skipAt(const std::vector<Skip>& skips, std::uint64_t positionMs) {
  for (const auto& s : skips)
    if (positionMs >= s.startMs && positionMs < s.endMs) return &s;
  return nullptr;
}

const char* skipKey(SkipKind kind) {
  switch (kind) {
    case SkipKind::Ending:
      return "ending";
    case SkipKind::Recap:
      return "recap";
    case SkipKind::Preview:
      return "preview";
    default:
      return "opening";
  }
}

}  // namespace cadence::screen::routes
