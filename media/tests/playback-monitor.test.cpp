#include "capture/playback-monitor.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <algorithm>
#include <array>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <numbers>
#include <optional>
#include <vector>

using namespace cadence;
using Catch::Matchers::WithinAbs;

namespace {

constexpr int kRate = 48000;
constexpr std::size_t kWindowFrames = 960;  // 20 ms
constexpr double kAmplitude = 0.25;
// the mapping led/audio-level.cpp gives the rms of a 0.25 sine: 20*log10(0.25/sqrt(2)) is -15.05 db
constexpr double kToneLevel = 0.8737;
// one active reference of two: rms 0.125, -18.06 db
constexpr double kHalfEnergyLevel = 0.7984;
constexpr std::size_t kSettleWindows = 15;  // 300 ms of filter warm-up

CaptureLayout map(std::size_t channels, std::vector<std::size_t> microphones, std::vector<std::size_t> speakers) {
  CaptureLayout capture;
  capture.device = "default";
  capture.sampleRate = kRate;
  capture.channels = channels;
  capture.microphones = std::move(microphones);
  capture.speakers = std::move(speakers);
  capture.periodFrames = 256;
  capture.bufferFrames = 8192;
  return capture;
}

CaptureLayout pickleMap() { return map(4, {0, 1, 2}, {3}); }

CaptureLayout midiMap() { return map(6, {0, 1, 2, 3}, {4, 5}); }

double tone(std::size_t frame, double frequency) {
  return kAmplitude * std::sin(2 * std::numbers::pi * frequency * static_cast<double>(frame) / kRate);
}

double bassTone(std::size_t frame, std::size_t) { return tone(frame, 100.0); }

double silence(std::size_t, std::size_t) { return 0.0; }

// feeds frames of interleaved capture in the given chunk sizes, sample(frame, channel) in [-1, 1],
// and returns every completed window in order
std::vector<MusicLevels> feed(PlaybackMonitor& monitor, std::size_t channels, std::size_t frames,
                              const std::function<double(std::size_t, std::size_t)>& sample,
                              const std::vector<std::size_t>& chunks) {
  std::vector<MusicLevels> windows;
  std::vector<std::int16_t> chunk;
  std::size_t start = 0;
  std::size_t step = 0;
  while (start < frames) {
    const auto count = std::min(chunks[step++ % chunks.size()], frames - start);
    chunk.clear();
    for (std::size_t frame = 0; frame < count; ++frame)
      for (std::size_t channel = 0; channel < channels; ++channel)
        chunk.push_back(static_cast<std::int16_t>(
            std::clamp(std::lround(sample(start + frame, channel) * 32768.0), -32768L, 32767L)));
    if (const auto levels = monitor.process(chunk))
      windows.push_back(*levels);
    start += count;
  }
  return windows;
}

std::vector<MusicLevels> feed(PlaybackMonitor& monitor, std::size_t channels, std::size_t frames,
                              const std::function<double(std::size_t, std::size_t)>& sample, std::size_t chunk) {
  return feed(monitor, channels, frames, sample, std::vector<std::size_t>{chunk});
}

bool zero(const MusicLevels& levels) { return levels.level == 0.0f && levels.bands == std::array<float, 3>{}; }

}  // namespace

TEST_CASE("an unconfigured monitor measures nothing") {
  PlaybackMonitor monitor;
  const std::vector<std::int16_t> loud(kWindowFrames * 4, 8192);
  CHECK_FALSE(monitor.process(loud));
}

TEST_CASE("configures the boards' digital speaker references") {
  PlaybackMonitor monitor;
  CHECK(monitor.configure(pickleMap()));
  CHECK_FALSE(monitor.process({}));
  CHECK(monitor.configure(midiMap()));
  CHECK_FALSE(monitor.process({}));
}

TEST_CASE("invalid reference maps disable monitoring and never fall back to microphones") {
  const int which = GENERATE(0, 1, 2, 3, 4, 5);
  CAPTURE(which);
  CaptureLayout bad = pickleMap();
  switch (which) {
    case 0: bad.sampleRate = 44100; break;
    case 1: bad.channels = 0; break;
    case 2: bad.speakers.clear(); break;
    case 3: bad.speakers = {4}; break;  // out of range for four channels
    case 4: bad = map(6, {0, 1, 2}, {3, 3}); break;  // the same reference twice
    case 5: bad.speakers = {2}; break;  // also a microphone
  }
  PlaybackMonitor monitor;
  REQUIRE(monitor.configure(pickleMap()));  // armed, so the bad map must disarm it
  CHECK_FALSE(monitor.configure(bad));
  const std::vector<std::int16_t> loud(kWindowFrames * 4, 8192);
  CHECK_FALSE(monitor.process(loud));
}

TEST_CASE("a tone lands in its own band") {
  const struct {
    double frequency;
    std::size_t band;
  } tones[] = {{100.0, 0}, {1000.0, 1}, {6000.0, 2}};
  for (const auto& [frequency, band] : tones) {
    CAPTURE(frequency, band);
    PlaybackMonitor monitor;
    REQUIRE(monitor.configure(pickleMap()));
    const auto windows =
        feed(monitor, 4, 20 * kWindowFrames, [frequency](std::size_t frame, std::size_t channel) {
          return channel == 3 ? tone(frame, frequency) : 0.0;
        }, 256);
    REQUIRE(windows.size() > kSettleWindows);
    for (auto window = windows.begin() + static_cast<std::ptrdiff_t>(kSettleWindows); window != windows.end(); ++window) {
      for (std::size_t other = 0; other < 3; ++other)
        if (other != band)
          CHECK(window->bands[band] > window->bands[other] + 0.15f);
      CHECK_THAT(window->level, WithinAbs(kToneLevel, 0.02));
      // butterworth sections attenuate, never add resonant gain to a settled tone
      for (const auto measured : window->bands)
        CHECK(measured <= window->level + 0.005f);
    }
  }
}

TEST_CASE("reference energy is averaged, so opposite-phase stereo does not cancel") {
  PlaybackMonitor monitor;
  REQUIRE(monitor.configure(midiMap()));
  const auto inPhase = feed(monitor, 6, 20 * kWindowFrames, [](std::size_t frame, std::size_t channel) {
    if (channel != 4 && channel != 5)
      return 0.0;
    return tone(frame, 100.0);
  }, 256);
  REQUIRE(inPhase.size() > kSettleWindows);
  const auto expected = inPhase.back();

  REQUIRE(monitor.configure(midiMap()));
  const auto antiPhase = feed(monitor, 6, 20 * kWindowFrames, [](std::size_t frame, std::size_t channel) {
    if (channel == 4)
      return tone(frame, 100.0);
    if (channel == 5)
      return -tone(frame, 100.0);
    return 0.0;
  }, 256);
  REQUIRE(antiPhase.size() > kSettleWindows);

  // negating a reference negates its filtered output exactly, so the energies match bit for bit
  CHECK_THAT(antiPhase.back().level, WithinAbs(expected.level, 1e-9));
  for (std::size_t band = 0; band < 3; ++band)
    CHECK_THAT(antiPhase.back().bands[band], WithinAbs(expected.bands[band], 1e-9));

  // one reference of two keeps its energy: half the energy, not none
  REQUIRE(monitor.configure(midiMap()));
  const auto rightOnly = feed(monitor, 6, 20 * kWindowFrames, [](std::size_t frame, std::size_t channel) {
    return channel == 5 ? tone(frame, 100.0) : 0.0;
  }, 256);
  REQUIRE(rightOnly.size() > kSettleWindows);
  CHECK(rightOnly.back().bands[0] > rightOnly.back().bands[1] + 0.15f);
  CHECK(rightOnly.back().bands[0] > rightOnly.back().bands[2] + 0.15f);
  CHECK(rightOnly.back().bands[0] > 0.5f);
  CHECK_THAT(rightOnly.back().level, WithinAbs(kHalfEnergyLevel, 0.02));
}

TEST_CASE("full-scale microphones leave the measurement unchanged") {
  PlaybackMonitor monitor;
  REQUIRE(monitor.configure(pickleMap()));
  const auto quiet = feed(monitor, 4, 20 * kWindowFrames, [](std::size_t frame, std::size_t channel) {
    return channel == 3 ? tone(frame, 100.0) : 0.0;
  }, 256);
  REQUIRE(quiet.size() > kSettleWindows);

  REQUIRE(monitor.configure(pickleMap()));
  const auto noisy = feed(monitor, 4, 20 * kWindowFrames, [](std::size_t frame, std::size_t channel) {
    if (channel == 3)
      return tone(frame, 100.0);
    return (frame % 2 == 0) ? 1.0 : -1.0;  // full-scale square wave on every microphone
  }, 256);
  REQUIRE(noisy.size() > kSettleWindows);

  // only channel 3 is read, so the microphone noise cannot change a single sample of it
  CHECK_THAT(noisy.back().level, WithinAbs(quiet.back().level, 1e-9));
  for (std::size_t band = 0; band < 3; ++band)
    CHECK_THAT(noisy.back().bands[band], WithinAbs(quiet.back().bands[band], 1e-9));
}

TEST_CASE("window results do not depend on how reads are chunked") {
  const auto run = [&](const std::vector<std::size_t>& chunks) {
    PlaybackMonitor monitor;
    REQUIRE(monitor.configure(pickleMap()));
    auto windows = feed(monitor, 4, 20 * kWindowFrames, bassTone, chunks);
    REQUIRE_FALSE(windows.empty());
    return windows;
  };
  const auto even = run({256});
  const auto irregular = run({7, 101, 257, 64});
  const auto whole = run({20 * kWindowFrames});
  CHECK(whole.size() == 1);  // one read spanning many windows returns only the newest

  CHECK_THAT(irregular.back().level, WithinAbs(even.back().level, 1e-9));
  CHECK_THAT(whole.back().level, WithinAbs(even.back().level, 1e-9));
  for (std::size_t band = 0; band < 3; ++band) {
    CHECK_THAT(irregular.back().bands[band], WithinAbs(even.back().bands[band], 1e-9));
    CHECK_THAT(whole.back().bands[band], WithinAbs(even.back().bands[band], 1e-9));
  }
}

TEST_CASE("silence, reset, reconfiguration, and a partial frame all measure exact zero") {
  PlaybackMonitor monitor;
  REQUIRE(monitor.configure(pickleMap()));
  feed(monitor, 4, kSettleWindows * kWindowFrames, bassTone, 256);

  // the filter tail rings out and the mapping clamps to exactly zero
  const auto decayed = feed(monitor, 4, 50 * kWindowFrames, silence, 256);
  REQUIRE(!decayed.empty());
  CHECK(zero(decayed.back()));

  // a loud partial window that must not survive any of the three clears
  const auto loudPartial = [&](std::size_t frames) {
    feed(monitor, 4, frames, [](std::size_t frame, std::size_t channel) {
      return channel == 3 ? 0.9 * tone(frame, 100.0) : 0.0;
    }, 256);
  };
  const auto silentWindow = [&] {
    const auto windows = feed(monitor, 4, kWindowFrames, silence, 256);
    REQUIRE(windows.size() == 1);
    CHECK(zero(windows[0]));
  };

  SECTION("reset drops the partial window") {
    loudPartial(kWindowFrames - 60);
    monitor.reset();
    silentWindow();
  }
  SECTION("reconfiguring drops the partial window") {
    loudPartial(kWindowFrames - 60);
    REQUIRE(monitor.configure(pickleMap()));
    silentWindow();
  }
  SECTION("a span that is not whole frames is a gap that drops the partial window") {
    loudPartial(kWindowFrames - 60);
    const std::vector<std::int16_t> partialFrame{1, 2, 3};
    const auto gap = monitor.process(partialFrame);
    REQUIRE(gap);
    CHECK(zero(*gap));
    silentWindow();
  }
  CHECK_FALSE(monitor.process({}));
}
