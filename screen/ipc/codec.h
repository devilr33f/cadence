#pragma once

#include "cadence/screen/v1/screen.pb.h"

#include <cstdint>
#include <deque>
#include <memory>
#include <span>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::screen::ipc {

using Envelope = cadence_screen_v1_Envelope;

// a decoded envelope: nanopb allocated its FT_POINTER fields (PB_ENABLE_MALLOC), which pb_release frees with it
struct DecodedDeleter {
  void operator()(Envelope* envelope) const;
};
using DecodedEnvelope = std::unique_ptr<Envelope, DecodedDeleter>;

// nullptr and a token in error (decode) when the frame is not an envelope
DecodedEnvelope decodeEnvelope(std::span<const std::uint8_t> frame, std::string* error);

// an envelope to send: its FT_POINTER fields point into strings it keeps, never into malloc'd memory, so it is never
// pb_release'd. the envelope struct is large (fixed-size fields), so it lives on the heap
class OutEnvelope {
 public:
  explicit OutEnvelope(pb_size_t whichBody);
  OutEnvelope(OutEnvelope&&) = default;
  OutEnvelope& operator=(OutEnvelope&&) = default;

  Envelope& get() { return *envelope_; }
  [[nodiscard]] const Envelope& get() const { return *envelope_; }
  Envelope* operator->() { return envelope_.get(); }

  // a nul-terminated copy that lives as long as this envelope, for a char* field
  char* keep(std::string_view text);
  // an array that lives as long as this envelope, zeroed, for a repeated FT_POINTER field
  template <typename T>
  T* keepArray(std::size_t count) {
    auto& block = blocks_.emplace_back(count * sizeof(T), std::uint8_t{0});
    return reinterpret_cast<T*>(block.data());
  }

 private:
  std::unique_ptr<Envelope> envelope_;
  std::deque<std::string> strings_;
  std::deque<std::vector<std::uint8_t>> blocks_;
};

// false when nanopb refuses it (a field over its bound)
bool encodeEnvelope(const Envelope& envelope, std::vector<std::uint8_t>& frame);

// copies text into a fixed char[n] field, cut at n - 1 bytes
template <std::size_t N>
void copyField(char (&field)[N], std::string_view text) {
  const std::size_t n = text.size() < N - 1 ? text.size() : N - 1;
  for (std::size_t i = 0; i < n; i++) field[i] = text[i];
  field[n] = '\0';
}

// the envelope's body as a token (media_play, hello, ...), for logs
const char* bodyName(pb_size_t whichBody);

}  // namespace cadence::screen::ipc
