#include "ui/preview/gallery.h"

#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>
#include <filesystem>
#include <fstream>
#include <iterator>

namespace cadence::screen::ui::preview {

namespace {

using namespace rml;

// fake catalogue data: titles as a russian household's screen shows them (no third-party text, no network)
struct Fake {
  const char* title;
  const char* meta;
};
constexpr Fake kFilms[] = {{"Тихая гавань", "2024 · Драма"},       {"Северный ветер", "2023 · Триллер"},
                           {"Последний поезд", "2022 · Приключения"}, {"Город огней", "2021 · Мелодрама"},
                           {"Дом у озера", "2020 · Комедия"},       {"Звёздный путь домой", "2019 · Фантастика"},
                           {"Осенний сад", "2018 · Драма"},          {"Белые ночи", "2017 · Детектив"}};
constexpr Fake kVideos[] = {{"Как устроен звук в кино", "Лекторий · 1,2 млн просмотров"},
                            {"Поход на Эльбрус за 5 дней", "Горы рядом · 340 тыс."},
                            {"Хлеб на закваске: просто", "Кухня · 88 тыс."},
                            {"Обзор недели: новинки", "Киноклуб · 51 тыс."},
                            {"Тайны древнего Рима", "История · 2,4 млн"},
                            {"Джаз для вечера", "Музыка · 12 тыс."}};
constexpr Fake kMusic[] = {{"Вечерний эфир", "Радио"},      {"Северное сияние", "Альбом · 2024"},
                           {"Лёгкий джаз", "Плейлист"},     {"Кухня", "Колонка · играет"},
                           {"Акустика", "Плейлист"},        {"Ночной город", "Альбом · 2021"},
                           {"Классика утром", "Плейлист"}};

Card fake(CardKind kind, const Fake& f, const std::string& id, int seed) {
  Card c;
  c.kind = kind;
  c.id = id;
  c.title = f.title;
  c.meta = f.meta;
  c.art = "gen:art:" + std::to_string(seed);
  c.action = "open:" + id;
  c.actions = "info|Информация|info;queue|В очередь|queue_add;restart|Воспроизвести с начала|repeat;watched|Отметить "
              "просмотренным|check";
  return c;
}

Row films(const std::string& id, CardKind kind = CardKind::Poster) {
  Row r;
  r.id = id;
  r.title = "Фильмы и сериалы";
  r.badge = badge(BadgeKind::Provider, "TorrServer", "TS");
  r.kind = kind;
  int i = 0;
  for (const auto& f : kFilms) {
    r.cards.push_back(fake(kind, f, id + "-" + std::to_string(i), 10 + i));
    ++i;
  }
  r.allAction = "all:" + id;
  return r;
}

Row videos(const std::string& id, bool progress) {
  Row r;
  r.id = id;
  r.title = progress ? "[[home.continue]]" : "YouTube";
  r.badge = badge(BadgeKind::Provider, "YouTube Music", "YM");
  r.kind = CardKind::Wide;
  int i = 0;
  for (const auto& f : kVideos) {
    auto c = fake(CardKind::Wide, f, id + "-" + std::to_string(i), 40 + i);
    if (progress) c.progress = 0.15 + 0.13 * i;
    r.cards.push_back(c);
    ++i;
  }
  return r;
}

Row music(const std::string& id) {
  Row r;
  r.id = id;
  r.title = "[[home.music]]";
  r.kind = CardKind::Square;
  int i = 0;
  for (const auto& f : kMusic) {
    r.cards.push_back(fake(CardKind::Square, f, id + "-" + std::to_string(i), 70 + i));
    ++i;
  }
  return r;
}

std::string safe(const std::string& inner) { return "<div class=\"safe\">" + inner + "</div>"; }
std::string column(const std::string& inner) { return "<div class=\"vscroll\">" + inner + "</div>"; }
std::string heading(const std::string& text) {
  return "<div class=\"t-title-m\" style=\"margin-bottom: 24dp;\">" + text + "</div>";
}
std::string hrow(const std::string& inner, int gap = 24) {
  return "<div class=\"hstack wrap\" style=\"gap: " + std::to_string(gap) + "dp; margin-bottom: 40dp;\">" + inner + "</div>";
}

std::string read(const std::string& path) {
  std::ifstream in(path, std::ios::binary);
  return {std::istreambuf_iterator<char>(in), {}};
}

std::string fixturePath(const std::string& name) { return std::string(CADENCE_SCREEN_CARD_FIXTURES) + "/" + name; }

// a card fixture's page: the drawer's stack on the right half, as the assistant drawer will hold it
Page cardPage(const std::string& file) {
  Page p;
  p.name = "card-" + file.substr(0, file.find('.'));
  p.body = [] {
    return safe("<div class=\"t-title-m\" style=\"margin-bottom: 24dp;\">[[assistant.title]]</div>"
                "<div id=\"cards\" class=\"cstack\"/>");
  };
  p.setup = [file](Context& c) {
    const auto bytes = read(fixturePath(file));
    const std::span<const std::uint8_t> span(reinterpret_cast<const std::uint8_t*>(bytes.data()), bytes.size());
    auto* host = c.ui.page()->GetElementById("cards");
    const bool page = file.starts_with("page_");
    c.cards.attach(host, page ? cards::kMaxPageCards : cards::kMaxDrawerCards, page);
    if (page) {
      if (auto pg = cards::decodePage(span)) c.cards.showPage(*pg);
    } else if (auto card = cards::decodeCard(span)) {
      c.cards.show(*card);
    }
  };
  return p;
}

}  // namespace

std::vector<std::string> cardFixtures() {
  std::vector<std::string> out;
  for (const auto& e : std::filesystem::directory_iterator(CADENCE_SCREEN_CARD_FIXTURES)) {
    const auto name = e.path().filename().string();
    if (name.ends_with(".binpb")) out.push_back(name);
  }
  std::sort(out.begin(), out.end());
  return out;
}

std::vector<Page> pages() {
  std::vector<Page> out;

  // home: the hero, three rows of three aspects, the clock tile (a data binding)
  out.push_back({"home",
                 [] {
                   return safe(column(hero("Тихая гавань", "2024 · Драма · 2 ч 4 мин · 16+", "gen:art:hero",
                                           {Button{"[[title.watch]]", "play", "play", true, false, false, "hero-play",
                                                   "default-focus"},
                                            Button{"[[common.more]]", "info", "more", false, false, false, {}, {}}}) +
                                      "<div style=\"height: 48dp;\"/>" + row(films("films")) + row(videos("continue", true)) +
                                      row(music("music"))) +
                               "<div style=\"position: absolute; right: 0dp; top: 0dp;\">" + clockTile() + "</div>");
                 },
                 [](Context& c) {
                   Ui::ClockModel clock;
                   clock.time = "21:40";
                   clock.date = "Суббота, 14 ноября";
                   clock.temp = "+3 °C";
                   clock.weatherIcon = rml::iconChar("cloud");
                   c.ui.setClock(clock);
                 }});

  // the focus treatment on each card shape, a focused poster in slot 2 (anchored scrolling)
  out.push_back({"cards",
                 [] {
                   Row jp = films("anime", CardKind::PosterJp);
                   jp.title = "Аниме";
                   jp.badge = badge(BadgeKind::Provider, "torrplayer", "tp") + sourceChip("AniLiberty");
                   return safe(column(row(jp) + row(videos("wide", false))));
                 },
                 [](Context& c) {
                   if (auto* e = c.ui.page()->GetElementById("anime-2")) c.ui.focus().focus(e);
                 }});

  // every non-happy state of a row (section 9)
  out.push_back({"row-states", [] {
                   Row loading;
                   loading.id = "loading";
                   loading.title = "Фильмы";
                   loading.state = RowState::Loading;
                   Row slow = loading;
                   slow.id = "slow";
                   slow.title = "Сериалы";
                   slow.state = RowState::Slow;
                   Row empty = loading;
                   empty.id = "empty";
                   empty.title = "Моя очередь";
                   empty.state = RowState::Empty;
                   empty.kind = CardKind::Wide;
                   Row error = loading;
                   error.id = "error";
                   error.title = "YouTube";
                   error.state = RowState::Error;
                   Row down = loading;
                   down.id = "down";
                   down.title = "TorrServer";
                   down.state = RowState::Down;
                   down.badge = badge(BadgeKind::Provider, "TorrServer", "TS");
                   down.stateText = "[[ui.provider_down|provider=TorrServer|minutes=12]]";
                   return safe(column(row(empty) + row(error) + row(down) + row(slow)));
                 }});

  // buttons, icon buttons (transport), chips, badges
  out.push_back({"buttons", [] {
                   std::string b = heading("Кнопки") +
                                   hrow(button({"[[title.watch]]", "play", "watch", true, false, false, "b-primary", "default-focus"}) +
                                        button({"[[title.in_queue]]", "queue_add", "queue", false, false, false, {}, {}}) +
                                        button({"[[title.subscribe]]", "subscribe", "sub", false, true, false, {}, {}}) +
                                        button({"Открыть замок", "lock", "unlock", false, false, true, {}, {}}));
                   b += hrow(iconButton("previous", "Назад", "prev") + iconButton("back10", "−10 с", "back10") +
                                 iconButton("pause", "[[player.osd.pause]]", "pause", true) +
                                 iconButton("forward10", "+10 с", "fwd10") + iconButton("next", "Далее", "next"),
                             40);
                   b += "<div style=\"height: 40dp;\"/>" + heading("Чипы и значки");
                   b += hrow(chip("Русский", "audio", true, "track:ru") + chip("English", "audio", false, "track:en") +
                             chip("Субтитры", "subtitles", std::nullopt, "subs") + chip("1080p", {}, std::nullopt, "q"));
                   b += hrow(badge(BadgeKind::Provider, "torrplayer", "tp") + sourceChip("AniLiberty") +
                                 badge(BadgeKind::Live, {}) + badge(BadgeKind::Quality, "4K") +
                                 badge(BadgeKind::Quality, "HDR") + badge(BadgeKind::New, {}) +
                                 badge(BadgeKind::Premiere, {}) + badge(BadgeKind::Subtitles, {}) +
                                 badge(BadgeKind::Explicit, {}),
                             16);
                   return safe(b);
                 }});

  // list items, value rows, toggles and a slider; the settings values come from a data model
  out.push_back({"lists", [] {
                   std::string l = "<div class=\"hstack\" style=\"gap: 64dp; align-items: flex-start;\"><div style=\"width: 820dp;\">" +
                                   heading("[[settings.title]]") + "<div class=\"list\">";
                   l += valueRow("[[settings.language]]", "[[settings.language.ru]]", "lang");
                   l += valueRow("[[settings.subtitles.size]]", "M", "subs");
                   l += valueRow("[[settings.overscan]]", "0 %", "overscan");
                   l += toggle({"[[settings.reduced_motion]]", {}, false, "reduced", false, "t-reduced"});
                   l += toggle({"[[settings.auto_skip_openings]]", {}, true, "skip", true, "t-skip"});
                   l += "</div></div><div style=\"width: 820dp;\">" + heading("[[audio.queue]]") + "<div class=\"list\">";
                   for (int i = 0; i < 3; ++i) {
                     ListItem it;
                     it.leadArt = "gen:art:q" + std::to_string(i);
                     it.title = kMusic[i].title;
                     it.meta = kMusic[i].meta;
                     it.trail = i == 0 ? "3:42" : "4:1" + std::to_string(i);
                     it.action = "play:" + std::to_string(i);
                     it.actions = "now|[[queue.play_next]]|play;remove|[[queue.remove]]|close";
                     l += listItem(it);
                   }
                   l += "</div><div style=\"height: 32dp;\"/>";
                   l += slider({"Громкость на кухне", 40, 0, 100, 5, "40 %", "volume", "s-volume"});
                   l += toggle({"Свет", {}, true, {}, false, {}});
                   l += "</div></div>";
                   return safe(l);
                 }});

  // progress, the scrubber, spinner and skeleton, the state block
  out.push_back({"progress", [] {
                   std::string p = heading("Прогресс");
                   p += "<div style=\"width: 900dp;\">" + progress(0.62) + "<div style=\"height: 32dp;\"/>" +
                        progress(std::nullopt) + "<div style=\"height: 32dp;\"/>" +
                        scrubber(0.38, 0.55, "41:12", "1:47:30") + "</div>";
                   p += "<div style=\"height: 48dp;\"/>" + hrow(spinner("[[resolving.getting_video]]") +
                                                                 button({"[[common.retry]]", "refresh", "retry", false, false, false, {}, "default-focus"}));
                   p += "<div class=\"hstack\" style=\"gap: 24dp;\">" + skeletonCard(CardKind::Poster) +
                        skeletonCard(CardKind::Poster) + "<div style=\"width: 64dp;\"/>" +
                        stateBlock("triangle", "[[common.load_failed]]", "TorrServer не ответил за 10 секунд.",
                                   Button{"[[common.retry]]", "refresh", "retry2", false, false, false, {}, {}}) +
                        "</div>";
                   return safe(p);
                 }});

  // a toast with and without its button
  out.push_back({"toast", [] { return safe(row(films("films"))); },
                 [](Context& c) {
                   c.ui.toast({"t1", ToastTone::Success, "Кухня включила: Вечерний эфир", "share_to_speaker", {}, {}, 60000});
                   c.ui.toast({"t2", ToastTone::Warning, "[[assistant.wake_word_needed]]", "locked", "retry", "[[common.retry]]", 60000});
                 }});

  // the confirm dialog: focus starts on the safe choice
  out.push_back({"dialog", [] { return safe(row(films("films"))); },
                 [](Context& c) {
                   c.ui.dialog({"Открыть входную дверь?", "Замок откроется, и дверь можно будет открыть снаружи.", "Открыть",
                                {}, nullptr, nullptr});
                 },
                 1});

  // a bottom sheet
  out.push_back({"sheet", [] { return safe(row(films("films"))); },
                 [](Context& c) {
                   std::string body = "<div class=\"list\">";
                   for (int i = 0; i < 3; ++i) body += chip(i == 0 ? "AniLibria" : i == 1 ? "AniDUB" : "Оригинал", "audio", i == 0, "vo:" + std::to_string(i));
                   body += "</div>";
                   c.ui.sheet("[[picker.voiceover]]", body);
                 },
                 1});

  // the actions sheet a long-press OK opens on a poster
  out.push_back({"actions", [] { return safe(row(films("films"))); },
                 [](Context& c) {
                   if (auto* e = c.ui.page()->GetElementById("films-1")) {
                     c.ui.focus().focus(e);
                     c.ui.actionsSheet(e, parseActions(e->GetAttribute<Rml::String>("data-actions", "")));
                   }
                 },
                 1});

  // the on-screen keyboard beside live results
  out.push_back({"keyboard", [] {
                   std::string k = "<div class=\"hstack\" style=\"gap: 64dp; align-items: flex-start;\">" +
                                   keyboard(KeyLayout::Cyrillic, "фри") + "<div class=\"grow\">" + heading("[[search.on_sources|count=3]]") +
                                   "<div class=\"list\">";
                   for (int i = 0; i < 4; ++i) {
                     ListItem it;
                     it.leadArt = "gen:art:s" + std::to_string(i);
                     it.title = kFilms[i].title;
                     it.meta = std::string("tp · AniLiberty · ") + kFilms[i].meta;
                     it.action = "result:" + std::to_string(i);
                     k += listItem(it);
                   }
                   return safe(k + "</div></div></div>");
                 }});

  // reduced motion: no scale, the ring, glow, title and line remain
  out.push_back({"reduced", [] { return safe(column(row(films("films")) + row(music("music")))); },
                 [](Context& c) { c.ui.setReducedMotion(true); }});

  for (const auto& f : cardFixtures()) out.push_back(cardPage(f));

  // a card naming a feature this screen lacks, and one over the focus limit: both drawn as their fallback
  out.push_back({"card-fallbacks", [] { return safe("<div id=\"cards\" class=\"cstack\"/>"); },
                 [](Context& c) {
                   c.cards.attach(c.ui.page()->GetElementById("cards"), cards::kMaxDrawerCards, false);
                   const auto bytes = read(fixturePath("card_light_living.binpb"));
                   auto card = cards::decodeCard({reinterpret_cast<const std::uint8_t*>(bytes.data()), bytes.size()});
                   if (!card) return;
                   // a feature from a newer minor version
                   static char feature[] = "hologram";
                   static char* features[] = {feature};
                   auto* saved = card->features;
                   const auto savedCount = card->features_count;
                   card->features = features;
                   card->features_count = 1;
                   c.cards.show(*card);
                   card->features = saved;
                   card->features_count = savedCount;
                 }});
  return out;
}

}  // namespace cadence::screen::ui::preview
