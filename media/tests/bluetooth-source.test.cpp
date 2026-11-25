#include "audiod/bluetooth-receiver.h"
#include "audiod/bluetooth-source.h"
#include "audiod/media-sessions.h"
#include "audiod/media-source.h"
#include "audiod/sys.h"
#include "backend/pcm-input.h"
#include "support/audiod-harness.h"
#include "support/fake-media-source.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <signal.h>
#include <unistd.h>

#include <array>
#include <cerrno>
#include <chrono>
#include <cstdint>
#include <functional>
#include <memory>
#include <string>
#include <utility>
#include <vector>

// the bluetooth receiver as the sessions' source, over a pipe standing in for bluealsa's fifo and a fake loop: the
// test runs the fd's watch and the silence timer itself, on its own thread, which plays the loop's
using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

struct Control {
  std::string id;
  MediaAction action;
};

class Rig {
 public:
  Rig() {
    ::signal(SIGPIPE, SIG_IGN);
    std::array<int, 2> ends{};
    REQUIRE(::pipe2(ends.data(), O_CLOEXEC) == 0);
    read_.reset(ends[0]);
    write_.reset(ends[1]);
    REQUIRE(::fcntl(write_.get(), F_SETFL, O_NONBLOCK) == 0);
    BluetoothReceiverHooks hooks{
        .control = [this](std::string_view id, MediaAction action) { controls.push_back({std::string(id), action}); },
        .watch =
            [this](int fd, std::function<void()> readable) {
              watched = fd;
              readable_ = std::move(readable);
              return true;
            },
        .unwatch =
            [this] {
              watched = -1;
              readable_ = nullptr;
            },
        .now = [this] { return now_; },
    };
    receiver = std::make_shared<BluetoothReceiver>(log_, std::move(hooks));
    sessions = std::make_unique<MediaSessions>(
        log_, music, [this] { return gateOpen; }, refuseUrls,
        [this](const MediaSession& session) { published.push_back(session); }, [] { return std::int64_t{1000}; });
    sessions->attach([this](std::shared_ptr<MediaSourceEvents> events) {
      receiver->events(std::move(events));
      return std::make_unique<BluetoothSource>(receiver);
    });
    receiver->gate(true);
  }
  ~Rig() {
    // as the part goes before the sessions
    receiver->shutdown();
  }
  Rig(const Rig&) = delete;
  Rig& operator=(const Rig&) = delete;

  // the phone's stream, its read end handed over
  void stream(std::string id = "phone1", std::uint32_t rate = 44100) {
    receiver->stream(BluetoothStreamInfo{.id = std::move(id), .rate = rate, .channels = 2, .sender = "Sim Phone"},
                     std::move(read_));
  }
  // frames of s16 stereo written, then read as the loop would
  void pcm(std::size_t frames) {
    const std::vector<std::int16_t> samples(frames * 2, 1000);
    const auto* data = reinterpret_cast<const std::uint8_t*>(samples.data());
    auto left = samples.size() * sizeof(std::int16_t);
    // the pipe may hold less than the write (a small default size): written and read in turns, as bluealsa's would be
    while (left > 0) {
      const auto written = ::write(write_.get(), data, left);
      REQUIRE((written > 0 || errno == EAGAIN));
      if (written > 0) {
        data += written;
        left -= static_cast<std::size_t>(written);
      }
      readable();
    }
  }
  void readable() {
    REQUIRE(readable_);
    auto callback = readable_;
    callback();
  }
  void advance(std::chrono::milliseconds by) {
    now_ += by;
    receiver->tick();
  }
  void hangUp() { write_.reset(); }
  [[nodiscard]] bool readerLeft() const {
    const char byte = 0;
    return ::write(write_.get(), &byte, 1) == 1;
  }

  [[nodiscard]] const MediaSession& session() const { return sessions->session(); }

  // in this order, so the sessions and the receiver go before what they use
  LogCapture logs;
  Log log_ = logs.log();
  bool gateOpen = true;
  PcmInput music;
  std::vector<Control> controls;
  std::vector<MediaSession> published;
  int watched = -1;
  UniqueFd read_;
  UniqueFd write_;
  std::function<void()> readable_;
  std::chrono::steady_clock::time_point now_{std::chrono::seconds(1000)};
  std::shared_ptr<BluetoothReceiver> receiver;
  std::unique_ptr<MediaSessions> sessions;
};

}  // namespace

TEST_CASE("a phone's pcm takes the session and flows into music") {
  Rig rig;
  rig.receiver->configure(true);
  rig.stream();
  CHECK(rig.watched >= 0);
  CHECK(rig.session().id.empty());
  rig.pcm(441);
  REQUIRE_FALSE(rig.session().id.empty());
  CHECK(rig.session().kind == MediaKind::Bluetooth);
  CHECK(rig.session().state == PlayState::Playing);
  CHECK(rig.session().metadata.sender == "Sim Phone");
  rig.pcm(4410);
  // 44.1 kHz resampled to 48: about 4800 frames of the second write (the first may have come before the output)
  CHECK(rig.music.status().framesIn > 4000);
  CHECK(rig.logs.count("Sim Phone") == 0);
}

TEST_CASE("a second of no pcm pauses it, and pcm again plays it") {
  Rig rig;
  rig.receiver->configure(true);
  rig.stream();
  rig.pcm(441);
  REQUIRE(rig.session().state == PlayState::Playing);
  rig.advance(500ms);
  CHECK(rig.session().state == PlayState::Playing);
  rig.advance(600ms);
  CHECK(rig.session().state == PlayState::Paused);
  rig.pcm(441);
  CHECK(rig.session().state == PlayState::Playing);
  CHECK(rig.controls.empty());
}

TEST_CASE("eof ends the stream and its session, its fd closed") {
  Rig rig;
  rig.receiver->configure(true);
  rig.stream();
  rig.pcm(441);
  REQUIRE_FALSE(rig.session().id.empty());
  rig.hangUp();
  rig.readable();
  CHECK(rig.session().id.empty());
  CHECK(rig.watched == -1);
  CHECK_FALSE(rig.receiver->hasStream());
  CHECK(rig.logs.count("\"reason\":\"eof\"") == 1);
}

TEST_CASE("its stream's end closes the fd") {
  Rig rig;
  rig.receiver->configure(true);
  rig.stream();
  rig.pcm(441);
  rig.receiver->streamEnd("other");
  CHECK(rig.receiver->hasStream());
  rig.receiver->streamEnd("phone1");
  CHECK_FALSE(rig.receiver->hasStream());
  CHECK(rig.session().id.empty());
  CHECK_FALSE(rig.readerLeft());
}

TEST_CASE("a displacement pauses the phone and drops its audio") {
  // before the rig, whose sessions keep its source
  FakeMedia fakes;
  Rig rig;
  auto spec = FakeSourceSpec{};
  spec.keepsPaused = true;
  rig.sessions->attach(fakes.longLived("other", spec));
  rig.receiver->configure(true);
  rig.stream();
  rig.pcm(441);
  REQUIRE(rig.session().kind == MediaKind::Bluetooth);
  fakes.source("other")->events().playing();
  CHECK(rig.session().kind == MediaKind::Url);
  REQUIRE(rig.controls.size() == 1);
  CHECK(rig.controls[0].id == "phone1");
  CHECK(rig.controls[0].action == MediaAction::Pause);
  // the phone plays on regardless: dropped, and it does not take the session back
  const auto before = rig.music.status().framesIn;
  rig.pcm(4410);
  CHECK(rig.session().kind == MediaKind::Url);
  CHECK(rig.music.status().framesIn == before);
}

TEST_CASE("the phone played again by hand takes the session back") {
  // before the rig, whose sessions keep its source
  FakeMedia fakes;
  Rig rig;
  auto spec = FakeSourceSpec{};
  spec.keepsPaused = true;
  rig.sessions->attach(fakes.longLived("other", spec));
  rig.receiver->configure(true);
  rig.stream();
  rig.receiver->player({.streamId = "phone1", .playing = true, .metadata = {}, .durationMs = 0, .positionMs = 0});
  rig.pcm(441);
  fakes.source("other")->events().playing();
  REQUIRE(rig.session().kind == MediaKind::Url);
  rig.receiver->player({.streamId = "phone1", .playing = false, .metadata = {}, .durationMs = 0, .positionMs = 0});
  rig.receiver->player({.streamId = "phone1", .playing = true, .metadata = {}, .durationMs = 0, .positionMs = 0});
  rig.pcm(441);
  CHECK(rig.session().kind == MediaKind::Bluetooth);
  CHECK(rig.session().state == PlayState::Playing);
}

TEST_CASE("a shut gate pauses the phone; nothing plays while it is shut") {
  Rig rig;
  rig.receiver->configure(true);
  rig.stream();
  rig.pcm(441);
  REQUIRE(rig.session().state == PlayState::Playing);
  rig.gateOpen = false;
  rig.receiver->gate(false);
  rig.sessions->gateShut();
  CHECK(rig.session().state == PlayState::Paused);
  REQUIRE(rig.controls.size() == 1);
  CHECK(rig.controls[0].action == MediaAction::Pause);
  const auto before = rig.music.status().framesIn;
  rig.pcm(4410);
  CHECK(rig.music.status().framesIn == before);
}

TEST_CASE("a phone that streams while the gate is shut is paused at once") {
  Rig rig;
  rig.gateOpen = false;
  rig.receiver->gate(false);
  rig.receiver->configure(true);
  rig.stream();
  rig.pcm(4410);
  CHECK(rig.music.status().framesIn == 0);
  REQUIRE(rig.controls.size() == 1);
  CHECK(rig.controls[0].action == MediaAction::Pause);
  CHECK(rig.session().id.empty());
}

TEST_CASE("the player's pause pauses it, and its metadata reaches the session") {
  Rig rig;
  rig.receiver->configure(true);
  rig.stream();
  MediaMetadata metadata;
  metadata.title = "Song";
  rig.receiver->player(
      {.streamId = "phone1", .playing = true, .metadata = metadata, .durationMs = 200000, .positionMs = 5000});
  rig.pcm(441);
  REQUIRE(rig.session().state == PlayState::Playing);
  CHECK(rig.session().metadata.title == "Song");
  CHECK(rig.session().metadata.sender == "Sim Phone");
  CHECK(rig.session().durationMs == 200000);
  rig.receiver->player(
      {.streamId = "phone1", .playing = false, .metadata = metadata, .durationMs = 200000, .positionMs = 6000});
  CHECK(rig.session().state == PlayState::Paused);
  CHECK(rig.logs.count("Song") == 0);
}

TEST_CASE("commands go to the phone and answer ok at once") {
  Rig rig;
  rig.receiver->configure(true);
  rig.stream();
  rig.pcm(441);
  CHECK(rig.sessions->command(MediaAction::Next, 0) == kMediaOk);
  CHECK(rig.sessions->command(MediaAction::Pause, 0) == kMediaOk);
  CHECK(rig.sessions->command(MediaAction::Resume, 0) == kMediaOk);
  CHECK(rig.sessions->command(MediaAction::Seek, 10) == kMediaUnsupported);
  REQUIRE(rig.controls.size() == 3);
  CHECK(rig.controls[0].action == MediaAction::Next);
  CHECK(rig.controls[1].action == MediaAction::Pause);
  CHECK(rig.controls[2].action == MediaAction::Resume);
}

TEST_CASE("disabled, a stream is refused and its fd closed") {
  Rig rig;
  rig.stream();
  CHECK_FALSE(rig.receiver->hasStream());
  CHECK(rig.logs.count("\"reason\":\"disabled\"") == 1);
  CHECK_FALSE(rig.readerLeft());
}

TEST_CASE("disabling drops the stream and ends its session") {
  Rig rig;
  rig.receiver->configure(true);
  rig.stream();
  rig.pcm(441);
  REQUIRE_FALSE(rig.session().id.empty());
  rig.receiver->configure(false);
  CHECK(rig.session().id.empty());
  CHECK_FALSE(rig.receiver->hasStream());
  CHECK_FALSE(rig.readerLeft());
}

TEST_CASE("an invalid stream is refused") {
  Rig rig;
  rig.receiver->configure(true);
  rig.stream("bad id!");
  CHECK_FALSE(rig.receiver->hasStream());
  CHECK(rig.logs.count("\"reason\":\"invalid\"") == 1);
}
