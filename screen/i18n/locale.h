#pragma once

#include "i18n/lang.h"

#include <cstdint>
#include <string>
#include <string_view>

// what the screen formats itself, as pkg/i18n formats it for the server (i18n/testdata/format.json)
namespace cadence::i18n {

// separates ru's thousands, and a number from its unit or an AM/PM marker
inline constexpr std::string_view kNbsp = "\xc2\xa0";

// a number's digits with "." before a fraction, at most 3 fraction digits, trailing zeros dropped
std::string plain_number(std::int64_t n);
std::string plain_number(double d);
// a plain number grouped by three with the language's decimal sign
std::string localize_number(Lang l, std::string_view plain);

// 12,345 or 12 345
std::string count(Lang l, std::int64_t n);
// at most 3 fraction digits: 4.2 or 4,2
std::string decimal(Lang l, double d);

enum class HourCycle { Default, H12, H23 };
// en 9:05 PM, ru 21:05 (default is the language's own)
std::string clock(Lang l, int hour, int minute, HourCycle cycle = HourCycle::Default);
// 0:09, 4:05, 1:02:03; negative is 0:00
std::string countdown(std::int64_t seconds);
// m:ss, or h:mm:ss when it or the length beside it reaches an hour
std::string elapsed(std::int64_t seconds, std::int64_t length);

enum class DurationStyle { Short, Long };
// an hour or more as hours and minutes (minutes left out at 0), a minute or more as minutes, else seconds:
// en 2 h 5 min / 2 hours 5 minutes, ru 2 ч 5 мин / 2 часа 5 минут
std::string duration(Lang l, std::int64_t seconds, DurationStyle style = DurationStyle::Short);

}  // namespace cadence::i18n
