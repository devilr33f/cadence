#pragma once

#include "player/pcm-ring.h"

#include <cstdint>
#include <optional>
#include <span>
#include <string>

namespace cadence::screen::player {

// audiod's RingOffer (audiod.proto) as the tap's client takes it: a MIXER session's pcm ring, which the session's
// player (mpv's ao_cadence) writes. the frame's SCM_RIGHTS carry [0] the memfd and [1] the eventfd
struct RingOffer {
  std::string sessionId;
  PcmRing ring;
};

// a tap frame and the fds that came with it into a RingOffer, taking the fds (each closed when refused): nullopt and
// a token for another body (not_offer), a frame that does not decode (decode), other than two fds (fds), or a ring
// that does not match its offer (PcmRing::adopt's tokens)
std::optional<RingOffer> ringOfferFrom(std::span<const std::uint8_t> frame, std::span<const int> fds,
                                       std::string* error);

// reads one tap frame from socket (SOCK_SEQPACKET) with its fds as ringOfferFrom takes them; eof and errno tokens too
std::optional<RingOffer> receiveRingOffer(int socket, std::string* error);

}  // namespace cadence::screen::player
