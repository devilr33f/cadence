#include "player/ring-offer.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <pb_decode.h>

#include <sys/socket.h>
#include <unistd.h>

#include <array>
#include <cerrno>
#include <cstring>
#include <vector>

namespace cadence::screen::player {

namespace {

// as audiod.proto orders them
constexpr std::size_t kFdRing = 0;
constexpr std::size_t kFdEvent = 1;
constexpr std::size_t kFds = 2;
// more than audiod ever sends, so a frame with extra fds is seen and refused rather than truncated
constexpr std::size_t kMaxFds = 4;

void closeAll(std::span<const int> fds) {
  for (const int fd : fds)
    if (fd >= 0) ::close(fd);
}

void refuse(std::string* error, const char* token) {
  if (error != nullptr) *error = token;
}

}  // namespace

std::optional<RingOffer> ringOfferFrom(std::span<const std::uint8_t> frame, std::span<const int> fds,
                                       std::string* error) {
  // the tap envelope's other bodies are fixed size; a pcm snapshot is the largest of them
  cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
  pb_istream_t stream = pb_istream_from_buffer(frame.data(), frame.size());
  if (!pb_decode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &envelope)) {
    closeAll(fds);
    refuse(error, "decode");
    return std::nullopt;
  }
  if (envelope.which_body != cadence_audiod_v1_TapEnvelope_ring_offer_tag) {
    closeAll(fds);
    refuse(error, "not_offer");
    return std::nullopt;
  }
  if (fds.size() != kFds) {
    closeAll(fds);
    refuse(error, "fds");
    return std::nullopt;
  }
  const auto& offer = envelope.body.ring_offer;
  if (offer.session_id[0] == '\0' || offer.rate != CADENCE_RING_RATE || offer.channels != CADENCE_RING_CHANNELS) {
    closeAll(fds);
    refuse(error, "offer");
    return std::nullopt;
  }
  auto ring = PcmRing::adopt(fds[kFdRing], fds[kFdEvent], offer.capacity_frames, error);
  if (!ring) return std::nullopt;
  return RingOffer{std::string(offer.session_id), std::move(*ring)};
}

std::optional<RingOffer> receiveRingOffer(int socket, std::string* error) {
  // a frame of any tap body fits, so a pcm snapshot read here is consumed whole and refused, not split
  // mirror frames are larger, but they never reach a socket that reads offers
  std::vector<std::uint8_t> frame(8192);
  iovec data{frame.data(), frame.size()};
  alignas(cmsghdr) std::array<char, CMSG_SPACE(sizeof(int) * kMaxFds)> control{};
  msghdr message{};
  message.msg_iov = &data;
  message.msg_iovlen = 1;
  message.msg_control = control.data();
  message.msg_controllen = control.size();
  ssize_t size = -1;
  do {
    size = ::recvmsg(socket, &message, MSG_CMSG_CLOEXEC);
  } while (size < 0 && errno == EINTR);
  if (size <= 0) {
    if (error != nullptr) *error = size == 0 ? "eof" : "recv_errno_" + std::to_string(errno);
    return std::nullopt;
  }
  std::vector<int> fds;
  for (auto* header = CMSG_FIRSTHDR(&message); header != nullptr; header = CMSG_NXTHDR(&message, header)) {
    if (header->cmsg_level != SOL_SOCKET || header->cmsg_type != SCM_RIGHTS) continue;
    const auto count = (header->cmsg_len - CMSG_LEN(0)) / sizeof(int);
    const auto first = fds.size();
    fds.resize(first + count);
    std::memcpy(fds.data() + first, CMSG_DATA(header), count * sizeof(int));
  }
  if ((message.msg_flags & (MSG_TRUNC | MSG_CTRUNC)) != 0) {
    closeAll(fds);
    refuse(error, "truncated");
    return std::nullopt;
  }
  return ringOfferFrom(std::span(frame.data(), static_cast<std::size_t>(size)), fds, error);
}

}  // namespace cadence::screen::player
