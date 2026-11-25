#include "audio/opus-codec.h"
#include "audiod/output.h"
#include "audiod/sys.h"
#include "file/wav.h"
#include "playback/earcons.h"
#include "playback/volume-control.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/fake-sink.h"
#include "support/held-sink.h"
#include "support/ogg-file.h"
#include "support/playback-client.h"
#include "support/samples.h"

#include <catch2/catch_test_macros.hpp>

#include <sys/socket.h>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <functional>
#include <memory>
#include <mutex>
#include <numbers>
#include <optional>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

TEST_CASE("PlaybackGatedUntilVolume") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  const auto out = audiod.file("out");
  const auto packets = opusTone(10, 440, 8000);

  SECTION("before any volume a stream is refused at once, a tone is dropped and nothing is recorded") {
    auto client = connectConfigured(audiod, out);
    sendFrame(client.get(), streamStart("early"));
    CHECK(expectDone(client.get(), "early").played_ms == 0);
    sendPackets(client.get(), "early", packets);
    sendFrame(client.get(), streamEnd("early"));
    sendFrame(client.get(), tone(cadence_audiod_v1_ToneKind_TONE_KIND_WAKE));
    expectPong(client.get(), 1);

    CHECK(audiod.logs().count(event("playback_gated") + R"(,"kind":"stream")") == 1);
    CHECK(audiod.logs().count(event("playback_gated") + R"(,"kind":"tone")") == 1);
    CHECK_FALSE(std::filesystem::exists(out + "/early.wav"));
    disconnect(audiod, client);
    CHECK(readWav(out + "/mix/mix-000.wav").frames() == 0);
  }

  SECTION("a volume the hardware control refuses keeps it gated") {
    fake->fail();
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, false);
    CHECK(fake->levels() == std::vector<int>{30});

    sendFrame(client.get(), streamStart("refused"));
    CHECK(expectDone(client.get(), "refused").played_ms == 0);
    CHECK(audiod.logs().count(event("volume_failed") + R"(,"level":30,"reason":"control")") == 1);
  }

  SECTION("a control that cannot be read back keeps it gated") {
    fake->failReadBack();
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, false);
    CHECK(fake->verifies() == 1);

    sendFrame(client.get(), streamStart("refused"));
    CHECK(expectDone(client.get(), "refused").played_ms == 0);
    CHECK(audiod.logs().count(event("volume_failed") + R"(,"level":30,"reason":"readback")") == 1);
    CHECK(audiod.logs().count(event("volume_readback_mismatch") + R"(,"wanted":5,"held":9,"ch":1)") == 1);
  }

  SECTION("a level above 100 is refused and keeps it gated") {
    auto client = connectConfigured(audiod, out);
    expectApplied(client.get(), 101, false);

    sendFrame(client.get(), streamStart("refused"));
    CHECK(expectDone(client.get(), "refused").played_ms == 0);
  }

  SECTION("once a volume held streams play, until cadenced is gone") {
    auto client = connectConfigured(audiod, out);
    expectApplied(client.get(), 40, true);
    playStream(client.get(), "played", packets);
    CHECK(expectDone(client.get(), "played").played_ms == 200);

    disconnect(audiod, client);
    client = connectConfigured(audiod, out);
    sendFrame(client.get(), streamStart("after"));
    CHECK(expectDone(client.get(), "after").played_ms == 0);
    CHECK(audiod.logs().count(event("playback_gated") + R"(,"kind":"stream")") == 1);
  }
}

namespace {

// the playing stream was cut, not played out: its StreamDone came with less than all of it
void expectCut(int fd, std::string_view id, std::uint32_t wholeMs) { CHECK(expectDone(fd, id).played_ms < wholeMs); }

// the gate is shut: a stream is refused at once with nothing played, and a tone is dropped
void expectGated(const AudiodHarness& audiod, int fd, std::string_view id) {
  const auto tones = audiod.logs().count(event("playback_gated") + R"(,"kind":"tone")");
  sendFrame(fd, streamStart(id));
  CHECK(expectDone(fd, id).played_ms == 0);
  sendFrame(fd, tone(cadence_audiod_v1_ToneKind_TONE_KIND_WAKE));
  expectPong(fd, 1);
  CHECK(audiod.logs().count(event("playback_gated") + R"(,"kind":"tone")") == tones + 1);
}

}  // namespace

// rule 7: an open gate shuts again, cutting what plays, once the level may not be the one asked for
TEST_CASE("PlaybackGateClosesAgain") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  const auto out = audiod.file("out");
  // 400 ms, queued whole before the player starts it
  const auto packets = opusTone(20, 440, 8000);

  SECTION("a level set again logs at debug, a changed one at info") {
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, true);
    expectApplied(client.get(), 30, true);
    expectApplied(client.get(), 40, true);
    CHECK(audiod.logs().count(R"("level":"INFO",)" + event("volume_applied")) == 2);
    CHECK(audiod.logs().count(R"("level":"DEBUG",)" + event("volume_applied")) == 1);
  }

  SECTION("a volume the hardware control refuses") {
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, true);
    playStream(client.get(), "playing", packets);

    fake->fail();
    sendFrame(client.get(), volumeSet(40));
    expectCut(client.get(), "playing", 400);
    const auto applied = expectFrame(client.get(), cadence_audiod_v1_Envelope_volume_applied_tag).body.volume_applied;
    CHECK(applied.level == 40);
    CHECK_FALSE(applied.ok);
    CHECK(fake->levels() == std::vector<int>{30, 40});
    expectGated(audiod, client.get(), "next");
  }

  SECTION("a volume the hardware control cannot read back") {
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, true);
    playStream(client.get(), "playing", packets);

    fake->failReadBack();
    sendFrame(client.get(), volumeSet(40));
    expectCut(client.get(), "playing", 400);
    const auto applied = expectFrame(client.get(), cadence_audiod_v1_Envelope_volume_applied_tag).body.volume_applied;
    CHECK(applied.level == 40);
    CHECK_FALSE(applied.ok);
    expectGated(audiod, client.get(), "next");
  }

  SECTION("a level above 100") {
    auto client = connectConfigured(audiod, out);
    expectApplied(client.get(), 30, true);
    playStream(client.get(), "playing", packets);

    sendFrame(client.get(), volumeSet(101));
    expectCut(client.get(), "playing", 400);
    const auto applied = expectFrame(client.get(), cadence_audiod_v1_Envelope_volume_applied_tag).body.volume_applied;
    CHECK(applied.level == 101);
    CHECK_FALSE(applied.ok);
    expectGated(audiod, client.get(), "next");
  }

  SECTION("a second Configure on the same connection") {
    auto client = connectConfigured(audiod, out);
    expectApplied(client.get(), 30, true);
    playStream(client.get(), "playing", packets);

    sendFrame(client.get(), configureOutput(out));
    expectCut(client.get(), "playing", 400);
    expectReady(client.get());
    expectGated(audiod, client.get(), "next");
  }
}

namespace {

constexpr VolumeRetry kQuickRetry{.min = 100ms, .max = 400ms};

// the VolumeApplied a retry sends on its own once the level held
cadence_audiod_v1_VolumeApplied expectRetried(int fd) {
  const auto received = receive(fd, 5s);
  REQUIRE(received.kind == Received::Kind::frame);
  REQUIRE(received.envelope.which_body == cadence_audiod_v1_Envelope_volume_applied_tag);
  return received.envelope.body.volume_applied;
}

// the time between each write and the one before it
std::vector<std::chrono::steady_clock::duration> gaps(const std::vector<std::chrono::steady_clock::time_point>& writes) {
  std::vector<std::chrono::steady_clock::duration> out;
  for (std::size_t index = 1; index < writes.size(); ++index)
    out.push_back(writes[index] - writes[index - 1]);
  return out;
}

}  // namespace

// ruling M2-R10: the board's mixer may create its control only after audiod set the level (hardvold on pickle), so a
// hardware volume that failed is set again at the level in force, 1 s doubling to 30 s apart, until it holds or a
// newer VolumeSet replaces it, with the gate shut meanwhile. a retry never sends any other level (rule 7)
TEST_CASE("a failed hardware volume is set again at the level in force") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake), openCapture, {}, {}, kQuickRetry);
  const auto out = audiod.file("out");
  const auto packets = opusTone(10, 440, 8000);

  SECTION("a control missing for the first writes: retried with the backoff, then the gate opens at that level") {
    fake->failWrites(5);
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, false);
    expectGated(audiod, client.get(), "early");

    const auto applied = expectRetried(client.get());
    CHECK(applied.level == 30);
    CHECK(applied.ok);
    CHECK(fake->levels() == std::vector<int>{30, 30, 30, 30, 30, 30});
    const auto between = gaps(fake->writes());
    REQUIRE(between.size() == 5);
    CHECK(between[0] >= 100ms);
    CHECK(between[1] >= 200ms);
    CHECK(between[2] >= 400ms);
    // the backoff stops doubling at its cap
    CHECK(between[3] >= 400ms);
    CHECK(between[3] < 800ms);
    CHECK(between[4] >= 400ms);
    CHECK(between[4] < 800ms);
    CHECK(audiod.logs().count(event("volume_failed") + R"(,"level":30,"reason":"control")") == 1);
    CHECK(audiod.logs().count(event("volume_retry_failed") + R"(,"level":30,"reason":"control","retries":1)") == 1);
    CHECK(audiod.logs().count(event("volume_applied") + R"(,"level":30,"control":"hardware","retries":5)") == 1);

    playStream(client.get(), "played", packets);
    CHECK(expectDone(client.get(), "played").played_ms == 200);
  }

  SECTION("a control read back at another level: retried until it reads back the level asked for") {
    fake->failReadBacks(2);
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, false);

    const auto applied = expectRetried(client.get());
    CHECK(applied.level == 30);
    CHECK(applied.ok);
    CHECK(fake->levels() == std::vector<int>{30, 30, 30});
    CHECK(audiod.logs().count(event("volume_readback_mismatch")) == 2);
    playStream(client.get(), "played", packets);
    CHECK(expectDone(client.get(), "played").played_ms == 200);
  }

  SECTION("a newer level while it retries is the one set from then on, with the backoff started over") {
    fake->fail();
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, false);
    // three writes of 30: the next would come 400 ms after the last
    REQUIRE(eventually([&] { return fake->levels().size() == 3; }));
    expectApplied(client.get(), 20, false);
    fake->heal();

    const auto applied = expectRetried(client.get());
    CHECK(applied.level == 20);
    CHECK(applied.ok);
    CHECK(fake->levels() == std::vector<int>{30, 30, 30, 20, 20});
    CHECK(gaps(fake->writes()).back() < 400ms);
    playStream(client.get(), "played", packets);
    CHECK(expectDone(client.get(), "played").played_ms == 200);
    // nothing more: the level held, and 30 was never set again
    std::this_thread::sleep_for(600ms);
    CHECK(fake->levels().size() == 5);
  }

  // what ends them comes right after the first answer, before a second retry could run
  const auto retriesEnd = [&](const std::function<void(UniqueFd&)>& end) {
    fake->fail();
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, false);
    end(client);
    const auto written = fake->levels().size();
    CHECK(written <= 2);
    std::this_thread::sleep_for(600ms);
    CHECK(fake->levels().size() == written);
  };

  SECTION("a level out of range ends the retries") {
    retriesEnd([](UniqueFd& client) { expectApplied(client.get(), 101, false); });
  }

  SECTION("a new Configure ends the retries") {
    retriesEnd([&](UniqueFd& client) {
      sendFrame(client.get(), configureOutput(out, "Master"));
      expectReady(client.get());
    });
  }

  SECTION("cadenced going away ends the retries") {
    retriesEnd([&](UniqueFd& client) { disconnect(audiod, client); });
  }
}

TEST_CASE("StreamPlaysToFile") {
  AudiodHarness audiod;
  const auto out = audiod.file("out");
  auto client = connectConfigured(audiod, out);
  expectApplied(client.get(), 100, true);

  SECTION("the decoded stream is recorded under its id, with no summary: the sim's speaker writes that") {
    playStream(client.get(), "reply-1", opusTone(15, 440, 8000));
    CHECK(expectDone(client.get(), "reply-1").played_ms == 300);

    const auto wav = readWav(out + "/reply-1.wav");
    CHECK(wav.format().sampleRate == 48000);
    CHECK(wav.format().channels == 1);
    CHECK(wav.frames() == 15 * kDecodedFrameSamples);
    CHECK(rms(wav.samples()) > 2000);
    CHECK_FALSE(std::filesystem::exists(out + "/reply-1.json"));
  }

  SECTION("an id that is no plain file name is refused") {
    sendFrame(client.get(), streamStart("../escape"));
    CHECK(expectDone(client.get(), "../escape").played_ms == 0);
    CHECK(audiod.logs().count(event("stream_refused") + R"(,"reason":"stream_id")") == 1);
    CHECK_FALSE(std::filesystem::exists(audiod.file("escape.wav")));
  }
}

TEST_CASE("StreamDoneReportsPlayed") {
  AudiodHarness audiod;
  const auto out = audiod.file("out");
  auto client = connectConfigured(audiod, out);
  expectApplied(client.get(), 100, true);

  SECTION("a stream that played out reports all of its audio, once it was heard") {
    const auto started = std::chrono::steady_clock::now();
    playStream(client.get(), "full", opusTone(10, 440, 8000));
    CHECK(expectDone(client.get(), "full").played_ms == 200);
    CHECK(std::chrono::steady_clock::now() - started >= 200ms);
  }

  SECTION("an empty stream reports nothing played") {
    sendFrame(client.get(), streamStart("empty"));
    sendFrame(client.get(), streamEnd("empty"));
    CHECK(expectDone(client.get(), "empty").played_ms == 0);
  }

  SECTION("a stream replaced while it still arrives reports what played of it") {
    sendFrame(client.get(), streamStart("first"));
    sendPackets(client.get(), "first", opusTone(20, 440, 8000));
    std::this_thread::sleep_for(200ms);

    sendFrame(client.get(), streamStart("second"));
    const auto first = expectDone(client.get(), "first");
    CHECK(first.played_ms > 0);
    CHECK(first.played_ms < 400);
    sendPackets(client.get(), "second", opusTone(5, 440, 8000));
    sendFrame(client.get(), streamEnd("second"));
    CHECK(expectDone(client.get(), "second").played_ms == 100);
  }

  SECTION("a stream that begins after the last one ended plays after it, and each reports in turn") {
    playStream(client.get(), "one", opusTone(10, 440, 8000));
    playStream(client.get(), "two", opusTone(5, 440, 8000));
    CHECK(expectDone(client.get(), "one").played_ms == 200);
    CHECK(expectDone(client.get(), "two").played_ms == 100);
  }
}

TEST_CASE("VolumeSoftwareGain") {
  const auto fake = std::make_shared<FakeVolume>();
  AudiodHarness audiod(compiledBackends(), {}, withFakeControl(fake));
  const auto out = audiod.file("out");
  const auto packets = opusTone(15, 440, 8000);

  SECTION("without a hardware control the level scales the mix") {
    auto client = connectConfigured(audiod, out);
    expectApplied(client.get(), 50, true);
    playStream(client.get(), "half", packets);
    expectDone(client.get(), "half");
    disconnect(audiod, client);

    const auto decoded = peak(readWav(out + "/half.wav").samples());
    const auto mixed = peak(firstChannel(readWav(out + "/mix/mix-000.wav")));
    CHECK(std::abs(mixed - static_cast<int>(std::lround(decoded * 0.5))) <= 1);
    CHECK(fake->levels().empty());
  }

  SECTION("level 0 is silence") {
    auto client = connectConfigured(audiod, out);
    expectApplied(client.get(), 0, true);
    playStream(client.get(), "muted", packets);
    expectDone(client.get(), "muted");
    disconnect(audiod, client);

    CHECK(readWav(out + "/mix/mix-000.wav").frames() == 0);
  }

  SECTION("with a hardware control the control takes the level and the mix stays whole") {
    auto client = connectConfigured(audiod, out, "Master");
    expectApplied(client.get(), 30, true);
    CHECK(fake->levels() == std::vector<int>{30});
    playStream(client.get(), "whole", packets);
    expectDone(client.get(), "whole");
    disconnect(audiod, client);

    CHECK(peak(firstChannel(readWav(out + "/mix/mix-000.wav"))) == peak(readWav(out + "/whole.wav").samples()));
  }
}

TEST_CASE("ToneMixesOverVoice") {
  AudiodHarness audiod;
  const auto out = audiod.file("out");
  auto client = connectConfigured(audiod, out);
  expectApplied(client.get(), 100, true);
  const auto packets = opusTone(20, 300, 6000);

  sendFrame(client.get(), streamStart("voice"));
  sendPackets(client.get(), "voice", packets, 0, 10);
  sendFrame(client.get(), tone(cadence_audiod_v1_ToneKind_TONE_KIND_WAKE));
  sendPackets(client.get(), "voice", packets, 10);
  sendFrame(client.get(), streamEnd("voice"));
  // the tone neither waits for the voice nor holds it back
  CHECK(expectDone(client.get(), "voice").played_ms == 400);
  disconnect(audiod, client);

  const auto voice = peak(readWav(out + "/voice.wav").samples());
  REQUIRE(voice < 7000);
  // only the two sounding at once reach above either alone
  CHECK(peak(firstChannel(readWav(out + "/mix/mix-000.wav"))) > std::max(voice, kEarconPeak) + 3000);
}

TEST_CASE("PromptTonePlaysItsFileOrTheEarcon") {
  AudiodHarness audiod;
  const auto out = audiod.file("out");
  const auto prompts = audiod.file("prompts");
  std::filesystem::create_directories(prompts + "/ru");
  {
    const auto bytes = oggOpusFile(25);
    std::ofstream file(prompts + "/ru/failed.opus", std::ios::binary);
    file.write(reinterpret_cast<const char*>(bytes.data()), static_cast<std::streamsize>(bytes.size()));
  }
  auto client = connectTo(audiod.socket());
  auto configure = configureOutput(out);
  copyText(configure.body.configure.prompts_dir, prompts);
  sendFrame(client.get(), configure);
  expectReady(client.get());
  expectApplied(client.get(), 100, true);

  auto prompt = [](std::string_view id) {
    auto envelope = tone(cadence_audiod_v1_ToneKind_TONE_KIND_ERROR);
    copyText(envelope.body.tone.prompt_id, id);
    copyText(envelope.body.tone.lang, "ru");
    return envelope;
  };
  sendFrame(client.get(), prompt("failed"));
  REQUIRE(eventually([&] { return audiod.logs().count(event("prompt_played") + R"(,"id":"failed")") == 1; }));
  // a prompt the device lacks plays the error earcon in its place
  sendFrame(client.get(), prompt("offline"));
  REQUIRE(eventually(
      [&] { return audiod.logs().count(event("prompt_fallback") + R"(,"id":"offline","reason":"missing")") == 1; }));
  disconnect(audiod, client);
  CHECK(audiod.logs().count(event("tone_refused")) == 0);
}

TEST_CASE("StreamCancelCutsAtOnce") {
  AudiodHarness audiod;
  const auto out = audiod.file("out");
  auto client = connectConfigured(audiod, out);
  expectApplied(client.get(), 100, true);

  playStream(client.get(), "long", opusTone(100, 440, 8000));
  std::this_thread::sleep_for(200ms);
  const auto cancelled = std::chrono::steady_clock::now();
  sendFrame(client.get(), streamCancel("long"));
  const auto done = expectDone(client.get(), "long");
  CHECK(std::chrono::steady_clock::now() - cancelled < 1s);
  CHECK(done.played_ms < 2000);
  CHECK(std::filesystem::exists(out + "/long.wav"));
  CHECK_FALSE(std::filesystem::exists(out + "/long.json"));

  // a finished or unknown id is ignored, and nothing more is reported or played for the cut one
  sendFrame(client.get(), streamCancel("long"));
  sendFrame(client.get(), streamCancel("never"));
  sendPackets(client.get(), "long", opusTone(5, 440, 8000));
  playStream(client.get(), "next", opusTone(5, 440, 8000));
  CHECK(expectDone(client.get(), "next").played_ms == 100);
}

TEST_CASE("StreamCancelCutsFromTheNamedStream") {
  AudiodHarness audiod;
  const auto out = audiod.file("out");
  auto client = connectConfigured(audiod, out);
  expectApplied(client.get(), 100, true);
  // 300 ms, then 100 ms queued behind it
  playStream(client.get(), "one", opusTone(15, 440, 8000));
  playStream(client.get(), "two", opusTone(5, 440, 8000));

  SECTION("cancelling the later stream leaves the earlier one to play out") {
    sendFrame(client.get(), streamCancel("two"));
    CHECK(expectDone(client.get(), "two").played_ms == 0);
    CHECK(expectDone(client.get(), "one").played_ms == 300);
    disconnect(audiod, client);

    // the mix holds the first whole, give or take a period at its edges, and nothing of the second
    const auto frames = readWav(out + "/mix/mix-000.wav").frames();
    CHECK(frames >= 14 * kDecodedFrameSamples);
    CHECK(frames <= 17 * kDecodedFrameSamples);
  }

  SECTION("cancelling the earlier stream cuts both") {
    sendFrame(client.get(), streamCancel("one"));
    CHECK(expectDone(client.get(), "one").played_ms < 300);
    CHECK(expectDone(client.get(), "two").played_ms == 0);
  }
}

// regression: the streams before the named one may have left the player's queue for the device, and the cut must
// still reach the ones queued after it
TEST_CASE("StreamCancelCutsQueuedStreamsBehindOnesInTheDevice") {
  fake_sink::FakePcmSink sink;
  AudiodHarness audiod(compiledBackends(), {}, heldOutput(sink));
  auto client = connectConfigured(audiod, audiod.file("out"));
  expectApplied(client.get(), 100, true);
  // nothing plays until the test plays it, so the device is full of the silence written before any stream
  constexpr std::size_t kFill = kPlayerConfig.maxDeviceFillFrames;
  REQUIRE(eventually([&] { return sink.bufferedFrames() == kFill; }));

  // one period each, then 200 ms
  playStream(client.get(), "a", opusTone(1, 440, 8000));
  playStream(client.get(), "b", opusTone(1, 440, 8000));
  playStream(client.get(), "c", opusTone(10, 440, 8000));
  expectPong(client.get(), 1);
  // room for three periods: the silence read before the streams, then all of a and b; c waits behind them
  const auto before = sink.written().size();
  sink.play(kFill);
  REQUIRE(eventually([&] { return sink.written().size() == before + kFill * kPlaybackChannels; }));

  // a and b are in the device and unsettled, c is still queued
  sendFrame(client.get(), streamCancel("b"));
  expectDone(client.get(), "b");
  expectDone(client.get(), "c");

  const auto cut = sink.written().size();
  for (int round = 0; round < 5; ++round) {
    sink.play(kFill);
    std::this_thread::sleep_for(20ms);
  }
  const auto written = sink.written();
  CHECK(audible(fake_sink::channel(std::vector(written.begin() + static_cast<std::ptrdiff_t>(cut), written.end()), 0))
            .empty());
}

TEST_CASE("the board's period and buffer replace pickle's, with the fill cap short of the brim") {
  AlsaPlayerConfig config = kPlayerConfig;
  OutputConfig output = cadence_audiod_v1_OutputConfig_init_zero;

  applyLayout(config, output);
  CHECK(config.periodFrames == kPlayerConfig.periodFrames);
  CHECK(config.bufferFrames == kPlayerConfig.bufferFrames);
  CHECK(config.maxDeviceFillFrames == kPlayerConfig.maxDeviceFillFrames);

  output.period_frames = 480;
  output.buffer_frames = 1920;
  applyLayout(config, output);
  CHECK(config.periodFrames == 480);
  CHECK(config.bufferFrames == 1920);
  CHECK(config.maxDeviceFillFrames == 1440);

  output.period_frames = 1200;
  output.buffer_frames = 2000;
  config = kPlayerConfig;
  applyLayout(config, output);
  CHECK(config.maxDeviceFillFrames == 2400);
}
