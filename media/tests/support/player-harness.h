#pragma once

#include "backend/pcm-backend.h"
#include "playback/alsa-player.h"
#include "support/eventually.h"
#include "support/fake-sink.h"
#include "support/samples.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <thread>
#include <vector>

namespace cadence::test_support {

// a smaller device buffer than pickle's keeps the tests quick
inline const AlsaPlayerConfig kConfig{.periodFrames = 480, .bufferFrames = 1920, .maxDeviceFillFrames = 1440, .jitter = {}};
inline constexpr std::size_t kPeriod = 480;
inline constexpr std::size_t kFill = 1440;
// the 100 ms start target of 48 kHz mono
inline constexpr std::size_t kTarget = 4800;
// the backend's limiter look-ahead: what the player reads is written this many frames later
inline constexpr std::size_t kLatency = PcmBackend::latencyFrames();

inline bool all(const std::vector<std::int16_t>& samples, std::int16_t value) {
  return !samples.empty() && std::all_of(samples.begin(), samples.end(), [value](std::int16_t sample) { return sample == value; });
}

// plays frames through the fake speaker as the player refills it
inline std::vector<std::int16_t> listen(fake_sink::FakePcmSink& sink, std::size_t frames) {
  std::vector<std::int16_t> heard;
  const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(5);
  while (heard.size() < frames * 2 && std::chrono::steady_clock::now() < deadline) {
    const auto played = sink.play(frames - heard.size() / 2);
    if (played.empty())
      std::this_thread::sleep_for(std::chrono::milliseconds(1));
    heard.insert(heard.end(), played.begin(), played.end());
  }
  REQUIRE(heard.size() == frames * 2);
  return heard;
}

// the device is filled to at least the cap with this response audio
inline bool deviceHolds(const fake_sink::FakePcmSink& sink, std::int16_t value) {
  return sink.bufferedFrames() >= kFill && all(sink.buffered(), value);
}

// a response queued before the player starts reaches the device behind the look-ahead's silence: that plays, then
// the device fills with the response
inline bool fillsWith(fake_sink::FakePcmSink& sink, std::int16_t value) {
  if (!silent(listen(sink, kLatency)))
    return false;
  return eventually([&] { return deviceHolds(sink, value); });
}

// dmix still plays the period the hardware is on; the player took back everything after it, so the device
// holds at most one period of older audio ahead of silence
inline void expectAtMostOneStalePeriod(const fake_sink::FakePcmSink& sink) {
  const auto held = sink.buffered();
  const auto stale = std::min(held.size(), kPeriod * 2);
  CHECK(silent(std::vector(held.begin() + static_cast<std::ptrdiff_t>(stale), held.end())));
}

// what a discard leaves: the stale period plays out, then only silence
inline void expectSilenceAfterOnePeriod(fake_sink::FakePcmSink& sink) {
  listen(sink, kPeriod);
  CHECK(silent(listen(sink, kFill * 3)));
}

}  // namespace cadence::test_support
