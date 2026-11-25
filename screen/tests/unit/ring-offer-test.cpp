#include "player/ring-offer.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <catch2/catch_test_macros.hpp>

#include <pb_encode.h>

#include <sys/eventfd.h>
#include <sys/socket.h>
#include <unistd.h>

#include <array>
#include <cstring>
#include <vector>

using namespace cadence::screen::player;

namespace {

std::vector<std::uint8_t> offerFrame(const char* id, std::uint32_t capacity) {
  cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
  envelope.which_body = cadence_audiod_v1_TapEnvelope_ring_offer_tag;
  std::strncpy(envelope.body.ring_offer.session_id, id, sizeof envelope.body.ring_offer.session_id - 1);
  envelope.body.ring_offer.capacity_frames = capacity;
  envelope.body.ring_offer.rate = 48000;
  envelope.body.ring_offer.channels = 2;
  std::vector<std::uint8_t> frame(256);
  pb_ostream_t stream = pb_ostream_from_buffer(frame.data(), frame.size());
  REQUIRE(pb_encode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &envelope));
  frame.resize(stream.bytes_written);
  return frame;
}

// what audiod's sendRingOffer does: the frame with the fds as SCM_RIGHTS, [0] the memfd, [1] the eventfd
void sendWithFds(int socket, const std::vector<std::uint8_t>& frame, std::vector<int> fds) {
  iovec data{const_cast<std::uint8_t*>(frame.data()), frame.size()};
  alignas(cmsghdr) std::array<char, CMSG_SPACE(sizeof(int) * 4)> control{};
  msghdr message{};
  message.msg_iov = &data;
  message.msg_iovlen = 1;
  if (!fds.empty()) {
    message.msg_control = control.data();
    message.msg_controllen = CMSG_SPACE(sizeof(int) * fds.size());
    cmsghdr* header = CMSG_FIRSTHDR(&message);
    header->cmsg_level = SOL_SOCKET;
    header->cmsg_type = SCM_RIGHTS;
    header->cmsg_len = CMSG_LEN(sizeof(int) * fds.size());
    std::memcpy(CMSG_DATA(header), fds.data(), sizeof(int) * fds.size());
  }
  REQUIRE(::sendmsg(socket, &message, MSG_NOSIGNAL) == static_cast<ssize_t>(frame.size()));
}

struct Pair {
  Pair() { REQUIRE(::socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, fds.data()) == 0); }
  ~Pair() {
    ::close(fds[0]);
    ::close(fds[1]);
  }
  std::array<int, 2> fds{};
};

}  // namespace

TEST_CASE("a ring offered over the tap maps the very ring its maker holds, with its eventfd") {
  std::string error;
  auto made = PcmRing::create(8192, &error);  // audiod's side, made here
  REQUIRE(made);
  Pair pair;
  sendWithFds(pair.fds[0], offerFrame("video-7", 8192), {made->ringFd(), made->eventFd()});

  auto offer = receiveRingOffer(pair.fds[1], &error);
  INFO(error);
  REQUIRE(offer);
  CHECK(offer->sessionId == "video-7");
  CHECK(offer->ring.header()->capacity == 8192);
  CHECK(offer->ring.ringFd() != made->ringFd());

  // the writer's line written through one mapping is seen through the other
  const_cast<cadence_ring_header*>(offer->ring.header())->write_pos = 480;
  CHECK(made->header()->write_pos == 480);
  REQUIRE(::eventfd_write(made->eventFd(), 2) == 0);
  eventfd_t value = 0;
  REQUIRE(::eventfd_read(offer->ring.eventFd(), &value) == 0);
  CHECK(value == 2);
}

TEST_CASE("a ring offer without its two fds, or that does not match its ring, is refused") {
  std::string error;
  auto made = PcmRing::create(4096, &error);
  REQUIRE(made);
  Pair pair;

  sendWithFds(pair.fds[0], offerFrame("video-7", 4096), {made->ringFd()});
  CHECK_FALSE(receiveRingOffer(pair.fds[1], &error));
  CHECK(error == "fds");

  sendWithFds(pair.fds[0], offerFrame("video-7", 8192), {made->ringFd(), made->eventFd()});
  CHECK_FALSE(receiveRingOffer(pair.fds[1], &error));
  CHECK(error == "size");

  // the fds swapped: the eventfd does not map as a ring
  sendWithFds(pair.fds[0], offerFrame("video-7", 4096), {made->eventFd(), made->ringFd()});
  CHECK_FALSE(receiveRingOffer(pair.fds[1], &error));
  CHECK_FALSE(error.empty());

  // another tap body is not an offer
  cadence_audiod_v1_TapEnvelope bands = cadence_audiod_v1_TapEnvelope_init_zero;
  bands.which_body = cadence_audiod_v1_TapEnvelope_bands_tag;
  bands.body.bands.bands.size = 16;
  std::vector<std::uint8_t> frame(64);
  pb_ostream_t stream = pb_ostream_from_buffer(frame.data(), frame.size());
  REQUIRE(pb_encode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &bands));
  frame.resize(stream.bytes_written);
  sendWithFds(pair.fds[0], frame, {});
  CHECK_FALSE(receiveRingOffer(pair.fds[1], &error));
  CHECK(error == "not_offer");
}
