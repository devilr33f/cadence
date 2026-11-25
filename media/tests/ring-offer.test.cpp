#include "audiod/ring-offer.h"
#include "ring/ring-input.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <catch2/catch_test_macros.hpp>

#include <pb_decode.h>

#include <sys/eventfd.h>
#include <sys/mman.h>
#include <sys/socket.h>
#include <unistd.h>

#include <array>
#include <cstdint>
#include <cstring>
#include <string>

using namespace cadence;
using namespace cadence::audiod;

namespace {

struct Received {
  cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
  std::array<int, 4> fds{-1, -1, -1, -1};
  std::size_t count = 0;
};

Received receive(int socket) {
  Received out;
  std::array<std::uint8_t, 256> frame{};
  iovec data{frame.data(), frame.size()};
  alignas(cmsghdr) std::array<char, CMSG_SPACE(sizeof(int) * 4)> control{};
  msghdr message{};
  message.msg_iov = &data;
  message.msg_iovlen = 1;
  message.msg_control = control.data();
  message.msg_controllen = control.size();
  const auto size = ::recvmsg(socket, &message, MSG_CMSG_CLOEXEC);
  REQUIRE(size > 0);
  for (auto* header = CMSG_FIRSTHDR(&message); header != nullptr; header = CMSG_NXTHDR(&message, header)) {
    if (header->cmsg_level != SOL_SOCKET || header->cmsg_type != SCM_RIGHTS)
      continue;
    out.count = (header->cmsg_len - CMSG_LEN(0)) / sizeof(int);
    std::memcpy(out.fds.data(), CMSG_DATA(header), out.count * sizeof(int));
  }
  pb_istream_t stream = pb_istream_from_buffer(frame.data(), static_cast<std::size_t>(size));
  REQUIRE(pb_decode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &out.envelope));
  return out;
}

}  // namespace

TEST_CASE("a ring offer carries the session's memfd and eventfd over the socket, in that order") {
  auto made = RingInput::create(8192);
  REQUIRE(made.ok());
  auto& ring = *made.value();
  std::array<int, 2> pair{};
  REQUIRE(::socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, pair.data()) == 0);

  REQUIRE(sendRingOffer(pair[0], "video-7", ring) == 0);
  const auto got = receive(pair[1]);
  REQUIRE(got.envelope.which_body == cadence_audiod_v1_TapEnvelope_ring_offer_tag);
  const auto& offer = got.envelope.body.ring_offer;
  CHECK(std::string(offer.session_id) == "video-7");
  CHECK(offer.capacity_frames == 8192);
  CHECK(offer.rate == 48000);
  CHECK(offer.channels == 2);
  REQUIRE(got.count == kRingOfferFds);

  // the received memfd maps the very ring: its header, and a frame written through it reaches audiod's mapping
  const auto bytes = cadence_ring_bytes(offer.capacity_frames);
  void* map = ::mmap(nullptr, bytes, PROT_READ | PROT_WRITE, MAP_SHARED, got.fds[kRingOfferFdRing], 0);
  REQUIRE(map != MAP_FAILED);
  auto* header = static_cast<cadence_ring_header*>(map);
  CHECK(header->magic == CADENCE_RING_MAGIC);
  CHECK(header->capacity == 8192);
  CADENCE_RING_STORE(&header->writer_state, static_cast<std::uint32_t>(CADENCE_RING_PAUSED));
  CHECK(ring.status().writerState == CADENCE_RING_PAUSED);

  // and the eventfd is the reader's: a count added on audiod's side is read on the screen's
  REQUIRE(::eventfd_write(ring.eventFd(), 3) == 0);
  eventfd_t value = 0;
  REQUIRE(::eventfd_read(got.fds[kRingOfferFdEvent], &value) == 0);
  CHECK(value == 3);

  ::munmap(map, bytes);
  for (std::size_t i = 0; i < got.count; ++i)
    ::close(got.fds[i]);
  ::close(pair[0]);
  ::close(pair[1]);
}

TEST_CASE("a ring offer refuses an id the frame cannot hold") {
  auto made = RingInput::create(256);
  REQUIRE(made.ok());
  std::array<std::uint8_t, 256> frame{};
  CHECK(encodeRingOffer(std::string(65, 'x'), *made.value(), frame) == 0);
  CHECK(encodeRingOffer("", *made.value(), frame) == 0);
  CHECK(encodeRingOffer("video-1", *made.value(), frame) > 0);
}
