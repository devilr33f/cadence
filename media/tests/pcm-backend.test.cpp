#include "backend/pcm-backend.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <algorithm>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <cstdlib>
#include <span>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;
using Catch::Matchers::WithinAbs;

namespace {

constexpr std::size_t kPeriod = 960;
constexpr std::size_t kLatency = PcmBackend::latencyFrames();
constexpr double kFullScale = 32768;
// the limiter's ceiling as an s16 value
constexpr int kCeiling = static_cast<int>(dsp::kLimiterCeiling * kFullScale);

// a backend over its own two inputs, run period by period the way the playback thread does, keeping all it wrote.
// music handed to play() is queued as it is read, so the ring never runs dry or overflows
struct Rig {
  explicit Rig(std::size_t channels = 2) : backend(channels, {&music, &overlay}) {
    backend.prepare(kPeriod);
    backend.setGateOpen(true);
  }

  void play(std::vector<float> stereo) {
    track = std::move(stereo);
    fed = 0;
  }

  // overlays: the mixer's mono samples for every period, with overlaysDuck as the caller would report them
  void run(std::size_t periods, bool overlaysDuck = false, std::int16_t overlays = 0) {
    std::vector<std::int16_t> mono(kPeriod, overlays);
    std::vector<std::int16_t> out(kPeriod * backend.channels());
    for (std::size_t period = 0; period < periods; ++period) {
      const auto want = std::min(track.size(), fed + 4 * kPeriod * 2);
      if (want > fed) {
        REQUIRE(music.push(std::span<const float>(track).subspan(fed, want - fed), 48000, 2));
        fed = want;
      }
      if (const auto frame = backend.process(mono, overlaysDuck, out))
        viz.push_back(*frame);
      output.insert(output.end(), out.begin(), out.end());
    }
  }

  [[nodiscard]] std::size_t frames() const { return output.size() / backend.channels(); }

  PcmInput music;
  PcmInput overlay;
  PcmBackend backend;
  std::vector<float> track;
  std::size_t fed = 0;
  std::vector<std::int16_t> output;
  std::vector<dsp::VizFrame> viz;
};

// the amplitude, as a fraction of full scale, of one frequency in the output over the frames that came from input
// frames [first, first + frames)
double level(const Rig& rig, double hz, std::size_t first, std::size_t frames, std::size_t channel = 0) {
  return amplitudeAt(rig.output, rig.backend.channels(), channel, first + kLatency, frames, hz) / kFullScale;
}

int peak(const std::vector<std::int16_t>& samples) {
  int highest = 0;
  for (const auto sample : samples)
    highest = std::max(highest, std::abs(static_cast<int>(sample)));
  return highest;
}

std::vector<std::int16_t> tail(const Rig& rig, std::size_t fromFrame) {
  return {rig.output.begin() + static_cast<std::ptrdiff_t>(fromFrame * rig.backend.channels()), rig.output.end()};
}

bool silent(const std::vector<std::int16_t>& samples) {
  return std::all_of(samples.begin(), samples.end(), [](std::int16_t sample) { return sample == 0; });
}

std::vector<std::int16_t> channelOf(const std::vector<std::int16_t>& stereo, std::size_t index) {
  std::vector<std::int16_t> samples;
  for (std::size_t i = index; i < stereo.size(); i += 2)
    samples.push_back(stereo[i]);
  return samples;
}

dsp::EqSettings peaking(double hz, float gainDb) {
  return {.preampDb = 0, .bands = {{.type = dsp::FilterType::Peaking, .freqHz = static_cast<float>(hz), .gainDb = gainDb, .q = 1}}};
}

// the sum of two sines on both channels
std::vector<float> twoTones(double low, double high, double amplitude, std::size_t frames) {
  auto signal = sine(low, amplitude, frames, 2);
  const auto other = sine(high, amplitude, frames, 2);
  for (std::size_t i = 0; i < signal.size(); ++i)
    signal[i] += other[i];
  return signal;
}

}  // namespace

TEST_CASE("DigitalSilenceUndithered") {
  Rig rig;
  rig.backend.setVolume(0.3f);

  SECTION("nothing to play writes exact zeros") {
    rig.run(25);
    CHECK(silent(rig.output));
  }

  SECTION("after music the delayed tail runs out into exact zeros again") {
    rig.play(sine(1000, 0.25, kPeriod * 3, 2));
    rig.run(4);
    CHECK_FALSE(silent(rig.output));
    rig.run(10);
    CHECK(silent(tail(rig, kPeriod * 5)));
  }
}

TEST_CASE("the mixer's s16 overlays pass exactly on both channels, after the look-ahead") {
  Rig rig;
  rig.run(3, true, -1234);

  const auto left = channelOf(rig.output, 0);
  CHECK(left == channelOf(rig.output, 1));
  CHECK(std::all_of(left.begin(), left.begin() + kLatency, [](std::int16_t sample) { return sample == 0; }));
  CHECK(std::all_of(left.begin() + kLatency, left.end(), [](std::int16_t sample) { return sample == -1234; }));
}

TEST_CASE("EqApplied") {
  // the eq crossfades in over 20 ms; measured from 200 ms on
  constexpr std::size_t kFrom = 9600;
  constexpr std::size_t kWindow = 4800;
  Rig flat;
  flat.play(sine(1000, 0.25, 24000, 2));
  flat.run(25);
  const double reference = level(flat, 1000, kFrom, kWindow);
  REQUIRE_THAT(db(reference / 0.25), WithinAbs(0, 0.05));

  Rig shaped;
  shaped.play(sine(1000, 0.25, 24000, 2));

  SECTION("a user band moves the music at its frequency by its gain") {
    shaped.backend.setEq({}, peaking(1000, 6));
    shaped.run(25);
    CHECK_THAT(db(level(shaped, 1000, kFrom, kWindow) / reference), WithinAbs(6, 0.5));
    CHECK_THAT(db(level(shaped, 1000, kFrom, kWindow, 1) / reference), WithinAbs(6, 0.5));
  }

  SECTION("the board profile and the user eq add up") {
    shaped.backend.setEq(peaking(1000, -3), peaking(1000, 6));
    shaped.run(25);
    CHECK_THAT(db(level(shaped, 1000, kFrom, kWindow) / reference), WithinAbs(3, 0.5));
  }
}

TEST_CASE("LimiterPreventsClipping") {
  Rig rig;
  rig.backend.setEq({}, peaking(1000, 12));
  // -3 dBFS
  rig.play(sine(1000, 0.708, 24000, 2));
  rig.run(25);

  CHECK(peak(rig.output) <= kCeiling);
  // limited, not muted
  CHECK(peak(rig.output) >= kCeiling - 400);
}

TEST_CASE("SoftwareVolumeAfterLimiter") {
  Rig unity;
  unity.play(sine(1000, 0.25, 24000, 2));
  unity.run(25);
  Rig quiet;
  quiet.backend.setVolume(0.3f);

  SECTION("level 30 scales the output by 0.30") {
    quiet.play(sine(1000, 0.25, 24000, 2));
    quiet.run(25);
    CHECK_THAT(level(quiet, 1000, 4800, 9600) / level(unity, 1000, 4800, 9600), WithinAbs(0.30, 0.003));
  }

  SECTION("a limited signal is scaled after the limiter took it to the ceiling") {
    quiet.backend.setEq({}, peaking(1000, 12));
    quiet.play(sine(1000, 0.708, 24000, 2));
    quiet.run(25);
    const auto highest = peak(tail(quiet, 9600));
    CHECK(highest <= static_cast<int>(kCeiling * 0.3) + 2);
    CHECK(highest >= static_cast<int>(kCeiling * 0.3 * 0.97));
  }
}

TEST_CASE("every duck trigger ducks the music by v1's envelope") {
  // the trigger holds from start for 600 ms; the music is 1 kHz at 0.25
  constexpr std::size_t kStart = 10 * kPeriod;
  constexpr std::size_t kHeld = 30;
  constexpr std::size_t kRelease = kStart + kHeld * kPeriod;
  Rig rig;
  rig.play(sine(1000, 0.25, 130 * kPeriod, 2));
  rig.run(10);

  SECTION("cadenced's DuckSet") {
    rig.backend.setDucked(true);
    rig.run(kHeld);
    rig.backend.setDucked(false);
  }
  SECTION("the mixer's overlays holding or sounding") { rig.run(kHeld, true); }
  SECTION("audio on the overlay input") {
    const auto voice = sine(300, 0.2, kHeld * kPeriod, 2);
    REQUIRE(rig.overlay.push(std::span<const float>(voice), 48000, 2));
    rig.run(kHeld);
  }
  rig.run(65);

  // windows of a period, so that a 300 Hz overlay cancels out; the expected gains are the envelope's means
  const auto ducked = [&](std::size_t first) { return db(level(rig, 1000, first, kPeriod) / 0.25); };
  CHECK_THAT(ducked(kStart - kPeriod), WithinAbs(0, 0.05));
  // the ramp down: 200 ms from 1 to 0.15
  CHECK_THAT(ducked(kStart), WithinAbs(db(1 - 0.85 * 479.5 / 9600), 1));
  CHECK_THAT(ducked(kStart + 4320), WithinAbs(db(0.575), 1));
  CHECK_THAT(ducked(kStart + 9600), WithinAbs(db(0.15), 1));
  // the 0.5 s hold after the trigger let go, then 500 ms back up
  CHECK_THAT(ducked(kRelease + 12000), WithinAbs(db(0.15), 1));
  CHECK_THAT(ducked(kRelease + 24000 + 11520), WithinAbs(db(0.575), 1));
  CHECK_THAT(ducked(kRelease + 48000), WithinAbs(0, 1));
}

TEST_CASE("MuffleLowPasses") {
  // 300 Hz and 5 kHz at 0.2 each, measured over 100 ms once the duck is down
  constexpr std::size_t kFrom = 14400;
  constexpr std::size_t kWindow = 4800;
  Rig rig;
  rig.play(twoTones(300, 5000, 0.2, 30 * kPeriod));
  const auto low = [&] { return level(rig, 300, kFrom, kWindow); };
  const auto high = [&] { return level(rig, 5000, kFrom, kWindow); };

  SECTION("duck lowers both alike") {
    rig.backend.setDucked(true);
    rig.run(25);
    CHECK_THAT(db(low() / 0.2), WithinAbs(db(0.15), 0.5));
    CHECK_THAT(db(high() / low()), WithinAbs(0, 0.5));
  }

  SECTION("muffle also takes the highs away") {
    rig.backend.setDuckMode(dsp::DuckMode::Muffle);
    rig.backend.setDucked(true);
    rig.run(25);
    CHECK_THAT(db(low() / 0.2), WithinAbs(db(0.15), 1));
    CHECK(db(high() / low()) < -20);
  }

  SECTION("muffle leaves music that is not ducked alone") {
    rig.backend.setDuckMode(dsp::DuckMode::Muffle);
    rig.run(25);
    CHECK_THAT(db(low() / 0.2), WithinAbs(0, 0.1));
    CHECK_THAT(db(high() / 0.2), WithinAbs(0, 0.1));
  }
}

TEST_CASE("MonoOutputDownmixes") {
  Rig rig(1);

  SECTION("the music's two channels are averaged") {
    auto left = sine(1000, 0.5, 9600, 2);
    for (std::size_t i = 1; i < left.size(); i += 2)
      left[i] = 0;
    rig.play(left);
    rig.run(10);
    CHECK(rig.frames() == 10 * kPeriod);
    CHECK_THAT(db(level(rig, 1000, 2400, 4800) / 0.25), WithinAbs(0, 0.05));
  }

  SECTION("the mixer's overlays keep their s16 values") {
    rig.run(3, true, 1000);
    CHECK(std::all_of(rig.output.begin() + kLatency, rig.output.end(), [](std::int16_t sample) { return sample == 1000; }));
  }
}

TEST_CASE("the inputs mix only while the gate is open, and shutting it clears them") {
  const int which = GENERATE(0, 1);
  Rig rig;
  rig.backend.setGateOpen(false);
  const auto loud = sine(1000, 0.5, 4800, 2);
  const auto quiet = sine(1000, 0.25, 48000, 2);
  auto& input = which == 0 ? rig.music : rig.overlay;

  // pushed while shut: read away unheard
  REQUIRE(input.push(std::span<const float>(loud), 48000, 2));
  rig.run(10);
  CHECK(silent(rig.output));

  CHECK_FALSE(rig.backend.setGateOpen(true));
  REQUIRE(input.push(std::span<const float>(quiet), 48000, 2));
  rig.run(5);
  const auto heard = peak(rig.output);
  CHECK(heard > 7000);
  CHECK(heard <= 8192 + 2);

  // shut with most of it still queued: none of it is heard, now or once the gate opens again. the period after the
  // shut still carries what the look-ahead delayed, so its rest is only dither
  CHECK(rig.backend.setGateOpen(false));
  const auto shut = rig.frames();
  rig.run(5);
  rig.backend.setGateOpen(true);
  rig.run(5);
  const auto rest = tail(rig, shut + kLatency);
  CHECK(peak(std::vector(rest.begin(), rest.begin() + (kPeriod - kLatency) * 2)) <= 1);
  CHECK(silent(tail(rig, shut + kPeriod)));
}

TEST_CASE("the viz taps the ducked music or the limiter output, only while on and sounding") {
  Rig rig;

  SECTION("off: no frames") {
    rig.play(sine(1000, 0.25, 24000, 2));
    rig.run(25);
    CHECK(rig.viz.empty());
  }

  SECTION("music: a frame every 1/30 s while it plays, peaking in the band of 1 kHz") {
    rig.backend.setViz(true, VizSource::Music);
    rig.play(sine(1000, 0.25, 24000, 2));
    rig.run(25);
    // 500 ms of music
    CHECK(rig.viz.size() == 15);
    // the band from 980 to 1386 Hz
    for (const auto& frame : rig.viz)
      CHECK(std::max_element(frame.begin(), frame.end()) - frame.begin() == 8);
    rig.run(25);
    CHECK(rig.viz.size() == 15);
  }

  SECTION("music hears no overlays, all does") {
    rig.backend.setViz(true, VizSource::Music);
    rig.run(25, true, 4000);
    CHECK(rig.viz.empty());
    rig.backend.setViz(true, VizSource::All);
    rig.run(25, true, 4000);
    CHECK(rig.viz.size() >= 14);
  }
}

TEST_CASE("discard forgets the audio the look-ahead still delays") {
  Rig rig;
  rig.run(1, true, 1000);
  rig.backend.discard();
  const auto discarded = rig.frames();
  rig.run(3);
  CHECK(silent(tail(rig, discarded)));
}

namespace {

// a reader standing in for a player's ring: a constant on both channels, counting the reads
struct ConstantReader final : PcmReader {
  explicit ConstantReader(float constant) : value(constant) {}
  std::size_t read(std::span<float> stereo, std::int64_t heard) override {
    std::fill(stereo.begin(), stereo.end(), value);
    ++reads;
    lastHeard = heard;
    return stereo.size() / 2;
  }
  float value;
  std::size_t reads = 0;
  std::int64_t lastHeard = 0;
};

// the mean of the left channel over the last period the rig wrote
double lastLevel(const std::vector<std::int16_t>& out) {
  double sum = 0;
  for (std::size_t i = out.size() - 2 * kPeriod; i < out.size(); i += 2)
    sum += out[i];
  return sum / static_cast<double>(kPeriod) / kFullScale;
}

struct ExternalRig {
  ExternalRig() : backend(2, {&music, &overlay, &slot}) {
    backend.prepare(kPeriod);
    backend.setGateOpen(true);
  }
  void run(std::size_t periods, std::int64_t heard = 0) {
    std::vector<std::int16_t> mono(kPeriod, 0);
    out.assign(kPeriod * 2, 0);
    for (std::size_t period = 0; period < periods; ++period)
      backend.process(mono, false, out, heard);
  }
  PcmInput music;
  PcmInput overlay;
  PcmReaderSlot slot;
  PcmBackend backend;
  std::vector<std::int16_t> out;
};

}  // namespace

TEST_CASE("the external slot's reader mixes as the music does, only while the gate is open") {
  ExternalRig rig;
  ConstantReader reader(0.25f);
  rig.run(2);
  CHECK(lastLevel(rig.out) == 0.0);
  rig.slot.set(&reader);
  rig.run(4, 123);
  CHECK_THAT(lastLevel(rig.out), WithinAbs(0.25, 1e-3));
  CHECK(reader.lastHeard == 123);
  // shut, it is still read at the device's pace, and never heard (rule 7)
  rig.backend.setGateOpen(false);
  const auto before = reader.reads;
  rig.run(4);
  CHECK(reader.reads == before + 4);
  CHECK(lastLevel(rig.out) == 0.0);
  rig.backend.setGateOpen(true);
  rig.slot.clear();
  rig.run(4);
  CHECK(reader.reads == before + 4);
  CHECK(lastLevel(rig.out) == 0.0);
}

TEST_CASE("the server's duck lowers the music to its level and never raises it") {
  ExternalRig rig;
  ConstantReader reader(0.25f);
  rig.slot.set(&reader);
  rig.backend.setServerDuck(true, 0.2f, 100);
  rig.run(10);
  CHECK_THAT(lastLevel(rig.out), WithinAbs(0.05, 1e-3));
  rig.backend.setServerDuck(true, 5.0f, 0);
  rig.run(4);
  CHECK_THAT(lastLevel(rig.out), WithinAbs(0.25, 1e-3));
  rig.backend.setServerDuck(true, 0.5f, 0);
  rig.run(4);
  rig.backend.setServerDuck(false, 0.5f, 50);
  rig.run(10);
  CHECK_THAT(lastLevel(rig.out), WithinAbs(0.25, 1e-3));
}

TEST_CASE("the mix's mute silences everything after the limiter, and the tap keeps the music") {
  ExternalRig rig;
  ConstantReader reader(0.25f);
  rig.slot.set(&reader);
  rig.backend.setSnapshots(true);
  rig.backend.setMuted(true);
  rig.run(4, 1'000'000'000);
  CHECK(lastLevel(rig.out) == 0.0);
  PcmSnapshot snapshot;
  REQUIRE(rig.backend.snapshots().take(snapshot));
  CHECK(snapshot.frames == PcmSnapshot::kFrames);
  CHECK(snapshot.samples[2 * 511] > 0.2f);
  rig.backend.setMuted(false);
  rig.run(4);
  CHECK_THAT(lastLevel(rig.out), WithinAbs(0.25, 1e-3));
}

TEST_CASE("the delay trim moves when the external reader is told each period is heard") {
  ExternalRig rig;
  ConstantReader reader(0.25f);
  rig.slot.set(&reader);
  rig.backend.setDelayTrim(40);
  rig.run(1, 1'000'000'000);
  CHECK(reader.lastHeard == 1'040'000'000);
  // an unknown delay stays unknown
  rig.run(1, 0);
  CHECK(reader.lastHeard == 0);
  rig.backend.setDelayTrim(5000);
  rig.run(1, 1'000'000'000);
  CHECK(reader.lastHeard == 2'000'000'000);
}

// the visualizer shows the music: taken before the duck, the eq, the limiter and the software volume, with no
// overlay in it, and stamped with when its first frame is heard, the delay trim included
TEST_CASE("the pcm tap is the music before it is levelled, stamped as it is heard") {
  ExternalRig rig;
  ConstantReader reader(0.25f);
  rig.slot.set(&reader);
  rig.backend.setSnapshots(true);
  rig.backend.setVolume(0.1f);
  rig.backend.setServerDuck(true, 0.2f, 0);
  rig.backend.setDucked(true);
  rig.backend.setDelayTrim(-40);
  std::vector<std::int16_t> overlays(kPeriod, 8000);
  std::vector<std::int16_t> out(kPeriod * 2, 0);
  const std::size_t periods = PcmSnapshot::kFrames / kPeriod + 1;
  for (std::size_t period = 0; period < periods; ++period)
    rig.backend.process(overlays, true, out, 1'000'000'000);
  PcmSnapshot snapshot;
  REQUIRE(rig.backend.snapshots().take(snapshot));
  REQUIRE(snapshot.frames == PcmSnapshot::kFrames);
  CHECK_THAT(snapshot.samples[0], WithinAbs(0.25, 1e-6));
  CHECK_THAT(snapshot.samples[2 * 511 + 1], WithinAbs(0.25, 1e-6));
  CHECK(lastLevel(out) < 0.25);
  // each period's first frame is heard at 1 s less the trim; the snapshot starts kFrames before the last one's end
  const auto periodNs = static_cast<std::int64_t>(kPeriod) * 1'000'000'000 / 48'000;
  const auto lastFrameNs = static_cast<std::int64_t>(PcmSnapshot::kFrames) * 1'000'000'000 / 48'000;
  CHECK(snapshot.monoNs == 960'000'000 + periodNs - lastFrameNs);
}

TEST_CASE("the delay trim is signed and bounded") {
  ExternalRig rig;
  ConstantReader reader(0.25f);
  rig.slot.set(&reader);
  rig.backend.setDelayTrim(-40);
  rig.run(1, 1'000'000'000);
  CHECK(reader.lastHeard == 960'000'000);
  rig.backend.setDelayTrim(-5000);
  CHECK(rig.backend.delayTrim() == -1000);
}
