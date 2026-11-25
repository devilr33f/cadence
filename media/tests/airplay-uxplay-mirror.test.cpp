#include "audiod/airplay-uxplay-mirror.h"
#include "support/mirror-frames.h"

#include <catch2/catch_test_macros.hpp>

#include <sys/socket.h>
#include <unistd.h>

#include <array>
#include <cerrno>
#include <cstring>
#include <deque>
#include <initializer_list>
#include <vector>

using namespace cadence::audiod::uxplay;
using namespace cadence::test_support;

namespace {

using Bytes = std::vector<std::uint8_t>;

// annex b: each nal its 4-byte start code, then its header and a body
Bytes nals(std::initializer_list<Bytes> units) {
  Bytes out;
  for (const auto& unit : units) {
    out.insert(out.end(), {0, 0, 0, 1});
    out.insert(out.end(), unit.begin(), unit.end());
  }
  return out;
}

const Bytes kSps{0x67, 0x42, 0x00, 0x1f, 0xaa};
const Bytes kPps{0x68, 0xce, 0x3c, 0x80};
const Bytes kIdr{0x65, 0x88, 0x84, 0x00, 0x10};
const Bytes kSlice{0x41, 0x9a, 0x02, 0x33};

// a socketpair as the tap's socket: the forwarder's send, the screen's reads
class Screen {
 public:
  Screen() {
    REQUIRE(::socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, fds_.data()) == 0);
    const int buffer = 8 << 20;
    ::setsockopt(fds_[0], SOL_SOCKET, SO_SNDBUF, &buffer, sizeof buffer);
    ::setsockopt(fds_[1], SOL_SOCKET, SO_RCVBUF, &buffer, sizeof buffer);
  }
  ~Screen() {
    ::close(fds_[0]);
    ::close(fds_[1]);
  }
  Screen(const Screen&) = delete;
  Screen& operator=(const Screen&) = delete;

  MirrorSend send() {
    return [this](std::span<const std::uint8_t> encoded) {
      if (!connected)
        return SendResult::NoClient;
      if (full)
        return SendResult::Dropped;
      return ::send(fds_[0], encoded.data(), encoded.size(), MSG_DONTWAIT | MSG_NOSIGNAL) >= 0 ? SendResult::Sent
                                                                                                : SendResult::Dropped;
    };
  }

  std::deque<TapMessage> read() {
    std::deque<TapMessage> out;
    std::vector<std::uint8_t> buffer(1 << 20);
    for (;;) {
      const auto size = ::recv(fds_[1], buffer.data(), buffer.size(), MSG_DONTWAIT);
      if (size <= 0)
        return out;
      auto message = decodeTap({buffer.data(), static_cast<std::size_t>(size)});
      REQUIRE(message);
      out.push_back(std::move(*message));
    }
  }

  bool connected = true;
  bool full = false;

 private:
  std::array<int, 2> fds_{};
};

}  // namespace

TEST_CASE("an h264 access unit is cut into its parameter sets and its frame") {
  const auto unit = splitAccessUnit(nals({kSps, kPps, kIdr}), false);
  CHECK(unit.valid);
  CHECK(unit.keyframe);
  CHECK(unit.config == nals({kSps, kPps}));
  CHECK(unit.frame == nals({kIdr}));

  const auto slice = splitAccessUnit(nals({kSlice}), false);
  CHECK(slice.valid);
  CHECK_FALSE(slice.keyframe);
  CHECK(slice.config.empty());
  CHECK(slice.frame == nals({kSlice}));
}

TEST_CASE("an h265 access unit is cut by its own nal types") {
  const Bytes vps{0x40, 0x01, 0x0c};
  const Bytes sps{0x42, 0x01, 0x01};
  const Bytes pps{0x44, 0x01, 0xc0};
  const Bytes idr{0x26, 0x01, 0xaf};
  const Bytes trail{0x02, 0x01, 0xd0};
  const auto key = splitAccessUnit(nals({vps, sps, pps, idr}), true);
  CHECK(key.valid);
  CHECK(key.keyframe);
  CHECK(key.config == nals({vps, sps, pps}));
  CHECK(key.frame == nals({idr}));
  const auto other = splitAccessUnit(nals({trail}), true);
  CHECK_FALSE(other.keyframe);
}

TEST_CASE("a unit libairplay could not decrypt or parse is invalid") {
  // libairplay marks it by setting its first byte to 1
  auto marked = nals({kSlice});
  marked[0] = 1;
  CHECK_FALSE(splitAccessUnit(marked, false).valid);
  // the forbidden zero bit
  CHECK_FALSE(splitAccessUnit(nals({{0x85, 0x00}}), false).valid);
  CHECK_FALSE(splitAccessUnit({}, false).valid);
}

TEST_CASE("a mirror frame encodes as the tap envelope the screen decodes") {
  const auto data = nals({kIdr});
  const auto encoded = encodeMirrorFrame("airplay-1", 123'456'789'000, false, true, data);
  REQUIRE_FALSE(encoded.empty());
  const auto message = decodeTap(encoded);
  REQUIRE(message);
  REQUIRE(message->mirror());
  CHECK(std::string(message->frame().session_id) == "airplay-1");
  CHECK(message->frame().pts_ns == 123'456'789'000);
  CHECK_FALSE(message->frame().config);
  CHECK(message->frame().keyframe);
  CHECK(message->data == data);
}

TEST_CASE("the screen gets the config first, then frames from a keyframe, each with its pts") {
  Screen screen;
  MirrorForwarder forwarder(screen.send());
  // nothing before a session
  forwarder.unit(splitAccessUnit(nals({kSps, kPps, kIdr}), false), 1);
  CHECK(screen.read().empty());

  forwarder.start("airplay-7");
  // a frame before any keyframe is useless to a decoder
  forwarder.unit(splitAccessUnit(nals({kSlice}), false), 5);
  forwarder.unit(splitAccessUnit(nals({kSps, kPps, kIdr}), false), 10'000'000);
  forwarder.unit(splitAccessUnit(nals({kSlice}), false), 26'666'666);
  auto got = screen.read();
  REQUIRE(got.size() == 3);
  CHECK(got[0].frame().config);
  CHECK(got[0].data == nals({kSps, kPps}));
  CHECK(got[0].frame().pts_ns == 10'000'000);
  CHECK(got[1].frame().keyframe);
  CHECK(got[1].data == nals({kIdr}));
  CHECK(got[1].frame().pts_ns == 10'000'000);
  CHECK_FALSE(got[2].frame().keyframe);
  CHECK(got[2].frame().pts_ns == 26'666'666);
  for (const auto& message : got)
    CHECK(std::string(message.frame().session_id) == "airplay-7");
  CHECK(forwarder.sent() == 2);
  CHECK(forwarder.dropped() == 1);

  // the same config again is not sent again
  forwarder.unit(splitAccessUnit(nals({kSps, kPps, kIdr}), false), 40'000'000);
  got = screen.read();
  REQUIRE(got.size() == 1);
  CHECK(got[0].frame().keyframe);
}

TEST_CASE("after a frame is lost nothing goes until the next keyframe") {
  Screen screen;
  MirrorForwarder forwarder(screen.send());
  forwarder.start("airplay-1");
  forwarder.unit(splitAccessUnit(nals({kSps, kPps, kIdr}), false), 1);
  screen.read();
  screen.full = true;
  forwarder.unit(splitAccessUnit(nals({kSlice}), false), 2);
  screen.full = false;
  forwarder.unit(splitAccessUnit(nals({kSlice}), false), 3);
  CHECK(screen.read().empty());
  forwarder.unit(splitAccessUnit(nals({kIdr}), false), 4);
  const auto got = screen.read();
  REQUIRE(got.size() == 1);
  CHECK(got[0].frame().pts_ns == 4);
  // an invalid unit counts as lost too
  auto broken = nals({kSlice});
  broken[0] = 1;
  forwarder.unit(splitAccessUnit(broken, false), 5);
  forwarder.unit(splitAccessUnit(nals({kSlice}), false), 6);
  CHECK(screen.read().empty());
}

TEST_CASE("a screen that connects gets the config again and waits for a keyframe") {
  Screen screen;
  MirrorForwarder forwarder(screen.send());
  forwarder.start("airplay-1");
  forwarder.unit(splitAccessUnit(nals({kSps, kPps, kIdr}), false), 1);
  forwarder.unit(splitAccessUnit(nals({kSlice}), false), 2);
  screen.read();
  forwarder.clientConnected();
  forwarder.unit(splitAccessUnit(nals({kSlice}), false), 3);
  auto got = screen.read();
  // the config, then nothing until the keyframe
  REQUIRE(got.size() == 1);
  CHECK(got[0].frame().config);
  CHECK(got[0].data == nals({kSps, kPps}));
  forwarder.unit(splitAccessUnit(nals({kIdr}), false), 4);
  got = screen.read();
  REQUIRE(got.size() == 1);
  CHECK(got[0].frame().keyframe);
}

TEST_CASE("a keyframe far larger than any envelope goes whole in one message") {
  Screen screen;
  MirrorForwarder forwarder(screen.send());
  forwarder.start("airplay-1");
  Bytes big{0x65};
  big.resize(150 * 1024, 0x5a);
  forwarder.unit(splitAccessUnit(nals({kSps, kPps, big}), false), 1);
  const auto got = screen.read();
  REQUIRE(got.size() == 2);
  CHECK(got[1].data.size() == big.size() + 4);
  CHECK(got[1].frame().keyframe);
}

TEST_CASE("a keyframe past one tap message goes as fragments of one seq, back to back") {
  Screen screen;
  MirrorForwarder forwarder(screen.send());
  forwarder.start("airplay-1");
  Bytes big{0x65};
  big.resize(600 * 1024, 0x5a);
  big.back() = 0x42;
  forwarder.unit(splitAccessUnit(nals({kSps, kPps, big}), false), 9);
  const auto got = screen.read();
  // the config, then three fragments of the frame (its 4-byte start code included)
  REQUIRE(got.size() == 4);
  const auto total = static_cast<std::uint32_t>(big.size() + 4);
  Bytes joined;
  for (std::size_t i = 1; i < got.size(); ++i) {
    const auto& f = got[i].frame();
    CHECK(f.seq == got[1].frame().seq);
    CHECK(f.seq != 0);
    CHECK(f.total_size == total);
    CHECK(f.offset == joined.size());
    CHECK(f.keyframe);
    CHECK(f.pts_ns == 9);
    CHECK(got[i].data.size() <= kMirrorFragmentBytes);
    joined.insert(joined.end(), got[i].data.begin(), got[i].data.end());
  }
  CHECK(joined == nals({big}));
  CHECK(forwarder.sent() == 1);

  // a fragment that does not go: the rest of the unit stays, and the next unit waits for a keyframe
  screen.full = true;
  forwarder.unit(splitAccessUnit(nals({big}), false), 10);
  screen.full = false;
  forwarder.unit(splitAccessUnit(nals({kSlice}), false), 11);
  CHECK(screen.read().empty());
  CHECK(forwarder.dropped() == 2);
}
