#include "audio-layout.h"
#include "support/engine-harness.h"
#include "support/test-layout.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>
#include <catch2/matchers/catch_matchers_string.hpp>

#include <algorithm>
#include <cmath>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <numbers>
#include <string>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::engine_harness;
using Catch::Matchers::ContainsSubstring;

namespace {

const auto& kCapture = kTestCaptureLayout;
const std::size_t kChannels = kCapture.channels;
// the wake gate merges confirmations closer than 1200 ms of stream time; a 480-frame chunk is 10 ms
constexpr std::size_t kChunksBetweenWakes = 130;

std::int16_t sampleOf(float value) { return static_cast<std::int16_t>(value * 32768.0f); }

// capture that must never be heard: it is spoken while the microphone is muted
constexpr std::int16_t kMutedSpeech = 7777;

auto constant(std::int16_t value) {
  return [value](std::size_t, std::size_t) { return value; };
}

bool hears(const fake_stock::FakeStock& stock, std::int16_t value) {
  return std::any_of(stock.lastMics.begin(), stock.lastMics.end(), [value](float mic) { return sampleOf(mic) == value; });
}

}  // namespace

TEST_CASE_METHOD(EngineFixture, "requests the board's exact capture format") {
  source.grant = CaptureFormat{kCapture.sampleRate, kCapture.channels, kCapture.periodFrames, 4096};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);

  const auto started = engine.start(config());

  REQUIRE(started.ok());
  REQUIRE(source.requested.has_value());
  CHECK(source.requested->sampleRate == kCapture.sampleRate);
  CHECK(source.requested->channels == kCapture.channels);
  CHECK(source.requested->periodFrames == kCapture.periodFrames);
  CHECK(source.requested->bufferFrames == kCapture.bufferFrames);
  // the status reports the buffer the device granted
  CHECK(started.value().capture.bufferFrames == 4096);
  CHECK(started.value().stock.chunkFrames == chunkFrames());
  CHECK(engine.status().running);
}

TEST_CASE_METHOD(EngineFixture, "refuses capture that differs from the board's exact format") {
  // vary one field on each side of the selected board's exact rate, channel count, and period
  const auto [rate, channels, period] = GENERATE(table<int, std::size_t, std::size_t>(
      {{44100, kCapture.channels, kCapture.periodFrames},
       {96000, kCapture.channels, kCapture.periodFrames},
       {kCapture.sampleRate, kCapture.channels - 1, kCapture.periodFrames},
       {kCapture.sampleRate, kCapture.channels + 1, kCapture.periodFrames},
       {kCapture.sampleRate, kCapture.channels, kCapture.periodFrames - 1},
       {kCapture.sampleRate, kCapture.channels, kCapture.periodFrames + 1}}));
  source.grant = CaptureFormat{rate, channels, period, 8192};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);

  const auto started = engine.start(config());

  REQUIRE_FALSE(started.ok());
  CHECK(started.error().code == AudioEngineErrorCode::CaptureFormatMismatch);
  CHECK_THAT(describe(started.error()),
             ContainsSubstring("need exactly " + std::to_string(kCapture.sampleRate) + " Hz x " +
                               std::to_string(kCapture.channels) + ", period " +
                               std::to_string(kCapture.periodFrames)));
  CHECK_THAT(describe(started.error()), ContainsSubstring(std::to_string(rate) + " Hz x " + std::to_string(channels) +
                                                          ", period " + std::to_string(period)));
  CHECK(source.closes == 1);
  CHECK_FALSE(engine.status().running);
}

TEST_CASE_METHOD(EngineFixture, "reports a capture device that cannot be opened") {
  source.openError = CaptureError{CaptureErrorCode::OpenFailed, "default: Device or resource busy"};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);

  const auto started = engine.start(config());

  REQUIRE_FALSE(started.ok());
  CHECK(started.error().code == AudioEngineErrorCode::CaptureOpenFailed);
  CHECK_THAT(describe(started.error()), ContainsSubstring("Device or resource busy"));
  CHECK_FALSE(engine.status().running);
}

TEST_CASE_METHOD(EngineFixture, "does not open capture when the vqe engine cannot start") {
  stock.failures["config"] = "unknown preset";
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);

  const auto started = engine.start(config());

  REQUIRE_FALSE(started.ok());
  CHECK(started.error().code == AudioEngineErrorCode::StockFailed);
  CHECK_THAT(describe(started.error()), ContainsSubstring("unknown preset"));
  CHECK(source.opens == 0);
}

TEST_CASE_METHOD(EngineFixture, "stops, restarts, and rejects a second start while running") {
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());

  const auto again = engine.start(config());
  REQUIRE_FALSE(again.ok());
  CHECK(again.error().code == AudioEngineErrorCode::AlreadyRunning);

  engine.stop();
  CHECK_FALSE(engine.status().running);
  CHECK(source.closes == 1);

  REQUIRE(engine.start(config()).ok());
  CHECK(source.opens == 2);
  feed(engine, 1);
}

TEST_CASE_METHOD(EngineFixture, "each start restarts the wake warm-up") {
  // 480-frame chunks are 10 ms: chunk 301 is the first past the warm-up, and the wake after the restart
  // comes 1500 ms after it, clear of the refractory window but inside a fresh warm-up
  constexpr std::size_t kWarmupChunks = 300;
  constexpr std::size_t kAfterRestart = 150;
  wakeChunks = {1, kWarmupChunks + 1, kWarmupChunks + 1 + kAfterRestart};
  auto settings = config();
  settings.stock.wakeWarmupMs = kWakeWarmupMs;
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(settings).ok());

  auto status = feed(engine, kWarmupChunks + 1);
  CHECK(status.wakes == 1);
  engine.stop();

  REQUIRE(engine.start(settings).ok());
  status = feed(engine, kAfterRestart);
  engine.stop();

  CHECK(status.wakes == 0);
  CHECK(status.spotterEvents[stock_abi::kSpotterFirstConfirmation] == 1);
  CHECK(recorder.wakes() == 1);
}

TEST_CASE_METHOD(EngineFixture, "feeds vqe microphones and speaker references from the board channel map") {
  const auto sample = [](std::size_t frame, std::size_t channel) {
    return static_cast<std::int16_t>((channel + 1) * 1000 + frame % 100);
  };
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());

  source.pushFrames(fake_pcm::captureFrames(chunkFrames(), kChannels, sample));
  handled(engine, 1);
  engine.stop();

  REQUIRE(stock.lastMics.size() == chunkFrames() * kCapture.microphones.size());
  REQUIRE(stock.lastSpeakers.size() == chunkFrames() * kCapture.speakers.size());
  for (std::size_t frame = 0; frame < chunkFrames(); ++frame) {
    for (std::size_t mic = 0; mic < kCapture.microphones.size(); ++mic)
      CHECK(sampleOf(stock.lastMics[frame * kCapture.microphones.size() + mic]) ==
            sample(frame, kCapture.microphones[mic]));
    for (std::size_t speaker = 0; speaker < kCapture.speakers.size(); ++speaker)
      CHECK(sampleOf(stock.lastSpeakers[frame * kCapture.speakers.size() + speaker]) ==
            sample(frame, kCapture.speakers[speaker]));
  }
}

TEST_CASE_METHOD(EngineFixture, "a capture gap discards the partial vqe chunk instead of splicing it") {
  const bool recovered = GENERATE(true, false);
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());

  source.pushFrames(fake_pcm::captureFrames(300, kChannels, [](std::size_t, std::size_t) { return std::int16_t{111}; }));
  if (recovered)
    source.pushRecovered();
  else
    source.pushFailure();
  source.pushFrames(fake_pcm::captureFrames(chunkFrames(), kChannels, [](std::size_t, std::size_t) { return std::int16_t{222}; }));
  const auto status = handled(engine, 1);
  engine.stop();

  CHECK(stock.processCalls == 1);
  CHECK(std::all_of(stock.lastMics.begin(), stock.lastMics.end(), [](float mic) { return sampleOf(mic) == 222; }));
  CHECK(status.gaps == 1);
  CHECK(status.discardedFrames == 300);
  if (recovered) {
    CHECK(status.recoveries == 1);
    CHECK_FALSE(status.lastError.has_value());
  } else {
    CHECK(status.captureErrors == 1);
    REQUIRE(status.lastError.has_value());
    CHECK(status.lastError->code == AudioEngineErrorCode::CaptureFailed);
    CHECK_THAT(describe(*status.lastError), ContainsSubstring("fake read failure"));
  }
}

TEST_CASE_METHOD(EngineFixture, "capture failing in a row is timed from the first failure until the stream works again") {
  const bool recovered = GENERATE(false, true);
  CAPTURE(recovered);
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());
  CHECK_FALSE(engine.status().captureFailingSince);

  source.pushFailure();
  source.pushFailure();
  REQUIRE(eventually([&] { return engine.status().captureErrors >= 1; }));
  const auto first = engine.status().captureFailingSince;
  REQUIRE(first);
  REQUIRE(eventually([&] { return engine.status().captureErrors == 2; }));
  CHECK(engine.status().captureFailingSince == first);
  // reads that find no audio yet are no success
  std::this_thread::sleep_for(std::chrono::milliseconds(250));
  CHECK(engine.status().captureFailingSince == first);

  if (recovered)
    source.pushRecovered();
  else
    source.pushFrames(fake_pcm::captureFrames(256, kChannels, constant(0)));
  CHECK(eventually([&] { return !engine.status().captureFailingSince; }));

  source.pushFailure();
  REQUIRE(eventually([&] { return engine.status().captureErrors == 3; }));
  const auto again = engine.status().captureFailingSince;
  REQUIRE(again);
  CHECK(*again > *first);
  engine.stop();
}

TEST_CASE_METHOD(EngineFixture, "a full one-second fifo drops the oldest capture and processing resumes on the newest") {
  // frame i carries i in channels 0 and 1 as (i % 30000, i / 30000)
  const auto indexed = [](std::size_t first) {
    return [first](std::size_t frame, std::size_t channel) {
      const auto index = first + frame;
      return static_cast<std::int16_t>(channel == 0 ? index % 30000 : channel == 1 ? index / 30000 : 0);
    };
  };
  ProcessStall stall;
  std::vector<std::size_t> chunkStarts;
  duringProcess = [&] {
    chunkStarts.push_back(static_cast<std::size_t>(sampleOf(stock.lastMics[0])) +
                          static_cast<std::size_t>(sampleOf(stock.lastMics[1])) * 30000);
    if (stock.processCalls == 1)
      stall.hold();
  };
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  const auto unstall = stall.releaseOnExit();
  REQUIRE(engine.start(config()).ok());

  source.pushFrames(fake_pcm::captureFrames(chunkFrames(), kChannels, indexed(0)));
  REQUIRE(stall.held());
  // 1.5 s of capture arrives while vqe is stalled
  source.pushFrames(fake_pcm::captureFrames(72000, kChannels, indexed(chunkFrames())));
  REQUIRE(eventually([&] { return engine.status().overflowFrames == 24000; }));
  stall.release();
  const auto status = handled(engine, 1 + 48000 / chunkFrames());
  engine.stop();

  CHECK(status.overflowFrames == 24000);
  CHECK(status.gaps == 1);
  CHECK(status.discardedFrames == 0);
  REQUIRE(chunkStarts.size() == 1 + 48000 / chunkFrames());
  CHECK(chunkStarts[0] == 0);
  for (std::size_t chunk = 1; chunk < chunkStarts.size(); ++chunk)
    CHECK(chunkStarts[chunk] == chunkFrames() + 24000 + (chunk - 1) * chunkFrames());
}

TEST_CASE_METHOD(EngineFixture, "delivers no microphone audio before activation") {
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());

  const auto status = feed(engine, 20);
  engine.stop();

  CHECK(recorder.frames().empty());
  CHECK(recorder.wakes() == 0);
  CHECK(status.frames == 0);
  CHECK_FALSE(status.streaming);
}

TEST_CASE_METHOD(EngineFixture, "stopping the stream ends delivery and drops the partial frame") {
  wakeChunks = {1};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());

  // chunks 2-4 give one frame and leave chunk 4's 160 samples pending
  feed(engine, 4);
  REQUIRE(recorder.frames().size() == 1);
  engine.setStreaming(false);
  CHECK_FALSE(engine.status().streaming);
  feed(engine, 3);
  CHECK(recorder.frames().size() == 1);

  engine.setStreaming(true);
  feed(engine, 2);
  engine.stop();

  const auto frames = recorder.frames();
  REQUIRE(frames.size() == 2);
  CHECK(frames[1] == sttSamples(8, 9));
}

TEST_CASE_METHOD(EngineFixture, "a callback may stop the stream before the rest of its chunk is delivered") {
  useOutputFrames(480);
  wakeChunks = {1};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  recorder.afterFrame = [&engine] { engine.setStreaming(false); };
  REQUIRE(engine.start(config()).ok());

  // chunks 2 and 3 hold three frames; the first frame's callback stops streaming
  const auto status = feed(engine, 3);
  engine.stop();

  CHECK(recorder.frames().size() == 1);
  CHECK(status.frames == 1);
  CHECK_FALSE(status.streaming);
}

TEST_CASE_METHOD(EngineFixture, "muting stops delivery at once and unmuting does not resume it") {
  wakeChunks = {1};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());

  feed(engine, 4);
  REQUIRE(recorder.frames().size() == 1);
  engine.setPhysicalMuted(true);
  const auto muted = engine.status();
  CHECK(muted.muted);
  CHECK_FALSE(muted.streaming);
  feed(engine, 3);
  CHECK(recorder.frames().size() == 1);

  engine.setPhysicalMuted(false);
  CHECK_FALSE(engine.status().streaming);
  feed(engine, 2);
  CHECK(recorder.frames().size() == 1);

  // a button activation after unmute starts a fresh frame at chunk 10
  engine.setStreaming(true);
  feed(engine, 2);
  engine.stop();

  const auto frames = recorder.frames();
  REQUIRE(frames.size() == 2);
  CHECK(frames[1] == sttSamples(10, 11));
}

TEST_CASE_METHOD(EngineFixture, "physical and software privacy mute keep independent coherent generations") {
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());
  engine.setStreaming(true);
  REQUIRE(engine.status().streaming);

  const auto physicalOn = engine.setPhysicalMuted(true);
  CHECK(physicalOn == MuteState{1, true, false, true});
  CHECK_FALSE(engine.status().streaming);

  const auto privacyOn = engine.setPrivacyMuted(true);
  CHECK(privacyOn == MuteState{2, true, true, true});
  const auto physicalOff = engine.setPhysicalMuted(false);
  CHECK(physicalOff == MuteState{3, false, true, true});
  CHECK(engine.muteState() == physicalOff);
  CHECK(engine.status().muted);

  // clearing the physical switch cannot reopen capture while software privacy remains on
  engine.setStreaming(true);
  CHECK_FALSE(engine.status().streaming);
  const auto privacyOff = engine.setPrivacyMuted(false);
  CHECK(privacyOff == MuteState{4, false, false, false});
  CHECK_FALSE(engine.status().streaming);
  CHECK_FALSE(engine.status().muted);
  CHECK(engine.setPrivacyMuted(false) == privacyOff);
  engine.stop();
}

TEST_CASE_METHOD(EngineFixture, "software privacy suppresses wake and command processing like physical mute") {
  wakeChunks = {1};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  engine.setPrivacyMuted(true);
  REQUIRE(engine.start(config()).ok());

  const auto status = feed(engine, 3);
  CHECK(recorder.wakes() == 0);
  CHECK(recorder.frames().empty());
  CHECK(status.suppressedWakes == 1);
  CHECK(status.muted);
  engine.stop();
}

TEST_CASE_METHOD(EngineFixture, "a muted microphone ignores wakes and activation") {
  const int unmutedWake = 1 + static_cast<int>(kChunksBetweenWakes);
  wakeChunks = {1, unmutedWake};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  engine.setPhysicalMuted(true);
  REQUIRE(engine.start(config()).ok());

  auto status = feed(engine, 3);
  CHECK(recorder.wakes() == 0);
  CHECK(status.wakes == 0);
  CHECK(status.suppressedWakes == 1);
  CHECK(status.muted);
  engine.setStreaming(true);
  CHECK_FALSE(engine.status().streaming);
  feed(engine, 2);
  CHECK(recorder.frames().empty());

  engine.setPhysicalMuted(false);
  status = feed(engine, static_cast<std::size_t>(unmutedWake) - 5 + 2);
  engine.stop();

  CHECK(recorder.wakes() == 1);
  CHECK(status.wakes == 1);
  const auto frames = recorder.frames();
  REQUIRE(frames.size() == 1);
  CHECK(frames[0] == sttSamples(static_cast<std::size_t>(unmutedWake) + 1, static_cast<std::size_t>(unmutedWake) + 2));
}

TEST_CASE_METHOD(EngineFixture, "audio captured while muted never reaches vqe, the spotter, or the stream") {
  constexpr std::int16_t kLive = 100;
  ProcessStall stall;
  bool vqeHeardLive = false;
  bool vqeHeardMuted = false;
  duringProcess = [&] {
    vqeHeardLive = vqeHeardLive || hears(stock, kLive);
    const bool heard = hears(stock, kMutedSpeech);
    vqeHeardMuted = vqeHeardMuted || heard;
    // this spotter wakes on muted speech, and stt echoes microphone 0 so frames show what was captured
    if (heard)
      stock.events.push_back(stock_abi::kSpotterFirstConfirmation);
    auto& stt = stock.outputs[fake_stock::kOmniWithGain];
    for (std::size_t index = 0; index < stt.size(); ++index)
      stt[index] = stock.lastMics[index * kCapture.microphones.size() * 3];
    if (stock.processCalls == 1)
      stall.hold();
  };
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  const auto unstall = stall.releaseOnExit();
  REQUIRE(engine.start(config()).ok());

  source.pushFrames(fake_pcm::captureFrames(chunkFrames(), kChannels, constant(kLive)));
  REQUIRE(stall.held());
  engine.setPhysicalMuted(true);
  // muted speech queues behind the stalled chunk and ends mid-chunk
  source.pushFrames(fake_pcm::captureFrames(5 * chunkFrames() + 100, kChannels, constant(kMutedSpeech)));
  REQUIRE(eventually([&] { return source.drained(); }));
  engine.setPhysicalMuted(false);
  engine.setStreaming(true);
  stall.release();
  handled(engine, 6);
  // well past the silenced tail of the capture buffer, so live audio is heard again
  feed(engine, (kCapture.bufferFrames + chunkFrames() - 1) / chunkFrames() + 5, kLive);
  const auto status = engine.status();
  engine.stop();

  CHECK_FALSE(vqeHeardMuted);
  CHECK(recorder.wakes() == 0);
  CHECK(status.wakes == 0);
  CHECK(status.suppressedWakes == 0);
  const auto samples = recorder.samples();
  CHECK_FALSE(samples.empty());
  CHECK(std::none_of(samples.begin(), samples.end(), [](std::int16_t sample) { return sample == kMutedSpeech; }));
  CHECK(vqeHeardLive);
}

TEST_CASE_METHOD(EngineFixture, "audio still in the device buffer when the mute ends stays silent") {
  bool vqeHeardLive = false;
  bool vqeHeardMuted = false;
  duringProcess = [&] {
    vqeHeardMuted = vqeHeardMuted || hears(stock, kMutedSpeech);
    vqeHeardLive = vqeHeardLive || hears(stock, 100);
  };
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());
  // the capture thread snapshots the mute epoch on its first run, which comes before its first read; a whole mute
  // before that would go unseen
  REQUIRE(source.waitForFirstRead());

  // nothing is read during the mute, so its speech is still waiting in the device when the mute ends
  source.holdReads(true);
  engine.setPhysicalMuted(true);
  source.pushFrames(fake_pcm::captureFrames(5 * chunkFrames(), kChannels, constant(kMutedSpeech)));
  engine.setPhysicalMuted(false);
  source.holdReads(false);
  handled(engine, 5);
  // well past the silenced tail of the capture buffer, so live audio is heard again
  feed(engine, (kCapture.bufferFrames + chunkFrames() - 1) / chunkFrames() + 5, 100);
  engine.stop();

  CHECK_FALSE(vqeHeardMuted);
  CHECK(vqeHeardLive);
}

TEST_CASE_METHOD(EngineFixture, "muting drops capture queued before it, including a partial vqe chunk") {
  bool vqeHeardAudio = false;
  duringProcess = [&] {
    vqeHeardAudio = vqeHeardAudio ||
                    std::any_of(stock.lastMics.begin(), stock.lastMics.end(), [](float mic) { return mic != 0.0f; });
  };
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());

  // less than a chunk, so it waits in the fifo or the partial vqe chunk
  source.pushFrames(fake_pcm::captureFrames(300, kChannels, [](std::size_t, std::size_t) { return std::int16_t{5555}; }));
  REQUIRE(eventually([&] { return source.drained(); }));
  engine.setPhysicalMuted(true);
  const auto status = feed(engine, 2);
  engine.stop();

  CHECK_FALSE(vqeHeardAudio);
  CHECK(status.gaps == 1);
}

TEST_CASE_METHOD(EngineFixture, "a wake streams whole stt frames from the next chunk on, and keeps them contiguous across a wake") {
  const int outputFrames = GENERATE(160, 256, 480);
  useOutputFrames(outputFrames);
  const int secondWake = 2 + static_cast<int>(kChunksBetweenWakes);
  wakeChunks = {1, secondWake};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());

  // the second wake is polled after its chunk is framed, so a partial frame is pending for most sizes
  const auto last = static_cast<std::size_t>(secondWake) + 3;
  const auto status = feed(engine, last);
  engine.stop();

  CHECK(recorder.wakes() == 2);
  CHECK(status.wakes == 2);
  CHECK(status.streaming);
  const auto expected = sttSamples(2, last);
  const auto whole = expected.size() / kPcm16FrameSamples;
  CHECK(status.frames == whole);
  const auto frames = recorder.frames();
  CHECK(std::all_of(frames.begin(), frames.end(), [](const auto& frame) { return frame.size() == 320; }));
  const auto samples = recorder.samples();
  REQUIRE(samples.size() == whole * kPcm16FrameSamples);
  CHECK(std::equal(samples.begin(), samples.end(), expected.begin()));
}

TEST_CASE_METHOD(EngineFixture, "setStreaming(true) while streaming keeps the stream contiguous") {
  wakeChunks = {1};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());

  // chunks 2-4 give one frame and leave chunk 4's 160 samples pending
  feed(engine, 4);
  REQUIRE(recorder.frames().size() == 1);
  engine.setStreaming(true);
  feed(engine, 3);
  engine.stop();

  const auto frames = recorder.frames();
  REQUIRE(frames.size() == 3);
  CHECK(frames[1] == sttSamples(4, 5));
  CHECK(frames[2] == sttSamples(6, 7));
}

TEST_CASE_METHOD(EngineFixture, "vqe failures are visible and processing continues") {
  wakeChunks = {1};
  failingChunks = {3};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());

  const auto status = feed(engine, 5);
  engine.stop();

  CHECK(status.vqeErrors == 1);
  REQUIRE(status.lastError.has_value());
  CHECK(status.lastError->code == AudioEngineErrorCode::VqeFailed);
  CHECK_THAT(describe(*status.lastError), ContainsSubstring("fake vqe fault"));
  CHECK(status.frames == 1);
  CHECK(stock.processCalls == 5);
}

TEST_CASE_METHOD(EngineFixture, "a spotter that failed to start is visible and activation still works") {
  stock.failures["spotter-create"] = "model rejected";
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  const auto started = engine.start(config());
  REQUIRE(started.ok());
  REQUIRE(started.value().stock.spotterError.has_value());

  auto status = feed(engine, 3);
  CHECK(status.spotterErrors == 3);
  REQUIRE(status.lastError.has_value());
  CHECK(status.lastError->code == AudioEngineErrorCode::SpotterFailed);
  CHECK_THAT(describe(*status.lastError), ContainsSubstring("model rejected"));

  engine.setStreaming(true);
  status = feed(engine, 2);
  engine.stop();

  CHECK(recorder.wakes() == 0);
  CHECK(status.frames == 1);
}

TEST_CASE_METHOD(EngineFixture, "a command hit calls onCommand and does not start streaming") {
  commandChunks = {{2, "стоп"}};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  const auto started = engine.start(configWithCommands());
  REQUIRE(started.ok());
  REQUIRE(started.value().stock.spotter.has_value());
  CHECK(started.value().stock.spotter->commands);

  const auto status = feed(engine, 5);
  engine.stop();

  CHECK(recorder.commands() == std::vector<std::string>{"стоп"});
  CHECK(status.commandHits == 1);
  CHECK(recorder.wakes() == 0);
  CHECK(status.wakes == 0);
  CHECK_FALSE(status.streaming);
  CHECK(recorder.frames().empty());
  CHECK(status.frames == 0);
}

TEST_CASE_METHOD(EngineFixture, "a command hit while streaming leaves the stream as it is") {
  wakeChunks = {1};
  // chunk 4 leaves a partial frame pending, which a command must not drop
  commandChunks = {{4, "тише"}};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(configWithCommands()).ok());

  const auto status = feed(engine, 6);
  engine.stop();

  CHECK(recorder.commands() == std::vector<std::string>{"тише"});
  CHECK(status.streaming);
  const auto expected = sttSamples(2, 6);
  const auto samples = recorder.samples();
  REQUIRE(samples.size() == expected.size() / kPcm16FrameSamples * kPcm16FrameSamples);
  CHECK(std::equal(samples.begin(), samples.end(), expected.begin()));
}

TEST_CASE_METHOD(EngineFixture, "a command hit is ignored without onCommand or while muted") {
  commandChunks = {{1, "громче"}};
  auto callbacks = recorder.callbacks();
  bool muted = false;
  SECTION("no onCommand") { callbacks.onCommand = nullptr; }
  SECTION("muted") { muted = true; }
  AudioEngine engine(source, callbacks, fake_stock::loadFake);
  engine.setPhysicalMuted(muted);
  REQUIRE(engine.start(configWithCommands()).ok());

  const auto status = feed(engine, 3);
  engine.stop();

  CHECK(recorder.commands().empty());
  CHECK(status.wakes == 0);
  CHECK_FALSE(status.streaming);
  CHECK(status.commandHits == (muted ? 0 : 1));
  CHECK(status.suppressedCommandHits == (muted ? 1 : 0));
}

TEST_CASE_METHOD(EngineFixture, "status counts hits from an unknown component, which change nothing") {
  duringProcess = [this] {
    if (stock.processCalls != 2)
      return;
    stock.hits.push_back({fake_stock::FakeComponent::Stray, {}});
    stock.events.push_back(stock_abi::kSpotterFirstConfirmation);
  };
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(configWithCommands()).ok());

  const auto status = feed(engine, 4);
  engine.stop();

  CHECK(status.unknownComponents == 1);
  CHECK(status.spotterEvents[stock_abi::kSpotterFirstConfirmation] == 1);
  CHECK(status.wakes == 0);
  CHECK(status.commandHits == 0);
  CHECK(recorder.commands().empty());
  CHECK_FALSE(status.streaming);
}

TEST_CASE_METHOD(EngineFixture, "the playback monitor reports reference levels without a wake or stream") {
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());
  CHECK(engine.status().playbackMonitorAvailable);

  // 60 chunks of 10 ms are 600 ms: past the filters' warm-up and enough windows to measure
  auto status = feedReferenceTone(engine, 60);
  engine.stop();

  CHECK(status.frames == 0);
  CHECK(recorder.wakes() == 0);
  CHECK(recorder.frames().empty());
  const auto levels = recorder.playbackLevels();
  REQUIRE(levels.size() >= 3);
  // the last published value is the synchronous zero from stop, after real measurements
  const auto last = levels.back();
  CHECK(last.level == 0.0f);
  CHECK(last.bands == std::array<float, 3>{});
  // every settled measurement keeps the tone in the bass band with a nonzero level
  int measured = 0;
  for (const auto& window : levels)
    if (window.level > 0.0f) {
      CHECK(window.bands[0] > window.bands[1]);
      CHECK(window.bands[0] > window.bands[2]);
      ++measured;
    }
  CHECK(measured >= 3);
}

TEST_CASE_METHOD(EngineFixture, "a muted microphone still monitors the speaker references privately") {
  bool vqeHeardReference = false;
  duringProcess = [this, &vqeHeardReference] {
    vqeHeardReference = vqeHeardReference ||
                        std::any_of(stock.lastMics.begin(), stock.lastMics.end(), [](float mic) { return mic != 0.0f; });
  };
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  engine.setPhysicalMuted(true);
  REQUIRE(engine.start(config()).ok());

  // a loud constant reference and loud constant microphones, both read while muted
  auto status = engine.status();
  source.pushFrames(fake_pcm::captureFrames(60 * chunkFrames(), kChannels, [](std::size_t, std::size_t) {
    return std::int16_t{20000};
  }));
  REQUIRE(eventually([&] { return engine.status().chunks >= 60; }));
  status = engine.status();
  engine.stop();

  // vqe still hears silence: mute zeroing is untouched
  CHECK_FALSE(vqeHeardReference);
  CHECK(status.suppressedWakes == 0);
  CHECK(recorder.frames().empty());
  // the monitor kept measuring the reference through the mute
  const auto levels = recorder.playbackLevels();
  int measured = 0;
  for (const auto& window : levels)
    if (window.level > 0.0f)
      ++measured;
  CHECK(measured >= 3);
  CHECK(levels.back().level == 0.0f);
}

TEST_CASE_METHOD(EngineFixture, "capture gaps reset the monitor and restart it cleanly") {
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());
  feedReferenceTone(engine, 60);

  SECTION("failed read") {
    source.pushFailure();
    REQUIRE(eventually([&] { return engine.status().captureErrors == 1; }));
  }
  SECTION("recovered overrun") {
    source.pushRecovered();
    REQUIRE(eventually([&] { return engine.status().recoveries == 1; }));
  }
  engine.stop();

  const auto levels = recorder.playbackLevels();
  REQUIRE(levels.size() >= 2);
  // the failure published a synchronous zero before the stop's own zero
  CHECK(levels[levels.size() - 2].level == 0.0f);
  CHECK(levels.back().level == 0.0f);

  // a restart clears history: only fresh measurements follow the new zero
  const std::size_t cleared = levels.size();
  REQUIRE(engine.start(config()).ok());
  feed(engine, 4, 0);
  const auto silentRestart = recorder.playbackLevels();
  REQUIRE(silentRestart.size() > cleared);
  for (auto window = silentRestart.begin() + static_cast<std::ptrdiff_t>(cleared); window != silentRestart.end(); ++window) {
    CHECK(window->level == 0);
    CHECK(window->bands == std::array<float, 3>{});
  }
  feedReferenceTone(engine, 60);
  engine.stop();
  const auto afterRestart = recorder.playbackLevels();
  REQUIRE(afterRestart.size() > cleared);
  int fresh = 0;
  for (auto window = afterRestart.begin() + static_cast<std::ptrdiff_t>(cleared); window != afterRestart.end();
       ++window)
    if (window->level > 0.0f)
      ++fresh;
  CHECK(fresh >= 3);
  engine.stop();
  CHECK(recorder.playbackLevels().size() == afterRestart.size());
}

TEST_CASE_METHOD(EngineFixture, "capture metering advances while vqe and the controller are stalled") {
  ProcessStall stall;
  duringProcess = [&] {
    if (stock.processCalls == 1)
      stall.hold();
  };
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  const auto unstall = stall.releaseOnExit();
  REQUIRE(engine.start(config()).ok());

  // the first chunk stalls vqe; the reference tone keeps flowing behind the one-second fifo
  const auto& capture = fake_stock::testConfig().capture;
  for (std::size_t batch = 0; batch < 6; ++batch)
    source.pushFrames(fake_pcm::captureFrames(10 * chunkFrames(), kChannels,
                                              [&capture, batch, this](std::size_t index, std::size_t channel) {
                                                const bool reference = std::find(capture.speakers.begin(),
                                                                                 capture.speakers.end(), channel) !=
                                                                       capture.speakers.end();
                                                if (!reference)
                                                  return std::int16_t{0};
                                                const auto frame = (batch * 10 * chunkFrames()) + index;
                                                const double sample = 0.25 * std::sin(2 * std::numbers::pi * 100.0 *
                                                                                      static_cast<double>(frame) / 48000.0);
                                                return static_cast<std::int16_t>(std::lround(sample * 32768.0));
                                              }));
  // capture measures while the processing thread cannot consume even its first chunk
  REQUIRE(eventually([&] { return recorder.playbackLevels().size() >= 30; }));
  REQUIRE(stall.held());
  CHECK(engine.status().chunks == 0);
  int measured = 0;
  for (const auto& window : recorder.playbackLevels())
    if (window.level > 0.0f)
      ++measured;
  CHECK(measured >= 30);
  stall.release();
  engine.stop();
}

TEST_CASE_METHOD(EngineFixture, "no callback means no monitor work") {
  auto callbacks = recorder.callbacks();
  callbacks.onPlaybackLevels = nullptr;
  AudioEngine engine(source, callbacks, fake_stock::loadFake);
  REQUIRE(engine.start(config()).ok());
  CHECK_FALSE(engine.status().playbackMonitorAvailable);

  feedReferenceTone(engine, 30);
  engine.stop();

  CHECK(recorder.playbackLevels().empty());
}

TEST_CASE_METHOD(EngineFixture, "a failed start never publishes playback levels") {
  source.openError = CaptureError{CaptureErrorCode::OpenFailed, "busy"};
  AudioEngine engine(source, recorder.callbacks(), fake_stock::loadFake);
  const auto started = engine.start(config());
  REQUIRE_FALSE(started.ok());
  engine.stop();
  CHECK(recorder.playbackLevels().empty());
}
