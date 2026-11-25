// libairplay's part in audiod as main wires it on a screen: AirplayConfig over the socket, the real receiver on
// loopback, the gate (rule 7), the pin, mirroring to the screen's tap with the presentation clock, and the sender's
// slider. the sender's streams are driven through the session as libairplay's threads would (fairplay and the stream
// keys need a real apple sender), an hls cast's included
#include "audiod/airplay-uxplay-handlers.h"
#include "audiod/airplay-volume.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/media-client.h"
#include "support/mirror-frames.h"
#include "support/playback-client.h"

#include <catch2/catch_test_macros.hpp>

#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

#include <chrono>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <functional>
#include <optional>
#include <string>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

constexpr auto kOff = cadence_audiod_v1_AirplayState_AIRPLAY_STATE_OFF;
constexpr auto kStarting = cadence_audiod_v1_AirplayState_AIRPLAY_STATE_STARTING;
constexpr auto kReady = cadence_audiod_v1_AirplayState_AIRPLAY_STATE_READY;
constexpr auto kFailed = cadence_audiod_v1_AirplayState_AIRPLAY_STATE_FAILED;
constexpr std::int64_t kMs = 1'000'000;

std::int64_t now(clockid_t id) {
  timespec value{};
  ::clock_gettime(id, &value);
  return static_cast<std::int64_t>(value.tv_sec) * 1'000'000'000 + value.tv_nsec;
}

// the frames until one of body that predicate takes, that one; the rest are skipped
std::optional<Envelope> until(int fd, pb_size_t body, const std::function<bool(const Envelope&)>& predicate = {},
                              std::chrono::milliseconds limit = 10s) {
  const auto deadline = std::chrono::steady_clock::now() + limit;
  while (std::chrono::steady_clock::now() < deadline) {
    const auto received = receive(fd, 100ms);
    if (received.kind == Received::Kind::closed)
      return std::nullopt;
    if (received.kind == Received::Kind::frame && received.envelope.which_body == body &&
        (!predicate || predicate(received.envelope)))
      return received.envelope;
  }
  return std::nullopt;
}

std::optional<Envelope> untilState(int fd, cadence_audiod_v1_AirplayState state) {
  return until(fd, cadence_audiod_v1_Envelope_airplay_status_tag,
               [state](const Envelope& e) { return e.body.airplay_status.state == state; });
}

Envelope airplayConfig(const std::string& scratch, bool enabled, bool pin,
                       cadence_audiod_v1_AirplayBackend backend = cadence_audiod_v1_AirplayBackend_AIRPLAY_BACKEND_LIBAIRPLAY) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_airplay_config_tag;
  auto& body = envelope.body.airplay_config;
  body.enabled = enabled;
  copyText(body.name, "Living Room");
  copyText(body.scratch_dir, scratch);
  body.backend = backend;
  body.mirroring = true;
  body.max_width = 1920;
  body.max_height = 1080;
  body.require_pin = pin;
  return envelope;
}

Envelope tapConfig(const std::string& path, bool mirror) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_tap_config_tag;
  copyText(envelope.body.tap_config.path, path);
  envelope.body.tap_config.mirror = mirror;
  return envelope;
}

Envelope speakerLevel(std::uint32_t level) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_sender_volume_set_tag;
  envelope.body.sender_volume_set.sender = cadence_audiod_v1_Sender_SENDER_AIRPLAY;
  envelope.body.sender_volume_set.level = level;
  return envelope;
}

int rtspStatus(std::uint16_t port, const std::string& request) {
  const int fd = ::socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
  REQUIRE(fd >= 0);
  timeval timeout{2, 0};
  ::setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof timeout);
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_port = htons(port);
  address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  REQUIRE(::connect(fd, reinterpret_cast<sockaddr*>(&address), sizeof address) == 0);
  ::send(fd, request.data(), request.size(), MSG_NOSIGNAL);
  char buffer[256] = {};
  const auto size = ::recv(fd, buffer, sizeof buffer - 1, 0);
  ::close(fd);
  return size > 12 ? std::stoi(std::string(buffer + 9, 3)) : 0;
}

UniqueFd connectTap(const std::string& path) {
  sockaddr_un address{};
  address.sun_family = AF_UNIX;
  std::memcpy(address.sun_path, path.data(), path.size());
  UniqueFd fd(::socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0));
  REQUIRE(fd);
  const int buffer = 8 << 20;
  ::setsockopt(fd.get(), SOL_SOCKET, SO_RCVBUF, &buffer, sizeof buffer);
  if (::connect(fd.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) != 0)
    return UniqueFd();
  return fd;
}

// the tap's messages until one predicate takes
std::optional<TapMessage> untilTap(int fd, const std::function<bool(const TapMessage&)>& predicate,
                                   std::chrono::milliseconds limit = 5s) {
  timeval timeout{0, 100'000};
  ::setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof timeout);
  std::vector<std::uint8_t> buffer(1 << 20);
  const auto deadline = std::chrono::steady_clock::now() + limit;
  while (std::chrono::steady_clock::now() < deadline) {
    const auto size = ::recv(fd, buffer.data(), buffer.size(), 0);
    if (size <= 0)
      continue;
    auto message = decodeTap({buffer.data(), static_cast<std::size_t>(size)});
    if (message && predicate(*message))
      return message;
  }
  return std::nullopt;
}

// the persisted level set and held: the gate opens (rule 7). waits longer than expectApplied, as the suites share the
// machine with the other configurations' tests
void openGate(int fd) {
  sendFrame(fd, volumeSet(30));
  REQUIRE(until(fd, cadence_audiod_v1_Envelope_volume_applied_tag,
                [](const Envelope& e) { return e.body.volume_applied.ok; }));
}

HarnessMedia uxplayMedia() {
  HarnessMedia media;
  media.uxplay = uxplay::UxplayOptions{.port = 0};
  return media;
}

std::vector<std::uint8_t> l16(std::size_t frames, std::int16_t value) {
  std::vector<std::uint8_t> out;
  for (std::size_t i = 0; i < 2 * frames; ++i) {
    out.push_back(static_cast<std::uint8_t>(static_cast<std::uint16_t>(value) >> 8));
    out.push_back(static_cast<std::uint8_t>(value & 0xff));
  }
  return out;
}

}  // namespace

TEST_CASE("the receiver starts once the persisted level holds, answers senders, and stops when disabled") {
  AudiodHarness audiod(compiledBackends(), {}, openOutput, openCapture, uxplayMedia());
  auto client = connectConfigured(audiod, audiod.file("out"));
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), true, false));
  // rule 7: nothing runs before a VolumeSet held
  REQUIRE(untilState(client.get(), kStarting));
  CHECK(audiod.onLoop([&](auto&, auto&) { return audiod.uxplay()->port(); }) == 0);
  sendFrame(client.get(), volumeSet(30));
  REQUIRE(untilState(client.get(), kReady));
  const auto port = audiod.onLoop([&](auto&, auto&) { return audiod.uxplay()->port(); });
  REQUIRE(port != 0);
  CHECK(rtspStatus(port, "GET /info RTSP/1.0\r\nCSeq: 1\r\n\r\n") == 200);
  CHECK(audiod.logs().count("airplay_receiver_started") == 1);
  // its files under the scratch, private
  CHECK(std::filesystem::exists(audiod.file("airplay/device-id")));
  CHECK(std::filesystem::exists(audiod.file("airplay/airplay.pem")));

  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), false, false));
  REQUIRE(untilState(client.get(), kOff));
  CHECK(audiod.onLoop([&](auto&, auto&) { return audiod.uxplay()->port(); }) == 0);
}

TEST_CASE("the receiver keeps its device id across restarts") {
  AudiodHarness audiod(compiledBackends(), {}, openOutput, openCapture, uxplayMedia());
  auto client = connectConfigured(audiod, audiod.file("out"));
  openGate(client.get());
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), true, false));
  REQUIRE(untilState(client.get(), kReady));
  std::string first;
  std::getline(std::ifstream(audiod.file("airplay/device-id")) >> std::ws, first);
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), false, false));
  REQUIRE(untilState(client.get(), kOff));
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), true, false));
  REQUIRE(untilState(client.get(), kReady));
  std::string second;
  std::getline(std::ifstream(audiod.file("airplay/device-id")) >> std::ws, second);
  CHECK(first.size() == 12);
  CHECK(first == second);
}

TEST_CASE("a shairport config on a libairplay build fails, and one without a scratch is invalid") {
  AudiodHarness audiod(compiledBackends(), {}, openOutput, openCapture, uxplayMedia());
  auto client = connectConfigured(audiod, audiod.file("out"));
  openGate(client.get());
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), true, false,
                                        cadence_audiod_v1_AirplayBackend_AIRPLAY_BACKEND_SHAIRPORT));
  auto failed = untilState(client.get(), kFailed);
  REQUIRE(failed);
  CHECK(std::string(failed->body.airplay_status.code) == "backend_unavailable");
  sendFrame(client.get(), airplayConfig("", true, false));
  failed = untilState(client.get(), kFailed);
  REQUIRE(failed);
  CHECK(std::string(failed->body.airplay_status.code) == "invalid_config");
}

TEST_CASE("a sender asking for the pin has it shown through cadenced, never logged") {
  AudiodHarness audiod(compiledBackends(), {}, openOutput, openCapture, uxplayMedia());
  auto client = connectConfigured(audiod, audiod.file("out"));
  openGate(client.get());
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), true, true));
  REQUIRE(untilState(client.get(), kReady));
  const auto port = audiod.onLoop([&](auto&, auto&) { return audiod.uxplay()->port(); });
  CHECK(rtspStatus(port, "POST /pair-pin-start RTSP/1.0\r\nCSeq: 1\r\nContent-Length: 0\r\n\r\n") == 200);
  const auto shown = until(client.get(), cadence_audiod_v1_Envelope_airplay_pin_tag);
  REQUIRE(shown);
  CHECK(shown->body.airplay_pin.active);
  const std::string pin = shown->body.airplay_pin.pin;
  CHECK(pin.size() == 4);
  CHECK(audiod.logs().count("airplay_pin_shown") == 1);
  CHECK(audiod.logs().count(pin) == 0);
  // the sender paired: hidden
  audiod.uxplay()->session().registered("AA:BB", std::string(43, 'k') + "=");
  const auto hidden = until(client.get(), cadence_audiod_v1_Envelope_airplay_pin_tag);
  REQUIRE(hidden);
  CHECK_FALSE(hidden->body.airplay_pin.active);
  CHECK(std::filesystem::exists(audiod.file("airplay/paired")));
}

TEST_CASE("mirroring reaches cadenced and the screen's tap: config, keyframe, pts and the presentation clock") {
  AudiodHarness audiod(compiledBackends(), {}, openOutput, openCapture, uxplayMedia());
  auto client = connectConfigured(audiod, audiod.file("out"));
  openGate(client.get());
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), true, false));
  REQUIRE(untilState(client.get(), kReady));
  const auto tapPath = audiod.file("tap.sock");
  sendFrame(client.get(), tapConfig(tapPath, true));
  REQUIRE(audiod.logs().eventually("\"msg\":\"tap_config\""));
  auto tap = connectTap(tapPath);
  REQUIRE(tap);
  REQUIRE(audiod.logs().eventually("\"msg\":\"tap_client_connected\""));

  auto& session = audiod.uxplay()->session();
  session.connectionOpened();
  bool admit = false;
  session.client("AA:BB", "iPhone15,2", "Masha's iPhone", admit);
  session.videoCodec(false);
  session.videoSize(1920, 886);
  session.mirrorRunning(true);
  const auto started = until(client.get(), cadence_audiod_v1_Envelope_mirror_state_tag);
  REQUIRE(started);
  const auto& state = started->body.mirror_state;
  CHECK(state.active);
  const std::string id = state.session_id;
  CHECK(id == "airplay-1");
  CHECK(state.codec == cadence_audiod_v1_MirrorCodec_MIRROR_CODEC_H264);
  CHECK(std::string(state.client_name) == "Masha's iPhone");
  // the sender's name is memory-only
  CHECK(audiod.logs().count("Masha") == 0);
  // the session is a screencast, on the mixer
  const auto media = until(client.get(), cadence_audiod_v1_Envelope_media_state_tag, [](const Envelope& e) {
    return e.body.media_state.source == cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_SCREENCAST;
  });
  REQUIRE(media);

  // the access unit, its pts the sender's clock; due 200 ms from now
  const std::int64_t remote = 3'600'000 * kMs;
  const std::vector<std::uint8_t> idr{0, 0, 0, 1, 0x67, 0x42, 0, 0, 0, 1, 0x68, 0xce, 0, 0, 0, 1, 0x65, 0x88, 0x84};
  const auto dueMono = now(CLOCK_MONOTONIC) + 200 * kMs;
  session.video(uxplay::VideoPacket{.data = idr, .h265 = false, .nalCount = 3,
                                    .localNs = now(CLOCK_REALTIME) + 200 * kMs, .remoteNs = remote});
  const auto config = untilTap(tap.get(), [](const TapMessage& m) { return m.mirror() && m.frame().config; });
  REQUIRE(config);
  CHECK(std::string(config->frame().session_id) == id);
  CHECK(config->data == std::vector<std::uint8_t>(idr.begin(), idr.begin() + 12));
  const auto key = untilTap(tap.get(), [](const TapMessage& m) { return m.mirror() && m.frame().keyframe; });
  REQUIRE(key);
  CHECK(key->frame().pts_ns == remote);
  CHECK(key->data == std::vector<std::uint8_t>(idr.begin() + 12, idr.end()));

  // the clock maps the sender's media time onto when it is heard: remote is heard at dueMono
  const auto clock = untilTap(tap.get(), [](const TapMessage& m) {
    return m.envelope.which_body == cadence_audiod_v1_TapEnvelope_clock_tag;
  });
  REQUIRE(clock);
  const auto& c = clock->envelope.body.clock;
  const auto offset = (c.media_ns - c.mono_ns) - (remote - dueMono);
  CHECK(offset > -40 * kMs);
  CHECK(offset < 40 * kMs);

  // the sender's audio flows into the mixer and anchors the clock where it is heard
  for (int i = 0; i < 20; ++i)
    session.audio(uxplay::AudioPacket{.data = l16(441, 8000), .codec = uxplay::AudioCodec::Pcm,
                                      .localNs = now(CLOCK_REALTIME) + 300 * kMs + i * 10 * kMs,
                                      .remoteNs = remote + 1000 * kMs + i * 10 * kMs, .rtpTime = 0, .seq = 0});
  REQUIRE(eventually([&] { return audiod.playback().music().status().framesIn > 0; }));

  // the sender stops mirroring: the screen is told, the clock unanchored
  session.mirrorRunning(false);
  const auto stopped = until(client.get(), cadence_audiod_v1_Envelope_mirror_state_tag);
  REQUIRE(stopped);
  CHECK_FALSE(stopped->body.mirror_state.active);
  CHECK(std::string(stopped->body.mirror_state.reason) == "sender");
  session.connectionClosed();
}

TEST_CASE("disabling the receiver ends a mirror session on the screen") {
  AudiodHarness audiod(compiledBackends(), {}, openOutput, openCapture, uxplayMedia());
  auto client = connectConfigured(audiod, audiod.file("out"));
  openGate(client.get());
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), true, false));
  REQUIRE(untilState(client.get(), kReady));
  auto& session = audiod.uxplay()->session();
  session.connectionOpened();
  session.mirrorRunning(true);
  REQUIRE(until(client.get(), cadence_audiod_v1_Envelope_mirror_state_tag));
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), false, false));
  const auto stopped = until(client.get(), cadence_audiod_v1_Envelope_mirror_state_tag);
  REQUIRE(stopped);
  CHECK_FALSE(stopped->body.mirror_state.active);
  CHECK(std::string(stopped->body.mirror_state.reason) == "disabled");
}

// the screen's stop key: a mirror with no audio is the media session alone, so cadenced's stop of the session ends it
TEST_CASE("a stop of the screencast session lets the sender go, its mirror ended with reason stopped") {
  AudiodHarness audiod(compiledBackends(), {}, openOutput, openCapture, uxplayMedia());
  auto client = connectConfigured(audiod, audiod.file("out"));
  openGate(client.get());
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), true, false));
  REQUIRE(untilState(client.get(), kReady));
  auto& session = audiod.uxplay()->session();
  session.connectionOpened();
  session.videoCodec(false);
  session.mirrorRunning(true);
  REQUIRE(until(client.get(), cadence_audiod_v1_Envelope_mirror_state_tag));
  REQUIRE(until(client.get(), cadence_audiod_v1_Envelope_media_state_tag, [](const Envelope& e) {
    return e.body.media_state.source == cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_SCREENCAST &&
           e.body.media_state.state == kPlaying;
  }));

  sendFrame(client.get(), mediaCommand("r-stop", kStop));
  std::optional<CommandResultFrame> result;
  std::optional<cadence_audiod_v1_MirrorState> ended;
  bool idle = false;
  const auto deadline = std::chrono::steady_clock::now() + 10s;
  while ((!result || !ended || !idle) && std::chrono::steady_clock::now() < deadline) {
    const auto received = receive(client.get(), 100ms);
    if (received.kind == Received::Kind::closed)
      break;
    if (received.kind != Received::Kind::frame)
      continue;
    const auto& e = received.envelope;
    if (e.which_body == cadence_audiod_v1_Envelope_command_result_tag)
      result = e.body.command_result;
    else if (e.which_body == cadence_audiod_v1_Envelope_mirror_state_tag)
      ended = e.body.mirror_state;
    else if (e.which_body == cadence_audiod_v1_Envelope_media_state_tag && e.body.media_state.state == kIdle)
      idle = true;
  }
  REQUIRE(result);
  CHECK(std::string(result->request_id) == "r-stop");
  CHECK(result->ok);
  REQUIRE(ended);
  CHECK_FALSE(ended->active);
  CHECK(std::string(ended->session_id) == "airplay-1");
  CHECK(std::string(ended->reason) == "stopped");
  CHECK(idle);
  // the sender is let go (libairplay drops its connection)
  CHECK(audiod.logs().eventually("\"msg\":\"airplay_released\""));
  session.connectionClosed();
}

TEST_CASE("the sender's slider reaches cadenced as SenderVolume, its stored level as it connects does not") {
  AudiodHarness audiod(compiledBackends(), {}, openOutput, openCapture, uxplayMedia());
  auto client = connectConfigured(audiod, audiod.file("out"));
  openGate(client.get());
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), true, false));
  REQUIRE(untilState(client.get(), kReady));
  sendFrame(client.get(), speakerLevel(40));
  auto& session = audiod.uxplay()->session();
  REQUIRE(eventually([&] { return session.clientVolume() == airplay::toDb(40); }));
  session.connectionOpened();
  session.volume(static_cast<float>(airplay::toDb(70)));
  session.volume(static_cast<float>(airplay::toDb(55)));
  const auto moved = until(client.get(), cadence_audiod_v1_Envelope_sender_volume_tag);
  REQUIRE(moved);
  CHECK(moved->body.sender_volume.sender == cadence_audiod_v1_Sender_SENDER_AIRPLAY);
  CHECK(moved->body.sender_volume.level == 55);
  session.connectionClosed();
}

namespace {

Envelope playbackInfo(const std::string& id, std::uint64_t position, bool ended = false) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_hls_playback_info_tag;
  auto& info = envelope.body.hls_playback_info;
  copyText(info.session_id, id);
  info.position_ms = position;
  info.duration_ms = 600'000;
  info.rate = 1;
  info.ready = true;
  info.buffered_ms = 5'000;
  info.likely_to_keep_up = true;
  info.ended = ended;
  if (ended)
    copyText(info.reason, "finished");
  return envelope;
}

std::optional<Envelope> untilCast(int fd, cadence_audiod_v1_HlsCastAction action) {
  return until(fd, cadence_audiod_v1_Envelope_hls_cast_tag,
               [action](const Envelope& e) { return e.body.hls_cast.action == action; });
}

}  // namespace

TEST_CASE("an hls cast: advertised, each sender request reaches cadenced as HlsCast, its playback answers the sender") {
  AudiodHarness audiod(compiledBackends(), {}, openOutput, openCapture, uxplayMedia());
  auto client = connectConfigured(audiod, audiod.file("out"));
  openGate(client.get());
  sendFrame(client.get(), airplayConfig(audiod.file("airplay"), true, false));
  REQUIRE(untilState(client.get(), kReady));
  // a screen's receiver offers video by url: features bits 0 (video) and 4 (hls)
  const auto txt = audiod.onLoop([&](auto&, auto&) { return audiod.uxplay()->receiver()->airplayTxt(); });
  const auto at = txt.find("features=");
  REQUIRE(at != std::string::npos);
  const auto low = std::stoul(txt.substr(at + 9, txt.find(',', at) - at - 9), nullptr, 16);
  CHECK((low & 1U) != 0);
  CHECK((low & (1U << 4)) != 0);

  // the fake sender, as libairplay's httpd thread calls the session: /play, /scrub, /rate
  auto& session = audiod.uxplay()->session();
  session.connectionOpened();
  bool admit = false;
  session.client("AA:BB", "iPhone15,2", "Phone of the owner", admit);
  const std::string url = "http://127.0.0.1:7100/master.m3u8?id=secret";
  session.hlsPlay(url, 12.5f);
  const auto play = untilCast(client.get(), cadence_audiod_v1_HlsCastAction_HLS_CAST_ACTION_PLAY);
  REQUIRE(play);
  const std::string id = play->body.hls_cast.session_id;
  CHECK(id.starts_with("airplay-hls-"));
  CHECK(std::string(play->body.hls_cast.url) == url);
  CHECK(play->body.hls_cast.start_ms == 12'500);
  CHECK(std::string(play->body.hls_cast.client_name) == "Phone of the owner");
  // before the screen says anything, the sender hears nothing is ready
  CHECK_FALSE(session.hlsInfo().ready);

  session.hlsScrub(30.0f);
  const auto scrub = untilCast(client.get(), cadence_audiod_v1_HlsCastAction_HLS_CAST_ACTION_SCRUB);
  REQUIRE(scrub);
  CHECK(std::string(scrub->body.hls_cast.session_id) == id);
  CHECK(scrub->body.hls_cast.position_ms == 30'000);
  session.hlsRate(0.0f);
  const auto rate = untilCast(client.get(), cadence_audiod_v1_HlsCastAction_HLS_CAST_ACTION_RATE);
  REQUIRE(rate);
  CHECK(rate->body.hls_cast.rate == 0.0f);

  // the screen's playback, as cadenced reports it, is what /playback-info answers; another cast's report is not
  sendFrame(client.get(), playbackInfo("airplay-hls-99", 1'000));
  sendFrame(client.get(), playbackInfo(id, 31'000));
  REQUIRE(eventually([&] { return session.hlsInfo().ready; }));
  const auto info = session.hlsInfo();
  CHECK(info.positionS == 31.0);
  CHECK(info.durationS == 600.0);
  CHECK(info.rate == 1.0f);
  CHECK(info.bufferedS == 5.0);
  CHECK(info.likelyToKeepUp);
  CHECK_FALSE(info.bufferEmpty);

  // it ended on the screen: the sender's session ends, and its stop's echo tells cadenced nothing more
  sendFrame(client.get(), playbackInfo(id, 600'000, true));
  REQUIRE(eventually([&] { return audiod.logs().count("airplay_hls_ended") == 1; }));
  session.hlsStop();
  CHECK_FALSE(until(client.get(), cadence_audiod_v1_Envelope_hls_cast_tag, {}, 500ms));

  // the next cast is a session of its own, and the sender's stop ends it
  session.hlsPlay(url, 0.0f);
  const auto second = untilCast(client.get(), cadence_audiod_v1_HlsCastAction_HLS_CAST_ACTION_PLAY);
  REQUIRE(second);
  CHECK(std::string(second->body.hls_cast.session_id) != id);
  session.hlsStop();
  const auto stop = untilCast(client.get(), cadence_audiod_v1_HlsCastAction_HLS_CAST_ACTION_STOP);
  REQUIRE(stop);
  CHECK(std::string(stop->body.hls_cast.session_id) == second->body.hls_cast.session_id);
  // rule 8: neither the url nor the sender's name in any log line
  CHECK(audiod.logs().count("master.m3u8") == 0);
  CHECK(audiod.logs().count("secret") == 0);
  CHECK(audiod.logs().count("Phone of the owner") == 0);
  session.connectionClosed();
}
