#include "app/routes/preview/playback-pages.h"

#include "app/routes/queue/queue.h"
#include "tests/fixture.h"

#include <RmlUi/Core/ElementDocument.h>

#include <functional>
#include <utility>

namespace cadence::screen::routes {
// the gallery picks a scene before the first frame (audio/viz-view.cpp)
void preselectSyncScene(VizView& view, const std::string& id);
}  // namespace cadence::screen::routes

namespace cadence::screen::routes::playback {

namespace {

constexpr std::uint64_t kMin = 60000;
using Step = std::pair<int, std::function<void()>>;

// runs each step its delay after the one before, on the ui's clock (the harness gives a page 1.5 s after its setup)
void steps(ui::Ui& ui, std::vector<Step> s, std::size_t i = 0) {
  if (i >= s.size()) return;
  ui.after(s[i].first, [&ui, s, i] {
    s[i].second();
    steps(ui, s, i + 1);
  });
}

void press(ui::Ui& ui, cadence_screen_v1_Key k) {
  platform::KeyEvent e;
  e.key = k;
  e.action = cadence_screen_v1_KeyAction_KEY_ACTION_DOWN;
  ui.key(e);
  e.action = k == cadence_screen_v1_Key_KEY_BACK ? cadence_screen_v1_KeyAction_KEY_ACTION_PRESS : cadence_screen_v1_KeyAction_KEY_ACTION_UP;
  ui.key(e);
}

player::Track track(const char* id, cadence_v2_MediaTrackKind kind, const char* lang, const char* label, bool selected) {
  player::Track t;
  t.id = id;
  t.kind = kind;
  t.language = lang;
  t.label = label;
  t.selected = selected;
  return t;
}

// the episode playing at a position, its details answered
void episodeAt(Rig& r, std::uint64_t positionMs, cadence_v2_PlaybackState st = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING) {
  auto s = fakeEpisode();
  r.state(fakeState(s, positionMs, st));
  r.play(s);
  r.answer("catalog.item", fakeDetailJson());
  r.answer("catalog.options", fakeOptionsJson());
}

void videoAt(Rig& r, std::uint64_t positionMs) {
  auto s = fakeVideo();
  r.state(fakeState(s, positionMs, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  r.play(s);
}

void trackAt(Rig& r, Session s, std::uint64_t positionMs) {
  r.state(fakeState(s, positionMs, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  r.play(s);
  r.playback->bands({200, 230, 180, 150, 170, 120, 140, 90, 100, 80, 70, 60, 50, 40, 30, 20});
}

ui::preview::Page page(const std::string& name, std::function<void(Rig&)> drive, int maxLayers = 2, double maxAnimated = 30,
                       bool walk = true) {
  ui::preview::Page p;
  p.name = "playback-" + name;
  p.body = [] { return std::string("<div/>"); };
  p.setup = [drive](ui::preview::Context& c) { drive(start(c.ui)); };
  p.maxLayers = maxLayers;
  p.maxAnimatedPct = maxAnimated;
  p.walk = walk;
  return p;
}

}  // namespace

Session fakeEpisode() {
  Session s;
  s.sessionId = "video-1";
  s.itemRef = "ref:title:1";
  s.mode = Mode::Video;
  s.kind = cadence_v2_MediaKind_MEDIA_KIND_EPISODE;
  s.title = "Ночь фонарей";
  s.series = "Сад камней";
  s.season = 1;
  s.episode = "5";
  s.providerId = "torrplayer";
  s.providerName = "torrplayer";
  s.providerSource = "AniLiberty";
  s.badges = {"1080p"};
  s.chapters = {{0, "Пролог"}, {3 * kMin, "Опенинг"}, {5 * kMin, "Ярмарка"}, {14 * kMin, "Фонари над рекой"}, {21 * kMin, "Эндинг"}};
  s.skips = {{SkipKind::Opening, 3 * kMin, 4 * kMin + 30000}, {SkipKind::Ending, 21 * kMin, 23 * kMin}};
  s.actions = {"next"};
  player::Rendition r1080, r720;
  r1080.name = "1080p";
  r1080.height = 1080;
  r1080.selected = true;
  r720.name = "720p";
  r720.height = 720;
  s.renditions = {r1080, r720};
  s.request.sessionId = s.sessionId;
  s.request.url = "http://fake/episode-5";
  return s;
}

Session fakeVideo() {
  Session s;
  s.sessionId = "video-2";
  s.itemRef = "ref:video:2";
  s.kind = cadence_v2_MediaKind_MEDIA_KIND_VIDEO;
  s.title = "Как устроен звук в кино";
  s.channelName = "Лекторий";
  s.providerId = "youtube";
  s.providerName = "YouTube";
  s.statsLabel = "1,2 млн просмотров · 3 дня назад";
  s.itemActions = {{"a-like", ActionKind::Like, "12 тыс.", "", true, true},
                   {"a-dislike", ActionKind::Dislike, "", "", false, true},
                   {"a-sub", ActionKind::Subscribe, "Подписаться", "", false, true},
                   {"a-queue", ActionKind::Queue, "В очередь", "", false, true}};
  s.panels = {{"p-desc", "description", "Описание", ""}, {"p-comments", "comments", "Комментарии", "1,4 тыс."},
              {"p-channel", "channel", "Канал", ""}};
  s.chapters = {{0, "Вступление"}, {2 * kMin, "Микрофоны"}, {9 * kMin, "Сведение"}};
  return s;
}

Session fakeTrack() {
  Session s;
  s.sessionId = "music-1";
  s.itemRef = "ref:track:1";
  s.mode = Mode::Audio;
  s.kind = cadence_v2_MediaKind_MEDIA_KIND_TRACK;
  s.title = "Северное сияние";
  s.artist = "Полярные огни";
  s.album = "Длинная ночь";
  s.providerId = "youtube_music";
  s.providerName = "YouTube Music";
  s.explicitContent = true;
  s.colorA = 0x6a3fa0;
  s.colorB = 0x1c3a6a;
  s.actions = {"next", "previous"};
  s.itemActions = {{"a-like", ActionKind::Like, "Нравится", "", true, true}};
  s.panels = {{"p-lyrics", "lyrics", "Текст", ""}, {"p-artist", "channel", "Исполнитель", ""}};
  s.upNext = QueueItem{"ref:track:2", "Тёплый ветер", "Полярные огни", 214000, "", ""};
  return s;
}

player::PlayerState fakeState(const Session& s, std::uint64_t positionMs, cadence_v2_PlaybackState st) {
  player::PlayerState p;
  p.sessionId = s.sessionId;
  p.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  p.state = st;
  p.positionMs = positionMs;
  p.rate = 1;
  p.durationMs = s.mode == Mode::Audio ? 247000 : 24 * kMin;
  p.bufferedMs = 90000;
  p.audioRoute = cadence_v2_AudioRoute_AUDIO_ROUTE_MIXER;
  if (s.mode == Mode::Video) {
    p.tracks = {track("v1", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_VIDEO, "", "", true),
                track("a1", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO, "ru", "Русский 5.1", true),
                track("a2", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO, "ja", "Японский", false),
                track("s1", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE, "ru", "Надписи", true),
                track("s2", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE, "ru", "Полные", false)};
  }
  return p;
}

std::string fakeDetailJson() {
  return R"({"item":{"ref":"ref:title:1","provider":"torrplayer","kind":"show","title":"Сад камней","playable":true,"hasChildren":true},
  "children":[
    {"ref":"ref:ep:4","title":"Дорога к морю","episode":{"label":"4","ordinal":4}},
    {"ref":"ref:ep:5","title":"Ночь фонарей","episode":{"label":"5","ordinal":5}},
    {"ref":"ref:ep:6","title":"Утренний поезд","durationS":1440,"episode":{"label":"6","ordinal":6}},
    {"ref":"ref:ep:7","title":"Старый маяк","durationS":1440,"episode":{"label":"7","ordinal":7}}],
  "panels":[{"id":"p-desc","kind":"description","title":"Описание"},{"id":"p-comments","kind":"comments","title":"Комментарии","countLabel":"312"}],
  "actions":[{"id":"a-save","kind":"my_list","label":"Моя очередь","toggled":true,"enabled":true},
             {"id":"a-watched","kind":"watched","label":"Просмотрено","toggled":false,"enabled":true}]})";
}

std::string fakeOptionsJson(bool nextMissing) {
  std::string unavailable = nextMissing ? R"(,"unavailable":true,"reason":"no_episode")" : "";
  return R"({"groups":[
    {"kind":"source","label":"Источник","choices":[{"id":"src-al","label":"AniLiberty"},{"id":"src-a365","label":"Anime365"}]},
    {"kind":"voiceover","label":"Озвучка","choices":[{"id":"vo-alb","label":"AniLibria")" + unavailable +
         R"(},{"id":"vo-dub","label":"AniDUB"},{"id":"vo-jam","label":"JAM CLUB","unavailable":true}]}],
  "selected":{"source":"src-al","voiceover":"vo-alb","quality":"1080"}})";
}

QueuePage fakeQueue() {
  QueuePage q;
  q.history = {{"r1", "Звёздная пыль", "Полярные огни", 201000, "", ""},
               {"r2", "Город спит", "Полярные огни", 188000, "", ""},
               {"r3", "Ледяной мост", "Полярные огни", 233000, "", ""},
               {"r4", "Над облаками", "Полярные огни", 199000, "", ""}};
  q.current = QueueItem{"ref:track:1", "Северное сияние", "Полярные огни", 247000, "", ""};
  q.next = {{"ref:track:2", "Тёплый ветер", "Полярные огни", 214000, "", ""},
            {"ref:track:3", "Очень длинное название песни, которое не помещается в одну строку списка", "Полярные огни", 305000, "", ""},
            {"ref:track:4", "Рассвет", "Полярные огни", 176000, "", ""}};
  return q;
}


std::vector<ui::preview::Page> pages() {
  std::vector<ui::preview::Page> out;
  // §12: the osd over the film, focus on play/pause; the voiceover chip, the track chips with their values
  out.push_back(page("osd", [](Rig& r) {
    episodeAt(r, 7 * kMin + 12000);
    steps(r.ui, {{300, [&r] { r.playback->playerPage().show(OsdState::Transport); }}});
  }));
  // ↓ to the details strip: the rows lift 72 dp, item actions and panel chips
  out.push_back(page("osd-details", [](Rig& r) {
    videoAt(r, 4 * kMin + 40000);
    steps(r.ui, {{100, [&r] { r.playback->playerPage().show(OsdState::Details, 0); }}});
  }));
  // paused: the osd stays, play shows what OK will do
  out.push_back(page("osd-paused", [](Rig& r) {
    episodeAt(r, 14 * kMin + 5000, cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED);
    steps(r.ui, {{300, [&r] { r.playback->playerPage().show(OsdState::Scrubber); }}});
  }));
  // the subtitles sheet from its chip: tracks and the size
  out.push_back(page("subtitles", [](Rig& r) {
    episodeAt(r, 7 * kMin);
    steps(r.ui, {{300, [&r] {
                   r.playback->playerPage().show(OsdState::Transport);
                   if (auto* e = r.playback->context().el("o-subs")) {
                     r.ui.focus().focus(e);
                     r.playback->playerPage().activate(ui::Activation{e, "osd:subs", false, std::nullopt, std::nullopt});
                   }
                 }}});
  }, 1));
  // the osd hidden in the opening: the skip pill (focused, its line shrinking) and two subtitle lines drawn by us
  out.push_back(page("skip-subs", [](Rig& r) {
    episodeAt(r, 3 * kMin + 40000);
    steps(r.ui, {{100, [&r] {
                   r.playback->playerPage().hide();
                   r.playback->subtitle("Фонари зажгут, когда стемнеет.\nТы придёшь?");
                 }}});
  }));
  // ← with the osd hidden: the compact bar
  out.push_back(page("compact", [](Rig& r) {
    episodeAt(r, 12 * kMin);
    steps(r.ui, {{100, [&r] { r.playback->playerPage().hide(); }}, {300, [&r] { press(r.ui, cadence_screen_v1_Key_KEY_LEFT); }}});
  }));
  // the end credits: up next with its countdown, same voiceover
  out.push_back(page("upnext", [](Rig& r) {
    episodeAt(r, 21 * kMin + 10000);
    steps(r.ui, {{100, [&r] { r.playback->playerPage().hide(); }}, {300, [&r] { r.answer("catalog.options", fakeOptionsJson()); }}});
  }));
  // the voiceover lacks the next episode: «Другая озвучка» instead of a countdown
  out.push_back(page("upnext-missing", [](Rig& r) {
    episodeAt(r, 21 * kMin + 10000);
    r.answer("catalog.options", fakeOptionsJson(true));
    steps(r.ui, {{100, [&r] { r.playback->playerPage().hide(); }}});
  }));
  // buffering over a second: the spinner and the percentage
  out.push_back(page("buffering", [](Rig& r) {
    episodeAt(r, 9 * kMin);
    r.fakePlayer.buffering = 37;
    r.state(fakeState(fakeEpisode(), 9 * kMin, cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING));
    steps(r.ui, {{100, [&r] { r.playback->playerPage().hide(); }}});
  }));
  // a failure: the state block with retry, another voiceover, back
  out.push_back(page("error", [](Rig& r) {
    episodeAt(r, 9 * kMin);
    steps(r.ui, {{200, [&r] {
                   auto st = fakeState(fakeEpisode(), 9 * kMin, cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE);
                   st.ended = true;
                   st.errorCode = "network";
                   r.state(st);
                 }}});
  }, 1));
  // §18.6: the switcher from the voiceover chip, what each row has and what OK will do
  out.push_back(page("switcher", [](Rig& r) {
    episodeAt(r, 14 * kMin + 52000);
    steps(r.ui, {{300, [&r] {
                   r.playback->playerPage().show(OsdState::Transport);
                   auto* vo = r.playback->context().el("o-vo");
                   r.ui.focus().focus(vo);
                   r.playback->playerPage().activate(ui::Activation{vo, "osd:voiceover", false, std::nullopt, std::nullopt});
                   r.answer("catalog.options", fakeOptionsJson());
                 }},
                 {300, [&r] { press(r.ui, cadence_screen_v1_Key_KEY_DOWN); }}});
  }));
  // §14: the comments panel, the film unscrimmed on the left
  out.push_back(page("panel-comments", [](Rig& r) {
    videoAt(r, 4 * kMin);
    steps(r.ui, {{100, [&r] {
                   r.playback->playerPage().show(OsdState::Details, 0);
                   r.playback->openPanel("p-comments", r.playback->context().el("o-panel-1"));
                   r.answerPage("catalog.panel", "page_comments.binpb");
                 }}});
  }));
  out.push_back(page("panel-description", [](Rig& r) {
    videoAt(r, 4 * kMin);
    steps(r.ui, {{100, [&r] {
                   r.playback->openPanel("p-desc", nullptr);
                   r.answerPage("catalog.panel", "page_youtube_description.binpb");
                 }}});
  }));
  // «Главы»: the chapters, the current one marked
  out.push_back(page("panel-chapters", [](Rig& r) {
    episodeAt(r, 15 * kMin);
    steps(r.ui, {{300, [&r] {
                   r.playback->playerPage().show(OsdState::Details);
                   auto* ch = r.playback->context().el("o-chapters");
                   r.playback->playerPage().activate(ui::Activation{ch, "osd:chapters", false, std::nullopt, std::nullopt});
                 }}});
  }));
  // §15: the queue over audio mode, history collapsed, the current item, numbered next
  out.push_back(page("queue", [](Rig& r) {
    trackAt(r, fakeTrack(), 95000);
    steps(r.ui, {{100, [&r] { press(r.ui, cadence_screen_v1_Key_KEY_OK); }},
                 {100, [&r] {
                    auto* q = r.playback->context().el("a-queue");
                    r.playback->audioPage().activate(ui::Activation{q, "audio:queue", false, std::nullopt, std::nullopt});
                    if (auto* c = dynamic_cast<QueueContent*>(r.playback->sheet().content())) c->show(fakeQueue());
                  }}});
  }));
  // §13: audio mode in lean-back, nobody holding the remote
  out.push_back(page("audio-lean", [](Rig& r) { trackAt(r, fakeTrack(), 95000); }, 2, 30, false));
  // a key: the controls, focus on play/pause
  out.push_back(page("audio-controls", [](Rig& r) {
    trackAt(r, fakeTrack(), 95000);
    steps(r.ui, {{100, [&r] { press(r.ui, cadence_screen_v1_Key_KEY_OK); }}});
  }));
  // no art from the server: the first letter on the album's duotone; a handoff hides the visualizer (§19)
  out.push_back(page("audio-handoff", [](Rig& r) {
    auto s = fakeTrack();
    s.colorA = 0x2a1e3a;
    s.colorB = 0x14161e;
    s.providerId = "spotify";
    s.providerName = "Spotify";
    s.playable = cadence_v2_PlayableKind_PLAYABLE_KIND_HANDOFF;
    s.handoffTarget = "Колонка «Кухня»";
    s.explicitContent = false;
    trackAt(r, s, 61000);
    steps(r.ui, {{100, [&r] { press(r.ui, cadence_screen_v1_Key_KEY_OK); }}});
  }));
  // an airplay cast from a phone (cadenced's NowPlaying): its cover tints the sleeve, the sender's name beside the
  // source; lean-back, then the controls (airplay takes play and pause alone)
  out.push_back(page("audio-cast-lean", [](Rig& r) {
    r.cast("ms-1", cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING, 94000);
  }, 2, 30, false));
  out.push_back(page("audio-cast-controls", [](Rig& r) {
    r.cast("ms-1", cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING, 94000);
    steps(r.ui, {{100, [&r] { press(r.ui, cadence_screen_v1_Key_KEY_OK); }}});
  }));
  // 2 min of lean-back: the dimmed sleeve, the group drifted
  out.push_back(page("audio-ambient", [](Rig& r) {
    trackAt(r, fakeTrack(), 95000);
    steps(r.ui, {{100, [&r] {
                   r.playback->audioPage().setState(AudioPage::State::Ambient);
                   r.playback->audioPage().driftTo(300);
                 }}});
  }, 2, 30, false));
  // the feedback scenes full screen under the column, from the fixture's pcm
  out.push_back(page("audio-scenes", [](Rig& r) {
    trackAt(r, fakeTrack(), 95000);
    const auto pcm = cadence::render::viz::fixture::stream(0, 48000 * 3);
    r.playback->pcm(pcm, 2, 48000, 0);
    preselectSyncScene(*r.playback->audioPage().viz(), "scene:vortex");
    r.playback->audioPage().setVisualizer(VizFamily::Scenes);
  }, 2, 100, false));
  // idle with an image: ken burns under the clock and the weather
  out.push_back(page("ambient-image", [](Rig& r) {
    r.playback->go(cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT);
    r.answer("ambient.next",
             R"({"kind":"image","id":"img-1","url":"http://fake/backdrop.jpg","duration":30,"weather":"+3 °C · облачно"})");
    steps(r.ui, {{100, [&r] { r.playback->ambientPage().driftTo(240); }}});
  }, 2, 100, false));
  // an aerial clip's caption at its time (the clip itself is the player's, under the layer)
  out.push_back(page("ambient-caption", [](Rig& r) {
    r.playback->go(cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT);
    r.answer("ambient.next", R"({"kind":"video","id":"a-1","url":"http://fake/aerial.mov","category":"landscapes",
      "weather":"+3 °C · облачно",
      "captions":[{"atS":0,"text":"Исландия"},{"atS":40,"text":"Ледник Ватнайёкюдль"}],"duration":0})");
    player::PlayerState st;
    st.sessionId = r.playback->ambientPage().clipSession();
    st.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_AMBIENT;
    st.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
    st.positionMs = 52000;
    st.rate = 1;
    steps(r.ui, {{500, [&r, st] { r.state(st); }}});
  }, 2, 30, false));
  return out;
}

}  // namespace cadence::screen::routes::playback
