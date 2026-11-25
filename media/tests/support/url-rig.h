#pragma once

#include "audiod/loop.h"
#include "audiod/media-handlers.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/http-server.h"
#include "support/media-client.h"
#include "support/playback-client.h"
#include "url/url-source.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <fstream>
#include <functional>
#include <iterator>
#include <memory>
#include <numbers>
#include <string>
#include <string_view>
#include <utility>

// audiod with the real url source, an http server for it to play from, and the wav files it serves
namespace cadence::test_support {

struct WavTags {
  std::string title;
  std::string artist;
  std::string album;
};

// a stereo 16-bit pcm wav of frames frames at rate; sample gives a frame's left and right, -1..1. tags go in a
// LIST INFO chunk before the data
inline std::string wavBytes(std::uint32_t rate, std::size_t frames,
                            const std::function<std::pair<double, double>(std::size_t frame)>& sample,
                            const WavTags& tags = {}) {
  std::string out;
  const auto u32 = [&](std::uint32_t value) {
    for (int shift = 0; shift < 32; shift += 8)
      out.push_back(static_cast<char>((value >> shift) & 0xff));
  };
  const auto u16 = [&](std::uint32_t value) {
    out.push_back(static_cast<char>(value & 0xff));
    out.push_back(static_cast<char>((value >> 8) & 0xff));
  };
  std::string info;
  const auto infoTag = [&](std::string_view id, const std::string& text) {
    if (text.empty())
      return;
    std::string value = text;
    value.push_back('\0');
    if (value.size() % 2 != 0)
      value.push_back('\0');
    info.append(id);
    for (int shift = 0; shift < 32; shift += 8)
      info.push_back(static_cast<char>((value.size() >> shift) & 0xff));
    info.append(value);
  };
  infoTag("INAM", tags.title);
  infoTag("IART", tags.artist);
  infoTag("IPRD", tags.album);
  const auto dataBytes = static_cast<std::uint32_t>(frames * 4);
  const auto listBytes = info.empty() ? 0 : static_cast<std::uint32_t>(12 + info.size());
  out.append("RIFF");
  u32(4 + 24 + listBytes + 8 + dataBytes);
  out.append("WAVEfmt ");
  u32(16);
  u16(1);
  u16(2);
  u32(rate);
  u32(rate * 4);
  u16(4);
  u16(16);
  if (!info.empty()) {
    out.append("LIST");
    u32(static_cast<std::uint32_t>(4 + info.size()));
    out.append("INFO");
    out.append(info);
  }
  out.append("data");
  u32(dataBytes);
  for (std::size_t frame = 0; frame < frames; ++frame) {
    const auto [left, right] = sample(frame);
    u16(static_cast<std::uint16_t>(static_cast<std::int16_t>(std::lround(left * 32767))));
    u16(static_cast<std::uint16_t>(static_cast<std::int16_t>(std::lround(right * 32767))));
  }
  return out;
}

// seconds of leftHz on the left and rightHz on the right
inline std::string toneWav(double seconds, std::uint32_t rate = 44100, double leftHz = 440, double rightHz = 660,
                           double amplitude = 0.5, const WavTags& tags = {}) {
  const auto frames = static_cast<std::size_t>(seconds * rate);
  return wavBytes(
      rate, frames,
      [=](std::size_t frame) {
        const double t = static_cast<double>(frame) / rate;
        return std::pair{amplitude * std::sin(2 * std::numbers::pi * leftHz * t),
                         amplitude * std::sin(2 * std::numbers::pi * rightHz * t)};
      },
      tags);
}

// the committed fixtures (media/tests/fixtures)
inline std::string fixture(std::string_view name) {
  std::ifstream in(std::string(CADENCE_MEDIA_TEST_FIXTURES) + "/" + std::string(name), std::ios::binary);
  REQUIRE(in);
  return {std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
}

// frames until done holds for what came in so far, or the limit passed
inline Frames until(int fd, const std::function<bool(const Frames&)>& done,
                    std::chrono::milliseconds limit = std::chrono::seconds(10)) {
  Frames frames;
  const auto deadline = std::chrono::steady_clock::now() + limit;
  while (!done(frames) && std::chrono::steady_clock::now() < deadline) {
    const auto received = receive(fd, std::chrono::milliseconds(50));
    if (received.kind == Received::Kind::closed)
      break;
    if (received.kind == Received::Kind::frame)
      collect(frames, received.envelope);
  }
  return frames;
}

inline bool hasResult(const Frames& frames, std::string_view requestId) {
  for (const auto& result : frames.results)
    if (std::string_view(result.request_id) == requestId)
      return true;
  return false;
}

inline bool hasState(const Frames& frames, cadence_audiod_v1_PlaybackState state) {
  for (const auto& frame : frames.states)
    if (frame.state == state)
      return true;
  return false;
}

// the first state among frames in that state
inline MediaStateFrame firstState(const Frames& frames, cadence_audiod_v1_PlaybackState state) {
  for (const auto& frame : frames.states)
    if (frame.state == state)
      return frame;
  FAIL("no such state");
  return {};
}

// audiod with the url source main makes, on the file backend with a hardware volume control so the music plays at
// unity once the gate opened. the server outlives it. a stopped decoder may still be closing its stream when the rig
// is gone; the factory, the https server and the test certificates all keep openssl from cleaning up at exit, so one
// still running as the test process ends finds it intact
class UrlRig {
 public:
  explicit UrlRig(url::UrlOptions options = {}, std::chrono::milliseconds playBound = audiod::kUrlPlayBound)
      : volume_(std::make_shared<FakeVolume>()),
        audiod_(audiod::compiledBackends(), {}, withFakeControl(volume_), audiod::openCapture,
                HarnessMedia{.playBound = playBound,
                             .make = [options](audiod::Loop& loop, const audiod::Log& log) {
                               return url::urlSourceFactory(
                                   log, [&loop](std::function<void()> task) { loop.post(std::move(task)); }, options);
                             }}) {}

  [[nodiscard]] HttpServer& server() { return server_; }
  [[nodiscard]] AudiodHarness& audiod() { return audiod_; }
  [[nodiscard]] std::string out() const { return audiod_.file("out"); }
  [[nodiscard]] std::string mix() const { return out() + "/mix/mix-000.wav"; }

  // configured with that ca bundle, its idle state taken, and the gate open
  audiod::UniqueFd connect(std::string_view caFile = {}) {
    auto client = connectTo(audiod_.socket());
    auto configuration = configureOutput(out(), "Master");
    copyText(configuration.body.configure.ca_file, caFile);
    sendFrame(client.get(), configuration);
    expectReady(client.get());
    expectApplied(client.get(), 40, true);
    return client;
  }

 private:
  HttpServer server_;
  std::shared_ptr<FakeVolume> volume_;
  AudiodHarness audiod_;
};

}  // namespace cadence::test_support
