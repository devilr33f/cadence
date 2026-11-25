#include "i18n/locale.h"

#include <array>
#include <cmath>
#include <cstdio>

namespace cadence::i18n {

namespace {

bool ru_rules(Lang l) { return l == Lang::Ru; }

std::string pad2(std::int64_t n) { return (n < 10 ? "0" : "") + std::to_string(n); }

struct UnitForms {
  std::string_view short_form;
  // one, few, many, other
  std::array<std::string_view, 4> long_forms;
};

constexpr std::array<UnitForms, 3> kEnUnits{{
    {"h", {"hour", "hours", "hours", "hours"}},
    {"min", {"minute", "minutes", "minutes", "minutes"}},
    {"s", {"second", "seconds", "seconds", "seconds"}},
}};

constexpr std::array<UnitForms, 3> kRuUnits{{
    {"ч", {"час", "часа", "часов", "часа"}},
    {"мин", {"минута", "минуты", "минут", "минуты"}},
    {"с", {"секунда", "секунды", "секунд", "секунды"}},
}};

std::size_t form_index(Plural p) {
  switch (p) {
    case Plural::One:
      return 0;
    case Plural::Few:
      return 1;
    case Plural::Many:
      return 2;
    default:
      return 3;
  }
}

}  // namespace

std::string plain_number(std::int64_t n) { return std::to_string(n); }

std::string plain_number(double d) {
  if (!std::isfinite(d)) return "0";
  std::array<char, 400> buf{};
  std::snprintf(buf.data(), buf.size(), "%.3f", d);
  std::string s(buf.data());
  while (!s.empty() && s.back() == '0') s.pop_back();
  if (!s.empty() && s.back() == '.') s.pop_back();
  if (s == "-0") return "0";
  return s;
}

std::string localize_number(Lang l, std::string_view plain) {
  const std::string_view group = ru_rules(l) ? kNbsp : ",";
  const std::string_view dec = ru_rules(l) ? "," : ".";
  std::string out;
  if (!plain.empty() && plain.front() == '-') {
    out.push_back('-');
    plain.remove_prefix(1);
  }
  const std::size_t dot = plain.find('.');
  const std::string_view int_part = plain.substr(0, dot);
  for (std::size_t i = 0; i < int_part.size(); ++i) {
    if (i > 0 && (int_part.size() - i) % 3 == 0) out += group;
    out.push_back(int_part[i]);
  }
  if (dot != std::string_view::npos) {
    out += dec;
    out += plain.substr(dot + 1);
  }
  return out;
}

std::string count(Lang l, std::int64_t n) { return localize_number(l, plain_number(n)); }

std::string decimal(Lang l, double d) { return localize_number(l, plain_number(d)); }

std::string clock(Lang l, int hour, int minute, HourCycle cycle) {
  hour = ((hour % 24) + 24) % 24;
  minute = ((minute % 60) + 60) % 60;
  if (cycle == HourCycle::Default) cycle = ru_rules(l) ? HourCycle::H23 : HourCycle::H12;
  if (cycle == HourCycle::H23) return pad2(hour) + ":" + pad2(minute);
  const int h = hour % 12 == 0 ? 12 : hour % 12;
  return std::to_string(h) + ":" + pad2(minute) + std::string(kNbsp) + (hour >= 12 ? "PM" : "AM");
}

std::string countdown(std::int64_t seconds) { return elapsed(seconds, seconds); }

std::string elapsed(std::int64_t seconds, std::int64_t length) {
  if (seconds < 0) seconds = 0;
  const std::int64_t h = seconds / 3600, m = seconds / 60 % 60, s = seconds % 60;
  if (h > 0 || length >= 3600) return std::to_string(h) + ":" + pad2(m) + ":" + pad2(s);
  return std::to_string(seconds / 60) + ":" + pad2(s);
}

std::string duration(Lang l, std::int64_t seconds, DurationStyle style) {
  if (seconds < 0) seconds = 0;
  const auto& units = ru_rules(l) ? kRuUnits : kEnUnits;
  auto part = [&](std::int64_t n, std::size_t unit) {
    std::string s = count(l, n) + std::string(kNbsp);
    if (style == DurationStyle::Short) return s + std::string(units[unit].short_form);
    return s + std::string(units[unit].long_forms[form_index(plural_of(l, plain_number(n)))]);
  };
  const std::int64_t h = seconds / 3600, m = seconds / 60 % 60;
  if (h > 0 && m > 0) return part(h, 0) + " " + part(m, 1);
  if (h > 0) return part(h, 0);
  if (seconds >= 60) return part(seconds / 60, 1);
  return part(seconds, 2);
}

}  // namespace cadence::i18n
