#pragma once

#include "i18n/lang.h"

#include <cstdint>
#include <optional>
#include <string>

namespace cadence::screen::routes {

// the cache's read speed in the language's units: 850 KB/s, 4.2 MB/s (ru 850 КБ/с, 4,2 МБ/с); empty for 0
std::string speedText(i18n::Lang lang, std::uint64_t bytesPerSecond);

// the osd's cache line (design §12's scrubber shows the range; this says it): how far ahead the cache reaches and what
// it reads now, as rml with the catalog's key, "Buffer 1:23 · 4.2 MB/s"; empty when the player says neither
std::string cacheLine(i18n::Lang lang, std::uint64_t bufferedMs, std::optional<std::uint64_t> bytesPerSecond);

}  // namespace cadence::screen::routes
