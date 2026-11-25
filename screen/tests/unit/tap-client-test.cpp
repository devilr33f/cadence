// the screen's tap client over a socketpair, as audiod's tap server sends: pcm snapshots and bands to the
// visualizer's sinks, a ring offer whose SCM_RIGHTS fds are real (the memfd and eventfd audiod made), stray fds and
// frames that do not decode refused, and the client's thread ending on stop and on the peer's close
#include "player/tap-client.h"

#include "cadence/audiod/v1/audiod.pb.h"
#include "ipc/seqpacket.h"

#include <catch2/catch_test_macros.hpp>

#include <pb_encode.h>

#include <fcntl.h>
#include <sys/eventfd.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#include <array>
#include <atomic>
#include <chrono>
#include <cstring>
#include <functional>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <vector>

using namespace cadence::screen::player;

namespace {

std::vector<std::uint8_t> encode(const cadence_audiod_v1_TapEnvelope& e) {
  std::vector<std::uint8_t> frame(8192);
  pb_ostream_t stream = pb_ostream_from_buffer(frame.data(), frame.size());
  REQUIRE(pb_encode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &e));
  frame.resize(stream.bytes_written);
  return frame;
}

std::vector<std::uint8_t> pcmFrame(std::uint64_t seq, std::int64_t monoNs) {
  cadence_audiod_v1_TapEnvelope e = cadence_audiod_v1_TapEnvelope_init_zero;
  e.which_body = cadence_audiod_v1_TapEnvelope_pcm_tag;
  auto& p = e.body.pcm;
  p.seq = seq;
  p.sample_rate = 48000;
  p.channels = 2;
  p.mono_ns = monoNs;
  std::array<float, 1024> s{};
  for (std::size_t i = 0; i < s.size(); ++i) s[i] = static_cast<float>(i % 7) * 0.1f;
  std::memcpy(p.samples.bytes, s.data(), sizeof s);
  p.samples.size = sizeof s;
  return encode(e);
}

std::vector<std::uint8_t> bandsFrame() {
  cadence_audiod_v1_TapEnvelope e = cadence_audiod_v1_TapEnvelope_init_zero;
  e.which_body = cadence_audiod_v1_TapEnvelope_bands_tag;
  e.body.bands.bands.size = 16;
  for (std::uint8_t i = 0; i < 16; ++i) e.body.bands.bands.bytes[i] = static_cast<std::uint8_t>(i * 16);
  return encode(e);
}

std::vector<std::uint8_t> offerFrame(const char* id, std::uint32_t capacity) {
  cadence_audiod_v1_TapEnvelope e = cadence_audiod_v1_TapEnvelope_init_zero;
  e.which_body = cadence_audiod_v1_TapEnvelope_ring_offer_tag;
  std::strncpy(e.body.ring_offer.session_id, id, sizeof e.body.ring_offer.session_id - 1);
  e.body.ring_offer.capacity_frames = capacity;
  e.body.ring_offer.rate = 48000;
  e.body.ring_offer.channels = 2;
  return encode(e);
}

// audiod's sendRingOffer: one frame, the fds as SCM_RIGHTS
void sendWithFds(int socket, const std::vector<std::uint8_t>& frame, const std::vector<int>& fds) {
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

bool eventually(const std::function<bool()>& pred, int timeoutMs = 3000) {
  const auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(timeoutMs);
  while (std::chrono::steady_clock::now() < deadline) {
    if (pred()) return true;
    std::this_thread::sleep_for(std::chrono::milliseconds(2));
  }
  return pred();
}

int openFds() {
  int n = 0;
  for (int fd = 0; fd < 1024; ++fd) n += ::fcntl(fd, F_GETFD) != -1 ? 1 : 0;
  return n;
}

struct Seen {
  std::mutex mu;
  std::vector<std::int64_t> pcmAt;
  std::size_t samples = 0;
  float sample3 = 0;
  std::array<std::uint8_t, 16> bands{};
  std::atomic<int> ringsReady{0};
};

TapSinks sinksFor(Seen& seen) {
  TapSinks s;
  s.pcm = [&seen](std::span<const float> pcm, std::uint32_t channels, std::uint32_t rate, std::int64_t monoNs) {
    std::lock_guard lock(seen.mu);
    CHECK(channels == 2);
    CHECK(rate == 48000);
    seen.pcmAt.push_back(monoNs);
    seen.samples = pcm.size();
    seen.sample3 = pcm[3];
  };
  s.bands = [&seen](const std::array<std::uint8_t, 16>& b) {
    std::lock_guard lock(seen.mu);
    seen.bands = b;
  };
  s.ringReady = [&seen] { seen.ringsReady++; };
  return s;
}

}  // namespace

TEST_CASE("tap: pcm and bands reach the visualizer's sinks, a ring offer's real fds reach the main thread") {
  std::array<int, 2> pair{};
  REQUIRE(::socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, pair.data()) == 0);
  Seen seen;
  TapClient client(sinksFor(seen));
  client.adopt(pair[1]);

  sendWithFds(pair[0], pcmFrame(1, 123456789), {});
  sendWithFds(pair[0], pcmFrame(2, 140000000), {});
  sendWithFds(pair[0], bandsFrame(), {});

  // audiod's ring: made there, offered with its memfd and eventfd
  std::string error;
  auto made = PcmRing::create(8192, &error);
  REQUIRE(made);
  sendWithFds(pair[0], offerFrame("video-7", 8192), {made->ringFd(), made->eventFd()});

  REQUIRE(eventually([&] { return seen.ringsReady.load() == 1; }));
  {
    std::lock_guard lock(seen.mu);
    REQUIRE(seen.pcmAt.size() == 2);
    CHECK(seen.pcmAt[0] == 123456789);
    CHECK(seen.pcmAt[1] == 140000000);
    CHECK(seen.samples == 1024);
    CHECK(seen.sample3 == 0.3f);
    CHECK(seen.bands[15] == 240);
  }
  auto rings = client.takeRings();
  REQUIRE(rings.size() == 1);
  CHECK(rings[0].sessionId == "video-7");
  CHECK(rings[0].ring.ringFd() != made->ringFd());
  // the very memory audiod mapped: the writer's position written here is audiod's, its eventfd signals ours
  const_cast<cadence_ring_header*>(rings[0].ring.header())->write_pos = 960;
  CHECK(made->header()->write_pos == 960);
  REQUIRE(::eventfd_write(made->eventFd(), 1) == 0);
  eventfd_t value = 0;
  REQUIRE(::eventfd_read(rings[0].ring.eventFd(), &value) == 0);
  CHECK(value == 1);
  CHECK(client.takeRings().empty());

  const auto counts = client.counts();
  CHECK(counts.pcm == 2);
  CHECK(counts.bands == 1);
  CHECK(counts.rings == 1);
  CHECK(counts.refused == 0);
  client.stop();
  ::close(pair[0]);
}

TEST_CASE("tap: stray fds are closed, a bad frame refused, a newer offer replaces a queued one") {
  std::array<int, 2> pair{};
  REQUIRE(::socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, pair.data()) == 0);
  Seen seen;
  TapClient client(sinksFor(seen));
  client.adopt(pair[1]);
  std::string error;
  auto made = PcmRing::create(4096, &error);
  REQUIRE(made);
  const int before = openFds();

  // a pcm frame carrying fds: drawn, its fds closed
  sendWithFds(pair[0], pcmFrame(1, 1), {made->ringFd(), made->eventFd()});
  // not a tap envelope
  sendWithFds(pair[0], {0xff, 0xff, 0xff, 0x07}, {});
  // an offer with one fd
  sendWithFds(pair[0], offerFrame("video-1", 4096), {made->ringFd()});
  // two good offers for one session: the second replaces the first in the queue
  sendWithFds(pair[0], offerFrame("video-1", 4096), {made->ringFd(), made->eventFd()});
  sendWithFds(pair[0], offerFrame("video-1", 4096), {made->ringFd(), made->eventFd()});
  REQUIRE(eventually([&] { return client.counts().frames == 5; }));
  const auto counts = client.counts();
  CHECK(counts.pcm == 1);
  CHECK(counts.refused == 2);
  CHECK(counts.rings == 2);
  auto rings = client.takeRings();
  CHECK(rings.size() == 1);
  rings.clear();
  // every fd the frames brought was closed or went with its ring
  CHECK(openFds() == before);

  // the peer going away ends the client's thread
  ::close(pair[0]);
  REQUIRE(eventually([&] { return !client.connected(); }));
  client.stop();
}

TEST_CASE("tap: the client connects to the path it is given and keeps retrying until audiod listens") {
  const std::string path = "@cadence-tap-test-" + std::to_string(::getpid());
  Seen seen;
  TapClient client(sinksFor(seen));
  client.connect(path);
  std::this_thread::sleep_for(std::chrono::milliseconds(150));
  CHECK_FALSE(client.connected());

  const int listener = ::socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0);
  REQUIRE(listener >= 0);
  sockaddr_un addr{};
  unsigned length = 0;
  REQUIRE(cadence::screen::ipc::fillAddress(path, &addr, &length));
  REQUIRE(::bind(listener, reinterpret_cast<sockaddr*>(&addr), length) == 0);
  REQUIRE(::listen(listener, 1) == 0);
  const int peer = ::accept(listener, nullptr, nullptr);
  REQUIRE(peer >= 0);
  sendWithFds(peer, pcmFrame(9, 42), {});
  REQUIRE(eventually([&] {
    std::lock_guard lock(seen.mu);
    return !seen.pcmAt.empty();
  }));
  CHECK(client.counts().connects == 1);
  client.stop();
  CHECK_FALSE(client.connected());
  ::close(peer);
  ::close(listener);
}

namespace {

bool writeBytes(pb_ostream_t* stream, const pb_field_t* field, void* const* arg) {
  const auto* bytes = static_cast<const std::vector<std::uint8_t>*>(*arg);
  return pb_encode_tag_for_field(stream, field) && pb_encode_string(stream, bytes->data(), bytes->size());
}

}  // namespace

TEST_CASE("tap: a mirror frame's nals reach the mirror sink whole (field 4 decoded alone), a large one too") {
  std::array<int, 2> pair{};
  REQUIRE(::socketpair(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0, pair.data()) == 0);
  // the kernel's default buffer would refuse a 300 KB datagram
  const int big = 1 << 20;
  ::setsockopt(pair[0], SOL_SOCKET, SO_SNDBUF, &big, sizeof big);
  ::setsockopt(pair[1], SOL_SOCKET, SO_RCVBUF, &big, sizeof big);
  std::mutex mu;
  std::vector<std::pair<std::int64_t, std::size_t>> got;
  bool config = false;
  std::uint8_t last = 0;
  TapSinks s;
  s.mirror = [&](const TapSinks::MirrorFrame& f) {
    std::lock_guard lock(mu);
    got.emplace_back(f.ptsNs, f.data.size());
    config = config || f.config;
    last = f.data.empty() ? 0 : f.data.back();
    CHECK(f.sessionId == "mirror-1");
  };
  TapClient client(std::move(s));
  client.adopt(pair[1]);
  for (const std::size_t size : {std::size_t{40}, std::size_t{300 * 1024}}) {
    std::vector<std::uint8_t> nals(size, 0x17);
    nals.back() = 0x42;
    cadence_audiod_v1_TapEnvelope e = cadence_audiod_v1_TapEnvelope_init_zero;
    e.which_body = cadence_audiod_v1_TapEnvelope_mirror_frame_tag;
    auto& m = e.body.mirror_frame;
    std::strcpy(m.session_id, "mirror-1");
    m.pts_ns = static_cast<std::int64_t>(size);
    m.config = size == 40;
    m.keyframe = true;
    m.data.funcs.encode = writeBytes;
    m.data.arg = &nals;
    std::vector<std::uint8_t> frame(size + 256);
    pb_ostream_t stream = pb_ostream_from_buffer(frame.data(), frame.size());
    REQUIRE(pb_encode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &e));
    frame.resize(stream.bytes_written);
    sendWithFds(pair[0], frame, {});
  }
  REQUIRE(eventually([&] { return client.counts().mirror == 2; }));
  {
    std::lock_guard lock(mu);
    REQUIRE(got.size() == 2);
    CHECK(got[0] == std::make_pair(std::int64_t{40}, std::size_t{40}));
    CHECK(got[1].second == 300 * 1024);
    CHECK(config);
    CHECK(last == 0x42);
  }
  CHECK(client.counts().refused == 0);
  client.stop();
  ::close(pair[0]);
}

namespace {

std::vector<std::uint8_t> fragmentFrame(std::vector<std::uint8_t>& part, std::uint32_t seq, std::uint32_t offset,
                                        std::uint32_t total) {
  cadence_audiod_v1_TapEnvelope e = cadence_audiod_v1_TapEnvelope_init_zero;
  e.which_body = cadence_audiod_v1_TapEnvelope_mirror_frame_tag;
  auto& m = e.body.mirror_frame;
  std::strcpy(m.session_id, "mirror-1");
  m.pts_ns = 7;
  m.keyframe = true;
  m.seq = seq;
  m.offset = offset;
  m.total_size = total;
  m.data.funcs.encode = writeBytes;
  m.data.arg = &part;
  std::vector<std::uint8_t> frame(part.size() + 256);
  pb_ostream_t stream = pb_ostream_from_buffer(frame.data(), frame.size());
  REQUIRE(pb_encode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &e));
  frame.resize(stream.bytes_written);
  return frame;
}

}  // namespace

TEST_CASE("tap: a mirror unit in fragments reaches the sink whole, once; a lost fragment drops its unit") {
  std::vector<std::vector<std::uint8_t>> units;
  TapSinks s;
  s.mirror = [&](const TapSinks::MirrorFrame& f) {
    CHECK(f.keyframe);
    CHECK(f.ptsNs == 7);
    units.emplace_back(f.data.begin(), f.data.end());
  };
  MirrorJoin join;
  std::optional<RingOffer> ring;
  std::string error;
  // 600 KiB as three fragments, the size one tap message never takes whole
  std::vector<std::uint8_t> unit(600 * 1024);
  for (std::size_t i = 0; i < unit.size(); ++i) unit[i] = static_cast<std::uint8_t>(i % 251);
  const std::uint32_t total = static_cast<std::uint32_t>(unit.size());
  std::vector<std::uint8_t> a(unit.begin(), unit.begin() + 250 * 1024);
  std::vector<std::uint8_t> b(unit.begin() + 250 * 1024, unit.begin() + 500 * 1024);
  std::vector<std::uint8_t> c(unit.begin() + 500 * 1024, unit.end());
  for (auto [part, offset] : {std::pair{&a, 0u}, std::pair{&b, 250u * 1024}, std::pair{&c, 500u * 1024}}) {
    auto f = fragmentFrame(*part, 1, offset, total);
    CHECK(dispatchTapFrame(f, {}, s, ring, &error, &join) == TapFrame::Mirror);
  }
  REQUIRE(units.size() == 1);
  CHECK(units[0] == unit);

  // the second fragment lost: the third breaks the unit, and the next unit starts afresh
  auto f = fragmentFrame(a, 2, 0, total);
  dispatchTapFrame(f, {}, s, ring, &error, &join);
  f = fragmentFrame(c, 2, 500u * 1024, total);
  dispatchTapFrame(f, {}, s, ring, &error, &join);
  CHECK(units.size() == 1);
  CHECK(join.dropped() == 1);
  std::vector<std::uint8_t> small{1, 2, 3, 4};
  std::vector<std::uint8_t> rest{5, 6, 7, 8};
  f = fragmentFrame(small, 3, 0, 8);
  dispatchTapFrame(f, {}, s, ring, &error, &join);
  f = fragmentFrame(rest, 3, 4, 8);
  dispatchTapFrame(f, {}, s, ring, &error, &join);
  REQUIRE(units.size() == 2);
  CHECK(units[1] == std::vector<std::uint8_t>{1, 2, 3, 4, 5, 6, 7, 8});

  // without a join a fragment is refused
  f = fragmentFrame(small, 4, 0, 8);
  CHECK(dispatchTapFrame(f, {}, s, ring, &error) == TapFrame::Refused);
}

// multiroom P4: a group clock reaches the follower's sink as it was sent
TEST_CASE("tap: a group clock reaches its sink") {
  cadence_audiod_v1_TapEnvelope e = cadence_audiod_v1_TapEnvelope_init_zero;
  e.which_body = cadence_audiod_v1_TapEnvelope_group_clock_tag;
  e.body.group_clock.media_ns = 61'000'000'000;
  e.body.group_clock.mono_ns = 5'000'000'000;
  e.body.group_clock.epoch = 3;
  e.body.group_clock.playing = true;
  const auto frame = encode(e);
  std::optional<FollowClock> got;
  TapSinks sinks;
  sinks.groupClock = [&](const FollowClock& c) { got = c; };
  std::optional<RingOffer> ring;
  std::string error;
  CHECK(dispatchTapFrame(frame, {}, sinks, ring, &error) == TapFrame::GroupClock);
  REQUIRE(got);
  CHECK(got->mediaNs == 61'000'000'000);
  CHECK(got->monoNs == 5'000'000'000);
  CHECK(got->epoch == 3);
  CHECK(got->playing);
  // with no sink it is dropped quietly
  CHECK(dispatchTapFrame(frame, {}, TapSinks{}, ring, &error) == TapFrame::GroupClock);
}
