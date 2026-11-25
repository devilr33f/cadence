#pragma once

#include "ring/cadence_ring.h"

#include <cstdint>
#include <optional>
#include <string>

namespace cadence::screen::player {

// ao_cadence's ring (the writer's side is mpv's): a sealed memfd holding the header and capacity frames of f32
// stereo 48 kHz, and the eventfd the reader signals each period. audiod (the reader) makes both and offers them over
// its tap (RingOffer, player/ring-offer.h), which adopt maps; create makes one here, for a screen with no audiod and
// the tests
class PcmRing {
 public:
  // capacity in frames, a power of two (8192 = 170 ms, as s5 ran it); nullopt and an errno token when it fails
  static std::optional<PcmRing> create(std::uint32_t capacity, std::string* error);
  // maps a ring another process made, taking both fds (closed on failure too): refused unless its size and header
  // say what the offer did (magic, version, rate, channels, capacity, data offset)
  static std::optional<PcmRing> adopt(int ringFd, int eventFd, std::uint32_t capacity, std::string* error);

  PcmRing(PcmRing&& other) noexcept;
  PcmRing& operator=(PcmRing&& other) noexcept;
  PcmRing(const PcmRing&) = delete;
  PcmRing& operator=(const PcmRing&) = delete;
  ~PcmRing();

  [[nodiscard]] int ringFd() const { return ringFd_; }
  [[nodiscard]] int eventFd() const { return eventFd_; }
  [[nodiscard]] const cadence_ring_header* header() const { return header_; }
  [[nodiscard]] std::uint64_t bytes() const { return bytes_; }

 private:
  PcmRing() = default;
  void release();

  int ringFd_ = -1;
  int eventFd_ = -1;
  cadence_ring_header* header_ = nullptr;
  std::uint64_t bytes_ = 0;
};

}  // namespace cadence::screen::player
