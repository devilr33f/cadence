// audiod's side of a group calibration: the capture part records a window of the microphone (the file backend's wav,
// a chirp in it) with its read stamps and refuses while muted or unconfigured; GroupCalibrate is refused by a device
// that leads no group, by a leader whose volume does not hold yet (rule 7), and by one with no member connected. no
// sound is made: the file backends alone
#include "audiod/capture.h"
#include "audiod/group-handlers.h"
#include "file/wav.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/playback-client.h"
#include "sync/calibration.h"
#include "sync/clock-socket.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <cstring>
#include <cmath>
#include <future>
#include <string>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

// a 16 kHz wav: silence with the calibration's chirp atS seconds in
std::string chirpWav(const AudiodHarness& audiod, double atS, double lengthS) {
  const auto path = audiod.file("mic.wav");
  auto writer = WavWriter::create(path, {16000, 1});
  REQUIRE(writer.ok());
  sync::calibration::ChirpShape shape;
  shape.amplitude = 0.3;
  std::vector<std::int16_t> pcm(static_cast<std::size_t>(lengthS * 16000));
  for (std::size_t n = 0; n < pcm.size(); ++n)
    pcm[n] = static_cast<std::int16_t>(
        std::lround(32767 * sync::calibration::chirpAt(shape, static_cast<double>(n) / 16000.0 - atS)));
  REQUIRE_FALSE(writer.value().write(pcm));
  REQUIRE_FALSE(writer.value().close());
  return path;
}

Envelope configure(const AudiodHarness& audiod, const std::string& wav) {
  auto envelope = configureOutput(audiod.file("out"));
  envelope.body.configure.has_capture = true;
  envelope.body.configure.capture.backend = kFile;
  copyText(envelope.body.configure.capture.file_path, wav);
  return envelope;
}

sync::MicResult recordNow(AudiodHarness& audiod, std::int64_t fromInNs, std::int64_t toInNs) {
  std::promise<sync::MicResult> p;
  auto f = p.get_future();
  const auto now = sync::monoNs();
  audiod.capture().record(now + fromInNs, now + toInNs, [&p](sync::MicResult r) { p.set_value(std::move(r)); });
  REQUIRE(f.wait_for(5s) == std::future_status::ready);
  return f.get();
}

Envelope muteState(bool muted) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_mute_state_tag;
  envelope.body.mute_state.muted = muted;
  return envelope;
}

Envelope groupCalibrate(std::string_view id, bool listener = false) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_group_calibrate_tag;
  copyText(envelope.body.group_calibrate.request_id, id);
  if (listener)
    envelope.body.group_calibrate.mode = cadence_audiod_v1_GroupCalibrateMode_GROUP_CALIBRATE_MODE_LISTENER;
  return envelope;
}

Envelope listenerResult(std::string_view id, std::uint32_t runId) {
  auto envelope = groupCalibrate(id);
  envelope.body.group_calibrate.mode = cadence_audiod_v1_GroupCalibrateMode_GROUP_CALIBRATE_MODE_LISTENER_RESULT;
  envelope.body.group_calibrate.run_id = runId;
  return envelope;
}

Envelope leaderConfig() {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_group_config_tag;
  auto& g = envelope.body.group_config;
  g.enabled = true;
  copyText(g.group_id, "g1");
  g.role = cadence_audiod_v1_GroupRole_GROUP_ROLE_LEADER;
  copyText(g.member_id, "leader");
  g.port = 0;
  g.key.size = 32;
  std::memset(g.key.bytes, 0x42, 32);
  g.latency_ms = 500;
  g.members_count = 1;
  copyText(g.members[0], "far");
  return envelope;
}

// the next frame of body, the GroupStatus and others before it skipped
Envelope expectBody(int fd, pb_size_t body) {
  for (int i = 0; i < 20; ++i) {
    const auto received = receive(fd, 3s);
    REQUIRE(received.kind == Received::Kind::frame);
    if (received.envelope.which_body == body)
      return received.envelope;
  }
  FAIL("no frame of the body");
  return {};
}

cadence_audiod_v1_GroupCalibrated expectCalibrated(int fd) {
  return expectBody(fd, cadence_audiod_v1_Envelope_group_calibrated_tag).body.group_calibrated;
}

}  // namespace

TEST_CASE("calibration: the capture records the window with its read stamps, the chirp in it found", "[group]") {
  AudiodHarness audiod;
  const auto wav = chirpWav(audiod, 0.6, 3.0);
  auto client = connectTo(audiod.socket());
  sendFrame(client.get(), configure(audiod, wav));
  expectReady(client.get());

  // the file plays from the recording's open: its 0.6 s are about 0.3 s into a window that opens 0.3 s later
  const auto r = recordNow(audiod, 300'000'000, 1'300'000'000);
  REQUIRE(r.code.empty());
  const auto& rec = r.recording;
  CHECK(rec.rate == 16000);
  // the file backend's one microphone
  REQUIRE(rec.channels == 1);
  CHECK(rec.frames() > 14'000);
  CHECK(rec.frames() < 19'000);
  REQUIRE(rec.stamps.size() > 10);
  for (std::size_t i = 1; i < rec.stamps.size(); ++i) {
    CHECK(rec.stamps[i].endFrame > rec.stamps[i - 1].endFrame);
    CHECK(rec.stamps[i].monoNs >= rec.stamps[i - 1].monoNs);
  }
  std::vector<float> mono;
  for (auto v : rec.pcm)
    mono.push_back(static_cast<float>(v) / 32768.0f);
  const auto corr = sync::calibration::correlate(mono, sync::calibration::chirpSamples({}, 16000));
  const auto found = sync::calibration::findArrival(corr, 0.1 * 16000, 0.6 * 16000);
  REQUIRE(found.code == sync::calibration::ArrivalCode::Ok);
  CHECK(found.lag / 16000 > 0.2);
  CHECK(found.lag / 16000 < 0.4);
  CHECK(audiod.logs().eventually(event("calibration_recorded")));
}

TEST_CASE("calibration: nothing is recorded while muted, or with no capture configured", "[group]") {
  AudiodHarness audiod;
  SECTION("unconfigured") {
    CHECK(recordNow(audiod, 0, 200'000'000).code == "no_capture");
  }
  SECTION("muted") {
    const auto wav = chirpWav(audiod, 0.2, 2.0);
    auto client = connectTo(audiod.socket());
    sendFrame(client.get(), configure(audiod, wav));
    expectReady(client.get());
    sendFrame(client.get(), muteState(true));
    REQUIRE(audiod.logs().eventually(event("capture_muted")));
    CHECK(recordNow(audiod, 0, 300'000'000).code == "muted");
  }
  SECTION("a window already past, or longer than a calibration's") {
    const auto wav = chirpWav(audiod, 0.2, 2.0);
    auto client = connectTo(audiod.socket());
    sendFrame(client.get(), configure(audiod, wav));
    expectReady(client.get());
    CHECK(recordNow(audiod, -2'000'000'000, -1'000'000'000).code == "no_capture");
    CHECK(recordNow(audiod, 0, 61'000'000'000).code == "no_capture");
  }
}

TEST_CASE("calibration: a device that leads no group, a gated leader and a leader alone are refused, in either mode",
          "[group]") {
  HarnessMedia media;
  media.group = true;
  AudiodHarness audiod(compiledBackends(), {}, openOutput, openCapture, std::move(media));
  const auto wav = chirpWav(audiod, 0.2, 2.0);
  auto client = connectTo(audiod.socket());
  sendFrame(client.get(), configure(audiod, wav));
  expectReady(client.get());

  sendFrame(client.get(), groupCalibrate("c1"));
  auto result = expectCalibrated(client.get());
  CHECK(std::string_view(result.request_id) == "c1");
  CHECK(std::string_view(result.code) == "not_leader");
  sendFrame(client.get(), groupCalibrate("l1", true));
  result = expectCalibrated(client.get());
  CHECK(std::string_view(result.request_id) == "l1");
  CHECK(std::string_view(result.code) == "not_leader");

  sendFrame(client.get(), leaderConfig());
  REQUIRE(audiod.logs().eventually(event("group_started")));
  // no volume set yet: nothing may sound (rule 7)
  sendFrame(client.get(), groupCalibrate("c2"));
  result = expectCalibrated(client.get());
  CHECK(std::string_view(result.request_id) == "c2");
  CHECK(std::string_view(result.code) == "gated");
  sendFrame(client.get(), groupCalibrate("l2", true));
  result = expectCalibrated(client.get());
  CHECK(std::string_view(result.request_id) == "l2");
  CHECK(std::string_view(result.code) == "gated");

  sendFrame(client.get(), volumeSet(20));
  expectBody(client.get(), cadence_audiod_v1_Envelope_volume_applied_tag);
  sendFrame(client.get(), groupCalibrate("c3"));
  result = expectCalibrated(client.get());
  CHECK(std::string_view(result.request_id) == "c3");
  CHECK(std::string_view(result.code) == "too_few");
  // a listener calibration needs no microphone, but a member to play with the leader
  sendFrame(client.get(), groupCalibrate("l3", true));
  result = expectCalibrated(client.get());
  CHECK(std::string_view(result.request_id) == "l3");
  CHECK(std::string_view(result.code) == "too_few");
  CHECK(result.slots_count == 0);
  CHECK(result.run_id == 0);
  // no listener run was made: its speakers' result is expired, not waited for
  sendFrame(client.get(), listenerResult("r1", 7));
  result = expectCalibrated(client.get());
  CHECK(std::string_view(result.request_id) == "r1");
  CHECK(std::string_view(result.code) == "expired");
  CHECK(audiod.logs().eventually(event("group_calibrated")));
}
