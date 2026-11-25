#include "playback/mixer.h"
#include "support/samples.h"

#include <catch2/catch_test_macros.hpp>

#include <array>
#include <cstddef>
#include <cstdint>
#include <limits>
#include <memory>
#include <span>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;

namespace {

constexpr std::size_t kTarget = 4800;

JitterBufferConfig roomFor(std::size_t samples) {
  JitterBufferConfig config;
  config.maxSamples = samples + kTarget;
  return config;
}

// one response of these samples, ended so it plays below the start target; the gain is set after it is queued
std::vector<std::int16_t> readOnce(float gain, const std::vector<std::int16_t>& input) {
  Mixer mixer(roomFor(input.size()));
  REQUIRE(mixer.source(MixerSource::Voice).begin("r1"));
  REQUIRE(mixer.source(MixerSource::Voice).push("r1", input) == PushResult::Accepted);
  REQUIRE(mixer.source(MixerSource::Voice).end("r1"));
  mixer.setGain(MixerSource::Voice, gain);
  std::vector<std::int16_t> out(input.size());
  CHECK(mixer.read(out).samples == input.size());
  return out;
}

// one read of the mixer and of a lone buffer with the same audio agree
MixerRead expectSameRead(Mixer& mixer, JitterBuffer& alone, std::vector<std::int16_t>& mixed) {
  std::vector<std::int16_t> direct(mixed.size());
  const auto got = mixer.read(mixed);
  const auto expected = alone.read(direct);
  CHECK(got.samples == expected.samples);
  CHECK(got.underrun == expected.underrun);
  CHECK(got.drained == expected.drained);
  CHECK(mixed == direct);
  return got;
}

}  // namespace

TEST_CASE("underrun and drained are the voice source's") {
  const auto config = roomFor(kTarget);
  Mixer mixer(config);
  JitterBuffer alone(config);
  auto& voice = mixer.source(MixerSource::Voice);
  REQUIRE(voice.begin("r1"));
  REQUIRE(alone.begin("r1"));
  REQUIRE(voice.push("r1", ramp(kTarget)) == PushResult::Accepted);
  REQUIRE(alone.push("r1", ramp(kTarget)) == PushResult::Accepted);

  std::vector<std::int16_t> mixed(960);
  for (int round = 0; round < 7; ++round)
    expectSameRead(mixer, alone, mixed);
  CHECK(voice.status().underruns == 1);
  CHECK(voice.status().underruns == alone.status().underruns);

  REQUIRE(voice.end("r1"));
  REQUIRE(alone.end("r1"));
  CHECK(expectSameRead(mixer, alone, mixed).drained);
  CHECK(voice.status().drained == 1);
}

TEST_CASE("gain scales like the player did, and a gain above 1 acts as 1, a negative or nan gain as 0") {
  const std::vector<std::int16_t> input{3, -3, 32767, -32768, 0};
  const auto zeros = constant(input.size(), 0);

  CHECK(readOnce(0.5f, input) == std::vector<std::int16_t>{2, -2, 16384, -16384, 0});
  CHECK(readOnce(0.25f, constant(8, 1000)) == constant(8, 250));
  CHECK(readOnce(2.0f, input) == input);
  CHECK(readOnce(-1.0f, input) == zeros);
  CHECK(readOnce(std::numeric_limits<float>::quiet_NaN(), input) == zeros);
  CHECK(readOnce(std::numeric_limits<float>::infinity(), input) == input);
}

TEST_CASE("generation changes when the voice source cancels") {
  Mixer mixer;
  auto& voice = mixer.source(MixerSource::Voice);
  REQUIRE(voice.begin("r1"));
  const auto before = mixer.generation();
  std::vector<std::int16_t> out(480);
  CHECK(mixer.read(out).generation == before);

  voice.cancel();

  CHECK(mixer.generation() != before);
  CHECK(mixer.read(out).generation == mixer.generation());
}

TEST_CASE("tones mix over the voice, and the sum is clamped to the int16 range") {
  Mixer mixer(roomFor(4));
  auto& voice = mixer.source(MixerSource::Voice);
  auto& tone = mixer.source(MixerSource::Tone);
  REQUIRE(voice.begin("r1"));
  REQUIRE(voice.push("r1", std::vector<std::int16_t>{1000, 20000, -20000, 30000}) == PushResult::Accepted);
  REQUIRE(voice.end("r1"));
  REQUIRE(tone.begin("t1"));
  REQUIRE(tone.push("t1", std::vector<std::int16_t>{500, 20000, -20000, 30000}) == PushResult::Accepted);
  REQUIRE(tone.end("t1"));

  std::vector<std::int16_t> out(4);
  const auto read = mixer.read(out);
  CHECK(read.samples == 4);
  CHECK(read.sources == std::array<std::size_t, kMixerSources>{4, 4});
  CHECK(out == std::vector<std::int16_t>{1500, 32767, -32768, 32767});

  REQUIRE(tone.begin("t2"));
  REQUIRE(tone.push("t2", constant(4, 1000)) == PushResult::Accepted);
  REQUIRE(tone.end("t2"));
  const auto toneOnly = mixer.read(out);
  CHECK(toneOnly.samples == 4);
  CHECK(toneOnly.sources == std::array<std::size_t, kMixerSources>{0, 4});
  CHECK(out == constant(4, 1000));
}

TEST_CASE("a looped period repeats without a gap across reads of any size, as tone samples") {
  Mixer mixer;
  const auto period = ramp(7);
  mixer.loop(period);

  std::vector<std::int16_t> heard;
  for (const std::size_t size : {std::size_t{3}, std::size_t{5}, std::size_t{11}, std::size_t{2}}) {
    std::vector<std::int16_t> out(size);
    const auto read = mixer.read(out);
    CHECK(read.samples == size);
    // it ducks the music for as long as it loops, silent stretches included
    CHECK(read.sources == std::array<std::size_t, kMixerSources>{0, size});
    heard.insert(heard.end(), out.begin(), out.end());
  }

  std::vector<std::int16_t> expected;
  for (std::size_t i = 0; i < heard.size(); ++i)
    expected.push_back(period[i % period.size()]);
  CHECK(heard == expected);
}

TEST_CASE("a loop mixes over the voice and the earcons, at the tone source's gain") {
  Mixer mixer(roomFor(4));
  auto& voice = mixer.source(MixerSource::Voice);
  auto& tone = mixer.source(MixerSource::Tone);
  REQUIRE(voice.begin("r1"));
  REQUIRE(voice.push("r1", constant(4, 1000)) == PushResult::Accepted);
  REQUIRE(voice.end("r1"));
  REQUIRE(tone.begin("t1"));
  REQUIRE(tone.push("t1", constant(2, 100)) == PushResult::Accepted);
  REQUIRE(tone.end("t1"));
  mixer.loop(std::vector<std::int16_t>{10, 20});
  mixer.setGain(MixerSource::Tone, 0.5f);

  std::vector<std::int16_t> out(4);
  const auto read = mixer.read(out);
  CHECK(read.sources == std::array<std::size_t, kMixerSources>{4, 4});
  CHECK(out == std::vector<std::int16_t>{1055, 1060, 1005, 1010});
}

TEST_CASE("stopping a loop silences it at once and changes the generation, so the player takes it back") {
  Mixer mixer;
  CHECK_FALSE(mixer.stopLoop());
  mixer.loop({});
  CHECK_FALSE(mixer.stopLoop());

  mixer.loop(constant(4, 500));
  const auto before = mixer.generation();
  std::vector<std::int16_t> out(4);
  CHECK(mixer.read(out).generation == before);
  CHECK(out == constant(4, 500));

  CHECK(mixer.stopLoop());
  CHECK(mixer.generation() != before);
  const auto read = mixer.read(out);
  CHECK(read.generation == mixer.generation());
  CHECK(read.samples == 0);
  CHECK(silent(out));
  CHECK_FALSE(mixer.stopLoop());
}

namespace {

// counts up from 1 on every fill
class CountingFeed final : public cadence::LoopFeed {
 public:
  void fill(std::span<std::int16_t> out) override {
    for (auto& sample : out)
      sample = ++next_;
  }

 private:
  std::int16_t next_ = 0;
};

}  // namespace

TEST_CASE("a feed loops in place of a period, with the tone's gain, until the loop stops") {
  cadence::Mixer mixer;
  mixer.setGain(cadence::MixerSource::Tone, 0.5f);
  mixer.loopFeed(std::make_shared<CountingFeed>());
  std::vector<std::int16_t> out(4);
  const auto read = mixer.read(out);
  CHECK(out == std::vector<std::int16_t>{1, 1, 2, 2});
  CHECK(read.sources[static_cast<std::size_t>(cadence::MixerSource::Tone)] == 4);
  const auto before = mixer.generation();
  CHECK(mixer.stopLoop());
  CHECK(mixer.generation() != before);
  CHECK_FALSE(mixer.stopLoop());
  mixer.read(out);
  CHECK(out == std::vector<std::int16_t>(4, 0));
}
