#include "audiod/ring-offer.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <pb_encode.h>

#include <sys/socket.h>

#include <array>
#include <cerrno>
#include <cstring>

namespace cadence::audiod {

std::size_t encodeRingOffer(std::string_view sessionId, const RingInput& ring, std::span<std::uint8_t> out) {
  cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
  envelope.which_body = cadence_audiod_v1_TapEnvelope_ring_offer_tag;
  auto& offer = envelope.body.ring_offer;
  if (sessionId.empty() || sessionId.size() >= sizeof offer.session_id)
    return 0;
  std::memcpy(offer.session_id, sessionId.data(), sessionId.size());
  offer.capacity_frames = ring.capacity();
  offer.rate = CADENCE_RING_RATE;
  offer.channels = CADENCE_RING_CHANNELS;
  pb_ostream_t stream = pb_ostream_from_buffer(out.data(), out.size());
  if (!pb_encode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &envelope))
    return 0;
  return stream.bytes_written;
}

int sendRingOffer(int socket, std::string_view sessionId, const RingInput& ring) {
  std::array<std::uint8_t, cadence_audiod_v1_RingOffer_size + 8> frame{};
  const auto size = encodeRingOffer(sessionId, ring, frame);
  if (size == 0)
    return EMSGSIZE;
  iovec data{frame.data(), size};
  alignas(cmsghdr) std::array<char, CMSG_SPACE(sizeof(int) * kRingOfferFds)> control{};
  msghdr message{};
  message.msg_iov = &data;
  message.msg_iovlen = 1;
  message.msg_control = control.data();
  message.msg_controllen = control.size();
  cmsghdr* header = CMSG_FIRSTHDR(&message);
  header->cmsg_level = SOL_SOCKET;
  header->cmsg_type = SCM_RIGHTS;
  header->cmsg_len = CMSG_LEN(sizeof(int) * kRingOfferFds);
  std::array<int, kRingOfferFds> fds{};
  fds[kRingOfferFdRing] = ring.ringFd();
  fds[kRingOfferFdEvent] = ring.eventFd();
  std::memcpy(CMSG_DATA(header), fds.data(), sizeof fds);
  for (;;) {
    if (::sendmsg(socket, &message, MSG_NOSIGNAL | MSG_DONTWAIT) >= 0)
      return 0;
    if (errno != EINTR)
      return errno;
  }
}

}  // namespace cadence::audiod
