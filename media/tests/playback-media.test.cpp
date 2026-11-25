#include "backend/pcm-input.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/playback-client.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <span>
#include <string>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

constexpr double kFullScale = 32768;
// the music every test plays: 1 kHz at -12 dBFS
constexpr double kMusic = 0.25;
// 20 ms: whole cycles of 1 kHz and of a 300 Hz voice, so one does not leak into the other's level
constexpr std::size_t kWindow = 960;

std::vector<float> music(std::size_t ms, double amplitude = kMusic) { return sine(1000, amplitude, 48 * ms, 2); }

void push(PcmInput& input, const std::vector<float>& stereo) { REQUIRE(input.push(std::span<const float>(stereo), 48000, 2)); }

// pushes in 100 ms pieces from its own thread, as a decoder would, waiting whenever the input is full
std::thread feed(PcmInput& input, std::vector<float> stereo) {
  return std::thread([&input, stereo = std::move(stereo)] {
    for (std::size_t first = 0; first < stereo.size(); first += 4800 * 2)
      input.push(std::span<const float>(stereo).subspan(first, std::min<std::size_t>(4800 * 2, stereo.size() - first)), 48000, 2);
  });
}

// until the player has read everything queued
void drain(PcmInput& input) {
  const auto before = input.status().underruns;
  input.attach();
  REQUIRE(eventually([&] { return input.status().underruns > before; }));
  input.detach();
}

std::string mixPath(const std::string& out) { return out + "/mix/mix-000.wav"; }

// the 1 kHz amplitude of each 20 ms window, as a fraction of full scale
std::vector<double> musicLevels(const std::vector<std::int16_t>& mono) {
  std::vector<double> levels;
  for (std::size_t first = 0; first + kWindow <= mono.size(); first += kWindow)
    levels.push_back(amplitudeAt(mono, 1, 0, first, kWindow, 1000) / kFullScale);
  return levels;
}

bool within(double level, double expected, double toleranceDb) { return std::abs(db(level / expected)) <= toleranceDb; }

// the music played at its own level, then ducked once by v1's envelope and came back: down to 0.15 within 200 ms
// of the onset, held there until heldMs after the onset (give or take toleranceMs), and back up 500 ms later
void expectDuckedOnce(const std::vector<double>& levels, double heldMs, double toleranceMs) {
  // the first windows may start part way into the music
  std::size_t onset = 2;
  while (onset < levels.size() && within(levels[onset], kMusic, 0.5))
    ++onset;
  CHECK(onset >= 10);
  REQUIRE(onset + 11 < levels.size());
  const double ducked = kMusic * 0.15;
  std::size_t rise = onset + 11;
  CHECK(within(levels[rise], ducked, 1));
  while (rise < levels.size() && levels[rise] <= ducked * std::pow(10, 1 / 20.0))
    ++rise;
  CHECK(std::all_of(levels.begin() + static_cast<std::ptrdiff_t>(onset + 11), levels.begin() + static_cast<std::ptrdiff_t>(rise),
                    [&](double level) { return within(level, ducked, 1); }));
  CHECK(std::abs(static_cast<double>((rise - onset) * 20) - heldMs) <= toleranceMs);
  REQUIRE(rise + 26 < levels.size());
  CHECK(within(levels[rise + 26], kMusic, 1));
}

// the VizFrames that came in until a pong to a fresh ping, and fails on anything else
std::vector<cadence_audiod_v1_VizFrame> vizUntilPong(int fd) {
  static std::uint64_t seq = 100;
  sendFrame(fd, ping(++seq));
  std::vector<cadence_audiod_v1_VizFrame> frames;
  for (;;) {
    const auto received = receive(fd);
    REQUIRE(received.kind == Received::Kind::frame);
    if (received.envelope.which_body == cadence_audiod_v1_Envelope_pong_tag) {
      CHECK(received.envelope.body.pong.seq == seq);
      return frames;
    }
    REQUIRE(received.envelope.which_body == cadence_audiod_v1_Envelope_viz_frame_tag);
    frames.push_back(received.envelope.body.viz_frame);
  }
}

}  // namespace

// rule 7: music sounds only between a held VolumeSet and the next failed one
TEST_CASE("GateSilencesMusic") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  const auto out = audiod.file("out");
  // a hardware control leaves the software volume at unity, so only the gate keeps the music quiet
  auto client = connectConfigured(audiod, out, "Master");
  auto& input = audiod.playback().music();

  // louder music before any volume is read away unheard
  push(input, music(300, 0.5));
  drain(input);

  expectApplied(client.get(), 40, true);
  const auto opened = std::chrono::steady_clock::now();
  push(input, music(1500));
  std::this_thread::sleep_for(300ms);
  sendFrame(client.get(), volumeSet(101));
  CHECK_FALSE(expectFrame(client.get(), cadence_audiod_v1_Envelope_volume_applied_tag).body.volume_applied.ok);
  const auto shut = std::chrono::steady_clock::now();

  // the rest of what was queued is gone, and louder music after it is read away unheard
  push(input, music(300, 0.5));
  drain(input);
  disconnect(audiod, client);

  const auto mix = firstChannel(readWav(mixPath(out)));
  CHECK(peak(mix) > 7000);
  CHECK(peak(mix) <= 8194);
  const auto openFrames = static_cast<std::size_t>(48 * std::chrono::duration_cast<std::chrono::milliseconds>(shut - opened).count());
  CHECK(mix.size() <= openFrames + 4800);
  CHECK(mix.size() + 4800 >= openFrames);
}

TEST_CASE("DuckSetDucksMusic") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  const auto out = audiod.file("out");
  auto client = connectConfigured(audiod, out, "Master");
  expectApplied(client.get(), 40, true);
  auto& input = audiod.playback().music();

  auto producer = feed(input, music(3000));
  std::this_thread::sleep_for(500ms);
  sendFrame(client.get(), duckSet(true));
  const auto on = std::chrono::steady_clock::now();
  std::this_thread::sleep_for(700ms);
  sendFrame(client.get(), duckSet(false));
  const auto off = std::chrono::steady_clock::now();
  producer.join();
  drain(input);
  std::this_thread::sleep_for(100ms);
  disconnect(audiod, client);

  const auto held = static_cast<double>(std::chrono::duration_cast<std::chrono::milliseconds>(off - on).count()) + 500;
  expectDuckedOnce(musicLevels(firstChannel(readWav(mixPath(out)))), held, 80);
}

TEST_CASE("VoiceStreamDucksMusic") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  const auto out = audiod.file("out");
  auto client = connectConfigured(audiod, out, "Master");
  expectApplied(client.get(), 40, true);
  auto& input = audiod.playback().music();

  auto producer = feed(input, music(3000));
  std::this_thread::sleep_for(500ms);
  // 400 ms of a 300 Hz voice, over the music
  playStream(client.get(), "voice", opusTone(20, 300, 6000));
  CHECK(expectDone(client.get(), "voice").played_ms == 400);
  producer.join();
  drain(input);
  std::this_thread::sleep_for(100ms);
  disconnect(audiod, client);

  // ducked while the voice queued and sounded, then for the 0.5 s hold
  expectDuckedOnce(musicLevels(firstChannel(readWav(mixPath(out)))), 400 + 500, 80);
}

TEST_CASE("MediaSettings' muffle mode takes the highs off the ducked music") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  const auto out = audiod.file("out");
  auto client = connectConfigured(audiod, out, "Master");
  expectApplied(client.get(), 40, true);
  sendFrame(client.get(), duckModeSettings(cadence_audiod_v1_DuckMode_DUCK_MODE_MUFFLE));
  expectPong(client.get(), 1);
  auto& input = audiod.playback().music();

  // 300 Hz and 5 kHz at 0.2 each, ducked from 400 ms until the end
  auto twoTones = sine(300, 0.2, 48 * 1500, 2);
  const auto high = sine(5000, 0.2, 48 * 1500, 2);
  for (std::size_t i = 0; i < twoTones.size(); ++i)
    twoTones[i] += high[i];
  push(input, twoTones);
  std::this_thread::sleep_for(400ms);
  sendFrame(client.get(), duckSet(true));
  drain(input);
  std::this_thread::sleep_for(100ms);
  disconnect(audiod, client);

  const auto mono = firstChannel(readWav(mixPath(out)));
  const auto level = [&](double hz, std::size_t window) {
    return amplitudeAt(mono, 1, 0, window * kWindow, kWindow, hz) / kFullScale;
  };
  const std::size_t windows = mono.size() / kWindow;
  REQUIRE(windows >= 60);
  // whole before the duck
  CHECK(within(level(300, 3), 0.2, 0.5));
  CHECK(within(level(5000, 3), 0.2, 0.5));
  // ducked, and muffled on top
  const std::size_t late = windows - 8;
  CHECK(within(level(300, late), 0.2 * 0.15, 1));
  CHECK(db(level(5000, late) / level(300, late)) < -20);
}

TEST_CASE("a duck cadenced held ends when it goes") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  const auto out = audiod.file("out");
  auto gone = connectConfigured(audiod, out, "Master");
  expectApplied(gone.get(), 40, true);
  sendFrame(gone.get(), duckSet(true));
  expectPong(gone.get(), 1);
  disconnect(audiod, gone);

  auto client = connectConfigured(audiod, out, "Master");
  expectApplied(client.get(), 40, true);
  auto& input = audiod.playback().music();
  push(input, music(600));
  drain(input);
  std::this_thread::sleep_for(100ms);
  disconnect(audiod, client);

  // the new configuration's mix: whole throughout, but for the windows at its edges
  const auto levels = musicLevels(firstChannel(readWav(mixPath(out))));
  REQUIRE(levels.size() >= 25);
  CHECK(std::all_of(levels.begin() + 2, levels.end() - 2, [](double level) { return within(level, kMusic, 1); }));
}

TEST_CASE("VizFramesOnlyWhenEnabled") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  auto client = connectConfigured(audiod, audiod.file("out"), "Master");
  expectApplied(client.get(), 40, true);
  auto& input = audiod.playback().music();

  // off until MediaSettings turns it on
  push(input, music(400));
  drain(input);
  CHECK(vizUntilPong(client.get()).empty());

  sendFrame(client.get(), vizSettings(true));
  push(input, music(400));
  drain(input);
  std::this_thread::sleep_for(50ms);
  const auto frames = vizUntilPong(client.get());
  // a frame per 1/30 s of the 400 ms
  CHECK(frames.size() >= 8);
  for (const auto& frame : frames) {
    REQUIRE(frame.bands.size == 16);
    // the band from 980 to 1386 Hz
    CHECK(std::max_element(frame.bands.bytes, frame.bands.bytes + 16) - frame.bands.bytes == 8);
  }

  // none while nothing sounds
  std::this_thread::sleep_for(200ms);
  CHECK(vizUntilPong(client.get()).empty());

  sendFrame(client.get(), vizSettings(false));
  push(input, music(400));
  drain(input);
  CHECK(vizUntilPong(client.get()).empty());
}

TEST_CASE("Configure's output channels pick a stereo or a mono mix") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  const auto out = audiod.file("out");

  SECTION("0 is stereo") {
    auto client = connectConfigured(audiod, out, "Master", 0);
    expectApplied(client.get(), 30, true);
    playStream(client.get(), "stereo", opusTone(5, 440, 8000));
    expectDone(client.get(), "stereo");
    disconnect(audiod, client);
    CHECK(readWav(mixPath(out)).format().channels == 2);
  }

  SECTION("1 is a mono downmix that keeps the voice's level") {
    auto client = connectConfigured(audiod, out, "Master", 1);
    expectApplied(client.get(), 30, true);
    playStream(client.get(), "mono", opusTone(15, 440, 8000));
    expectDone(client.get(), "mono");
    disconnect(audiod, client);

    const auto mix = readWav(mixPath(out));
    CHECK(mix.format().channels == 1);
    CHECK(peak(mix.samples()) == peak(readWav(out + "/mono.wav").samples()));
  }

  SECTION("more than 2 is refused like a backend this build lacks") {
    auto client = connectTo(audiod.socket());
    sendFrame(client.get(), configureOutput(out, {}, 3));
    expectClosed(client.get());
    CHECK(audiod.logs().count(event("configure_refused") + R"(,"reason":"channels","section":"output")") == 1);
  }
}

TEST_CASE("Configure's board eq shapes the music and MediaSettings' user eq adds to it") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  const auto out = audiod.file("out");
  const cadence_audiod_v1_EqBand band{cadence_audiod_v1_EqType_EQ_TYPE_PEAKING, 1000, 6, 1};
  auto configuration = configureOutput(out, "Master");
  configuration.body.configure.output.has_eq = true;
  configuration.body.configure.output.eq.bands_count = 1;
  configuration.body.configure.output.eq.bands[0] = band;
  auto client = connectTo(audiod.socket());
  sendFrame(client.get(), configuration);
  expectReady(client.get());
  expectApplied(client.get(), 40, true);
  auto& input = audiod.playback().music();

  push(input, music(600, kMusic / 4));
  drain(input);
  Envelope settings = cadence_audiod_v1_Envelope_init_zero;
  settings.which_body = cadence_audiod_v1_Envelope_media_settings_tag;
  settings.body.media_settings.has_eq = true;
  settings.body.media_settings.eq.bands_count = 1;
  settings.body.media_settings.eq.bands[0] = band;
  sendFrame(client.get(), settings);
  expectPong(client.get(), 1);
  push(input, music(600, kMusic / 4));
  drain(input);
  std::this_thread::sleep_for(100ms);
  disconnect(audiod, client);

  // the two stretches sit back to back in the mix, its silence left out
  const auto levels = musicLevels(firstChannel(readWav(mixPath(out))));
  REQUIRE(levels.size() >= 50);
  CHECK(within(levels[10], kMusic / 2, 0.5));
  CHECK(within(levels[levels.size() - 10], kMusic, 0.5));
}
