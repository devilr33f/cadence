#pragma once

#include <span>
#include <string_view>

namespace cadence::i18n {

enum class Lang {
  En,
  Ru,
  // pseudo-locale xx, made from en: never a setting's value
  Pseudo,
};

// a language setting or tag (ru, ru-RU, ru_RU, RU) as a supported language; anything else is en
Lang parse_lang(std::string_view tag);
std::string_view lang_code(Lang l);

enum class Plural { Zero, One, Two, Few, Many, Other };

std::string_view plural_name(Plural p);
// the cldr cardinal categories a language has: en one/other, ru one/few/many/other (xx takes en's)
std::span<const Plural> plural_categories(Lang l);
// the category of a number as shown: digits with "." before a visible fraction (v != 0 takes the fraction rules)
Plural plural_of(Lang l, std::string_view formatted);

}  // namespace cadence::i18n
