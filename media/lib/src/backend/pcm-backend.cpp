#include "backend/pcm-backend.h"

#include <algorithm>
#include <cmath>
#include <ctime>

namespace cadence {

namespace {

constexpr float kS16Scale = 1.0f / 32768.0f;
constexpr float kFullScale = 32768.0f;
// the limiter's ceiling as an s16 value: dither may not carry a limited sample past it
const auto kCeiling = static_cast<std::int16_t>(std::floor(dsp::kLimiterCeiling * kFullScale));

// written so that nan also becomes silence
float clampGain(float gain) { return gain >= 0.0f ? std::min(gain, 1.0f) : 0.0f; }

std::int64_t monotonicNs() {
  timespec now{};
  clock_gettime(CLOCK_MONOTONIC, &now);
  return static_cast<std::int64_t>(now.tv_sec) * 1'000'000'000 + now.tv_nsec;
}

bool silent(std::span<const float> samples) {
  return std::all_of(samples.begin(), samples.end(), [](float sample) { return sample == 0.0f; });
}

// every sample is an s16 value: nothing touched it but the exact s16 to float conversion and unity gains
bool onS16Grid(std::span<const float> samples) {
  return std::all_of(samples.begin(), samples.end(), [](float sample) {
    const float scaled = sample * kFullScale;
    return scaled >= -kFullScale && scaled < kFullScale && std::nearbyint(scaled) == scaled;
  });
}

}  // namespace

PcmBackend::PcmBackend(std::size_t channels, PcmBackendInputs inputs) : channels_(channels), inputs_(inputs) {}

bool PcmBackend::setGateOpen(bool open) {
  const bool was = gateOpen_.exchange(open, std::memory_order_acq_rel);
  if (!open)
    for (auto* input : {inputs_.music, inputs_.overlay})
      if (input != nullptr)
        input->clear();
  return was;
}

void PcmBackend::setVolume(float gain) { volume_.store(clampGain(gain), std::memory_order_relaxed); }

void PcmBackend::setDucked(bool active) { ducked_.store(active, std::memory_order_relaxed); }

void PcmBackend::setDuckMode(dsp::DuckMode mode) { duckMode_.store(mode, std::memory_order_relaxed); }

void PcmBackend::setEq(const dsp::EqSettings& profile, const dsp::EqSettings& user) { eq_.set(profile, user); }

void PcmBackend::setViz(bool enabled, VizSource source) {
  vizSource_.store(source, std::memory_order_relaxed);
  vizEnabled_.store(enabled, std::memory_order_release);
}

void PcmBackend::setServerDuck(bool active, float level, std::uint32_t rampMs) {
  serverDuckLevel_.store(active ? clampGain(level) : 1.0f, std::memory_order_relaxed);
  serverDuckRampMs_.store(rampMs, std::memory_order_relaxed);
  serverDucked_.store(active, std::memory_order_relaxed);
  serverDuckSerial_.fetch_add(1, std::memory_order_release);
}

void PcmBackend::setMuted(bool muted) { muted_.store(muted, std::memory_order_relaxed); }

void PcmBackend::prepare(std::size_t periodFrames) {
  music_.assign(periodFrames * dsp::kStereo, 0.0f);
  external_.assign(periodFrames * dsp::kStereo, 0.0f);
  mix_.assign(periodFrames * dsp::kStereo, 0.0f);
  vizMono_.assign(periodFrames, 0.0f);
}

bool PcmBackend::readInput(PcmInput* input, std::span<float> block, bool open) {
  if (input == nullptr) {
    std::fill(block.begin(), block.end(), 0.0f);
    return false;
  }
  // read while shut too, so a source runs at the device's pace and nothing piles up behind the gate
  const bool read = input->read(block) > 0;
  if (open)
    return read;
  std::fill(block.begin(), block.end(), 0.0f);
  return false;
}

void PcmBackend::tap(std::span<const float> stereo) {
  const std::size_t frames = stereo.size() / dsp::kStereo;
  const std::span<float> mono(vizMono_.data(), frames);
  for (std::size_t i = 0; i < frames; ++i)
    mono[i] = (stereo[2 * i] + stereo[2 * i + 1]) * 0.5f;
  if (!silent(mono))
    viz_.push(mono);
}

void PcmBackend::quantize(std::span<const float> block, std::span<std::int16_t> out) {
  if (onS16Grid(block)) {
    std::transform(block.begin(), block.end(), out.begin(),
                   [](float sample) { return static_cast<std::int16_t>(sample * kFullScale); });
    return;
  }
  dsp::ditherToS16(block, out, rng_);
  for (auto& sample : out)
    sample = std::clamp<std::int16_t>(sample, static_cast<std::int16_t>(-kCeiling), kCeiling);
}

std::optional<dsp::VizFrame> PcmBackend::process(std::span<const std::int16_t> overlays, bool overlaysDuck,
                                                 std::span<std::int16_t> out, std::int64_t heardNs) {
  const std::size_t frames = overlays.size();
  const std::span<float> music(music_.data(), frames * dsp::kStereo);
  const std::span<float> mix(mix_.data(), frames * dsp::kStereo);
  const bool open = gateOpen_.load(std::memory_order_acquire);
  if (heardNs != 0)
    heardNs += static_cast<std::int64_t>(delayTrimMs_.load(std::memory_order_relaxed)) * 1'000'000;

  readInput(inputs_.music, music, open);
  if (inputs_.external != nullptr) {
    const std::span<float> external(external_.data(), music.size());
    // read while shut too, as the music is, so the player runs at the device's pace and is never heard
    if (inputs_.external->read(external, heardNs) > 0 && open)
      for (std::size_t i = 0; i < music.size(); ++i)
        music[i] += external[i];
  }
  // the visualizer's pcm: the music before anything levels it
  if (snapshotsOn_.load(std::memory_order_relaxed))
    snapshots_.push(music, heardNs);
  // the overlay input goes straight into the mix, after the duck
  const bool overlayHeard = readInput(inputs_.overlay, mix, open);

  duck_.setMode(duckMode_.load(std::memory_order_relaxed));
  duck_.setActive(overlaysDuck || overlayHeard || ducked_.load(std::memory_order_relaxed));
  duck_.apply(music);
  if (const auto serial = serverDuckSerial_.load(std::memory_order_acquire); serial != seenServerDuck_) {
    seenServerDuck_ = serial;
    serverDuck_.setTarget(serverDuckLevel_.load(std::memory_order_relaxed),
                          dsp::framesFor(serverDuckRampMs_.load(std::memory_order_relaxed)));
  }
  if (!serverDuck_.settled() || serverDuck_.gain() != 1.0f)
    serverDuck_.apply(music);

  const bool viz = vizEnabled_.load(std::memory_order_acquire);
  const auto source = vizSource_.load(std::memory_order_relaxed);
  if (viz && source == VizSource::Music)
    tap(music);

  for (std::size_t i = 0; i < frames; ++i) {
    const float overlay = static_cast<float>(overlays[i]) * kS16Scale;
    mix[2 * i] += music[2 * i] + overlay;
    mix[2 * i + 1] += music[2 * i + 1] + overlay;
  }
  eq_.process(mix);
  limiter_.process(mix);
  if (viz && source == VizSource::All)
    tap(mix);
  clock_.period(processed_, heardNs, heardNs != 0 ? heardNs - monotonicNs() : 0, frames,
                inputs_.music != nullptr ? inputs_.music->readPosition() : 0);
  processed_ += frames;
  if (const bool muted = muted_.load(std::memory_order_relaxed); muted != seenMuted_) {
    seenMuted_ = muted;
    mute_.setTarget(muted ? 0.0f : 1.0f, dsp::framesFor(5));
  }
  if (!mute_.settled() || mute_.gain() != 1.0f)
    mute_.apply(mix);

  if (const float volume = volume_.load(std::memory_order_relaxed); volume != 1.0f)
    for (auto& sample : mix)
      sample *= volume;

  if (channels_ == 1) {
    for (std::size_t i = 0; i < frames; ++i)
      mix[i] = (mix[2 * i] + mix[2 * i + 1]) * 0.5f;
    quantize(mix.first(frames), out.first(frames));
  } else {
    quantize(mix, out.first(frames * dsp::kStereo));
  }

  if (viz && viz_.ready())
    return viz_.bands();
  return std::nullopt;
}

void PcmBackend::discard() {
  limiter_.reset();
  eq_.reset();
}

}  // namespace cadence
