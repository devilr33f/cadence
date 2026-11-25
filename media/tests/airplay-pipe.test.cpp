#include "audiod/airplay-files.h"
#include "audiod/airplay-pipe.h"
#include "audiod/log.h"
#include "backend/pcm-input.h"
#include "support/eventually.h"
#include "support/fifo-writer.h"
#include "support/temp-dir.h"
#include "ws/base64.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <atomic>
#include <cerrno>
#include <chrono>
#include <csignal>
#include <cstdint>
#include <fstream>
#include <iterator>
#include <span>
#include <memory>
#include <mutex>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;
using Clock = std::chrono::steady_clock;

namespace {

// 20 ms of 48 kHz s16 stereo
constexpr std::size_t kChunkFrames = 960;
// what the writer may take for a chunk and for all of it while the reader keeps reading, with a loaded machine's slack:
// what counts is that it never stalls (shairport waits for a reader that stopped), not how fast it goes
constexpr auto kSlowestChunk = 750ms;
constexpr auto kWhole = 6s;

class PipeRig {
 public:
  PipeRig() {
    REQUIRE_FALSE(airplay::privateFifo(audio()));
    REQUIRE_FALSE(airplay::privateFifo(metadata()));
    pipe_ = AirplayPipe::start(
        log_, audio(), metadata(), output_,
        [this](const MediaMetadata& metadata) {
          std::scoped_lock lock(mutex_);
          metadata_.push_back(metadata);
        },
        [this](const airplay::Progress& progress) {
          std::scoped_lock lock(mutex_);
          progress_.push_back(progress);
        },
        artwork());
    REQUIRE(pipe_);
  }

  [[nodiscard]] std::string audio() const { return dir_.file("audio.fifo"); }
  [[nodiscard]] std::string metadata() const { return dir_.file("metadata.fifo"); }
  [[nodiscard]] std::string artwork() const { return dir_.file("artwork"); }
  [[nodiscard]] std::vector<airplay::Progress> progress() const {
    std::scoped_lock lock(mutex_);
    return progress_;
  }
  [[nodiscard]] AirplayOutput& output() { return output_; }
  [[nodiscard]] PcmInput& music() { return music_; }
  void stop() { pipe_.reset(); }
  // what reached the sink
  [[nodiscard]] std::vector<MediaMetadata> sunk() const {
    std::scoped_lock lock(mutex_);
    return metadata_;
  }
  [[nodiscard]] std::string logs() const {
    std::scoped_lock lock(mutex_);
    std::string text;
    for (const auto& line : lines_)
      text += line;
    return text;
  }
  // log lines holding fragment
  [[nodiscard]] std::size_t logged(std::string_view fragment) const {
    std::scoped_lock lock(mutex_);
    return static_cast<std::size_t>(std::count_if(lines_.begin(), lines_.end(), [&](const std::string& line) {
      return line.find(fragment) != std::string::npos;
    }));
  }

 private:
  mutable std::mutex mutex_;
  std::vector<std::string> lines_;
  std::vector<MediaMetadata> metadata_;
  std::vector<airplay::Progress> progress_;
  Log log_{[this](std::string_view line) {
    std::scoped_lock lock(mutex_);
    lines_.emplace_back(line);
  }};
  TempDir dir_;
  PcmInput music_;
  AirplayOutput output_{log_};
  std::unique_ptr<AirplayPipe> pipe_;
};

}  // namespace

TEST_CASE("while airplay does not own the output, its audio is drained and dropped and the writer never waits") {
  PipeRig rig;
  // the gate open, the output not: another source owns the session
  rig.output().gate(true);
  rig.output().hold(false);
  const auto fd = fifoWriter(rig.audio());
  // 4 s at once is many times what the fifo holds: only a reader that keeps reading lets it through
  const auto written = writeFrames(fd.get(), 4 * kAirplayRate);
  CHECK_FALSE(written.stalled);
  CHECK(written.slowest < kSlowestChunk);
  CHECK(written.total < kWhole);
  CHECK(rig.music().status().framesIn == 0);

  // owner now, but the gate shut: dropped all the same
  rig.output().gate(false);
  rig.output().open(rig.music());
  const auto shut = writeFrames(fd.get(), 2 * kAirplayRate);
  CHECK_FALSE(shut.stalled);
  CHECK(shut.slowest < kSlowestChunk);
  CHECK(rig.music().status().framesIn == 0);
}

TEST_CASE("the output is held until the receiver plays, and a hold drops what comes after it") {
  PipeRig rig;
  rig.output().gate(true);
  rig.output().open(rig.music());
  const auto fd = fifoWriter(rig.audio());
  // owner with the gate open, but held from the start: never optimistically released
  CHECK(writeFrames(fd.get(), 2 * kAirplayRate).slowest < kSlowestChunk);
  CHECK(rig.music().status().framesIn == 0);

  rig.output().hold(false);
  writeFrames(fd.get(), kChunkFrames * 10);
  REQUIRE(eventually([&] { return rig.music().status().framesIn >= kChunkFrames * 10; }));
  rig.output().hold(true);
  CHECK(rig.music().status().queued == 0);
  const auto in = rig.music().status().framesIn;
  CHECK(writeFrames(fd.get(), 2 * kAirplayRate).slowest < kSlowestChunk);
  std::this_thread::sleep_for(100ms);
  CHECK(rig.music().status().framesIn == in);
}

TEST_CASE("a pause on its way holds the output, and a release from a session that has not taken it changes nothing") {
  PipeRig rig;
  rig.output().gate(true);
  rig.output().open(rig.music());
  rig.output().release(0);
  const auto fd = fifoWriter(rig.audio());
  writeFrames(fd.get(), kChunkFrames);
  REQUIRE(eventually([&] { return rig.music().status().framesIn >= kChunkFrames; }));

  CHECK(rig.output().pause() == 1);
  CHECK(rig.output().pauses() == 1);
  const auto in = rig.music().status().framesIn;
  // the session's release, checked before the pause came, lands after it
  rig.output().release(0);
  writeFrames(fd.get(), kChunkFrames);
  std::this_thread::sleep_for(100ms);
  CHECK(rig.music().status().framesIn == in);

  // once the session took it, its release opens the output again
  rig.output().release(1);
  writeFrames(fd.get(), kChunkFrames);
  CHECK(eventually([&] { return rig.music().status().framesIn >= in + kChunkFrames; }));
}

TEST_CASE("the output's flags set from different threads never undo each other") {
  PipeRig rig;
  // each round: the gate and the hold from two threads at once, the output from a third
  for (int round = 0; round < 200; ++round) {
    rig.output().close();
    rig.output().gate(false);
    rig.output().hold(true);
    std::thread gate([&] { rig.output().gate(true); });
    std::thread hold([&] { rig.output().hold(false); });
    rig.output().open(rig.music());
    gate.join();
    hold.join();
    const auto fd = fifoWriter(rig.audio());
    const auto in = rig.music().status().framesIn;
    writeFrames(fd.get(), kChunkFrames);
    REQUIRE(eventually([&] { return rig.music().status().framesIn >= in + kChunkFrames; }));
    // as the player would, or the ring fills up over the rounds
    std::vector<float> stereo(2 * kChunkFrames);
    rig.music().read(stereo);
  }
}

TEST_CASE("while it owns the output with the gate open, its audio goes into music as it came") {
  PipeRig rig;
  rig.output().gate(true);
  rig.output().hold(false);
  rig.output().open(rig.music());
  const auto fd = fifoWriter(rig.audio());
  writeFrames(fd.get(), kChunkFrames * 5, 16384);
  REQUIRE(eventually([&] { return rig.music().status().framesIn >= kChunkFrames * 5; }));
  std::array<float, 64> stereo{};
  CHECK(rig.music().read(stereo) == stereo.size() / 2);
  CHECK(stereo.front() == 0.5f);
  CHECK(stereo.back() == 0.5f);

  // a frame split between two writes waits for its rest
  const auto before = rig.music().status().framesIn;
  const std::array<std::int16_t, 3> head{1, 2, 3};
  const std::array<std::int16_t, 1> tail{4};
  REQUIRE(::write(fd.get(), head.data(), sizeof head) == static_cast<ssize_t>(sizeof head));
  REQUIRE(eventually([&] { return rig.music().status().framesIn == before + 1; }));
  REQUIRE(::write(fd.get(), tail.data(), sizeof tail) == static_cast<ssize_t>(sizeof tail));
  REQUIRE(eventually([&] { return rig.music().status().framesIn == before + 2; }));
}

// multiroom (P1): a group leader's retarget closes the output on the music input and opens it on its feed, and back
TEST_CASE("its audio goes into the input the output was opened on, and follows a reopen on another") {
  PipeRig rig;
  PcmInput feed;
  rig.output().gate(true);
  rig.output().hold(false);
  rig.output().open(rig.music());
  const auto fd = fifoWriter(rig.audio());
  writeFrames(fd.get(), kChunkFrames * 5);
  REQUIRE(eventually([&] { return rig.music().status().framesIn >= kChunkFrames * 5; }));

  rig.output().close();
  rig.output().open(feed);
  CHECK(rig.music().status().queued == 0);
  const auto heard = rig.music().status().framesIn;
  writeFrames(fd.get(), kChunkFrames * 5);
  REQUIRE(eventually([&] { return feed.status().framesIn >= kChunkFrames * 5; }));
  CHECK(rig.music().status().framesIn == heard);

  // an open on another input while one is open closes that one first: what it held is cleared
  rig.output().open(rig.music());
  CHECK(feed.status().queued == 0);
  const auto fed = feed.status().framesIn;
  writeFrames(fd.get(), kChunkFrames * 5);
  REQUIRE(eventually([&] { return rig.music().status().framesIn >= heard + kChunkFrames * 5; }));
  CHECK(feed.status().framesIn == fed);
  // each flow that stopped said what it carried
  CHECK(rig.logged("airplay_output_closed") == 2);
}

TEST_CASE("a retarget while shairport streams moves its audio without a stuck push, and back") {
  PipeRig rig;
  PcmInput feed;
  rig.output().gate(true);
  rig.output().hold(false);
  rig.output().open(rig.music());
  const auto fd = fifoWriter(rig.audio());
  std::atomic<bool> streaming{true};
  std::atomic<bool> stalled{false};
  std::thread writer([&] {
    while (streaming && !stalled)
      stalled = writeFrames(fd.get(), kChunkFrames).stalled;
  });
  // as a player would read each input, so neither fills up and drops
  std::thread reader([&] {
    std::vector<float> stereo(2 * kChunkFrames);
    while (streaming) {
      rig.music().read(stereo);
      feed.read(stereo);
      std::this_thread::sleep_for(5ms);
    }
  });
  REQUIRE(eventually([&] { return rig.music().status().framesIn >= kChunkFrames * 10; }));

  rig.output().close();
  rig.output().open(feed);
  const auto heard = rig.music().status().framesIn;
  CHECK(eventually([&] { return feed.status().framesIn >= kChunkFrames * 10; }));
  CHECK(rig.music().status().framesIn == heard);

  rig.output().close();
  rig.output().open(rig.music());
  const auto fed = feed.status().framesIn;
  CHECK(eventually([&] { return rig.music().status().framesIn >= heard + kChunkFrames * 10; }));
  CHECK(feed.status().framesIn == fed);

  streaming = false;
  writer.join();
  reader.join();
  CHECK_FALSE(stalled);
}

TEST_CASE("a full input drops what finds no room, so the writer still never waits") {
  PipeRig rig;
  rig.output().gate(true);
  rig.output().hold(false);
  rig.output().open(rig.music());
  const auto fd = fifoWriter(rig.audio());
  // nothing reads music: past its 2 s the rest finds no room
  const auto written = writeFrames(fd.get(), 4 * kAirplayRate);
  CHECK_FALSE(written.stalled);
  CHECK(written.slowest < kSlowestChunk);
  REQUIRE(eventually([&] { return rig.music().status().dropped > 0; }));
}

TEST_CASE("closing the output clears music, and what comes after is dropped") {
  PipeRig rig;
  rig.output().gate(true);
  rig.output().hold(false);
  rig.output().open(rig.music());
  const auto fd = fifoWriter(rig.audio());
  writeFrames(fd.get(), kChunkFrames * 10);
  REQUIRE(eventually([&] { return rig.music().status().framesIn >= kChunkFrames * 10; }));
  rig.output().close();
  CHECK(rig.music().status().queued == 0);
  const auto in = rig.music().status().framesIn;
  writeFrames(fd.get(), kChunkFrames * 10);
  std::this_thread::sleep_for(100ms);
  CHECK(rig.music().status().framesIn == in);
  // and a gate that shuts while it flows does the same
  rig.output().open(rig.music());
  writeFrames(fd.get(), kChunkFrames * 10);
  REQUIRE(eventually([&] { return rig.music().status().framesIn >= in + kChunkFrames * 10; }));
  rig.output().gate(false);
  CHECK(rig.music().status().queued == 0);
}

TEST_CASE("music counts underruns only while airplay's audio flows, and each flow's close logs its counts") {
  PipeRig rig;
  std::vector<float> period(2 * kChunkFrames);
  const auto underruns = [&] { return rig.music().status().underruns; };
  // the player reads on whoever produces: not airplay's until its audio flows
  rig.music().read(period);
  rig.output().gate(true);
  rig.output().open(rig.music());
  rig.music().read(period);
  CHECK(underruns() == 0);

  rig.output().hold(false);
  // flowing, before the first frame came: airplay's
  rig.music().read(period);
  CHECK(underruns() == 1);
  const auto fd = fifoWriter(rig.audio());
  writeFrames(fd.get(), kChunkFrames * 5);
  REQUIRE(eventually([&] { return rig.music().status().framesIn == kChunkFrames * 5; }));
  CHECK(rig.music().read(period) == kChunkFrames);
  CHECK(underruns() == 1);
  CHECK(rig.logged("airplay_output_closed") == 0);

  // 4 periods left, 80 ms, as the hold stops the flow
  rig.output().hold(true);
  CHECK(rig.logged(R"("msg":"airplay_output_closed","frames":4800,"dropped":0,"underruns":1,"queued_ms":80})") == 1);
  rig.music().read(period);
  CHECK(underruns() == 1);

  // the next flow's line counts its own
  rig.output().hold(false);
  writeFrames(fd.get(), kChunkFrames);
  REQUIRE(eventually([&] { return rig.music().status().framesIn == kChunkFrames * 6; }));
  // the player takes it, and with it the room the last flow's frames held
  CHECK(rig.music().read(period) == kChunkFrames);
  // more than music holds: the rest is dropped
  writeFrames(fd.get(), 2 * kAirplayRate + kChunkFrames * 10);
  REQUIRE(eventually([&] { return rig.music().status().dropped == kChunkFrames * 10; }));
  rig.output().close();
  CHECK(rig.logged(R"("frames":96960,"dropped":9600,"underruns":0,"queued_ms":2000})") == 1);
  CHECK(rig.logged("airplay_output_closed") == 2);
  // and a stop of no flow logs nothing
  rig.output().gate(false);
  CHECK(rig.logged("airplay_output_closed") == 2);
}

TEST_CASE("after shairport closes its end, the pipe is read again for the next session") {
  PipeRig rig;
  rig.output().gate(true);
  rig.output().hold(false);
  rig.output().open(rig.music());
  const auto closed = [&](std::size_t count) {
    return eventually([&] { return rig.logged("\"fifo\":\"audio\"") >= count; });
  };
  {
    const auto first = fifoWriter(rig.audio());
    writeFrames(first.get(), kChunkFrames);
  }
  REQUIRE(closed(1));
  // half a frame left behind by the last session never joins the next one
  {
    const auto torn = fifoWriter(rig.audio());
    const std::int16_t half = 7;
    REQUIRE(::write(torn.get(), &half, sizeof half) == static_cast<ssize_t>(sizeof half));
  }
  REQUIRE(closed(2));
  const auto second = fifoWriter(rig.audio());
  writeFrames(second.get(), kChunkFrames);
  REQUIRE(eventually([&] { return rig.music().status().framesIn >= 2 * kChunkFrames; }));
  std::this_thread::sleep_for(50ms);
  REQUIRE(rig.music().status().framesIn == 2 * kChunkFrames);
  std::vector<float> stereo(2 * 2 * kChunkFrames);
  CHECK(rig.music().read(stereo) == 2 * kChunkFrames);
  CHECK(std::all_of(stereo.begin(), stereo.end(), [](float sample) { return sample == 1000.0f / 32768.0f; }));
}

TEST_CASE("the metadata pipe is always drained, and the title, artist and album it sets reach the sink unlogged") {
  PipeRig rig;
  auto fd = fifoWriter(rig.metadata());
  // a mebibyte of what is no item, sixteen times what a fifo holds
  const auto written = writeFrames(fd.get(), 256 * 1024);
  CHECK_FALSE(written.stalled);
  CHECK(written.total < kWhole);
  CHECK(rig.music().status().framesIn == 0);

  // the items as shairport writes them: "Night Drive" by "The Hum" on "Tapes"
  const std::string items =
      "<item><type>73736e63</type><code>6d647374</code><length>0</length></item>\n"
      "<item><type>636f7265</type><code>6d696e6d</code><length>11</length>\n"
      "<data encoding=\"base64\">\nTmlnaHQgRHJpdmU=</data></item>\n"
      "<item><type>636f7265</type><code>61736172</code><length>7</length>\n"
      "<data encoding=\"base64\">\nVGhlIEh1bQ==</data></item>\n"
      "<item><type>636f7265</type><code>6173616c</code><length>5</length>\n"
      "<data encoding=\"base64\">\nVGFwZXM=</data></item>\n"
      "<item><type>73736e63</type><code>6d64656e</code><length>0</length></item>\n";
  REQUIRE(::write(fd.get(), items.data(), items.size()) == static_cast<ssize_t>(items.size()));
  REQUIRE(eventually([&] { return !rig.sunk().empty(); }));
  CHECK(rig.sunk().back() ==
        MediaMetadata{.title = "Night Drive", .artist = "The Hum", .album = "Tapes", .artworkUrl = {}, .artworkId = {}, .sender = {}});

  // the writer goes: what the reader skipped is counted, nothing it read is logged
  fd.reset();
  REQUIRE(eventually([&] { return rig.logged("\"fifo\":\"metadata\"") >= 1; }));
  const auto logs = rig.logs();
  for (const auto* text : {"Night", "Hum", "Tapes", "TmlnaHQ"})
    CHECK(logs.find(text) == std::string::npos);
  CHECK(logs.find("\"skipped\":1") != std::string::npos);
}

TEST_CASE("a stopped pipe lets go of both fifos") {
  PipeRig rig;
  const auto fd = fifoWriter(rig.audio());
  rig.stop();
  // no reader is left: a write fails at once instead of waiting
  ::signal(SIGPIPE, SIG_IGN);
  const std::int16_t frame[2]{1, 1};
  CHECK(::write(fd.get(), frame, sizeof frame) < 0);
  CHECK(errno == EPIPE);
  const UniqueFd late(::open(rig.metadata().c_str(), O_WRONLY | O_NONBLOCK | O_CLOEXEC));
  CHECK_FALSE(late.valid());
  CHECK(errno == ENXIO);
}

TEST_CASE("a metadata writer that goes in the middle of a sequence leaves nothing of it to the next one") {
  PipeRig rig;
  {
    const auto fd = fifoWriter(rig.metadata());
    // a sequence begins, "Half" as its title
    const std::string half =
        "<item><type>73736e63</type><code>6d647374</code><length>0</length></item>\n"
        "<item><type>636f7265</type><code>6d696e6d</code><length>4</length>\n"
        "<data encoding=\"base64\">\nSGFsZg==</data></item>\n";
    REQUIRE(::write(fd.get(), half.data(), half.size()) == static_cast<ssize_t>(half.size()));
  }
  REQUIRE(eventually([&] { return rig.logged("\"fifo\":\"metadata\"") >= 1; }));
  const auto fd = fifoWriter(rig.metadata());
  // the next writer ends a sequence it never began, then names an artist
  const std::string next =
      "<item><type>73736e63</type><code>6d64656e</code><length>0</length></item>\n"
      "<item><type>636f7265</type><code>61736172</code><length>4</length>\n"
      "<data encoding=\"base64\">\nTmV4dA==</data></item>\n";
  REQUIRE(::write(fd.get(), next.data(), next.size()) == static_cast<ssize_t>(next.size()));
  REQUIRE(eventually([&] { return !rig.sunk().empty(); }));
  CHECK(rig.sunk() == std::vector<MediaMetadata>{{.title = {}, .artist = "Next", .album = {}, .artworkUrl = {}, .artworkId = {}, .sender = {}}});
}

TEST_CASE("a cover is written whole before its id is passed on, and progress goes to its own sink") {
  PipeRig rig;
  const auto fd = fifoWriter(rig.metadata());
  // a jpeg's first bytes, then pixels, base64 as shairport writes it
  const std::string jpeg = std::string("\xFF\xD8\xFF\xE0", 4) + std::string(3000, 'p');
  const std::span bytes(reinterpret_cast<const std::uint8_t*>(jpeg.data()), jpeg.size());
  const std::string items = "<item><type>73736e63</type><code>50494354</code><length>" + std::to_string(jpeg.size()) +
                            "</length>\n<data encoding=\"base64\">\n" + ws::base64(bytes) +
                            "</data></item>\n"
                            "<item><type>73736e63</type><code>70726772</code><length>18</length>\n"
                            "<data encoding=\"base64\">\nMTAwMC80NTEwMC84ODIxMDAw</data></item>\n";
  REQUIRE(::write(fd.get(), items.data(), items.size()) == static_cast<ssize_t>(items.size()));
  REQUIRE(eventually([&] { return !rig.sunk().empty() && !rig.progress().empty(); }));
  const auto shown = rig.sunk().back();
  CHECK(shown.artworkId.size() == 16);
  std::ifstream file(rig.artwork(), std::ios::binary);
  const std::string written((std::istreambuf_iterator<char>(file)), std::istreambuf_iterator<char>());
  CHECK(written == jpeg);
  CHECK(rig.progress().back() == airplay::Progress{.positionMs = 1000, .durationMs = 200000});
  // the log holds the size alone
  CHECK(rig.logged("\"airplay_artwork\"") == 1);
  CHECK(rig.logs().find("pppp") == std::string::npos);
}
