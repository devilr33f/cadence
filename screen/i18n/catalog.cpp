#include "i18n/catalog.h"

#include <algorithm>

namespace cadence::i18n {

std::optional<std::size_t> find_key(std::span<const CatalogEntry> entries, std::string_view key) {
  const auto it = std::lower_bound(entries.begin(), entries.end(), key,
                                   [](const CatalogEntry& e, std::string_view k) { return e.key < k; });
  if (it == entries.end() || it->key != key) return std::nullopt;
  return static_cast<std::size_t>(it - entries.begin());
}

}  // namespace cadence::i18n
