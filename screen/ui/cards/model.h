#pragma once

#include "cadence/cards/v1/cards.pb.h"

#include <cstddef>
#include <cstdint>
#include <memory>
#include <span>
#include <string>
#include <string_view>
#include <vector>

// cadence cards on the device (design r2 section 17): decoding (nanopb, every field a pointer: cards.options), the
// device's limits and the features -> fallback rule. a card the device cannot draw whole is drawn as a plain card
// of its fallback text with the «недоступно на этом экране» line; the reason is a counter token, never logged with
// the card's text
namespace cadence::screen::ui::cards {

// the vocabulary versions this screen renders (Hello.card_versions)
inline constexpr std::uint32_t kVersion = 1;
std::vector<std::uint32_t> versions();
// every primitive by its Node oneof name and every optional field (Hello.card_features; pkg/cards.Known())
std::vector<std::string> features();
[[nodiscard]] bool knownFeature(std::string_view feature);

// device limits (pkg/cards' limits.go)
inline constexpr int kMaxDepth = 4;
inline constexpr int kMaxNodes = 64;
inline constexpr int kMaxFocusables = 12;
inline constexpr std::size_t kMaxCardBytes = 16u << 10;
inline constexpr std::size_t kMaxPageBytes = 128u << 10;
inline constexpr int kMaxPageCards = 20;
inline constexpr int kMaxListItems = 20;
inline constexpr int kMaxDrawerCards = 6;

// a decoded card or page, released with pb_release
struct CardDeleter {
  void operator()(cadence_cards_v1_Card* c) const;
};
struct PageDeleter {
  void operator()(cadence_cards_v1_Page* p) const;
};
using CardPtr = std::unique_ptr<cadence_cards_v1_Card, CardDeleter>;
using PagePtr = std::unique_ptr<cadence_cards_v1_Page, PageDeleter>;

// nullptr when the bytes are over the limit or do not decode
CardPtr decodeCard(std::span<const std::uint8_t> bytes);
PagePtr decodePage(std::span<const std::uint8_t> bytes);
// a deep copy through encode and decode (the envelope that carried it is released after the message)
CardPtr copyCard(const cadence_cards_v1_Card& card);
// the card's encoded size
std::size_t encodedSize(const cadence_cards_v1_Card& card);

// why a card is drawn as its fallback: "" when it is drawn whole
struct Verdict {
  std::string reason;
  int nodes = 0;
  int focusables = 0;
  int depth = 0;
  [[nodiscard]] bool ok() const { return reason.empty(); }
};
Verdict check(const cadence_cards_v1_Card& card);

// field access for nanopb's pointer fields
inline std::string_view str(const char* s) { return s != nullptr ? std::string_view(s) : std::string_view(); }
template <class T>
T val(const T* p, T fallback = T{}) {
  return p != nullptr ? *p : fallback;
}
// code points
std::size_t runes(std::string_view s);

}  // namespace cadence::screen::ui::cards
