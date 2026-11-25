#include "pulse-shim/abi.h"
#include "pulse-shim/guard.h"

#include <cstring>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

namespace cadence::pulse {

namespace {

// string properties a client attaches; nothing reads them beyond keeping the copy the api promises
struct Proplist {
  std::vector<std::pair<std::string, std::string>> entries;

  void set(std::string_view key, std::string_view value) {
    for (auto& [k, v] : entries)
      if (k == key) {
        v = value;
        return;
      }
    entries.emplace_back(key, value);
  }
};

// keys are printable ascii
bool validKey(std::string_view key) {
  if (key.empty())
    return false;
  for (const auto c : key)
    if (static_cast<unsigned char>(c) < 0x20 || static_cast<unsigned char>(c) > 0x7e)
      return false;
  return true;
}

// well-formed utf-8: no overlong forms, no surrogates, nothing past U+10FFFF
bool validUtf8(std::string_view text) {
  std::size_t i = 0;
  while (i < text.size()) {
    const auto lead = static_cast<unsigned char>(text[i]);
    std::size_t length = 0;
    unsigned min = 0;
    unsigned code = 0;
    if (lead < 0x80) {
      ++i;
      continue;
    } else if ((lead & 0xe0) == 0xc0) {
      length = 2;
      min = 0x80;
      code = lead & 0x1fU;
    } else if ((lead & 0xf0) == 0xe0) {
      length = 3;
      min = 0x800;
      code = lead & 0x0fU;
    } else if ((lead & 0xf8) == 0xf0) {
      length = 4;
      min = 0x10000;
      code = lead & 0x07U;
    } else {
      return false;
    }
    if (i + length > text.size())
      return false;
    for (std::size_t j = 1; j < length; ++j) {
      const auto next = static_cast<unsigned char>(text[i + j]);
      if ((next & 0xc0) != 0x80)
        return false;
      code = code << 6 | (next & 0x3fU);
    }
    if (code < min || code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff))
      return false;
    i += length;
  }
  return true;
}

}  // namespace

}  // namespace cadence::pulse

using namespace cadence::pulse;

extern "C" {

pa_proplist* pa_proplist_new(void) {
  return guarded(static_cast<pa_proplist*>(nullptr), [] { return reinterpret_cast<pa_proplist*>(new Proplist()); });
}

void pa_proplist_free(pa_proplist* p) {
  guarded([&] { delete reinterpret_cast<Proplist*>(p); });
}

int pa_proplist_sets(pa_proplist* p, const char* key, const char* value) {
  return guarded(-1, [&] {
    if (p == nullptr || key == nullptr || value == nullptr)
      return -1;
    const std::string_view k(key);
    const std::string_view v(value);
    if (!validKey(k) || !validUtf8(v))
      return -1;
    reinterpret_cast<Proplist*>(p)->set(k, v);
    return 0;
  });
}

}  // extern "C"
