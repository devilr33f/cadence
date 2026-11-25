#include "audiod/soloist-handlers.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/jail-rig.h"
#include "support/playback-client.h"
#include "support/soloist-rig.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <filesystem>
#include <functional>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

using StatusFrame = cadence_audiod_v1_SoloistStatus;
using ProbeResultFrame = cadence_audiod_v1_SoloistProbeResult;

constexpr auto kOff = cadence_audiod_v1_SoloistState_SOLOIST_STATE_OFF;
constexpr auto kStarting = cadence_audiod_v1_SoloistState_SOLOIST_STATE_STARTING;
constexpr auto kReady = cadence_audiod_v1_SoloistState_SOLOIST_STATE_READY;
constexpr auto kKeyMissing = cadence_audiod_v1_SoloistState_SOLOIST_STATE_KEY_MISSING;

Envelope switchBuild() {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_soloist_switch_tag;
  return envelope;
}

Envelope probe(std::string_view requestId, std::string_view path) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_soloist_probe_tag;
  copyText(envelope.body.soloist_probe.request_id, requestId);
  copyText(envelope.body.soloist_probe.path, path);
  return envelope;
}

// every frame until one matches, that one last; none when it does not come in time
std::vector<Envelope> until(int fd, const std::function<bool(const Envelope&)>& matches,
                            std::chrono::milliseconds limit = std::chrono::seconds(10)) {
  std::vector<Envelope> frames;
  const auto deadline = std::chrono::steady_clock::now() + limit;
  while (std::chrono::steady_clock::now() < deadline) {
    const auto received = receive(fd, 100ms);
    if (received.kind == Received::Kind::closed)
      break;
    if (received.kind != Received::Kind::frame)
      continue;
    frames.push_back(received.envelope);
    if (matches(received.envelope))
      return frames;
  }
  FAIL("the frame never came");
  return frames;
}

std::optional<StatusFrame> statusOf(const Envelope& envelope) {
  if (envelope.which_body != cadence_audiod_v1_Envelope_soloist_status_tag)
    return std::nullopt;
  return envelope.body.soloist_status;
}

StatusFrame untilState(int fd, cadence_audiod_v1_SoloistState state) {
  const auto frames = until(fd, [state](const Envelope& envelope) {
    const auto status = statusOf(envelope);
    return status && status->state == state;
  });
  return *statusOf(frames.back());
}

// audiod with soloist's part over the jail rig's runtime and the fake soloist, the output on the file backend
class Rig {
 public:
  Rig()
      : dir_(std::filesystem::path(jail_.root()).parent_path().string()),
        audiod_(compiledBackends(), {}, openOutput, openCapture, media()) {
    std::filesystem::copy_file(CADENCE_FAKE_SOLOIST, dir_ + "/soloist");
    std::filesystem::permissions(dir_ + "/soloist", std::filesystem::perms(0755));
  }

  [[nodiscard]] AudiodHarness& audiod() { return audiod_; }
  [[nodiscard]] const std::string& dir() const { return dir_; }
  [[nodiscard]] FakeSoloist fake() const { return FakeSoloist(dir_ + "/scratch/home"); }

  Envelope config(std::string_view key = kFakeKey, bool enabled = true) const {
    Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
    envelope.which_body = cadence_audiod_v1_Envelope_soloist_config_tag;
    auto& body = envelope.body.soloist_config;
    body.enabled = enabled;
    copyText(body.api_key, key);
    copyText(body.device_name, "Kitchen");
    copyText(body.dir, dir_);
    copyText(body.scratch_dir, dir_ + "/scratch");
    copyText(body.ca_file, jail_.file("ca.crt"));
    body.initial_volume = 40;
    return envelope;
  }

  // configured with its idle media state taken, the gate still shut
  UniqueFd connect() { return connectConfigured(audiod_, audiod_.file("out")); }

 private:
  HarnessMedia media() const {
    HarnessMedia media;
    media.soloist = SoloistOptions{.timings = quickTimings(), .resolvConf = jail_.file("resolv.conf"), .pcm = {}};
    return media;
  }

  JailRig jail_;
  std::string dir_;
  AudiodHarness audiod_;
};

}  // namespace

TEST_CASE("a cadenced that sends no soloist config gets no soloist status") {
  Rig rig;
  auto client = rig.connect();
  expectPong(client.get(), 1);
  CHECK(receive(client.get(), 300ms).kind == Received::Kind::timeout);
}

TEST_CASE("a config starts soloist once a volume held, and its status follows") {
  Rig rig;
  auto client = rig.connect();
  sendFrame(client.get(), rig.config());
  untilState(client.get(), kStarting);
  CHECK_FALSE(rig.fake().started(300ms));

  sendFrame(client.get(), volumeSet(40));
  const auto ready = untilState(client.get(), kReady);
  CHECK(std::string_view(ready.code).empty());
  CHECK(std::string_view(ready.version) == "1.3.10.18");
  CHECK(ready.build_epoch == 1'793'059'327);
  CHECK(ready.silent_starts == 0);
  CHECK(rig.fake().started());
}

TEST_CASE("soloist outlives cadenced, and the next one learns its status at its config") {
  Rig rig;
  {
    auto client = rig.connect();
    sendFrame(client.get(), rig.config());
    sendFrame(client.get(), volumeSet(40));
    untilState(client.get(), kReady);
  }
  REQUIRE(eventually([&] { return rig.audiod().logs().count(event("playback_closed")) > 0; }));
  auto client = rig.connect();
  expectPong(client.get(), 7);
  sendFrame(client.get(), rig.config());
  untilState(client.get(), kReady);
  CHECK(rig.audiod().logs().count(event("soloist_start")) == 1);
}

TEST_CASE("a probe is answered with its id and the build") {
  Rig rig;
  auto client = rig.connect();
  sendFrame(client.get(), rig.config(kFakeKey, false));
  untilState(client.get(), kOff);
  sendFrame(client.get(), probe("probe-1", rig.dir() + "/soloist"));
  const auto frames = until(client.get(), [](const Envelope& envelope) {
    return envelope.which_body == cadence_audiod_v1_Envelope_soloist_probe_result_tag;
  });
  const auto result = frames.back().body.soloist_probe_result;
  CHECK(std::string_view(result.request_id) == "probe-1");
  CHECK(result.ok);
  CHECK(std::string_view(result.code).empty());
  CHECK(std::string_view(result.version) == "1.3.10.18");
  CHECK(result.build_epoch == 1'793'059'327);

  sendFrame(client.get(), probe("probe-2", rig.dir() + "/missing"));
  const auto missing = until(client.get(), [](const Envelope& envelope) {
                         return envelope.which_body == cadence_audiod_v1_Envelope_soloist_probe_result_tag;
                       }).back().body.soloist_probe_result;
  CHECK(std::string_view(missing.request_id) == "probe-2");
  CHECK_FALSE(missing.ok);
  CHECK(std::string_view(missing.code) == "io");
}

TEST_CASE("a switch starts the installed build again") {
  Rig rig;
  auto client = rig.connect();
  sendFrame(client.get(), rig.config());
  sendFrame(client.get(), volumeSet(40));
  untilState(client.get(), kReady);
  sendFrame(client.get(), switchBuild());
  CHECK(eventually([&] { return rig.audiod().logs().count(event("soloist_answered")) == 2; }));
  CHECK(rig.audiod().logs().count(event("soloist_switch")) == 1);
}

TEST_CASE("the key reaches neither a status frame nor a log") {
  const std::string key = "secret-test-key-0123456789";
  Rig rig;
  auto client = rig.connect();
  sendFrame(client.get(), rig.config(key));
  sendFrame(client.get(), volumeSet(40));
  auto frames = until(client.get(), [](const Envelope& envelope) {
    const auto status = statusOf(envelope);
    return status && status->state == kReady;
  });
  sendFrame(client.get(), rig.config(key + " x"));
  const auto more = until(client.get(), [](const Envelope& envelope) {
    const auto status = statusOf(envelope);
    return status && status->state == kKeyMissing;
  });
  frames.insert(frames.end(), more.begin(), more.end());
  for (const auto& envelope : frames) {
    const auto bytes = encode(envelope);
    CHECK(std::string(bytes.begin(), bytes.end()).find("secret-test-key") == std::string::npos);
  }
  CHECK(rig.audiod().logs().count("secret-test-key") == 0);
}

TEST_CASE("a probe before this connection's config is answered to no one, and logged") {
  Rig rig;
  auto client = rig.connect();
  sendFrame(client.get(), probe("early", rig.dir() + "/soloist"));
  CHECK(eventually([&] { return rig.audiod().logs().count(event("soloist_probe_dropped")) == 1; }));
  for (auto received = receive(client.get(), 300ms); received.kind == Received::Kind::frame;
       received = receive(client.get(), 300ms))
    CHECK(received.envelope.which_body != cadence_audiod_v1_Envelope_soloist_probe_result_tag);
}

TEST_CASE("a probe result the client's full socket refused goes out again") {
  Rig rig;
  auto client = rig.connect();
  sendFrame(client.get(), rig.config(kFakeKey, false));
  untilState(client.get(), kOff);
  rig.audiod().onLoop([](IpcServer& ipc, Daemon&) {
    Envelope pong = cadence_audiod_v1_Envelope_init_zero;
    pong.which_body = cadence_audiod_v1_Envelope_pong_tag;
    while (ipc.send(pong)) {
    }
    return 0;
  });
  sendFrame(client.get(), probe("full", rig.dir() + "/soloist"));
  // it answers while nothing is read
  REQUIRE(eventually([&] { return rig.audiod().logs().count(R"("msg":"soloist_probe","code":"ok")") == 1; }));
  std::this_thread::sleep_for(300ms);

  std::vector<ProbeResultFrame> results;
  for (auto received = receive(client.get(), 500ms); received.kind == Received::Kind::frame;
       received = receive(client.get(), 500ms))
    if (received.envelope.which_body == cadence_audiod_v1_Envelope_soloist_probe_result_tag)
      results.push_back(received.envelope.body.soloist_probe_result);
  REQUIRE(results.size() == 1);
  CHECK(std::string_view(results[0].request_id) == "full");
  CHECK(results[0].ok);
}
