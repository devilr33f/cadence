#include "app/routes/player/cache-info.h"

#include "i18n/locale.h"

#include <algorithm>
#include <cmath>

namespace cadence::screen::routes {

std::string speedText(i18n::Lang lang, std::uint64_t bps) {
  if (bps == 0) return "";
  const bool ru = lang == i18n::Lang::Ru;
  const std::string sep(i18n::kNbsp);
  // a megabyte and more with one decimal, below as whole kilobytes (at least 1)
  if (bps >= 1000 * 1000) {
    const double mb = std::round(static_cast<double>(bps) / 100000.0) / 10.0;
    return i18n::decimal(lang, mb) + sep + (ru ? "МБ/с" : "MB/s");
  }
  const auto kb = std::max<std::uint64_t>(1, (bps + 500) / 1000);
  return i18n::count(lang, static_cast<std::int64_t>(kb)) + sep + (ru ? "КБ/с" : "KB/s");
}

std::string cacheLine(i18n::Lang lang, std::uint64_t bufferedMs, std::optional<std::uint64_t> bps) {
  std::string out;
  if (bufferedMs >= 1000) {
    out = "[[player.osd.buffer_ahead|time=" + i18n::countdown(static_cast<std::int64_t>(bufferedMs / 1000)) + "]]";
  }
  if (bps) {
    const auto speed = speedText(lang, *bps);
    if (!speed.empty()) out += (out.empty() ? "" : " · ") + speed;
  }
  return out;
}

}  // namespace cadence::screen::routes
