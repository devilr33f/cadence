#include "backend/pcm-snapshots.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/playback-client.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

#include <pb_decode.h>

#include <sys/socket.h>
#include <sys/un.h>

#include <vector>
#include <sys/wait.h>
#include <unistd.h>

#include <array>
#include <chrono>
#include <cstddef>
#include <cstring>
#include <optional>
#include <span>
#include <string>
#include <thread>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

Envelope tapConfig(std::string_view path, bool pcm, bool bands) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_tap_config_tag;
  copyText(envelope.body.tap_config.path, path);
  envelope.body.tap_config.pcm = pcm;
  envelope.body.tap_config.bands = bands;
  return envelope;
}

UniqueFd connectTap(const std::string& path) {
  sockaddr_un address{};
  address.sun_family = AF_UNIX;
  std::memcpy(address.sun_path, path.data(), path.size());
  socklen_t length = sizeof address;
  if (path.front() == '@') {
    address.sun_path[0] = '\0';
    length = static_cast<socklen_t>(offsetof(sockaddr_un, sun_path) + path.size());
  }
  UniqueFd fd(::socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0));
  REQUIRE(fd);
  if (::connect(fd.get(), reinterpret_cast<const sockaddr*>(&address), length) != 0)
    return UniqueFd();
  return fd;
}

std::optional<cadence_audiod_v1_TapEnvelope> receiveTap(int fd, std::chrono::milliseconds limit = 2s) {
  timeval timeout{};
  timeout.tv_sec = static_cast<time_t>(limit.count() / 1000);
  timeout.tv_usec = static_cast<suseconds_t>((limit.count() % 1000) * 1000);
  ::setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof timeout);
  std::vector<std::uint8_t> frame(32768);
  const auto size = ::recv(fd, frame.data(), frame.size(), 0);
  if (size <= 0)
    return std::nullopt;
  cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
  pb_istream_t stream = pb_istream_from_buffer(frame.data(), static_cast<std::size_t>(size));
  if (!pb_decode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &envelope))
    return std::nullopt;
  return envelope;
}

}  // namespace

TEST_CASE("the tap sends pcm snapshots of the mix to the screen while pcm is on") {
  const bool abstract = GENERATE(false, true);
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  auto client = connectConfigured(audiod, audiod.file("out"), "Master");
  expectApplied(client.get(), 40, true);
  const std::string path =
      abstract ? "@cadence-tap-test-" + std::to_string(::getpid()) : audiod.file("tap.sock");
  sendFrame(client.get(), tapConfig(path, true, false));
  REQUIRE(audiod.logs().eventually("\"msg\":\"tap_config\""));
  auto tap = connectTap(path);
  REQUIRE(tap);

  REQUIRE(audiod.logs().eventually("\"msg\":\"tap_client_connected\""));
  const auto music = sine(1000, 0.25, 48 * 1000, 2);
  REQUIRE(audiod.playback().music().push(std::span<const float>(music), 48000, 2));
  std::optional<cadence_audiod_v1_TapEnvelope> heard;
  REQUIRE(eventually([&] {
    const auto frame = receiveTap(tap.get());

    if (frame && frame->which_body == cadence_audiod_v1_TapEnvelope_pcm_tag && frame->body.pcm.samples.size == cadence::PcmSnapshot::kFrames * 8) {
      float sample = 0;
      for (std::size_t i = 0; i < cadence::PcmSnapshot::kFrames && sample < 0.1f; ++i)
        std::memcpy(&sample, frame->body.pcm.samples.bytes + 8 * i, sizeof sample);
      if (sample >= 0.1f)
        heard = frame;
    }
    return heard.has_value();
  }));
  REQUIRE(heard);
  CHECK(audiod.logs().count("tap_encode_failed") == 0);
  CHECK(heard->body.pcm.sample_rate == 48000);
  CHECK(heard->body.pcm.channels == 2);
  CHECK(heard->body.pcm.seq > 0);

  // an empty path closes it
  sendFrame(client.get(), tapConfig("", false, false));
  REQUIRE(eventually([&] { return !connectTap(path); }));
}

TEST_CASE("the tap sends nothing for pcm that is off") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, audiod.file("out"), "Master");
  const auto path = audiod.file("tap.sock");
  sendFrame(client.get(), tapConfig(path, false, true));
  REQUIRE(audiod.logs().eventually("\"msg\":\"tap_config\""));
  auto tap = connectTap(path);
  REQUIRE(tap);
  CHECK_FALSE(receiveTap(tap.get(), 300ms));
}

TEST_CASE("the tap's abstract name refuses a client of another uid") {
  // android's two processes run as the app's one uid; anyone else on the device reaches an abstract name
  if (::geteuid() != 0)
    SKIP("needs root to connect as another uid");
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, audiod.file("out"), "Master");
  const std::string path = "@cadence-tap-uid-test-" + std::to_string(::getpid());
  sendFrame(client.get(), tapConfig(path, false, true));
  REQUIRE(audiod.logs().eventually("\"msg\":\"tap_config\""));
  const pid_t child = ::fork();
  REQUIRE(child >= 0);
  if (child == 0) {
    // nobody: connects (the kernel queues it), and the tap closes it unread
    if (::setgid(65534) != 0 || ::setuid(65534) != 0)
      ::_exit(2);
    auto tap = connectTap(path);
    if (!tap)
      ::_exit(3);
    std::array<std::uint8_t, 64> buffer{};
    timeval timeout{2, 0};
    ::setsockopt(tap.get(), SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof timeout);
    ::_exit(::recv(tap.get(), buffer.data(), buffer.size(), 0) == 0 ? 0 : 4);
  }
  int status = 0;
  REQUIRE(::waitpid(child, &status, 0) == child);
  REQUIRE(WIFEXITED(status));
  CHECK(WEXITSTATUS(status) == 0);
  CHECK(audiod.logs().eventually("\"msg\":\"tap_client_refused\""));
  CHECK(audiod.logs().count("tap_client_connected") == 0);
  // this uid still connects
  auto tap = connectTap(path);
  REQUIRE(tap);
  REQUIRE(audiod.logs().eventually("\"msg\":\"tap_client_connected\""));
}
