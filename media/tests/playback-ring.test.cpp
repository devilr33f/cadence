#include "audiod/output.h"
#include "playback/alarm-tones.h"
#include "playback/earcons.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/fake-sink.h"
#include "support/held-sink.h"
#include "support/playback-client.h"
#include "support/samples.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <cstdlib>
#include <memory>
#include <string>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

constexpr auto kAlarm = cadence_audiod_v1_ToneKind_TONE_KIND_ALARM;
constexpr auto kTimer = cadence_audiod_v1_ToneKind_TONE_KIND_TIMER;

// what the fake speaker played, as the player refills it: the left channel, from when the device opened
class Speaker {
 public:
  explicit Speaker(fake_sink::FakePcmSink& sink) : sink_(sink) {}

  // plays until frames were heard in all
  void playTo(std::size_t frames) {
    const auto deadline = std::chrono::steady_clock::now() + 10s;
    while (left_.size() < frames) {
      REQUIRE(std::chrono::steady_clock::now() < deadline);
      append(sink_.play(frames - left_.size()));
    }
  }

  // plays until a sample that is not zero was heard, and returns its index
  std::size_t playToSound() {
    const auto deadline = std::chrono::steady_clock::now() + 10s;
    for (;;) {
      const auto sounded = std::find_if(left_.begin(), left_.end(), [](std::int16_t sample) { return sample != 0; });
      if (sounded != left_.end())
        return static_cast<std::size_t>(sounded - left_.begin());
      REQUIRE(std::chrono::steady_clock::now() < deadline);
      append(sink_.play(kPlayerConfig.periodFrames));
    }
  }

  [[nodiscard]] const std::vector<std::int16_t>& left() const { return left_; }

 private:
  void append(const std::vector<std::int16_t>& played) {
    if (played.empty())
      std::this_thread::sleep_for(1ms);
    const auto heard = fake_sink::channel(played, 0);
    left_.insert(left_.end(), heard.begin(), heard.end());
  }

  fake_sink::FakePcmSink& sink_;
  std::vector<std::int16_t> left_;
};

}  // namespace

TEST_CASE("a ring sounds only once a volume held, and only an alarm or a timer loops") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, audiod.file("out"));

  sendFrame(client.get(), toneStart("alarm-1", kAlarm));
  expectToneState(client.get(), "alarm-1", false, "gated");
  CHECK(audiod.logs().count(event("playback_gated") + R"(,"kind":"ring")") == 1);

  expectApplied(client.get(), 40, true);
  sendFrame(client.get(), toneStart("wake-1", cadence_audiod_v1_ToneKind_TONE_KIND_WAKE));
  expectToneState(client.get(), "wake-1", false, "invalid");
  sendFrame(client.get(), toneStart("", kTimer));
  expectToneState(client.get(), "", false, "invalid");
  CHECK(audiod.logs().count(event("tone_loop_refused") + R"(,"reason":"invalid")") == 2);

  // a one-shot Tone with a looped kind gets no answer, only a log
  sendFrame(client.get(), tone(kAlarm));
  sendFrame(client.get(), tone(kTimer));
  expectPong(client.get(), 1);
  CHECK(audiod.logs().count(event("tone_invalid")) == 2);
  CHECK(audiod.logs().count(event("tone_loop_started")) == 0);
}

TEST_CASE("one ring at a time: a second is busy, a reply plays over it, and only its own id stops it") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, audiod.file("out"));
  expectApplied(client.get(), 40, true);

  sendFrame(client.get(), toneStart("alarm-1", kAlarm));
  expectToneState(client.get(), "alarm-1", true, "");
  sendFrame(client.get(), toneStart("timer-1", kTimer));
  expectToneState(client.get(), "timer-1", false, "busy");
  // the ring that sounds, asked for again, sounds on
  sendFrame(client.get(), toneStart("alarm-1", kAlarm));
  expectToneState(client.get(), "alarm-1", true, "");

  // an id that does not sound is ignored
  sendFrame(client.get(), toneStop("timer-1"));
  sendFrame(client.get(), toneStop("never"));
  expectPong(client.get(), 1);

  playStream(client.get(), "reply", opusTone(10, 440, 8000));
  CHECK(expectDone(client.get(), "reply").played_ms == 200);

  sendFrame(client.get(), toneStop("alarm-1"));
  expectToneState(client.get(), "alarm-1", false, "stopped");
  sendFrame(client.get(), toneStop("alarm-1"));
  expectPong(client.get(), 2);

  sendFrame(client.get(), toneStart("timer-1", kTimer));
  expectToneState(client.get(), "timer-1", true, "");

  CHECK(audiod.logs().count(event("tone_loop_started") + R"(,"kind":"alarm")") == 1);
  CHECK(audiod.logs().count(event("tone_loop_started") + R"(,"kind":"timer")") == 1);
  CHECK(audiod.logs().count(event("tone_loop_refused") + R"(,"reason":"busy")") == 1);
  CHECK(audiod.logs().count(event("tone_loop_stopped") + R"(,"reason":"stopped")") == 1);
}

TEST_CASE("the ring that sounds, asked for with another kind, loops that kind from then on") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, audiod.file("out"));
  expectApplied(client.get(), 40, true);

  sendFrame(client.get(), toneStart("ring-1", kAlarm));
  expectToneState(client.get(), "ring-1", true, "");
  sendFrame(client.get(), toneStart("ring-1", kTimer));
  expectToneState(client.get(), "ring-1", true, "");
  // the kind it loops, asked for again, changes nothing
  sendFrame(client.get(), toneStart("ring-1", kTimer));
  expectToneState(client.get(), "ring-1", true, "");
  sendFrame(client.get(), toneStop("ring-1"));
  expectToneState(client.get(), "ring-1", false, "stopped");

  CHECK(audiod.logs().count(event("tone_loop_started") + R"(,"kind":"alarm")") == 1);
  CHECK(audiod.logs().count(event("tone_loop_started") + R"(,"kind":"timer")") == 1);
  CHECK(audiod.logs().count(event("tone_loop_stopped")) == 1);
}

TEST_CASE("a ring repeats its period whole in the device and leaves it within one period of its stop") {
  fake_sink::FakePcmSink sink;
  AudiodHarness audiod(compiledBackends(), {}, heldOutput(sink));
  auto client = connectConfigured(audiod, audiod.file("out"));
  // without a hardware control the software gain at 100 leaves every sample as it is
  expectApplied(client.get(), 100, true);
  constexpr std::size_t kFill = kPlayerConfig.maxDeviceFillFrames;
  REQUIRE(eventually([&] { return sink.bufferedFrames() == kFill; }));

  sendFrame(client.get(), toneStart("timer-1", kTimer));
  expectToneState(client.get(), "timer-1", true, "");

  const auto period = ringPeriodPcm(RingKind::Timer);
  const auto lead = static_cast<std::size_t>(
      std::find_if(period.begin(), period.end(), [](std::int16_t sample) { return sample != 0; }) - period.begin());
  Speaker speaker(sink);
  const auto sounded = speaker.playToSound();
  REQUIRE(sounded >= lead);
  const auto start = sounded - lead;
  // two whole periods and the seam between them; the device then holds the next one's first beep
  speaker.playTo(start + 2 * period.size());
  std::vector<std::int16_t> expected;
  for (std::size_t i = 0; i < 2 * period.size(); ++i)
    expected.push_back(period[i % period.size()]);
  CHECK(std::vector(speaker.left().begin() + static_cast<std::ptrdiff_t>(start), speaker.left().end()) == expected);
  REQUIRE(eventually([&] { return sink.bufferedFrames() == kFill; }));
  REQUIRE_FALSE(audible(fake_sink::channel(sink.buffered(), 0)).empty());

  sendFrame(client.get(), toneStop("timer-1"));
  expectToneState(client.get(), "timer-1", false, "stopped");

  // dmix plays out the period it is on, and nothing of the ring after it
  const auto cut = speaker.left().size();
  speaker.playTo(cut + 3 * kFill);
  const std::vector after(speaker.left().begin() + static_cast<std::ptrdiff_t>(cut), speaker.left().end());
  CHECK(silent(std::vector(after.begin() + static_cast<std::ptrdiff_t>(kPlayerConfig.periodFrames), after.end())));
}

TEST_CASE("a ring lands in the sim's mix as an earcon does") {
  AudiodHarness audiod;
  const auto out = audiod.file("out");
  auto client = connectConfigured(audiod, out);
  expectApplied(client.get(), 100, true);

  sendFrame(client.get(), toneStart("alarm-1", kAlarm));
  expectToneState(client.get(), "alarm-1", true, "");
  // its first two tones
  std::this_thread::sleep_for(400ms);
  sendFrame(client.get(), toneStop("alarm-1"));
  expectToneState(client.get(), "alarm-1", false, "stopped");
  disconnect(audiod, client);

  const auto mix = firstChannel(readWav(out + "/mix/mix-000.wav"));
  CHECK(peak(mix) >= 8100);
  CHECK(peak(mix) <= kEarconPeak);
  // at least one whole tone, and nothing much past the stop: the mix leaves the silence between the tones out
  CHECK(mix.size() >= 7200);
  CHECK(mix.size() <= 48 * 600);
}

TEST_CASE("the gate shutting silences a ring and says so") {
  AudiodHarness audiod;
  const auto out = audiod.file("out");
  auto client = connectConfigured(audiod, out);
  expectApplied(client.get(), 40, true);
  sendFrame(client.get(), toneStart("alarm-1", kAlarm));
  expectToneState(client.get(), "alarm-1", true, "");

  SECTION("a volume that fails") {
    sendFrame(client.get(), volumeSet(101));
    expectToneState(client.get(), "alarm-1", false, "gated");
    CHECK_FALSE(expectFrame(client.get(), cadence_audiod_v1_Envelope_volume_applied_tag).body.volume_applied.ok);
  }

  SECTION("a second Configure") {
    sendFrame(client.get(), configureOutput(out));
    expectToneState(client.get(), "alarm-1", false, "gated");
    expectReady(client.get());
  }

  CHECK(audiod.logs().count(event("tone_loop_stopped") + R"(,"reason":"gated")") == 1);
  // nothing is left to stop, and nothing new sounds until a volume holds again
  sendFrame(client.get(), toneStop("alarm-1"));
  sendFrame(client.get(), toneStart("alarm-1", kAlarm));
  expectToneState(client.get(), "alarm-1", false, "gated");
}

namespace {

// slow enough that the test speaks between a failure and its retry
constexpr VolumeRetry kSlowRetry{.min = 500ms, .max = 1s};

}  // namespace

// an alarm's ramp: the hardware takes the level, but the gate and the level a retry sets stay the last plain set's
TEST_CASE("a transient volume sets the control but leaves the level in force and the gate") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake), openCapture, {}, {}, kSlowRetry);
  const auto out = audiod.file("out");
  const auto packets = opusTone(10, 440, 8000);

  SECTION("it reaches the control and is read back like any level, and a plain set after it restores") {
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, true);
    sendFrame(client.get(), toneStart("alarm-1", kAlarm));
    expectToneState(client.get(), "alarm-1", true, "");

    expectApplied(client.get(), 60, true, true);
    CHECK(fake->levels() == std::vector<int>{30, 60});
    CHECK(fake->verifies() == 2);
    playStream(client.get(), "played", packets);
    CHECK(expectDone(client.get(), "played").played_ms == 200);

    expectApplied(client.get(), 30, true);
    CHECK(fake->levels() == std::vector<int>{30, 60, 30});
    sendFrame(client.get(), toneStop("alarm-1"));
    expectToneState(client.get(), "alarm-1", false, "stopped");
    CHECK(audiod.logs().count(event("volume_transient_applied") + R"(,"level":60,"control":"hardware")") == 1);
    CHECK(audiod.logs().count(event("volume_applied")) == 2);
  }

  SECTION("one the control refuses only logs: the gate stays open, the ring sounds on and nothing is set again") {
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, true);
    sendFrame(client.get(), toneStart("alarm-1", kAlarm));
    expectToneState(client.get(), "alarm-1", true, "");

    fake->fail();
    CHECK(std::string(expectApplied(client.get(), 60, false, true).code) == "control");
    fake->heal();
    CHECK(audiod.logs().count(event("volume_transient_failed") + R"(,"level":60,"reason":"control")") == 1);
    playStream(client.get(), "played", packets);
    CHECK(expectDone(client.get(), "played").played_ms == 200);
    sendFrame(client.get(), toneStop("alarm-1"));
    expectToneState(client.get(), "alarm-1", false, "stopped");
    std::this_thread::sleep_for(700ms);
    CHECK(fake->levels() == std::vector<int>{30, 60});
  }

  SECTION("one that cannot be read back keeps the gate open too") {
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, true);
    sendFrame(client.get(), toneStart("alarm-1", kAlarm));
    expectToneState(client.get(), "alarm-1", true, "");
    fake->failReadBack();
    CHECK(std::string(expectApplied(client.get(), 60, false, true).code) == "readback");
    fake->heal();
    CHECK(audiod.logs().count(event("volume_transient_failed") + R"(,"level":60,"reason":"readback")") == 1);
    playStream(client.get(), "played", packets);
    CHECK(expectDone(client.get(), "played").played_ms == 200);
  }

  SECTION("while the gate is shut it never reaches the control, and the retry sets the level in force") {
    fake->failWrites(1);
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, false);
    CHECK(std::string(expectApplied(client.get(), 60, false, true).code) == "gated");
    CHECK(audiod.logs().count(event("volume_transient_failed") + R"(,"level":60,"reason":"gated")") == 1);

    const auto retried = expectFrame(client.get(), cadence_audiod_v1_Envelope_volume_applied_tag).body.volume_applied;
    CHECK(retried.level == 30);
    CHECK(retried.ok);
    CHECK(fake->levels() == std::vector<int>{30, 30});
  }

  SECTION("with no ring sounding it is refused before the control, so a step after the stop leaves the plain level") {
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, true);
    CHECK(std::string(expectApplied(client.get(), 60, false, true).code) == "no_ring");
    sendFrame(client.get(), toneStart("alarm-1", kAlarm));
    expectToneState(client.get(), "alarm-1", true, "");
    CHECK(std::string(expectApplied(client.get(), 60, true, true).code).empty());
    sendFrame(client.get(), toneStop("alarm-1"));
    expectToneState(client.get(), "alarm-1", false, "stopped");
    expectApplied(client.get(), 30, true);
    // a ramp step cadenced sent late: the hardware stays at the level in force
    CHECK(std::string(expectApplied(client.get(), 65, false, true).code) == "no_ring");
    CHECK(fake->levels() == std::vector<int>{30, 60, 30});
    CHECK(audiod.logs().count(event("volume_transient_failed") + R"(,"level":60,"reason":"no_ring")") == 1);
    CHECK(audiod.logs().count(event("volume_transient_failed") + R"(,"level":65,"reason":"no_ring")") == 1);
    // the gate stayed open all along
    playStream(client.get(), "played", packets);
    CHECK(expectDone(client.get(), "played").played_ms == 200);
  }

  SECTION("without a hardware control it scales the mix like any level") {
    auto client = connectConfigured(audiod, out);
    expectApplied(client.get(), 100, true);
    sendFrame(client.get(), toneStart("alarm-1", kAlarm));
    expectToneState(client.get(), "alarm-1", true, "");
    expectApplied(client.get(), 50, true, true);
    sendFrame(client.get(), toneStop("alarm-1"));
    expectToneState(client.get(), "alarm-1", false, "stopped");
    // the ring stopped without a plain set after it: the stream still plays at the transient level
    playStream(client.get(), "half", packets);
    expectDone(client.get(), "half");
    disconnect(audiod, client);

    // the mix holds the ring before the stream; its last 100 ms are the stream's steady tone
    const auto tail = [](std::vector<std::int16_t> samples) {
      constexpr std::size_t kTail = 4800;
      REQUIRE(samples.size() > kTail);
      return peak(std::vector<std::int16_t>(samples.end() - kTail, samples.end()));
    };
    const auto decoded = tail(readWav(out + "/half.wav").samples());
    const auto mixed = tail(firstChannel(readWav(out + "/mix/mix-000.wav")));
    CHECK(std::abs(mixed - static_cast<int>(std::lround(decoded * 0.5))) <= 2);
    CHECK(fake->levels().empty());
  }
}

namespace {

cadence_audiod_v1_RingSegment toneSegment(cadence_audiod_v1_ToneKind kind) {
  cadence_audiod_v1_RingSegment segment = cadence_audiod_v1_RingSegment_init_zero;
  segment.tone = kind;
  return segment;
}

cadence_audiod_v1_RingSegment silenceSegment(std::uint32_t ms) {
  cadence_audiod_v1_RingSegment segment = cadence_audiod_v1_RingSegment_init_zero;
  segment.silence_ms = ms;
  return segment;
}

cadence_audiod_v1_RingSegment urlSegment(std::string_view url) {
  cadence_audiod_v1_RingSegment segment = cadence_audiod_v1_RingSegment_init_zero;
  copyText(segment.url, url);
  return segment;
}

// the next frame is a ToneState of id that says the ring fell back with code
void expectFallback(int fd, std::string_view id, std::string_view code) {
  const auto state = expectFrame(fd, cadence_audiod_v1_Envelope_tone_state_tag).body.tone_state;
  CHECK(std::string_view(state.id) == id);
  CHECK(state.sounding);
  CHECK(state.fallback);
  CHECK(std::string_view(state.code) == code);
}

}  // namespace

TEST_CASE("a ring of segments sounds its chime first, and loops") {
  fake_sink::FakePcmSink sink;
  AudiodHarness audiod(compiledBackends(), {}, heldOutput(sink));
  auto client = connectConfigured(audiod, audiod.file("out"));
  expectApplied(client.get(), 100, true);

  auto start = toneStart("rem-1", cadence_audiod_v1_ToneKind_TONE_KIND_REMINDER);
  auto& ring = start.body.tone_start;
  ring.segments[0] = toneSegment(cadence_audiod_v1_ToneKind_TONE_KIND_REMINDER);
  ring.segments[1] = silenceSegment(500);
  ring.segments_count = 2;
  ring.loop = true;
  sendFrame(client.get(), start);
  expectToneState(client.get(), "rem-1", true, "");
  CHECK(audiod.logs().count(event("tone_loop_started") + R"(,"kind":"sequence","segments":2)") == 1);

  Speaker speaker(sink);
  const auto first = speaker.playToSound();
  const auto chime = ringPeriodPcm(RingKind::Reminder);
  // two passes of the chime and its half second of silence
  speaker.playTo(first + 2 * (chime.size() + 24000));
  const auto& heard = speaker.left();
  const auto silentAt = first + 46000;
  CHECK(std::all_of(heard.begin() + static_cast<std::ptrdiff_t>(silentAt + 1000),
                    heard.begin() + static_cast<std::ptrdiff_t>(first + chime.size() + 24000 - 1000),
                    [](std::int16_t sample) { return sample == 0; }));
  CHECK(std::any_of(heard.begin() + static_cast<std::ptrdiff_t>(first + chime.size() + 24000),
                    heard.begin() + static_cast<std::ptrdiff_t>(first + chime.size() + 24000 + 4800),
                    [](std::int16_t sample) { return sample != 0; }));

  sendFrame(client.get(), toneStop("rem-1"));
  expectToneState(client.get(), "rem-1", false, "stopped");
}

TEST_CASE("a ring's url that cannot stream falls back, said once, and the device's token is never logged") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, audiod.file("out"));
  expectApplied(client.get(), 40, true);

  auto start = toneStart("alarm-1", kAlarm);
  auto& ring = start.body.tone_start;
  ring.segments[0] = urlSegment("https://server.example/v2/ring-sound/abc");
  ring.segments_count = 1;
  ring.loop = true;
  copyText(ring.url_authorization, "Bearer secret-token-xyz");
  sendFrame(client.get(), start);
  expectToneState(client.get(), "alarm-1", true, "");
  // this harness has no url source: the first read fails it
  expectFallback(client.get(), "alarm-1", "unsupported");
  CHECK(audiod.logs().count(event("ring_fallback") + R"(,"code":"unsupported")") == 1);
  // asked for again, it sounds on as it was
  sendFrame(client.get(), start);
  expectToneState(client.get(), "alarm-1", true, "");
  sendFrame(client.get(), toneStop("alarm-1"));
  expectToneState(client.get(), "alarm-1", false, "stopped");
  CHECK(audiod.logs().count("secret-token") == 0);
  CHECK(audiod.logs().count("ring-sound") == 0);
}

TEST_CASE("a segment with no field or two, or a url in the fallback, makes the ring invalid") {
  AudiodHarness audiod;
  auto client = connectConfigured(audiod, audiod.file("out"));
  expectApplied(client.get(), 40, true);

  auto start = toneStart("ring-1", kAlarm);
  start.body.tone_start.segments[0] = silenceSegment(100);
  start.body.tone_start.segments[0].tone = kTimer;
  start.body.tone_start.segments_count = 1;
  sendFrame(client.get(), start);
  expectToneState(client.get(), "ring-1", false, "invalid");

  start.body.tone_start.segments[0] = cadence_audiod_v1_RingSegment_init_zero;
  sendFrame(client.get(), start);
  expectToneState(client.get(), "ring-1", false, "invalid");

  start.body.tone_start.segments[0] = silenceSegment(100);
  start.body.tone_start.fallback[0] = urlSegment("https://server.example/x");
  start.body.tone_start.fallback_count = 1;
  sendFrame(client.get(), start);
  expectToneState(client.get(), "ring-1", false, "invalid");
}
