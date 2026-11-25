#pragma once

#include "i18n/lang.h"

#include <cstddef>
#include <optional>
#include <span>
#include <string_view>

namespace cadence::i18n {

// one key's messages as written in i18n/screen/<lang>.json
struct CatalogEntry {
  std::string_view key;
  std::string_view en;
  std::string_view ru;
};

// i18n/screen built in (gen/catalog.cpp), sorted by key
std::span<const CatalogEntry> builtin_entries();

// a catalog's index of a key, by binary search
std::optional<std::size_t> find_key(std::span<const CatalogEntry> entries, std::string_view key);

}  // namespace cadence::i18n
