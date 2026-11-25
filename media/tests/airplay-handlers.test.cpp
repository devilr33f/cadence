#include "audiod/airplay-handlers.h"
#include "support/airplay-rig.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/playback-client.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <filesystem>
#include <functional>
#include <optional>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

using StatusFrame = cadence_audiod_v1_AirplayStatus;

constexpr auto kOff = cadence_audiod_v1_AirplayState_AIRPLAY_STATE_OFF;
constexpr auto kStarting = cadence_audiod_v1_AirplayState_AIRPLAY_STATE_STARTING;
constexpr auto kReady = cadence_audiod_v1_AirplayState_AIRPLAY_STATE_READY;
constexpr auto kFailed = cadence_audiod_v1_AirplayState_AIRPLAY_STATE_FAILED;

std::optional<StatusFrame> statusOf(const Envelope& envelope) {
  if (envelope.which_body != cadence_audiod_v1_Envelope_airplay_status_tag)
    return std::nullopt;
  return envelope.body.airplay_status;
}

// the statuses until one of state, that one last
StatusFrame untilState(int fd, cadence_audiod_v1_AirplayState state,
                       std::chrono::milliseconds limit = std::chrono::seconds(15)) {
  const auto deadline = std::chrono::steady_clock::now() + limit;
  while (std::chrono::steady_clock::now() < deadline) {
    const auto received = receive(fd, 100ms);
    if (received.kind == Received::Kind::closed)
      break;
    if (received.kind != Received::Kind::frame)
      continue;
    if (const auto status = statusOf(received.envelope); status && status->state == state)
      return *status;
  }
  FAIL("the status never came");
  return {};
}

// audiod with the airplay part over a runtime of fakes, the output on the file backend
class Rig {
 public:
  Rig() : audiod_(compiledBackends(), {}, openOutput, openCapture, media()) {}

  [[nodiscard]] AudiodHarness& audiod() { return audiod_; }
  [[nodiscard]] const AirplayRuntime& runtime() const { return runtime_; }

  [[nodiscard]] Envelope config(bool enabled = true) const {
    Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
    envelope.which_body = cadence_audiod_v1_Envelope_airplay_config_tag;
    auto& body = envelope.body.airplay_config;
    body.enabled = enabled;
    copyText(body.name, "Kitchen");
    copyText(body.dir, runtime_.dir());
    copyText(body.scratch_dir, runtime_.scratch());
    return envelope;
  }

  // configured, the gate still shut
  UniqueFd connect() { return connectConfigured(audiod_, audiod_.file("out")); }

 private:
  [[nodiscard]] HarnessMedia media() const {
    HarnessMedia media;
    media.airplay = runtime_.options();
    return media;
  }

  AirplayRuntime runtime_;
  AudiodHarness audiod_;
};

}  // namespace

TEST_CASE("a cadenced that sends no airplay config gets no airplay status") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  expectPong(client.get(), 1);
  CHECK(receive(client.get(), 300ms).kind == Received::Kind::timeout);
}

TEST_CASE("a config starts the receiver once a volume held, and its status follows") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  sendFrame(client.get(), rig.config());
  untilState(client.get(), kStarting);
  std::this_thread::sleep_for(300ms);
  CHECK_FALSE(rig.runtime().started("avahi-daemon"));

  sendFrame(client.get(), volumeSet(40));
  const auto ready = untilState(client.get(), kReady);
  CHECK(std::string_view(ready.code).empty());
  CHECK(rig.runtime().started("shairport-sync"));

  sendFrame(client.get(), rig.config(false));
  untilState(client.get(), kOff);
}

TEST_CASE("a failed attempt reaches cadenced with its code") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  std::filesystem::remove(rig.runtime().runtime() + "/bin/shairport-sync");
  auto client = rig.connect();
  sendFrame(client.get(), rig.config());
  sendFrame(client.get(), volumeSet(40));
  const auto failed = untilState(client.get(), kFailed);
  CHECK(std::string_view(failed.code) == "runtime_missing");
}

TEST_CASE("a restarted cadenced learns the receiver's status from its config, and the receiver ran on") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  {
    auto client = rig.connect();
    sendFrame(client.get(), rig.config());
    sendFrame(client.get(), volumeSet(40));
    untilState(client.get(), kReady);
  }
  const auto starts = rig.runtime().starts().size();
  auto client = rig.connect();
  sendFrame(client.get(), rig.config());
  untilState(client.get(), kReady);
  CHECK(rig.runtime().starts().size() == starts);
}
