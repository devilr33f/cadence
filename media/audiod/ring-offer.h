#pragma once

#include "ring/ring-input.h"

#include <cstddef>
#include <cstdint>
#include <span>
#include <string_view>

namespace cadence::audiod {

// the fds a RingOffer frame carries as SCM_RIGHTS, in this order (audiod.proto's RingOffer)
inline constexpr std::size_t kRingOfferFdRing = 0;
inline constexpr std::size_t kRingOfferFdEvent = 1;
inline constexpr std::size_t kRingOfferFds = 2;

// the TapEnvelope{ring_offer} for the session's ring into out; its size, 0 when it does not fit or the id is too long
std::size_t encodeRingOffer(std::string_view sessionId, const RingInput& ring, std::span<std::uint8_t> out);

// sends a session's ring to the tap's client on socket (SOCK_SEQPACKET, non-blocking): one frame with the ring's memfd
// and eventfd as SCM_RIGHTS. the fds stay audiod's; the kernel gives the client its own copies. 0, or an errno
// (EMSGSIZE for an id the frame cannot hold)
int sendRingOffer(int socket, std::string_view sessionId, const RingInput& ring);

}  // namespace cadence::audiod
