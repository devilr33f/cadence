#include "i18n/locale.h"
#include "i18n/message.h"

#include <catch2/catch_test_macros.hpp>
#include <nlohmann/json.hpp>

#include <fstream>
#include <string>
#include <vector>

using cadence::i18n::Arg;
using cadence::i18n::Lang;
using cadence::i18n::Message;
using cadence::i18n::Value;
namespace i18n = cadence::i18n;

namespace {

nlohmann::json cases() {
  std::ifstream f(CADENCE_I18N_CASES);
  REQUIRE(f.good());
  return nlohmann::json::parse(f);
}

}  // namespace

// the cases pkg/i18n answers too: the two formatters agree
TEST_CASE("shared message cases") {
  for (const auto& c : cases()["messages"]) {
    const std::string msg = c["msg"];
    INFO(msg);
    auto m = Message::parse(msg);
    REQUIRE(m);
    Lang lang = i18n::parse_lang(c["lang"].get<std::string>());
    if (c.value("pseudo", false)) {
      m = m->pseudolocalized();
      lang = Lang::Pseudo;
    }
    std::vector<std::string> texts;
    texts.reserve(16);
    std::vector<Arg> args;
    if (c.contains("args")) {
      for (const auto& [k, v] : c["args"].items()) {
        texts.push_back(k);
        const std::string_view name = texts.back();
        if (v.is_string()) {
          texts.push_back(v.get<std::string>());
          args.push_back(Arg{name, Value(std::string_view(texts.back()))});
        } else if (v.is_number_integer()) {
          args.push_back(Arg{name, Value(v.get<std::int64_t>())});
        } else {
          args.push_back(Arg{name, Value(v.get<double>())});
        }
      }
    }
    bool ok = true;
    CHECK(m->format(lang, args, &ok) == c["want"].get<std::string>());
    CHECK(ok == !c.value("error", false));
  }
}

TEST_CASE("shared invalid cases") {
  for (const auto& c : cases()["invalid"]) {
    const std::string s = c;
    INFO(s);
    auto m = Message::parse(s);
    CHECK((!m || !m->args()));
  }
}

TEST_CASE("shared helper cases") {
  for (const auto& c : cases()["helpers"]) {
    const std::string fn = c["fn"];
    const Lang l = i18n::parse_lang(c["lang"].get<std::string>());
    const auto& a = c["args"];
    INFO(fn << " " << a.dump());
    auto i = [&](std::size_t k) { return a[k].get<std::int64_t>(); };
    std::string got;
    if (fn == "count") got = i18n::count(l, i(0));
    else if (fn == "decimal") got = i18n::decimal(l, a[0].get<double>());
    else if (fn == "clock") got = i18n::clock(l, static_cast<int>(i(0)), static_cast<int>(i(1)));
    else if (fn == "clock24") got = i18n::clock(l, static_cast<int>(i(0)), static_cast<int>(i(1)), i18n::HourCycle::H23);
    else if (fn == "clock12") got = i18n::clock(l, static_cast<int>(i(0)), static_cast<int>(i(1)), i18n::HourCycle::H12);
    else if (fn == "countdown") got = i18n::countdown(i(0));
    else if (fn == "elapsed") got = i18n::elapsed(i(0), i(1));
    else if (fn == "duration") got = i18n::duration(l, i(0));
    else if (fn == "duration_long") got = i18n::duration(l, i(0), i18n::DurationStyle::Long);
    else FAIL("unknown helper " << fn);
    CHECK(got == c["want"].get<std::string>());
  }
}

TEST_CASE("parse errors carry an offset") {
  Message::ParseError err;
  CHECK_FALSE(Message::parse("ab {n, plural, one {x}}", &err));
  CHECK(err.offset > 3);
  CHECK(err.reason == "plural needs other");
}

TEST_CASE("plural rules") {
  using i18n::Plural;
  CHECK(i18n::plural_of(Lang::Ru, "21") == Plural::One);
  CHECK(i18n::plural_of(Lang::Ru, "112") == Plural::Many);
  CHECK(i18n::plural_of(Lang::Ru, "122") == Plural::Few);
  CHECK(i18n::plural_of(Lang::Ru, "1.5") == Plural::Other);
  CHECK(i18n::plural_of(Lang::Pseudo, "1") == Plural::One);
  CHECK(i18n::plural_of(Lang::En, "21") == Plural::Other);
}
