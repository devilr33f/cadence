#pragma once

#include "backend/pcm-input.h"
#include "backend/pcm-reader-slot.h"
#include "backend/pcm-snapshots.h"
#include "backend/presentation-clock.h"
#include "dsp/dither.h"
#include "dsp/duck.h"
#include "dsp/eq.h"
#include "dsp/gain-ramp.h"
#include "dsp/limiter.h"
#include "dsp/viz.h"

#include <algorithm>
#include <atomic>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <vector>

namespace cadence {

enum class VizSource {
  // the music after the duck
  Music,
  // everything, after the limiter
  All,
};

// the inputs a backend mixes, any of which may be absent; they outlive it. external is the slot of the reader a
// screen's player writes (mpv's ao_cadence ring), mixed with the music as part of it: ducked, gated and tapped alike
struct PcmBackendInputs {
  PcmInput* music = nullptr;
  PcmInput* overlay = nullptr;
  PcmReaderSlot* external = nullptr;
};

// the playback thread's float chain (S0 §8.2), one period at a time, float stereo 48 kHz throughout:
// music (+ external) -> pcm tap -> duck or muffle -> server duck -> + overlay input + the mixer's overlays (mono s16, on both channels) -> eq (board profile,
// then user) -> limiter -> viz tap -> software volume -> s16, stereo or a mono downmix.
//
// the music and the overlay input are mixed only while the gate is open (rule 7); while it is shut they are still
// read, at the device's pace, and thrown away, and shutting it clears them. any duck trigger ducks the music: the
// mixer's overlays (the caller says when they hold or sound), DuckSet, or audio on the overlay input. the server's duck
// (a turn in the room, SessionControl's Duck) ramps the music to its level on top, never above 1 (rule 7), and the
// mix's mute ramps everything after the limiter to silence. the limiter
// delays everything by its look-ahead. a block already on the s16 grid (unprocessed s16 audio, digital silence
// included) is written exactly; anything else gets tpdf dither, bounded by the limiter's ceiling.
//
// the settings are safe from any thread and reach the playback thread whole at the next period; prepare belongs to
// the controlling thread before the playback thread runs, process and discard to the playback thread
class PcmBackend {
 public:
  // 1 or 2 output channels
  PcmBackend(std::size_t channels, PcmBackendInputs inputs);

  PcmBackend(const PcmBackend&) = delete;
  PcmBackend& operator=(const PcmBackend&) = delete;

  // false while shut, the default; returns the previous state
  bool setGateOpen(bool open);
  // the software volume after the limiter; clamped to 0..1, nan is 0
  void setVolume(float gain);
  // cadenced's DuckSet
  void setDucked(bool active);
  void setDuckMode(dsp::DuckMode mode);
  void setEq(const dsp::EqSettings& profile, const dsp::EqSettings& user);
  void setViz(bool enabled, VizSource source);
  // the server's duck of the music: level 0..1 of it while active, ramped over rampMs; inactive ramps back to 1
  void setServerDuck(bool active, float level, std::uint32_t rampMs);
  // the whole mix's mute, after the limiter
  void setMuted(bool muted);
  // the board's audio delay trim (audio.output delayMs, a tv's hdmi path): added to when each period is heard, as the
  // external reader, the snapshots and the clock are told, so a player's own a/v sync shifts its video by it.
  // -1..1 s, clamped; negative where the device reports more latency than the path has
  static constexpr std::int32_t kMaxDelayTrimMs = 1000;
  void setDelayTrim(std::int32_t ms) {
    delayTrimMs_.store(std::clamp(ms, -kMaxDelayTrimMs, kMaxDelayTrimMs), std::memory_order_relaxed);
  }
  [[nodiscard]] std::int32_t delayTrim() const { return delayTrimMs_.load(std::memory_order_relaxed); }
  // the pcm tap: the music (and the external player's ring) as it was read, before the duck, the eq, the limiter and
  // the software volume, so the visualizer shows the music at its own size; stamped with when its first frame is
  // heard (the output's latency, the limiter's and the delay trim included). pushed every period while on
  void setSnapshots(bool enabled) { snapshotsOn_.store(enabled, std::memory_order_relaxed); }
  [[nodiscard]] const PcmSnapshots& snapshots() const { return snapshots_; }
  [[nodiscard]] PresentationClock& clock() { return clock_; }

  // sizes the period buffers, so process never allocates
  void prepare(std::size_t periodFrames);

  // one period: overlays is the mixer's read and out takes as many frames (times the channels). a viz frame once
  // one is due, while the viz is on and its tap carried sound
  // heardNs is when the period's first frame sounds (CLOCK_MONOTONIC), 0 when the device's delay is not known
  std::optional<dsp::VizFrame> process(std::span<const std::int16_t> overlays, bool overlaysDuck,
                                       std::span<std::int16_t> out, std::int64_t heardNs = 0);
  // forgets the audio the limiter delays and the eq's filters hold, so nothing of a discarded period sounds later
  void discard();

  [[nodiscard]] std::size_t channels() const { return channels_; }
  // frames by which the output trails what process was given
  [[nodiscard]] static constexpr std::size_t latencyFrames() { return dsp::Limiter::latencyFrames(); }

 private:
  // a period of one input into block, silence after it or while the gate is shut; whether it had audio
  bool readInput(PcmInput* input, std::span<float> block, bool open);
  void tap(std::span<const float> stereo);
  void quantize(std::span<const float> block, std::span<std::int16_t> out);

  const std::size_t channels_;
  const PcmBackendInputs inputs_;

  std::atomic<bool> gateOpen_{false};
  std::atomic<float> volume_{1.0f};
  std::atomic<bool> ducked_{false};
  std::atomic<dsp::DuckMode> duckMode_{dsp::DuckMode::Duck};
  std::atomic<bool> vizEnabled_{false};
  std::atomic<VizSource> vizSource_{VizSource::Music};
  std::atomic<bool> serverDucked_{false};
  std::atomic<float> serverDuckLevel_{1.0f};
  std::atomic<std::uint32_t> serverDuckRampMs_{0};
  std::atomic<std::uint32_t> serverDuckSerial_{0};
  std::atomic<bool> muted_{false};
  std::atomic<std::int32_t> delayTrimMs_{0};
  std::atomic<bool> snapshotsOn_{false};
  PcmSnapshots snapshots_;
  PresentationClock clock_;

  // the playback thread's
  dsp::DuckEnvelope duck_{dsp::DuckMode::Duck};
  dsp::Eq eq_{dsp::kStereo};
  dsp::Limiter limiter_;
  dsp::VizBands viz_;
  dsp::GainRamp serverDuck_;
  std::uint32_t seenServerDuck_ = 0;
  dsp::GainRamp mute_;
  bool seenMuted_ = false;
  std::uint64_t processed_ = 0;
  dsp::Rng rng_{0x5eed};
  std::vector<float> music_;
  std::vector<float> external_;
  std::vector<float> mix_;
  std::vector<float> vizMono_;
};

}  // namespace cadence
