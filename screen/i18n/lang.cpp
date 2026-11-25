#include "i18n/lang.h"

#include <array>
#include <cctype>
#include <string>

namespace cadence::i18n {

Lang parse_lang(std::string_view tag) {
  std::string t;
  for (char c : tag) {
    if (c == '-' || c == '_') break;
    if (c == ' ' || c == '\t') continue;
    t.push_back(static_cast<char>(std::tolower(static_cast<unsigned char>(c))));
  }
  if (t == "ru") return Lang::Ru;
  if (t == "xx") return Lang::Pseudo;
  return Lang::En;
}

std::string_view lang_code(Lang l) {
  switch (l) {
    case Lang::Ru:
      return "ru";
    case Lang::Pseudo:
      return "xx";
    case Lang::En:
      break;
  }
  return "en";
}

std::string_view plural_name(Plural p) {
  switch (p) {
    case Plural::Zero:
      return "zero";
    case Plural::One:
      return "one";
    case Plural::Two:
      return "two";
    case Plural::Few:
      return "few";
    case Plural::Many:
      return "many";
    case Plural::Other:
      break;
  }
  return "other";
}

std::span<const Plural> plural_categories(Lang l) {
  static constexpr std::array kEn{Plural::One, Plural::Other};
  static constexpr std::array kRu{Plural::One, Plural::Few, Plural::Many, Plural::Other};
  if (l == Lang::Ru) return kRu;
  return kEn;
}

Plural plural_of(Lang l, std::string_view formatted) {
  if (!formatted.empty() && formatted.front() == '-') formatted.remove_prefix(1);
  if (formatted.find('.') != std::string_view::npos) return Plural::Other;
  int i100 = 0;
  for (char c : formatted) i100 = (i100 * 10 + (c - '0')) % 100;
  const int i10 = i100 % 10;
  if (l == Lang::Ru) {
    if (i10 == 1 && i100 != 11) return Plural::One;
    if (i10 >= 2 && i10 <= 4 && (i100 < 12 || i100 > 14)) return Plural::Few;
    return Plural::Many;
  }
  return formatted == "1" ? Plural::One : Plural::Other;
}

}  // namespace cadence::i18n
