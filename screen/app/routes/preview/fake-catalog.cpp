#include "app/routes/preview/fake-catalog.h"

#include "app/routes/preview/fake-devices.h"

#include "app/routes/core/methods.h"
#include "ipc/codec.h"

#if defined(__GNUC__)
#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wconversion"
#pragma GCC diagnostic ignored "-Wsign-conversion"
#pragma GCC diagnostic ignored "-Wshadow"
#pragma GCC diagnostic ignored "-Wmissing-field-initializers"
#endif
// the implementation is the gallery harness's (harness.cpp)
#include <stb/stb_image_write.h>
#if defined(__GNUC__)
#pragma GCC diagnostic pop
#endif

#include <algorithm>
#include <cmath>
#include <cstdio>

namespace cadence::screen::routes::fake {

namespace {

// made-up titles a russian household's screen shows (no third-party text)
struct Show {
  const char* ru;
  const char* en;
  int year;
  const char* format;
  int episodes;
};
constexpr Show kAnime[] = {{"Лунный аптекарь", "The Moon Apothecary", 2023, "TV", 24},
                           {"Хроники северного ветра", "Northwind Chronicles", 2024, "TV", 12},
                           {"Город тысячи фонарей", "City of a Thousand Lanterns", 2022, "TV", 26},
                           {"Последний рейс на Марс", "Last Flight to Mars", 2021, "Movie", 1},
                           {"Сад стеклянных цветов", "Garden of Glass Flowers", 2024, "ONA", 10},
                           {"Мастер тихих клинков", "Master of Quiet Blades", 2020, "TV", 24},
                           {"Дневник небесного кита", "Sky Whale Diary", 2023, "TV", 13},
                           {"Песнь о медной лисе", "Song of the Copper Fox", 2019, "TV", 50}};
constexpr Show kFilms[] = {{"Тихая гавань", "", 2024, "Фильм", 0},     {"Северный ветер", "", 2023, "Фильм", 0},
                           {"Последний поезд", "", 2022, "Фильм", 0},  {"Город огней", "", 2021, "Сериал", 8},
                           {"Дом у озера", "", 2020, "Фильм", 0},      {"Звёздный путь домой", "", 2019, "Фильм", 0},
                           {"Осенний сад", "", 2018, "Сериал", 10},    {"Белые ночи", "", 2017, "Фильм", 0}};
constexpr const char* kVideos[] = {"Как устроен звук в кино", "Поход на Эльбрус за 5 дней", "Хлеб на закваске: просто",
                                   "Обзор недели: новинки", "Тайны древнего Рима", "Джаз для вечера"};

Json showItem(const std::string& ref, const Show& s, int seed, const std::string& provider, const std::string& source,
              const std::string& kind) {
  Json show{{"names", {{"ru", s.ru}, {"en", s.en}}}, {"year", s.year}, {"format", s.format}};
  if (s.episodes > 0) {
    show["episodesAired"] = s.episodes;
    show["episodesTotal"] = s.episodes;
  }
  Json it{{"ref", ref},           {"provider", provider}, {"kind", kind},     {"title", s.ru},
          {"playable", false},    {"hasChildren", true},  {"show", show},
          {"artwork", {{"hash", imageId(seed)}, {"aspect", "poster"}}}};
  if (!source.empty()) it["source"] = source;
  return it;
}

Json anime(int i, int sources) {
  auto it = showItem("ref-tg-" + std::to_string(i + 1), kAnime[i % 8], 100 + i, "torrplayer", "aniliberty", "title_group");
  it["show"]["sourcesWithIt"] = sources;
  return it;
}

Json film(int i, bool progress) {
  auto it = showItem("ref-film-" + std::to_string(i + 1), kFilms[i % 8], 10 + i, "torrserver", "", "movie");
  it["playable"] = true;
  it["hasChildren"] = false;
  it["durationS"] = 6000 + i * 300;
  if (progress) it["progress"] = {{"positionS", 1800 + i * 900}};
  return it;
}

Json video(int i) {
  return Json{{"ref", "ref-yt-" + std::to_string(i + 1)},
              {"provider", "ytmusic"},
              {"kind", "video"},
              {"title", kVideos[i % 6]},
              {"subtitle", "Лекторий"},
              {"durationS", 600 + i * 97},
              {"playable", true},
              {"hasChildren", false},
              {"artwork", {{"hash", imageId(40 + i)}, {"aspect", "wide"}}}};
}

void writeBytes(void* ctx, void* data, int size) {
  static_cast<std::string*>(ctx)->append(static_cast<const char*>(data), static_cast<std::size_t>(size));
}

}  // namespace

std::string imageId(int seed) {
  char buf[32];
  std::snprintf(buf, sizeof buf, "fakeart%017d", seed);
  return buf;
}

bool FakeImages::fetch(const std::string& id, std::string& bytes, std::string& code) {
  if (!id.starts_with("fakeart")) {
    code = "not_found";
    return false;
  }
  const int seed = std::atoi(id.c_str() + 7);
  fetched++;
  // a poster-sized painting: a warm or cool gradient per seed, a horizon and a sun, a pale band where a printed
  // title would sit (rule 1: nothing is drawn over art, so the art carries its own). "fakeart<seed>-<w>-<h>" paints
  // it at that size instead: a music cover's real shape (a square album, a wide artist photo)
  int w = 268, h = 402;
  if (const auto dash = id.find('-'); dash != std::string::npos) {
    const int sw = std::atoi(id.c_str() + dash + 1);
    const auto dash2 = id.find('-', dash + 1);
    const int sh = dash2 != std::string::npos ? std::atoi(id.c_str() + dash2 + 1) : 0;
    if (sw >= 16 && sw <= 2048 && sh >= 16 && sh <= 2048) {
      w = sw;
      h = sh;
    }
  }
  std::vector<std::uint8_t> px(static_cast<std::size_t>(w * h * 3));
  const float hue = static_cast<float>((seed * 47) % 360);
  const auto channel = [&](float shift, float y) {
    const float v = 0.5f + 0.5f * std::cos((hue + shift) * 3.14159f / 180.0f);
    return static_cast<std::uint8_t>(std::clamp(40.0f + 150.0f * v * (0.55f + 0.45f * y), 0.0f, 255.0f));
  };
  const float sunX = static_cast<float>(60 + (seed * 37) % 150);
  const float sunY = static_cast<float>(90 + (seed * 53) % 120);
  for (int y = 0; y < h; ++y) {
    const float fy = static_cast<float>(y) / static_cast<float>(h);
    for (int x = 0; x < w; ++x) {
      auto* p = px.data() + static_cast<std::size_t>((y * w + x) * 3);
      p[0] = channel(0, 1 - fy);
      p[1] = channel(120, 1 - fy);
      p[2] = channel(240, 1 - fy);
      const float dx = static_cast<float>(x) - sunX, dy = static_cast<float>(y) - sunY;
      if (dx * dx + dy * dy < 900) p[0] = p[1] = 236, p[2] = 200;
      if (y > h * 2 / 3) {
        p[0] = static_cast<std::uint8_t>(p[0] / 3);
        p[1] = static_cast<std::uint8_t>(p[1] / 3);
        p[2] = static_cast<std::uint8_t>(p[2] / 3);
      }
      if (y > 24 && y < 44 && x > 20 && x < 20 + (seed * 13) % 200) p[0] = p[1] = p[2] = 230;
    }
  }
  bytes.clear();
  return stbi_write_png_to_func(writeBytes, &bytes, w, h, 3, px.data(), w * 3) != 0;
}

Json homeSections(const std::string& provider) {
  Json secs = Json::array();
  if (provider == "torrserver") {
    Json cont = Json::array(), lib = Json::array();
    for (int i = 0; i < 3; ++i) cont.push_back(film(i + 3, true));
    for (int i = 0; i < 8; ++i) lib.push_back(film(i, false));
    secs.push_back({{"id", "torrserver/continue"}, {"provider", "torrserver"}, {"title", "Продолжить просмотр"}, {"items", cont}});
    secs.push_back({{"id", "torrserver/library"}, {"provider", "torrserver"}, {"title", "Фильмы и сериалы"}, {"items", lib},
                    {"next", "c2"}});
  } else if (provider == "torrplayer") {
    // continue watching as torrplayer's server feed answers it: 2:3 covers, progress by episodes watched
    Json cont = Json::array();
    for (int i = 0; i < 3; ++i) {
      auto it = anime(i + 2, 1);
      it["progress"] = {{"count", 3 + i * 4}, {"total", 12 + i * 12}};
      cont.push_back(it);
    }
    secs.push_back({{"id", "torrplayer/continue"}, {"provider", "torrplayer"}, {"title", "Продолжить"}, {"items", cont}});
    Json items = Json::array();
    for (int i = 0; i < 8; ++i) items.push_back(anime(i, 1 + i % 5));
    secs.push_back({{"id", "torrplayer/new"}, {"provider", "torrplayer"}, {"title", "Аниме: новые серии и мой список"},
                    {"items", items}});
  }
  return Json{{"sections", secs}};
}

Json searchEvent(int step, bool empty) {
  switch (step) {
    case 0:
      return Json{{"pending",
                   {{{"provider", "torrplayer"}, {"source", "aniliberty"}, {"name", "AniLiberty"}, {"enabled", true}},
                    {{"provider", "torrplayer"}, {"source", "anime365"}, {"name", "anime365"}, {"enabled", true}},
                    {{"provider", "torrplayer"}, {"source", "animego"}, {"name", "AnimeGo"}, {"enabled", true}},
                    {{"provider", "torrplayer"}, {"source", "nyaa"}, {"name", "Nyaa"}, {"enabled", true}},
                    {{"provider", "torrserver"}, {"name", "TorrServer"}, {"enabled", true}},
                    {{"provider", "ytmusic"}, {"name", "YouTube Music"}, {"enabled", true}}}}};
    case 1: {
      Json items = Json::array();
      if (!empty) {
        items.push_back(anime(0, 3));
        items.push_back(anime(1, 1));
        items.push_back(anime(2, 2));
      }
      return Json{{"provider", "torrplayer"}, {"source", "aniliberty"}, {"items", items}};
    }
    case 2: {
      Json items = Json::array();
      if (!empty) {
        for (int i = 0; i < 4; ++i) items.push_back(video(i));
      }
      return Json{{"provider", "ytmusic"}, {"items", items}};
    }
    case 3:
      return Json{{"provider", "torrplayer"}, {"source", "animego"}, {"failure", {{"code", "timeout"}}}};
    case 4: {
      if (empty) return Json{{"provider", "torrplayer"}, {"source", "anime365"}};
      auto merged = anime(0, 5);
      auto lost = anime(4, 0);
      lost["unavailable"] = true;
      return Json{{"provider", "torrplayer"}, {"source", "anime365"}, {"updated", {merged}}, {"items", {lost}},
                  {"unenforced", {"studio"}}};
    }
    case 5: {
      Json items = Json::array();
      if (!empty) {
        for (int i = 0; i < 3; ++i) items.push_back(film(i, false));
      }
      return Json{{"provider", "torrserver"}, {"items", items}};
    }
    case 6: {
      Json items = Json::array();
      if (!empty) {
        items.push_back(anime(5, 1));
        items.push_back(anime(6, 1));
      }
      return Json{{"provider", "torrplayer"}, {"source", "nyaa"}, {"items", items}};
    }
    default:
      return Json{{"done", true}};
  }
}

Json titleDetail(const Script& s, const std::string& ref) {
  const auto& show = kAnime[0];
  Json it = showItem(ref.starts_with("ref-v-") ? "ref-tg-1" : ref, show, 100, "torrplayer", "", "title_group");
  it["show"]["episodesAired"] = s.episodes;
  it["show"]["episodesTotal"] = std::max(s.episodes, 24);
  it["show"]["status"] = s.episodes < 24 ? "ongoing" : "released";
  it["show"]["season"] = "autumn";
  it["show"]["genres"] = {"Драма", "Детектив", "Исторический"};
  it["show"]["score"] = 8.9;
  it["show"]["ageRating"] = "16+";
  it["show"]["description"] =
      "Девушка из столичной аптеки попадает во дворец и раскрывает одну тайну за другой: отравления, подменённые письма и "
      "забытые рецепты. Каждое дело приближает её к правде о собственной семье, но и делает врагов опаснее. История о "
      "травах, ядах и людях, которые умеют молчать.";
  Json children = Json::array();
  int ordinal = 1;
  for (int i = 0; i < s.episodes; ++i, ++ordinal) {
    const std::string label = std::to_string(ordinal);
    Json ep{{"ref", "ref-ep-" + label},
            {"provider", "torrplayer"},
            {"kind", "episode"},
            {"title", "Серия " + label},
            {"durationS", 1440},
            {"playable", true},
            {"hasChildren", false},
            {"episode", {{"label", label}, {"ordinal", ordinal}}}};
    if (ordinal % 4 == 1) ep["episode"]["name"] = ordinal == 1 ? "Аптека на окраине" : "Письмо без подписи";
    if (ordinal <= 12) ep["progress"] = {{"watched", true}};
    if (ordinal == 13 && s.continuePoint) ep["progress"] = {{"positionS", 600}};
    if (ordinal == s.episodes) ep["badges"] = {{{"kind", "new"}}};
    if (ordinal == 2) ep["episode"]["type"] = "ova";
    children.push_back(ep);
    if (ordinal == 5) {
      children.push_back(Json{{"ref", "ref-ep-5.5"},
                              {"provider", "torrplayer"},
                              {"kind", "episode"},
                              {"title", "Итоги"},
                              {"durationS", 1380},
                              {"playable", true},
                              {"hasChildren", false},
                              {"progress", {{"watched", true}}},
                              {"episode", {{"label", "5.5"}, {"ordinal", 5.5}, {"type", "recap"}, {"name", "Итоги"}}}});
    }
  }
  auto variant = [&](const std::string& src, int eps, bool unavailable, Json vos) {
    Json v = showItem("ref-v-" + src, show, 100, "torrplayer", src, "series");
    v["show"]["episodesAired"] = eps;
    v["show"]["voiceovers"] = vos;
    v["badges"] = {{{"kind", "hd"}, {"label", "1080p"}}};
    if (unavailable) v["unavailable"] = true;
    return v;
  };
  Json out{{"item", it},
           {"children", children},
           {"variants",
            {variant("aniliberty", s.episodes, false, {"AniLibria", "AniDUB", "Studio Band", "JAM Club"}),
             variant("anime365", s.episodes + 1, false, {"Crunchyroll", "AniMedia"}), variant("animego", 0, true, Json::array())}},
           {"selected", "ref-v-aniliberty"},
           {"panels", Json::array()},
           {"actions",
            {{{"id", "act-list"},
              {"kind", "custom"},
              {"label", "Мой список"},
              {"toggled", true},
              {"enabled", true},
              {"value", "Смотрю"},
              {"values", {"Смотрю", "В планах", "Просмотрено", "Отложено", "Брошено"}}}}}};
  if (s.continuePoint) out["continue"] = {{"episode", "13"}, {"positionS", 600}};
  return out;
}

Json titleOptions(const Script& s, const std::string& episode) {
  auto vo = [](const std::string& id, const std::string& name, const std::string& type, const std::string& lang, int q,
               bool unavailable) {
    Json c{{"id", id}, {"label", name}, {"stream", {{"translator", name}, {"transType", type}, {"language", lang}, {"quality", q}}}};
    if (unavailable) {
      c["unavailable"] = true;
      c["reason"] = "not_found";
    }
    return c;
  };
  Json groups = Json::array();
  groups.push_back({{"kind", "voiceover"},
                    {"label", "Озвучка"},
                    {"hidden", 12},
                    {"choices",
                     {vo("vo-anilibria", "AniLibria", "voice", "ru", 1080, false), vo("vo-anidub", "AniDUB", "voice", "ru", 1080, false),
                      vo("vo-band", "Studio Band", "voice", "ru", 720, false), vo("vo-cr", "Crunchyroll", "sub", "en", 1080, false),
                      vo("vo-raw", "Оригинал", "raw", "ja", 1080, false), vo("vo-jam", "JAM Club", "voice", "ru", 720, true)}}});
  groups.push_back({{"kind", "quality"},
                    {"label", "Качество"},
                    {"choices", {{{"id", "480"}, {"label", "480"}}, {{"id", "720"}, {"label", "720"}}, {{"id", "1080"}, {"label", "1080"}}}}});
  groups.push_back(
      {{"kind", "torrent"},
       {"label", "Торренты"},
       {"choices",
        {{{"id", "tor-1"},
          {"label", "1–12"},
          {"torrent", {{"episodes", "1–12"}, {"sizeBytes", 4200000000LL}, {"seeders", 34}, {"quality", 1080}, {"codec", "HEVC"}}}},
         {{"id", "tor-2"},
          {"label", "1–24"},
          {"torrent",
           {{"episodes", "1–24"}, {"sizeBytes", 9800000000LL}, {"seeders", 0}, {"quality", 1080}, {"codec", "AVC"}, {"hardsub", true}}}}}}});
  Json out{{"groups", groups}, {"selected", {{"voiceover", "vo-anilibria"}, {"quality", "1080"}}}};
  if (s.remembered) out["remembered"] = {"voiceover", "quality"};
  (void)episode;
  return out;
}

Json playable(const std::string& ref, const std::string& episode) {
  return Json{{"kind", "stream"},
              {"stream",
               {{"url", "http://fake.invalid/play/" + episode},
                {"subtitles", {{{"url", "http://fake.invalid/subs/ru.ass"}, {"language", "ru"}, {"label", "Русские"}, {"format", "ass"}, {"default", true}}}},
                {"skips", {{{"kind", "opening"}, {"startMs", 90000}, {"endMs", 180000}}}},
                {"resumeMs", episode == "13" ? 600000 : 0},
                {"durationMs", 1440000}}},
              {"episode", episode},
              {"selected", {{"voiceover", "vo-anilibria"}, {"quality", "1080"}}},
              {"ref", ref}};
}

bool playEnvelope(const Json& pl, const std::string& sessionId, ipc::OutEnvelope& out) {
  // what cadenced sends the screen for the server's media.video/play: the stream, its subtitles and skips, the
  // metadata with the variant's ref
  if (!pl.is_object() || !pl.contains("stream") || !pl["stream"].is_object()) return false;
  const auto& st = pl["stream"];
  const std::string url = st.value("url", "");
  if (!url.starts_with("http://") && !url.starts_with("https://")) return false;
  auto& m = out->body.media_play;
  ipc::copyField(m.session_id, sessionId);
  m.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  m.url = out.keep(url);
  const auto resume = st.value("resumeMs", 0LL);
  m.start_ms = resume > 0 ? static_cast<std::uint64_t>(resume) : 0;
  const auto& subs = st.value("subtitles", Json::array());
  if (!subs.empty()) {
    m.subtitles = out.keepArray<cadence_screen_v1_Subtitle>(subs.size());
    for (const auto& sub : subs) {
      auto& x = m.subtitles[m.subtitles_count++];
      x.url = out.keep(sub.value("url", ""));
      ipc::copyField(x.language, sub.value("language", ""));
      x.label = out.keep(sub.value("label", ""));
      x.format = cadence_screen_v1_SubtitleFormat_SUBTITLE_FORMAT_ASS;
      x.is_default = sub.value("default", false);
    }
  }
  for (const auto& sk : st.value("skips", Json::array())) {
    if (m.skips_count >= 8) break;
    auto& x = m.skips[m.skips_count++];
    x.kind = sk.value("kind", "") == "ending" ? cadence_screen_v1_SkipKind_SKIP_KIND_ENDING : cadence_screen_v1_SkipKind_SKIP_KIND_OPENING;
    x.start_ms = static_cast<std::uint64_t>(sk.value("startMs", 0LL));
    x.end_ms = static_cast<std::uint64_t>(sk.value("endMs", 0LL));
  }
  m.playable = cadence_v2_PlayableKind_PLAYABLE_KIND_STREAM;
  m.has_metadata = true;
  const std::string episode = pl.value("episode", "");
  m.metadata.kind = episode.empty() ? cadence_v2_MediaKind_MEDIA_KIND_VIDEO : cadence_v2_MediaKind_MEDIA_KIND_EPISODE;
  ipc::copyField(m.metadata.episode, episode);
  ipc::copyField(m.metadata.item_ref, pl.value("ref", ""));
  m.metadata.has_provider = true;
  ipc::copyField(m.metadata.provider.id, "torrplayer");
  return true;
}

FakeCatalog::FakeCatalog(ui::Ui& ui, Script s) : script(std::move(s)), ui_(ui) {}

FakeCatalog::~FakeCatalog() { *alive_ = false; }

void FakeCatalog::later(int ms, std::function<void()> fn) {
  ui_.after(ms, [alive = alive_, fn = std::move(fn)] {
    if (*alive) fn();
  });
}

const Sent* FakeCatalog::last(const std::string& method) const {
  for (auto it = calls.rbegin(); it != calls.rend(); ++it) {
    if (it->method == method) return &*it;
  }
  return nullptr;
}

void FakeCatalog::respond(const std::string& id, const std::string& status, const Json& json) {
  if (target_ == nullptr) return;
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_rpc_response_tag);
  ipc::copyField(out->body.rpc_response.id, id);
  ipc::copyField(out->body.rpc_response.status, status);
  out->body.rpc_response.json = out.keep(json.dump());
  target_->message(out.get());
}

void FakeCatalog::event(const std::string& id, const Json& json, bool last) {
  if (target_ == nullptr || std::find(open_.begin(), open_.end(), id) == open_.end()) return;
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_rpc_event_tag);
  ipc::copyField(out->body.rpc_event.id, id);
  out->body.rpc_event.seq = ++seqs_[id];
  out->body.rpc_event.json = out.keep(json.dump());
  out->body.rpc_event.last = last;
  if (last) open_.erase(std::remove(open_.begin(), open_.end(), id), open_.end());
  target_->message(out.get());
}

void FakeCatalog::answer(const Sent& s) {
  const auto id = s.id;
  if (script.hang) return;
  const auto& a = s.args;
  if (s.method == rpc::kCatalogBrowse) {
    const std::string path = a.value("path", "");
    if (std::find(script.absent.begin(), script.absent.end(), path) != script.absent.end()) {
      later(30, [this, id] { respond(id, "not_found", Json{{"code", "not_found"}}); });
    } else if (std::find(script.down.begin(), script.down.end(), path) != script.down.end()) {
      later(60, [this, id] { respond(id, "unavailable", Json{{"code", "unavailable"}, {"failure", "timeout"}}); });
    } else {
      const int delay = path == "torrplayer" ? 120 : 60;
      const auto it = script.home.find(path);
      const Json answer = it != script.home.end() ? it->second : homeSections(path);
      later(delay, [this, id, answer] { respond(id, "ok", answer); });
    }
    return;
  }
  if (s.method == rpc::kCatalogSearch) {
    open_.push_back(id);
    const bool empty = script.searchEmpty;
    later(20, [this, id, empty] { respond(id, "ok", searchEvent(0, empty)); });
    // no times: the caller streams the rest itself
    if (script.searchTimesMs.empty()) return;
    const int steps = script.searchHangs ? 2 : 7;
    for (int i = 1; i <= steps; ++i) {
      const int at = i - 1 < static_cast<int>(script.searchTimesMs.size()) ? script.searchTimesMs[static_cast<std::size_t>(i - 1)] : 900;
      later(at, [this, id, i, empty, steps] { event(id, searchEvent(i, empty), i == 7 && steps == 7); });
    }
    return;
  }
  if (s.method == rpc::kCatalogItem) {
    later(40, [this, id, a] { respond(id, "ok", titleDetail(script, a.value("ref", ""))); });
    return;
  }
  if (s.method == rpc::kCatalogOptions) {
    later(40, [this, id, a] { respond(id, "ok", titleOptions(script, a.value("episode", ""))); });
    return;
  }
  if (s.method == rpc::kCatalogResolve) {
    open_.push_back(id);
    const auto ref = a.value("ref", "");
    const auto ep = a.value("episode", "");
    const bool torrent = a.contains("choices") && a["choices"].contains("torrent");
    later(20, [this, id] { respond(id, "ok", Json{{"resolving", true}}); });
    if (torrent) {
      later(150, [this, id] { event(id, Json{{"stage", "connecting_peers"}}, false); });
      later(400, [this, id] { event(id, Json{{"stage", "reading_stream"}}, false); });
      later(650, [this, id] { event(id, Json{{"stage", "matching"}}, false); });
    } else {
      later(150, [this, id] { event(id, Json{{"stage", "reading_stream"}}, false); });
      later(450, [this, id] { event(id, Json{{"stage", "starting"}}, false); });
    }
    const auto outcome = script.resolve;
    if (outcome == "playable") {
      later(900, [this, id, ref, ep] { event(id, Json{{"playable", playable(ref, ep)}}, true); });
    } else if (outcome == "no_seeders") {
      later(900, [this, id] {
        event(id, Json{{"status", "unavailable"}, {"refused", {{"code", "unavailable"}, {"failure", "no_seeders"}}}}, true);
      });
    }
    return;
  }
  if (s.method == rpc::kMediaPlay) {
    // the server resolves it and has cadenced play it: the screen gets cadenced's MediaPlay, then the answer
    const auto ref = a.value("ref", "");
    const auto ep = a.value("episode", "");
    const auto outcome = script.resolve;
    if (outcome == "playable") {
      later(900, [this, id, ref, ep, a] {
        // a cancelled play stops on the server with its resolve
        if (std::find(cancels.begin(), cancels.end(), id) != cancels.end()) return;
        const auto sid = "cad-" + std::to_string(plays.size() + 1);
        ipc::OutEnvelope out(cadence_screen_v1_Envelope_media_play_tag);
        auto pl = playable(ref, ep);
        if (a.contains("startMs")) pl["stream"]["resumeMs"] = a["startMs"];
        if (a.value("from", "") == "start") pl["stream"]["resumeMs"] = 0;
        playEnvelope(pl, sid, out);
        play(out.get());
        plays.back().args = a;
        if (target_ != nullptr) target_->message(out.get());
        respond(id, "ok", Json{{"sessionId", sid}, {"episode", ep}});
      });
    } else if (outcome == "no_seeders") {
      later(900, [this, id] { respond(id, "unavailable", Json{{"code", "unavailable"}, {"failure", "no_seeders"}}); });
    }
    return;
  }
  if (s.method == "devices.media") {
    open_.push_back(id);
    const Json json = script.nearby ? devicesMedia() : Json{{"items", Json::array()}};
    later(20, [this, id, json] { respond(id, "ok", json); });
    return;
  }
  if (s.method == rpc::kCatalogAct) {
    later(40, [this, id, a] {
      auto acts = titleDetail(script, "ref-tg-1")["actions"];
      acts[0]["value"] = a.value("value", "");
      respond(id, "ok", Json{{"actions", acts}});
    });
    return;
  }
  later(10, [this, id] { respond(id, "ok", Json::object()); });
}

bool FakeCatalog::send(const cadence_screen_v1_Envelope& e) {
  switch (e.which_body) {
    case cadence_screen_v1_Envelope_rpc_request_tag: {
      const auto& r = e.body.rpc_request;
      Sent s;
      s.method = r.method;
      s.id = r.id;
      s.subscribe = r.subscribe;
      s.args = r.json != nullptr ? Json::parse(r.json, nullptr, false) : Json::object();
      calls.push_back(s);
      answer(calls.back());
      return true;
    }
    case cadence_screen_v1_Envelope_rpc_cancel_tag:
      cancels.emplace_back(e.body.rpc_cancel.id);
      open_.erase(std::remove(open_.begin(), open_.end(), std::string(e.body.rpc_cancel.id)), open_.end());
      return true;
    case cadence_screen_v1_Envelope_screen_context_tag: {
      auto c = e.body.screen_context;
      contextQueries.emplace_back(c.query != nullptr ? c.query : "");
      c.query = nullptr;
      contexts.push_back(c);
      return true;
    }
    default:
      return true;
  }
}

bool FakeCatalog::play(const cadence_screen_v1_Envelope& e) {
  if (e.which_body != cadence_screen_v1_Envelope_media_play_tag) return false;
  const auto& m = e.body.media_play;
  Play p;
  p.sessionId = m.session_id;
  p.url = m.url != nullptr ? m.url : "";
  p.itemRef = m.metadata.item_ref;
  p.episode = m.metadata.episode;
  p.startMs = m.start_ms;
  p.subtitles = m.subtitles_count;
  p.skips = m.skips_count;
  plays.push_back(p);
  return true;
}

}  // namespace cadence::screen::routes::fake
