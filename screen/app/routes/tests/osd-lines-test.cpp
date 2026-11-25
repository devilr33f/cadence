// the player's words that need no gl: the source chip's provider and codecs, and the queue's lines
#include "app/routes/player/source-line.h"
#include "app/routes/playback/session.h"
#include "app/routes/queue/queue.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen;
using namespace cadence::screen::routes;

namespace {

player::Track track(cadence_v2_MediaTrackKind kind, const char* codec, bool selected) {
  player::Track t;
  t.kind = kind;
  t.codec = codec;
  t.selected = selected;
  return t;
}

}  // namespace

TEST_CASE("source chip: mpv's codecs by their usual names, the selected video's and audio's") {
  CHECK(codecName("hevc") == "HEVC");
  CHECK(codecName("h264") == "H.264");
  CHECK(codecName("eac3") == "E-AC-3");
  CHECK(codecName("prores") == "PRORES");
  // not a decoder's token: nothing shown
  CHECK(codecName("a b").empty());
  CHECK(codecName("<script>").empty());
  const auto video = cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_VIDEO;
  const auto audio = cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO;
  CHECK(codecLine({track(video, "hevc", true), track(audio, "opus", false), track(audio, "aac", true)}) == "HEVC \xc2\xb7 AAC");
  CHECK(codecLine({track(audio, "flac", true)}) == "FLAC");
  CHECK(codecLine({track(video, "", true)}).empty());
  CHECK(codecLine({}).empty());
}

TEST_CASE("source chip: torrplayer names the source it played from, another provider its own name") {
  Session s;
  s.providerId = "torrplayer";
  s.providerName = "torrplayer";
  CHECK(providerLabel(s) == "torrplayer");
  s.providerSource = "anime365";
  CHECK(providerLabel(s) == "anime365");
  s.providerId = "youtube";
  s.providerName = "YouTube";
  CHECK(providerLabel(s) == "YouTube");
}

TEST_CASE("source chip: catalog.item names a torrplayer play's source, its own or its selected variant's") {
  Session s;
  s.providerId = "torrplayer";
  applyDetail(s, R"({"item":{"ref":"r","provider":"torrplayer","source":"aniliberty","kind":"series"}})");
  CHECK(s.providerSource == "aniliberty");
  Session g;
  g.providerId = "torrplayer";
  applyDetail(g, R"({"item":{"ref":"tg","provider":"torrplayer","kind":"title_group"},"selected":"v2",)"
                 R"("variants":[{"ref":"v1","provider":"torrplayer","source":"anime365"},{"ref":"v2","provider":"torrplayer","source":"yummyanime"}]})");
  CHECK(g.providerSource == "yummyanime");
}

TEST_CASE("queue: an episode reads by its number and its name, never the bare number twice") {
  QueueItem q;
  q.episode = "7";
  q.title = "7";
  auto l = QueueContent::line(q);
  CHECK(l.title == "[[episodes.episode|n=7]]");
  CHECK(l.meta.empty());
  q.title = "Серия 7";
  CHECK(QueueContent::line(q).meta.empty());
  q.title = "Письмо без подписи";
  CHECK(QueueContent::line(q).meta == "Письмо без подписи");
  q.title = "7";
  q.episodeName = "Аптека на окраине";
  CHECK(QueueContent::line(q).meta == "Аптека на окраине");
  q.episodeName.clear();
  q.subtitle = "Лунный аптекарь";
  CHECK(QueueContent::line(q).meta == "Лунный аптекарь");
  // not an episode: its own title and subtitle, escaped
  QueueItem song;
  song.title = "A & B";
  song.subtitle = "Artist";
  l = QueueContent::line(song);
  CHECK(l.title == "A &amp; B");
  CHECK(l.meta == "Artist");
}

TEST_CASE("queue: an item's art from the catalog's artwork, its episode's name from its label's object") {
  const auto q = queueFrom(R"({"next":[{"ref":"e8","title":"8","episode":{"label":"8","name":"Ночной рынок"},)"
                           R"("artwork":{"hash":"abcdefghijklmnopqrstuvwx"}},{"ref":"e9","title":"9","episode":"9"}]})");
  REQUIRE(q);
  REQUIRE(q->next.size() == 2);
  CHECK(q->next[0].art == "img:abcdefghijklmnopqrstuvwx");
  CHECK(q->next[0].episodeName == "Ночной рынок");
  CHECK(q->next[1].art.empty());
  // the show's art stands in when the session has it
  auto s = Session{};
  s.itemRef = "tg";
  s.title = "Show";
  s.art = "img:abcdefghijklmnopqrstuvwx";
  s.episode = "7";
  const auto own = QueueContent::fromSession(s);
  REQUIRE(own.current);
  CHECK(own.current->art == s.art);
}
