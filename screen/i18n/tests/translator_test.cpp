#include "i18n/gen/keys.h"
#include "i18n/translator.h"

#include <catch2/catch_test_macros.hpp>

#include <array>
#include <string>

using cadence::i18n::Arg;
using cadence::i18n::Lang;
using cadence::i18n::Translator;
using cadence::i18n::Value;
namespace keys = cadence::i18n::keys;
namespace i18n = cadence::i18n;

// the built-in catalog: every message parses in every language, with en's arguments
TEST_CASE("every built-in message parses in every language") {
  const auto entries = i18n::builtin_entries();
  REQUIRE(entries.size() == keys::kCount);
  for (std::size_t i = 0; i < entries.size(); ++i) {
    INFO(entries[i].key);
    if (i > 0) CHECK(entries[i - 1].key < entries[i].key);
    auto en = i18n::Message::parse(entries[i].en);
    auto ru = i18n::Message::parse(entries[i].ru);
    REQUIRE(en);
    REQUIRE(ru);
    REQUIRE(en->args());
    REQUIRE(ru->args());
    CHECK(en->args()->size() == ru->args()->size());
  }
}

TEST_CASE("typed keys") {
  Translator t(Lang::Ru);
  CHECK(t.tr(keys::TasksCount{21}) == "21 задача");
  CHECK(t.tr(keys::TasksCount{5}) == "5 задач");
  CHECK(t.tr(keys::AudioUpNext{"Песня"}) == "Далее: Песня");
  CHECK(t.tr(keys::AssistantListening{"speaker", "Кухня"}) == "Слушаю через колонку «Кухня»");
  t.set_language(Lang::En);
  CHECK(t.tr(keys::TasksCount{1}) == "1 task");
  CHECK(t.tr(keys::TitleWatchEpisode{1300}) == "Watch: episode 1,300");
  CHECK(t.tr(keys::SettingsTitle{}) == "Settings");
}

TEST_CASE("pseudo-locale") {
  Translator t(Lang::Pseudo);
  CHECK(t.tr(keys::SettingsTitle{}) == "[Šéţţíñĝš··]");
  CHECK(t.tr(keys::TasksCount{2}) == "[2 ţášķš·]");
}

TEST_CASE("by name") {
  Translator t(Lang::Ru);
  const std::array args{Arg{"title", Value(std::string_view("X"))}};
  CHECK(t.tr("audio.up_next", args) == "Далее: X");
  CHECK(t.tr("no.such.key") == "no.such.key");
}

TEST_CASE("translate rewrites [[key]] for rmlui") {
  Translator t(Lang::Ru);
  std::string out;
  CHECK(t.translate(out, "plain text") == 0);
  CHECK(out == "plain text");
  CHECK(t.translate(out, "[[settings.title]]") == 1);
  CHECK(out == "Настройки");
  CHECK(t.translate(out, "<b>[[tasks.count|count=3]]</b> · [[audio.up_next|title=Песня]]") == 2);
  CHECK(out == "<b>3 задачи</b> · Далее: Песня");
  CHECK(t.translate(out, "[[tasks.count|count=1.5]]") == 1);
  CHECK(out == "1,5 задачи");
  CHECK(t.translate(out, "a [[no.such]] b [[settings.title]]") == 1);
  CHECK(out == "a [[no.such]] b Настройки");
  CHECK(t.translate(out, "[[unclosed") == 0);
  CHECK(out == "[[unclosed");
}

TEST_CASE("live switching notifies once per change") {
  Translator t(Lang::En);
  int calls = 0;
  Lang seen = Lang::En;
  t.on_change([&](Lang l) {
    ++calls;
    seen = l;
  });
  t.set_language(Lang::En);
  CHECK(calls == 0);
  CHECK(t.generation() == 0);
  t.set_language(Lang::Ru);
  CHECK(calls == 1);
  CHECK(seen == Lang::Ru);
  CHECK(t.generation() == 1);
  CHECK(t.tr(keys::SettingsTitle{}) == "Настройки");
}

TEST_CASE("language tags") {
  CHECK(i18n::parse_lang("ru-RU") == Lang::Ru);
  CHECK(i18n::parse_lang("RU") == Lang::Ru);
  CHECK(i18n::parse_lang("de") == Lang::En);
  CHECK(i18n::parse_lang("") == Lang::En);
  CHECK(i18n::parse_lang("xx") == Lang::Pseudo);
}

TEST_CASE("assistant failure reasons, ru and en") {
  Translator t(Lang::Ru);
  std::string out;
  t.translate(out, "[[assistant.failed_timeout_llm|seconds=10]]");
  CHECK(out == "Модель не ответила за 10 с");
  t.translate(out, "[[assistant.failed_rate_limited]]");
  CHECK(out == "Сервис модели ограничил число запросов");
  t.set_language(Lang::En);
  t.translate(out, "[[assistant.failed_timeout_llm|seconds=10]]");
  CHECK(out == "The model did not answer within 10 s");
  t.translate(out, "[[assistant.failed_http|status=502]]");
  CHECK(out == "The model provider answered with HTTP 502");
}

TEST_CASE("error reasons and hints, ru and en") {
  Translator t(Lang::Ru);
  std::string out;
  t.translate(out, "[[error.reason.timeout|seconds=10]] / [[error.hint.running|provider=torrplayer]]");
  CHECK(out == "Источник не ответил за 10 с / Проверь, что torrplayer запущен");
  t.translate(out, "[[error.reason.server|status=502]]");
  CHECK(out == "Ошибка на стороне источника (HTTP 502)");
  t.translate(out, "[[error.reason.auth]] / [[error.hint.token]]");
  CHECK(out == "Нет доступа к источнику / Проверь токен в настройках");
  t.translate(out, "[[error.reason.no_seeders]] / [[error.hint.other_release]]");
  CHECK(out == "Нет раздающих / Выбери другую раздачу или озвучку");
  t.set_language(Lang::En);
  t.translate(out, "[[error.reason.timeout|seconds=10]] / [[error.hint.running|provider=torrplayer]]");
  CHECK(out == "The source did not answer in 10 s / Check that torrplayer is running");
  t.translate(out, "[[error.reason.not_found]] / [[error.hint.other_source]]");
  CHECK(out == "Not found at this source / Try another source");
}
