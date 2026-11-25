#include "support/engine-harness.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <numbers>

namespace cadence::engine_harness {

void ProcessStall::hold() {
  held_.set_value();
  released_.wait();
}

bool ProcessStall::held() { return heldSignal_.wait_for(std::chrono::seconds(5)) == std::future_status::ready; }

void ProcessStall::release() {
  std::call_once(releaseOnce_, [this] { release_.set_value(); });
}

AudioEngineCallbacks Recorder::callbacks() {
  return {
      .onWake =
          [this] {
            const std::lock_guard lock(mutex_);
            ++wakes_;
          },
      .onPcm16 =
          [this](std::span<const std::int16_t> frame) {
            {
              const std::lock_guard lock(mutex_);
              frames_.emplace_back(frame.begin(), frame.end());
            }
            if (afterFrame)
              afterFrame();
          },
      .onCommand =
          [this](std::string phrase) {
            const std::lock_guard lock(mutex_);
            commands_.push_back(std::move(phrase));
          },
      .onPlaybackLevels =
          [this](MusicLevels levels) {
            const std::lock_guard lock(mutex_);
            playbackLevels_.push_back(levels);
          },
  };
}

int Recorder::wakes() const {
  const std::lock_guard lock(mutex_);
  return wakes_;
}

std::vector<std::string> Recorder::commands() const {
  const std::lock_guard lock(mutex_);
  return commands_;
}

std::vector<std::vector<std::int16_t>> Recorder::frames() const {
  const std::lock_guard lock(mutex_);
  return frames_;
}

std::vector<std::int16_t> Recorder::samples() const {
  std::vector<std::int16_t> all;
  for (const auto& frame : frames())
    all.insert(all.end(), frame.begin(), frame.end());
  return all;
}

std::vector<MusicLevels> Recorder::playbackLevels() const {
  const std::lock_guard lock(mutex_);
  return playbackLevels_;
}

EngineFixture::EngineFixture() {
  useOutputFrames(stock.outputFrames);
  stock.onProcess = [this] {
    const int chunk = stock.processCalls;
    auto& stt = stock.outputs[fake_stock::kOmniWithGain];
    for (std::size_t index = 0; index < stt.size(); ++index)
      stt[index] = static_cast<float>(sttSample(static_cast<std::size_t>(chunk), index)) / 32768.0f;
    if (wakeChunks.count(chunk) != 0) {
      stock.hits.push_back({fake_stock::FakeComponent::Alice, {}});
      stock.events.push_back(stock_abi::kSpotterFirstConfirmation);
    }
    if (const auto command = commandChunks.find(chunk); command != commandChunks.end()) {
      stock.hits.push_back({fake_stock::FakeComponent::Command, command->second});
      stock.events.push_back(stock_abi::kSpotterFirstConfirmation);
    }
    if (failingChunks.count(chunk) != 0)
      stock.failures["process"] = "fake vqe fault";
    else
      stock.failures.erase("process");
    if (duringProcess)
      duringProcess();
  };
}

void EngineFixture::useOutputFrames(int frames) {
  stock.outputFrames = frames;
  stock.chunkFrames = frames * 3;
  for (const int type : {fake_stock::kOmni, fake_stock::kOmniWithGain, fake_stock::kRawMainMic, fake_stock::kBnr})
    setOutputs(type, 0.0f);
}

AudioEngineConfig EngineFixture::config() const {
  AudioEngineConfig config;
  config.stock = fake_stock::testConfig();
  // engine tests wake from the first chunk; the warm-up has its own gate, stock, and engine tests
  config.stock.wakeWarmupMs = 0;
  return config;
}

AudioEngineConfig EngineFixture::configWithCommands() const {
  auto settings = config();
  fake_stock::enableCommands(settings.stock);
  return settings;
}

std::int16_t EngineFixture::sttSample(std::size_t chunk, std::size_t index) const {
  return static_cast<std::int16_t>(((chunk - 1) * static_cast<std::size_t>(stock.outputFrames) + index) % 32768);
}

std::vector<std::int16_t> EngineFixture::sttSamples(std::size_t first, std::size_t last) const {
  std::vector<std::int16_t> samples;
  for (std::size_t chunk = first; chunk <= last; ++chunk)
    for (std::size_t index = 0; index < static_cast<std::size_t>(stock.outputFrames); ++index)
      samples.push_back(sttSample(chunk, index));
  return samples;
}

AudioEngineStatus EngineFixture::feed(const AudioEngine& engine, std::size_t chunks, std::int16_t value) {
  const std::size_t batch = std::max<std::size_t>(1, 24000 / chunkFrames());
  auto status = engine.status();
  for (std::size_t left = chunks; left > 0;) {
    const auto queued = std::min(left, batch);
    source.pushFrames(
        fake_pcm::captureFrames(queued * chunkFrames(), fake_stock::testConfig().capture.channels,
                                [value](std::size_t, std::size_t) { return value; }));
    status = handled(engine, status.chunks + queued);
    left -= queued;
  }
  return status;
}

AudioEngineStatus EngineFixture::feedReferenceTone(const AudioEngine& engine, std::size_t chunks) {
  const auto& capture = fake_stock::testConfig().capture;
  const std::size_t batch = std::max<std::size_t>(1, 24000 / chunkFrames());
  std::size_t frame = 0;
  auto status = engine.status();
  for (std::size_t left = chunks; left > 0;) {
    const auto queued = std::min(left, batch);
    const std::size_t start = frame;
    source.pushFrames(fake_pcm::captureFrames(queued * chunkFrames(), capture.channels,
                                              [&capture, start](std::size_t index, std::size_t channel) {
                                                const bool reference = std::find(capture.speakers.begin(),
                                                                                 capture.speakers.end(),
                                                                                 channel) != capture.speakers.end();
                                                if (!reference)
                                                  return std::int16_t{0};
                                                const double sample =
                                                    0.25 * std::sin(2 * std::numbers::pi * 100.0 *
                                                                    static_cast<double>(start + index) / 48000.0);
                                                return static_cast<std::int16_t>(std::lround(sample * 32768.0));
                                              }));
    status = handled(engine, status.chunks + queued);
    frame += queued * chunkFrames();
    left -= queued;
  }
  return status;
}

AudioEngineStatus EngineFixture::handled(const AudioEngine& engine, std::uint64_t chunks) const {
  REQUIRE(eventually([&] { return engine.status().chunks >= chunks; }));
  const auto status = engine.status();
  REQUIRE(status.chunks == chunks);
  return status;
}

}  // namespace cadence::engine_harness
