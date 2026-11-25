// the playback routes' parts that need no gl: the item from media.video/play and the catalog's answers, the bars,
// the drift, idle's timer, ambient's picks and the rpc client
#include "app/convert.h"
#include "app/routes/ambient/ambient.h"
#include "app/routes/ambient/drift.h"
#include "app/routes/audio/audio.h"
#include "app/routes/audio/bars.h"
#include "app/routes/playback/rpc.h"
#include "app/routes/playback/session.h"
#include "app/routes/preview/playback-pages.h"
#include "app/routes/queue/queue.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>
#include <nlohmann/json.hpp>

#include <cmath>
#include <cstring>

using namespace cadence::screen;
using namespace cadence::screen::routes;
using Catch::Approx;

TEST_CASE("session: media.video/play's metadata, skips, actions and renditions; tracks are audio mode") {
  cadence_screen_v1_MediaPlay p = cadence_screen_v1_MediaPlay_init_zero;
  std::strcpy(p.session_id, "video-9");
  p.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  char url[] = "http://fake/a";
  p.url = url;
  p.has_metadata = true;
  char title[] = "Ночь фонарей", series[] = "Сад камней", chapter[] = "Опенинг";
  p.metadata.title = title;
  p.metadata.series = series;
  p.metadata.season = 1;
  std::strcpy(p.metadata.episode, "5");
  std::strcpy(p.metadata.item_ref, "ref:1");
  p.metadata.kind = cadence_v2_MediaKind_MEDIA_KIND_EPISODE;
  p.metadata.has_provider = true;
  std::strcpy(p.metadata.provider.id, "torrplayer");
  cadence_v2_MediaChapter chapters[2] = {{180000, chapter}, {0, chapter}};
  p.metadata.chapters = chapters;
  p.metadata.chapters_count = 2;
  p.metadata.badges_count = 1;
  p.metadata.badges[0].kind = cadence_v2_MediaBadgeKind_MEDIA_BADGE_KIND_EXPLICIT;
  p.skips_count = 2;
  p.skips[0] = {cadence_screen_v1_SkipKind_SKIP_KIND_OPENING, 180000, 270000};
  p.skips[1] = {cadence_screen_v1_SkipKind_SKIP_KIND_ENDING, 5, 5};
  p.actions_count = 1;
  std::strcpy(p.actions[0], "next");
  const auto s = sessionFrom(p, app::playRequestOf(p));
  CHECK(s.sessionId == "video-9");
  CHECK(s.mode == Mode::Video);
  CHECK(s.torrplayer());
  CHECK(s.itemRef == "ref:1");
  CHECK(s.explicitContent);
  // sorted by start, an empty skip dropped
  REQUIRE(s.chapters.size() == 2);
  CHECK(s.chapters[0].startMs == 0);
  REQUIRE(s.skips.size() == 1);
  CHECK(s.skips[0].kind == SkipKind::Opening);
  CHECK(s.has("next"));
  CHECK_FALSE(s.has("previous"));
  CHECK(s.request.url == "http://fake/a");

  p.metadata.kind = cadence_v2_MediaKind_MEDIA_KIND_TRACK;
  CHECK(sessionFrom(p, app::playRequestOf(p)).mode == Mode::Audio);
}

TEST_CASE("session: a youtube play's title, channel, provider and art reach what the osd shows") {
  cadence_screen_v1_MediaPlay p = cadence_screen_v1_MediaPlay_init_zero;
  std::strcpy(p.session_id, "video-1");
  p.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  char url[] = "http://fake/a";
  p.url = url;
  p.has_metadata = true;
  char title[] = "Как устроен звук в кино", channel[] = "Лекторий", art[] = "https://cadence.test/v2/images/AbCdEfGhIjKlMnOpQrStUvWx";
  p.metadata.title = title;
  p.metadata.artwork_url = art;
  p.metadata.kind = cadence_v2_MediaKind_MEDIA_KIND_VIDEO;
  p.metadata.has_channel = true;
  p.metadata.channel.name = channel;
  p.metadata.has_provider = true;
  std::strcpy(p.metadata.provider.id, "youtube");
  const auto s = sessionFrom(p, app::playRequestOf(p));
  CHECK(s.title == "Как устроен звук в кино");
  CHECK(s.channelName == "Лекторий");
  // a token alone is named on the screen
  CHECK(s.providerName == "YouTube");
  CHECK(monogram(s.providerId) == "YT");
  CHECK(s.art == "img:AbCdEfGhIjKlMnOpQrStUvWx");
  CHECK(s.mode == Mode::Video);
  // an art url that is not the image proxy's is never fetched
  char other[] = "https://i.ytimg.com/vi/x/hq.jpg";
  p.metadata.artwork_url = other;
  CHECK(sessionFrom(p, app::playRequestOf(p)).art.empty());
}

TEST_CASE("session: catalog.item's item names what a nameless play left out, and never what it named") {
  Session s;
  s.sessionId = "video-1";
  s.itemRef = "c:1";
  const std::string detail =
      R"({"item":{"ref":"c:1","provider":"ytmusic","source":"video","kind":"video","title":"Как устроен звук в кино",)"
      R"("subtitle":"Лекторий","artwork":{"hash":"AbCdEfGhIjKlMnOpQrStUvWx","aspect":"wide"}},"children":[],"panels":[],"actions":[]})";
  applyDetail(s, detail);
  CHECK(s.title == "Как устроен звук в кино");
  CHECK(s.channelName == "Лекторий");
  CHECK(s.providerId == "youtube");
  CHECK(s.providerName == "YouTube");
  CHECK(s.art == "img:AbCdEfGhIjKlMnOpQrStUvWx");

  Session named;
  named.title = "Своё";
  named.providerId = "torrplayer";
  named.providerName = "torrplayer";
  named.art = "gen:art:1";
  applyDetail(named, detail);
  CHECK(named.title == "Своё");
  CHECK(named.providerId == "torrplayer");
  CHECK(named.art == "gen:art:1");
  // a series' russian name wins over its own title
  Session ep;
  applyDetail(ep, R"({"item":{"provider":"torrplayer","kind":"series","title":"Frieren","show":{"names":{"ru":"Фрирен"}}}})");
  CHECK(ep.title == "Фрирен");
  CHECK(ep.channelName.empty());
  CHECK(ep.providerName == "torrplayer");
}

TEST_CASE("session: catalog.item's actions, panels and the episode after this one") {
  auto s = playback::fakeEpisode();
  applyDetail(s, playback::fakeDetailJson());
  CHECK(s.detailLoaded);
  REQUIRE(s.itemActions.size() == 2);
  CHECK(s.itemActions[0].kind == ActionKind::Save);
  CHECK(s.itemActions[0].toggled);
  REQUIRE(s.panels.size() == 2);
  CHECK(s.panels[1].countLabel == "312");
  REQUIRE(s.upNext);
  CHECK(s.upNext->episode == "6");
  CHECK(s.upNext->durationMs == 1440000);
  CHECK(s.episodes.size() == 4);

  applyActions(s, R"({"actions":[{"id":"a-save","toggled":false,"enabled":true}]})");
  CHECK_FALSE(s.itemActions[0].toggled);
  // garbage changes nothing
  applyDetail(s, "{nope");
  CHECK(s.itemActions.size() == 2);
}

TEST_CASE("session: the switcher's rows, its selection and a resolve's playable") {
  const auto rows = switchChoices(playback::fakeOptionsJson(true));
  // voiceovers first, then sources
  REQUIRE(rows.size() == 5);
  CHECK(rows[0].group == "voiceover");
  CHECK(rows[0].current);
  CHECK_FALSE(rows[0].hasEpisode);
  CHECK(rows[2].label == "JAM CLUB");
  CHECK_FALSE(rows[2].hasEpisode);
  CHECK(rows[3].group == "source");

  const auto sel = nlohmann::json::parse(selectionJson(playback::fakeOptionsJson(), "voiceover", "vo-dub"));
  CHECK(sel["voiceover"] == "vo-dub");
  CHECK(sel["source"] == "src-al");
  const auto other = nlohmann::json::parse(selectionJson(playback::fakeOptionsJson(), "source", "src-a365"));
  CHECK_FALSE(other.contains("voiceover"));

  std::vector<Skip> skips;
  const auto r = requestFromPlayable(
      R"({"playable":{"kind":"stream","stream":{"url":"","renditions":[{"height":1080,"url":"http://fake/1080","audioUrl":"http://fake/a"},{"height":720,"fps":60,"url":"http://fake/720"}],
          "subtitles":[{"url":"http://fake/s.vtt","language":"ru","label":"Надписи","default":true}],"skips":[{"kind":"opening","startMs":1000,"endMs":2000}]},"ref":"v"}})",
      "video-1", 52000, &skips);
  REQUIRE(r);
  CHECK(r->url == "http://fake/1080");
  CHECK(r->audioUrl == "http://fake/a");
  CHECK(r->startMs == 52000);
  REQUIRE(r->renditions.size() == 2);
  CHECK(r->renditions[1].name == "720p60");
  REQUIRE(r->subtitles.size() == 1);
  CHECK(r->subtitles[0].isDefault);
  REQUIRE(skips.size() == 1);
  CHECK_FALSE(requestFromPlayable(R"({"kind":"handoff","handoff":{"provider":"spotify","target":"x"}})", "s", 0, nullptr));
}

TEST_CASE("session: times, the chapter under a position, the skippable segment playing") {
  CHECK(clockTime(0) == "0:00");
  CHECK(clockTime(7000) == "0:07");
  CHECK(clockTime(41 * 60000 + 12000) == "41:12");
  CHECK(clockTime(6450000) == "1:47:30");
  const std::vector<Chapter> ch = {{0, "a"}, {60000, "b"}, {120000, "c"}};
  CHECK(chapterAt(ch, 0) == 0);
  CHECK(chapterAt(ch, 119999) == 1);
  CHECK(chapterAt({}, 5) == -1);
  const std::vector<Skip> skips = {{SkipKind::Recap, 1000, 5000}};
  CHECK(skipAt(skips, 999) == nullptr);
  CHECK(skipAt(skips, 1000) != nullptr);
  CHECK(skipAt(skips, 5000) == nullptr);
  CHECK(std::string(skipKey(SkipKind::Recap)) == "recap");
}

TEST_CASE("queue: media.queue's page, and the session's own queue before it answers") {
  const auto q = queueFrom(R"({"history":[{"ref":"a","title":"A"}],"current":{"ref":"b","title":"B","durationMs":1000},"next":[{"ref":"c","title":"C","episode":"6"}],"cursor":"k"})");
  REQUIRE(q);
  CHECK(q->history.size() == 1);
  CHECK(q->current->durationMs == 1000);
  CHECK(q->next[0].episode == "6");
  CHECK(q->cursor == "k");
  auto s = playback::fakeEpisode();
  applyDetail(s, playback::fakeDetailJson());
  const auto own = QueueContent::fromSession(s);
  REQUIRE(own.current);
  CHECK(own.current->episode == "5");
  REQUIRE(own.next.size() == 2);
  CHECK(own.next[0].episode == "6");
}

TEST_CASE("audio queue: the full page keeps its collection, play order and proxied covers") {
  const auto q = audioQueueFrom(
      R"({"name":"Evening","artworkUrl":"https://cadence.example/v2/images/AbCdEfGhIjKlMnOpQrStUvWx","total":3,"position":1,"items":[{"ref":"mq:0","title":"One","subtitle":"Artist","durationMs":181000,"artworkUrl":"https://cadence.example/v2/images/AbCdEfGhIjKlMnOpQrStUvWx"},{"ref":"mq:1","title":"Two"},{"ref":"mq:2","title":"Three"}]})");
  REQUIRE(q);
  CHECK(q->name == "Evening");
  CHECK(q->art == "img:AbCdEfGhIjKlMnOpQrStUvWx");
  CHECK(q->total == 3);
  CHECK(q->position == 1);
  REQUIRE(q->items.size() == 3);
  CHECK(q->items[0].durationMs == 181000);
  CHECK(q->items[0].art == q->art);
  CHECK(q->items[1].server);
  CHECK(q->items[2].ref == "mq:2");
  CHECK_FALSE(audioQueueFrom(R"({"items":[]})"));
}

TEST_CASE("drift: whole dp inside its box, at its speed: ambient's 8 dp a minute") {
  DriftOffset last = drift(0, 8.0 / 60, 160, 90);
  int moves = 0;
  for (int s = 1; s <= 3600; ++s) {
    const auto d = drift(s, 8.0 / 60, 160, 90);
    CHECK(d.x >= 0);
    CHECK(d.x <= 160);
    CHECK(d.y >= 0);
    CHECK(d.y <= 90);
    // never more than a dp a second on either axis
    CHECK(std::abs(d.x - last.x) <= 1);
    CHECK(std::abs(d.y - last.y) <= 1);
    if (!(d == last)) ++moves;
    last = d;
  }
  // 480 dp of path in an hour: a redraw every few seconds, never every frame
  CHECK(moves > 300);
  CHECK(moves < 700);
}

TEST_CASE("idle: after idleAfterS without a key while nothing plays, sleep after sleepAfterS more") {
  IdleTimer t;
  t.configure(600, 3600);
  t.touch(0);
  CHECK_FALSE(t.idle(599999, false));
  CHECK(t.idle(600000, false));
  CHECK_FALSE(t.idle(600000, true));
  CHECK_FALSE(t.sleep(600000 + 3599999));
  CHECK(t.sleep(600000 + 3600000));
  t.touch(700000);
  CHECK_FALSE(t.idle(800000, false));
  t.configure(0, 3600);
  CHECK_FALSE(t.idle(1 << 30, false));
}

TEST_CASE("idle over a paused video: pausedAfterS, idleAfterS when absent, never when 0") {
  IdleTimer t;
  t.configure(600, 3600);
  t.touch(0);
  CHECK(t.idle(600000, false, true));
  t.configure(600, 3600, 1800);
  CHECK_FALSE(t.idle(600000, false, true));
  CHECK(t.idle(600000, false, false));
  CHECK(t.idle(1800000, false, true));
  t.configure(600, 3600, 0);
  CHECK_FALSE(t.idle(1 << 30, false, true));
  CHECK(t.idle(600000, false, false));
}

TEST_CASE("ambient: ambient.next's pick, its captions in order, the one showing") {
  const auto p = ambientPickFrom(R"({"kind":"video","id":"x","url":"https://sylvan.apple.com/x.mov","captions":[{"atS":40,"text":"b"},{"atS":0,"text":"a"}],"duration":0})");
  REQUIRE(p);
  CHECK(p->captions[0].text == "a");
  CHECK(captionAt(p->captions, 39.9) == "a");
  CHECK(captionAt(p->captions, 40) == "b");
  CHECK(p->weather.empty());
  const auto w = ambientPickFrom(R"({"kind":"image","url":"https://x/i.jpg","weather":"+3 °C · облачно"})");
  REQUIRE(w);
  CHECK(w->weather == "+3 °C · облачно");
  CHECK_FALSE(ambientPickFrom(R"({"kind":"sound","url":"x"})"));
  CHECK_FALSE(ambientPickFrom(R"({"kind":"image"})"));
}

TEST_CASE("rpc: ids of its own, answers by id, a timeout, nothing on a down link") {
  std::vector<std::string> sent;
  bool up = true;
  PlaybackRpc rpc([&](const cadence_screen_v1_Envelope& e) {
    sent.emplace_back(e.body.rpc_request.id);
    return up;
  });
  std::string got;
  REQUIRE(rpc.call("catalog.item", "{}", [&](const RpcAnswer& a) { got = a.status + ":" + a.json; }, 0, 100));
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_rpc_response_tag;
  std::strcpy(e.body.rpc_response.id, "card-1");
  CHECK_FALSE(rpc.take(e));
  std::strcpy(e.body.rpc_response.id, sent[0].c_str());
  std::strcpy(e.body.rpc_response.status, "ok");
  char json[] = "{\"a\":1}";
  e.body.rpc_response.json = json;
  CHECK(rpc.take(e));
  CHECK(got == "ok:{\"a\":1}");
  CHECK_FALSE(rpc.take(e));
  rpc.call("catalog.item", "{}", [&](const RpcAnswer& a) { got = a.status; }, 0, 100);
  rpc.tick(99);
  CHECK(rpc.pending() == 1);
  rpc.tick(100);
  CHECK(got == "timeout");
  up = false;
  CHECK_FALSE(rpc.call("x", "{}", nullptr, 0));
  CHECK(rpc.pending() == 0);
}

TEST_CASE("audio: a placeholder's duotone is stable per album, backgrounds stay under 18 % luminance") {
  CHECK(placeholderColors("Длинная ночь") == placeholderColors("Длинная ночь"));
  const auto c = darkened(0xffffff);
  const auto v = static_cast<unsigned>(std::stoul(c.substr(1), nullptr, 16));
  const auto ch = [&](unsigned shift) { return static_cast<double>((v >> shift) & 0xffu) / 255.0; };
  const double luma = 0.2126 * ch(16) + 0.7152 * ch(8) + 0.0722 * ch(0);
  CHECK(luma <= 0.185);
  CHECK(darkened(0x101010) == "#101010");
}
