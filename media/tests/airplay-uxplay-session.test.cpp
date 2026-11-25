// the session as a fake sender drives it: the calls libairplay makes once a sender set up its streams (rtsp, pairing
// and fairplay are libairplay's, and the receiver test runs them for real), with clocks the test moves
#include "audiod/airplay-uxplay-session.h"
#include "audiod/airplay-volume.h"

#include <catch2/catch_test_macros.hpp>

#include <sys/stat.h>

#include <atomic>
#include <filesystem>
#include <fstream>
#include <functional>
#include <optional>
#include <string>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::audiod::uxplay;
using namespace std::chrono_literals;

namespace {

constexpr std::int64_t kMs = 1'000'000;
// the device's clocks: monotonic at 1000 s, realtime 1.7e9 s on
constexpr std::int64_t kRealOffset = 1'700'000'000LL * 1'000'000'000;

class Scratch {
 public:
  Scratch() {
    static std::atomic<int> counter{0};
    path_ = std::filesystem::temp_directory_path() /
            ("cadence-uxplay-" + std::to_string(::getpid()) + "-" + std::to_string(counter++));
    std::filesystem::create_directories(path_);
  }
  ~Scratch() {
    std::error_code ignored;
    std::filesystem::remove_all(path_, ignored);
  }
  [[nodiscard]] std::string str() const { return path_.string(); }

 private:
  std::filesystem::path path_;
};

struct Recorded : MediaSourceEvents {
  std::vector<std::string> states;
  std::optional<MediaMetadata> meta;
  std::uint64_t durationMs = 0;
  std::uint64_t positionMs = 0;

  void playing() override { states.emplace_back("playing"); }
  void paused() override { states.emplace_back("paused"); }
  void buffering() override { states.emplace_back("buffering"); }
  void ended() override { states.emplace_back("ended"); }
  void failed(std::string_view code) override { states.emplace_back("failed:" + std::string(code)); }
  void metadata(const MediaMetadata& value) override { meta = value; }
  void duration(std::uint64_t ms) override { durationMs = ms; }
  void position(std::uint64_t ms) override { positionMs = ms; }
};

// the session with a loop of the test's own: posts queue until drain runs them, as the loop thread would
class Rig {
 public:
  explicit Rig(SessionSettings settings = {}) {
    SessionHooks hooks{
        .post = [this](std::function<void()> task) { posted_.push_back(std::move(task)); },
        .senderVolume = [this](std::uint8_t level) { levels.push_back(level); },
        .mirrorState = [this](const MirrorReport& report) { mirrors.push_back(report); },
        .pin = [this](const PinReport& report) { pins.push_back(report); },
        .mirrorUnit = [this](const std::string& id, const AccessUnit& unit,
                             std::int64_t pts) { units.push_back({id, unit, pts}); },
        .hls = [this](const HlsRequest& request) { hls.push_back(request); },
        .disconnect = [this] { ++disconnects; },
    };
    SessionClocks clocks{
        .monoNs = [this] { return mono.load(); },
        .realNs = [this] { return mono.load() + kRealOffset; },
        .steady = [this] { return std::chrono::steady_clock::time_point(std::chrono::nanoseconds(mono.load())); },
    };
    session = std::make_unique<Session>(log_, music, std::move(hooks), std::move(settings), std::move(clocks));
    session->events(recorded);
  }

  void drain() {
    while (!posted_.empty()) {
      auto tasks = std::move(posted_);
      posted_.clear();
      for (auto& task : tasks)
        task();
    }
  }

  // an l16 packet of frames, due to be heard at dueNs on the device's monotonic clock
  void sendAudio(std::int64_t dueNs, std::size_t frames, std::int16_t value = 8192, std::int64_t remoteNs = 0) {
    std::vector<std::uint8_t> data;
    for (std::size_t i = 0; i < frames * 2; ++i) {
      data.push_back(static_cast<std::uint8_t>(static_cast<std::uint16_t>(value) >> 8));
      data.push_back(static_cast<std::uint8_t>(value & 0xff));
    }
    session->audio(AudioPacket{.data = data,
                               .codec = AudioCodec::Pcm,
                               .localNs = dueNs + kRealOffset,
                               .remoteNs = remoteNs != 0 ? remoteNs : dueNs,
                               .rtpTime = 0,
                               .seq = 0});
  }

  // an l16 packet on the rtp timeline: rtp time and sequence number as a sender numbers them
  void sendRtp(std::int64_t dueNs, std::size_t frames, std::uint32_t rtp, std::uint16_t seq, std::int16_t value = 8192) {
    std::vector<std::uint8_t> data;
    for (std::size_t i = 0; i < frames * 2; ++i) {
      data.push_back(static_cast<std::uint8_t>(static_cast<std::uint16_t>(value) >> 8));
      data.push_back(static_cast<std::uint8_t>(value & 0xff));
    }
    session->audio(AudioPacket{.data = data,
                               .codec = AudioCodec::Pcm,
                               .localNs = dueNs + kRealOffset,
                               .remoteNs = dueNs,
                               .rtpTime = rtp,
                               .seq = seq});
  }

  std::atomic<std::int64_t> mono{1'000'000 * kMs};
  PcmInput music;
  std::shared_ptr<Recorded> recorded = std::make_shared<Recorded>();
  std::unique_ptr<Session> session;
  std::vector<std::uint8_t> levels;
  std::vector<MirrorReport> mirrors;
  std::vector<PinReport> pins;
  struct Unit {
    std::string id;
    AccessUnit unit;
    std::int64_t pts = 0;
  };
  std::vector<Unit> units;
  std::vector<HlsRequest> hls;
  int disconnects = 0;

 private:
  Log log_{[](std::string_view) {}};
  std::vector<std::function<void()>> posted_;
};

std::vector<float> readAll(PcmInput& music) {
  std::vector<float> out(2 * 48000);
  const auto frames = music.read(out);
  out.resize(frames * 2);
  return out;
}

// the first frame read that carries sound
std::optional<std::size_t> firstSound(const std::vector<float>& stereo) {
  for (std::size_t i = 0; i < stereo.size() / 2; ++i)
    if (std::abs(stereo[2 * i]) > 0.05f)
      return i;
  return std::nullopt;
}

std::vector<std::uint8_t> dmap(std::initializer_list<std::pair<std::string, std::string>> items) {
  std::vector<std::uint8_t> inner;
  for (const auto& [code, value] : items) {
    inner.insert(inner.end(), code.begin(), code.end());
    const auto size = static_cast<std::uint32_t>(value.size());
    for (int shift = 24; shift >= 0; shift -= 8)
      inner.push_back(static_cast<std::uint8_t>(size >> shift));
    inner.insert(inner.end(), value.begin(), value.end());
  }
  std::vector<std::uint8_t> out{'m', 'l', 'i', 't'};
  const auto size = static_cast<std::uint32_t>(inner.size());
  for (int shift = 24; shift >= 0; shift -= 8)
    out.push_back(static_cast<std::uint8_t>(size >> shift));
  out.insert(out.end(), inner.begin(), inner.end());
  return out;
}

}  // namespace

TEST_CASE("the sender's audio reaches the music input placed when the sender asked for it to be heard") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->audioFormat(AudioCodec::Pcm, 352, false, true);
  rig.session->openOutput(rig.music);
  rig.session->outputLatency(50 * kMs);
  const auto now = rig.mono.load();
  // due 120 ms from now: with 50 ms of output latency, 70 ms of silence goes before it
  rig.sendAudio(now + 120 * kMs, 4410);
  rig.drain();
  CHECK(rig.recorded->states == std::vector<std::string>{"playing"});
  const auto heard = readAll(rig.music);
  const auto first = firstSound(heard);
  REQUIRE(first);
  // 70 ms at 48 kHz, give or take the resampler's few frames
  CHECK(*first >= 3360 - 96);
  CHECK(*first <= 3360 + 96);
}

// multiroom (P1): a group leader's retarget opens the output on its feed, whose timeline paces and steers it
TEST_CASE("into a group leader's feed the sender's audio goes as it falls due, unplaced and unsteered") {
  Rig rig;
  PcmInput feed;
  rig.session->connectionOpened();
  rig.session->audioFormat(AudioCodec::Pcm, 352, false, true);
  rig.session->openOutput(feed);
  rig.session->outputLatency(50 * kMs);
  const auto now = rig.mono.load();
  // due 120 ms from now: the music input would get 70 ms of silence before it, the feed none
  rig.sendAudio(now + 120 * kMs, 4410);
  rig.drain();
  CHECK(rig.recorded->states == std::vector<std::string>{"playing"});
  CHECK(rig.music.status().framesIn == 0);
  const auto heard = readAll(feed);
  const auto first = firstSound(heard);
  REQUIRE(first);
  CHECK(*first <= 96);
  CHECK(feed.steering() == 0);

  // not yet due: it waits there too
  rig.sendAudio(rig.mono.load() + 2'000 * kMs, 352);
  const auto in = feed.status().framesIn;
  rig.session->tick();
  CHECK(feed.status().framesIn == in);
  rig.mono += 1'900 * kMs;
  rig.session->tick();
  CHECK(feed.status().framesIn > in);
}

TEST_CASE("a retarget while the sender streams moves its audio to the feed and back, the session playing on") {
  Rig rig;
  PcmInput feed;
  rig.session->connectionOpened();
  rig.session->audioFormat(AudioCodec::Pcm, 352, false, true);
  rig.session->openOutput(rig.music);
  rig.session->outputLatency(50 * kMs);
  rig.sendAudio(rig.mono.load() + 60 * kMs, 441);
  CHECK(rig.music.status().framesIn > 0);

  // as MediaSessions::retarget does
  rig.session->closeOutput();
  rig.session->openOutput(feed);
  CHECK(rig.music.status().queued == 0);
  const auto heard = rig.music.status().framesIn;
  rig.mono += 10 * kMs;
  rig.sendAudio(rig.mono.load() + 60 * kMs, 441);
  CHECK(feed.status().framesIn > 0);
  CHECK(rig.music.status().framesIn == heard);

  // an open on another input while one is open closes that one first: what it held is cleared
  rig.session->openOutput(rig.music);
  CHECK(feed.status().queued == 0);
  const auto fed = feed.status().framesIn;
  rig.mono += 10 * kMs;
  rig.sendAudio(rig.mono.load() + 60 * kMs, 441);
  CHECK(rig.music.status().framesIn > heard);
  CHECK(feed.status().framesIn == fed);
  rig.drain();
  // the retarget is no pause: the sender's session played on
  CHECK(rig.recorded->states == std::vector<std::string>{"playing"});
}

TEST_CASE("audio not yet due waits, and is pushed once it falls due") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->openOutput(rig.music);
  rig.session->outputLatency(50 * kMs);
  const auto now = rig.mono.load();
  // two seconds of lead, as alac senders have
  rig.sendAudio(now + 2'000 * kMs, 352);
  CHECK(rig.music.status().framesIn == 0);
  rig.mono += 1'700 * kMs;
  rig.session->tick();
  CHECK(rig.music.status().framesIn == 0);
  rig.mono += 200 * kMs;
  rig.session->tick();
  CHECK(rig.music.status().framesIn > 0);
}

TEST_CASE("a stream later than the output can play is delayed, and the clock follows where the audio is") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->openOutput(rig.music);
  // a tv's 300 ms against aac-eld's 170 ms lead
  rig.session->outputLatency(300 * kMs);
  rig.session->mirrorRunning(true);
  const auto now = rig.mono.load();
  rig.sendAudio(now + 170 * kMs, 480, 8192, 5'000 * kMs);
  const auto anchor = rig.session->anchor();
  REQUIRE(anchor);
  // the media time 5 s is heard 300 ms from now, not 170
  CHECK(anchor->mediaNs == 5'000 * kMs);
  CHECK(anchor->monoNs == now + 300 * kMs);
}

TEST_CASE("audio the sessions did not give the output to is drained unheard") {
  Rig rig;
  rig.session->connectionOpened();
  const auto now = rig.mono.load();
  rig.sendAudio(now + 20 * kMs, 4410);
  rig.drain();
  // it still reports playing, so it can take the session
  CHECK(rig.recorded->states == std::vector<std::string>{"playing"});
  CHECK(rig.music.status().framesIn == 0);
  rig.session->tick();
  CHECK(rig.music.status().framesIn == 0);
}

TEST_CASE("a hold silences the sender until resumed, and the sender's flush pauses the session") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->openOutput(rig.music);
  rig.sendAudio(rig.mono.load() + 60 * kMs, 441);
  rig.drain();
  rig.session->hold(true);
  rig.drain();
  CHECK(rig.music.status().queued == 0);
  rig.sendAudio(rig.mono.load() + 60 * kMs, 441);
  CHECK(rig.music.status().queued == 0);
  rig.session->hold(false);
  rig.sendAudio(rig.mono.load() + 60 * kMs, 441);
  CHECK(rig.music.status().queued > 0);
  rig.session->audioFlush();
  CHECK(rig.music.status().queued == 0);
  rig.drain();
  CHECK(rig.recorded->states == std::vector<std::string>{"playing", "paused", "playing", "paused"});
  // the next audio plays again
  rig.sendAudio(rig.mono.load() + 60 * kMs, 441);
  rig.drain();
  CHECK(rig.recorded->states.back() == "playing");
}

TEST_CASE("a sender whose rtp stops without a flush is paused after the resend window, then resumes on audio") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->audioFormat(AudioCodec::Pcm, 352, false, true);
  rig.session->openOutput(rig.music);
  rig.sendAudio(rig.mono.load() + 60 * kMs, 441);
  rig.drain();
  CHECK(rig.recorded->states == std::vector<std::string>{"playing"});

  // libairplay can wait about two seconds for a lost packet's resend: that alone is not a pause
  rig.mono += 2'000 * kMs;
  rig.session->tick();
  rig.drain();
  CHECK(rig.recorded->states == std::vector<std::string>{"playing"});
  rig.mono += 600 * kMs;
  rig.session->tick();
  rig.drain();
  CHECK(rig.recorded->states == std::vector<std::string>{"playing", "paused"});
  CHECK(rig.music.status().queued == 0);

  rig.sendAudio(rig.mono.load() + 60 * kMs, 441);
  rig.drain();
  CHECK(rig.recorded->states == std::vector<std::string>{"playing", "paused", "playing"});
}

TEST_CASE("the sender going away ends the session, and closing the output cuts what was queued") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->openOutput(rig.music);
  rig.sendAudio(rig.mono.load() + 60 * kMs, 4410);
  CHECK(rig.music.status().queued > 0);
  rig.session->closeOutput();
  CHECK(rig.music.status().queued == 0);
  rig.session->connectionClosed();
  rig.drain();
  CHECK(rig.recorded->states == std::vector<std::string>{"playing", "ended"});
}

TEST_CASE("the sender's slider moves the speaker only as a deliberate move (rule 7)") {
  Rig rig;
  rig.session->speakerLevel(40);
  rig.session->connectionOpened();
  // the slider starts at the speaker's level
  CHECK(rig.session->clientVolume() == airplay::toDb(40));
  // the app's stored level as it connects is no request
  rig.session->volume(static_cast<float>(airplay::toDb(70)));
  rig.drain();
  CHECK(rig.levels.empty());
  // a move after it is
  rig.session->volume(static_cast<float>(airplay::toDb(55)));
  rig.drain();
  REQUIRE(rig.levels == std::vector<std::uint8_t>{55});
  // the slider's mute is 0
  rig.mono += 300 * kMs;
  rig.session->volume(-144.0f);
  rig.drain();
  CHECK(rig.levels.back() == 0);
  // a value no slider sends never reaches cadenced
  rig.mono += 300 * kMs;
  rig.session->volume(-50.0f);
  rig.drain();
  CHECK(rig.levels.size() == 2);
}

TEST_CASE("slider moves are coalesced, the latest going out after the interval") {
  Rig rig;
  rig.session->speakerLevel(40);
  rig.session->connectionOpened();
  // the speaker's own level is an echo; the first other one the app's stored level
  rig.session->volume(static_cast<float>(airplay::toDb(40)));
  rig.session->volume(static_cast<float>(airplay::toDb(50)));
  rig.session->volume(static_cast<float>(airplay::toDb(60)));
  rig.session->volume(static_cast<float>(airplay::toDb(62)));
  rig.session->volume(static_cast<float>(airplay::toDb(65)));
  rig.drain();
  CHECK(rig.levels == std::vector<std::uint8_t>{60});
  rig.mono += 250 * kMs;
  rig.session->tick();
  rig.drain();
  CHECK(rig.levels == std::vector<std::uint8_t>{60, 65});
}

TEST_CASE("the sender's metadata, progress and cover reach the session, the cover written for cadenced's upload") {
  Scratch scratch;
  Rig rig(SessionSettings{.scratchDir = scratch.str()});
  rig.session->connectionOpened();
  rig.session->metadata(dmap({{"minm", "Title"}, {"asar", "Artist"}, {"asal", "Album"}, {"asgn", "Genre"}}));
  rig.drain();
  REQUIRE(rig.recorded->meta);
  CHECK(rig.recorded->meta->title == "Title");
  CHECK(rig.recorded->meta->artist == "Artist");
  CHECK(rig.recorded->meta->album == "Album");
  CHECK(rig.recorded->meta->artworkId.empty());

  std::vector<std::uint8_t> jpeg{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4, 5, 6};
  rig.session->cover(jpeg);
  rig.drain();
  REQUIRE_FALSE(rig.recorded->meta->artworkId.empty());
  const auto path = scratch.str() + "/run/artwork";
  std::ifstream in(path, std::ios::binary);
  const std::vector<std::uint8_t> written((std::istreambuf_iterator<char>(in)), std::istreambuf_iterator<char>());
  CHECK(written == jpeg);
  struct stat info {};
  REQUIRE(::stat(path.c_str(), &info) == 0);
  CHECK((info.st_mode & 0777) == 0600);
  // not an image: no cover
  rig.session->cover(std::vector<std::uint8_t>{'h', 'i'});
  rig.drain();
  CHECK(rig.recorded->meta->artworkId.empty());

  // 44.1 kHz rtp frames: 10 s into a 200 s track
  rig.session->progress(1000, 1000 + 441000, 1000 + 8820000);
  rig.drain();
  CHECK(rig.recorded->durationMs == 200'000);
  CHECK(rig.recorded->positionMs == 10'000);

  // the sender going clears it
  rig.session->connectionClosed();
  rig.drain();
  CHECK(rig.recorded->meta->title.empty());
}

TEST_CASE("an empty or partial track update keeps the cast's metadata and cover until another track arrives") {
  Scratch scratch;
  Rig rig(SessionSettings{.scratchDir = scratch.str()});
  rig.session->connectionOpened();
  rig.session->metadata(dmap({{"minm", "First"}, {"asar", "Artist"}, {"asal", "Album"}}));
  const std::vector<std::uint8_t> jpeg{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3};
  rig.session->cover(jpeg);
  rig.drain();
  REQUIRE(rig.recorded->meta);
  const auto firstArt = rig.recorded->meta->artworkId;
  REQUIRE_FALSE(firstArt.empty());

  // a phone can follow the cover with an empty or partial dmap for the same stream: neither is a new track
  rig.session->metadata(dmap({{"minm", ""}, {"asar", ""}, {"asal", ""}}));
  rig.drain();
  CHECK(rig.recorded->meta->title == "First");
  CHECK(rig.recorded->meta->artist == "Artist");
  CHECK(rig.recorded->meta->album == "Album");
  CHECK(rig.recorded->meta->artworkId == firstArt);
  rig.session->metadata(dmap({{"minm", "First"}}));
  rig.drain();
  CHECK(rig.recorded->meta->artist == "Artist");
  CHECK(rig.recorded->meta->album == "Album");
  CHECK(rig.recorded->meta->artworkId == firstArt);

  // a genuinely different title starts cleanly: no artist, album or cover leaks from the song before
  rig.session->metadata(dmap({{"minm", "Next"}, {"asar", "Other"}}));
  rig.drain();
  CHECK(rig.recorded->meta->title == "Next");
  CHECK(rig.recorded->meta->artist == "Other");
  CHECK(rig.recorded->meta->album.empty());
  CHECK(rig.recorded->meta->artworkId.empty());
  const std::vector<std::uint8_t> png{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 4, 5};
  rig.session->cover(png);
  rig.drain();
  CHECK(rig.recorded->meta->title == "Next");
  CHECK_FALSE(rig.recorded->meta->artworkId.empty());
  CHECK(rig.recorded->meta->artworkId != firstArt);
}

TEST_CASE("the sender's name rides on the metadata from the stream's start, and goes with the sender") {
  Rig rig;
  rig.session->connectionOpened();
  bool admit = false;
  rig.session->client("dev-1", "iPhone15,2", "iPhone Маши", admit);
  REQUIRE(admit);
  rig.session->audioFormat(AudioCodec::Pcm, 352, false, true);
  rig.session->openOutput(rig.music);
  // no dmap yet: the stream's start names the sender alone
  rig.sendAudio(rig.mono.load() + 120 * kMs, 4410);
  rig.drain();
  REQUIRE(rig.recorded->meta);
  CHECK(rig.recorded->meta->sender == "iPhone Маши");
  CHECK(rig.recorded->meta->title.empty());

  rig.session->metadata(dmap({{"minm", "worms"}, {"asar", "guardin"}}));
  rig.drain();
  CHECK(rig.recorded->meta->title == "worms");
  CHECK(rig.recorded->meta->sender == "iPhone Маши");

  rig.session->connectionClosed();
  rig.drain();
  CHECK(rig.recorded->meta->title.empty());
  CHECK(rig.recorded->meta->sender.empty());
}

TEST_CASE("a malformed dmap changes nothing") {
  Rig rig;
  rig.session->metadata(std::vector<std::uint8_t>{'m', 'l', 'i', 't', 0, 0, 0, 99, 'x'});
  rig.drain();
  CHECK_FALSE(rig.recorded->meta);
}

TEST_CASE("mirroring is reported with its session id, its units go to the screen with the sender's pts") {
  Rig rig(SessionSettings{.scratchDir = {}, .requirePin = false, .mirroring = true, .h265 = false});
  rig.session->connectionOpened();
  bool admit = false;
  rig.session->client("AA:BB", "iPhone15,2", "iPhone", admit);
  CHECK(admit);
  CHECK(rig.session->videoCodec(false));
  CHECK_FALSE(rig.session->videoCodec(true));
  rig.session->videoSize(1170, 2532);
  rig.session->mirrorRunning(true);
  rig.drain();
  REQUIRE(rig.mirrors.size() == 1);
  CHECK(rig.mirrors[0].active);
  CHECK(rig.mirrors[0].sessionId == "airplay-1");
  CHECK(rig.mirrors[0].clientName == "iPhone");
  CHECK(rig.mirrors[0].width == 1170);
  CHECK_FALSE(rig.mirrors[0].h265);
  CHECK(rig.session->mirroring());
  // the sessions see it as screencast: a state change makes them read it again
  CHECK(rig.recorded->states == std::vector<std::string>{"buffering", "playing"});

  const std::vector<std::uint8_t> idr{0, 0, 0, 1, 0x67, 1, 0, 0, 0, 1, 0x68, 2, 0, 0, 0, 1, 0x65, 3};
  const auto now = rig.mono.load();
  rig.session->video(VideoPacket{.data = idr, .h265 = false, .nalCount = 3,
                                 .localNs = now + 80 * kMs + kRealOffset, .remoteNs = 77 * kMs});
  rig.drain();
  REQUIRE(rig.units.size() == 1);
  CHECK(rig.units[0].id == "airplay-1");
  CHECK(rig.units[0].pts == 77 * kMs);
  CHECK(rig.units[0].unit.keyframe);
  // no audio: the clock is the video's own
  const auto anchor = rig.session->anchor();
  REQUIRE(anchor);
  CHECK(anchor->mediaNs == 77 * kMs);
  CHECK(anchor->monoNs == now + 80 * kMs);

  rig.session->mirrorRunning(false);
  rig.drain();
  REQUIRE(rig.mirrors.size() == 2);
  CHECK_FALSE(rig.mirrors[1].active);
  CHECK(rig.mirrors[1].reason == "sender");
  CHECK_FALSE(rig.session->anchor());
}

TEST_CASE("with audio flowing the clock is anchored where the audio is heard") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->openOutput(rig.music);
  rig.session->outputLatency(40 * kMs);
  rig.session->mirrorRunning(true);
  const auto now = rig.mono.load();
  rig.sendAudio(now + 100 * kMs, 480, 8192, 9'000 * kMs);
  rig.session->video(VideoPacket{.data = {}, .h265 = false, .nalCount = 0,
                                 .localNs = now + 500 * kMs + kRealOffset, .remoteNs = 1});
  const auto anchor = rig.session->anchor();
  REQUIRE(anchor);
  CHECK(anchor->mediaNs == 9'000 * kMs);
  CHECK(anchor->monoNs == now + 100 * kMs);
  // a second without audio and the video's own takes over
  rig.mono += 1'500 * kMs;
  CHECK(rig.session->anchor()->mediaNs == 1);
}

TEST_CASE("displaced or stopped, the sender is let go and the screen told why") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->mirrorRunning(true);
  rig.session->release("displaced");
  rig.drain();
  CHECK(rig.disconnects == 1);
  REQUIRE(rig.mirrors.size() == 2);
  CHECK_FALSE(rig.mirrors[1].active);
  CHECK(rig.mirrors[1].reason == "displaced");
  // units after it are not forwarded
  rig.session->video(VideoPacket{.data = {}, .h265 = false, .nalCount = 0, .localNs = 0, .remoteNs = 0});
  rig.drain();
  CHECK(rig.units.empty());
}

// an apple sender whose connection drops asks again at once; let go, it is refused, so it ends its cast on its side
TEST_CASE("a sender let go is refused for 10 s, then admitted again with a new mirror session") {
  Rig rig(SessionSettings{.scratchDir = {}, .requirePin = false, .mirroring = true, .h265 = false});
  rig.session->connectionOpened();
  bool admit = false;
  rig.session->client("AA:BB", "iPad13,1", "iPad", admit);
  REQUIRE(admit);
  rig.session->mirrorRunning(true);
  rig.session->release("stopped");
  rig.drain();
  CHECK(rig.disconnects == 1);
  CHECK_FALSE(rig.session->mirroring());
  REQUIRE(rig.mirrors.size() == 2);
  CHECK(rig.mirrors[1].reason == "stopped");

  // its reconnect comes before the old connection is gone: refused, another sender is not
  rig.session->connectionOpened();
  rig.session->client("AA:BB", "iPad13,1", "iPad", admit);
  CHECK_FALSE(admit);
  rig.session->client("CC:DD", "iPhone15,2", "iPhone", admit);
  CHECK(admit);
  rig.session->connectionClosed();
  rig.mono += 10'000 * kMs;
  rig.session->client("AA:BB", "iPad13,1", "iPad", admit);
  CHECK(admit);
  // its next mirror is reported, though the first connection never closed
  rig.session->mirrorRunning(true);
  rig.drain();
  REQUIRE(rig.mirrors.size() == 3);
  CHECK(rig.mirrors[2].active);
  CHECK(rig.mirrors[2].sessionId == "airplay-2");
}

TEST_CASE("a pin is shown when a sender asks, hidden once it paired, and the sender known from then on") {
  Scratch scratch;
  Rig rig(SessionSettings{.scratchDir = scratch.str(), .requirePin = true});
  const std::string key = std::string(43, 'k') + "=";
  rig.session->connectionOpened();
  CHECK_FALSE(rig.session->known(key));
  rig.session->pin("0427");
  rig.drain();
  REQUIRE(rig.pins.size() == 1);
  CHECK(rig.pins[0].active);
  CHECK(rig.pins[0].pin == "0427");
  rig.session->registered("AA:BB", key);
  rig.drain();
  REQUIRE(rig.pins.size() == 2);
  CHECK_FALSE(rig.pins[1].active);
  CHECK(rig.pins[1].pin.empty());
  CHECK(rig.session->known(key));
  // a new session over the same scratch knows it too
  Rig again(SessionSettings{.scratchDir = scratch.str(), .requirePin = true});
  CHECK(again.session->known(key));
  // without a pin required every sender is known
  Rig open;
  CHECK(open.session->known("anything"));
}

TEST_CASE("a pin nobody entered goes after a minute, and with the sender") {
  Rig rig(SessionSettings{.scratchDir = {}, .requirePin = true});
  rig.session->connectionOpened();
  rig.session->pin("1234");
  rig.mono += 59'000 * kMs;
  rig.session->tick();
  rig.drain();
  CHECK(rig.pins.size() == 1);
  rig.mono += 2'000 * kMs;
  rig.session->tick();
  rig.drain();
  REQUIRE(rig.pins.size() == 2);
  CHECK_FALSE(rig.pins[1].active);
  rig.session->pin("5678");
  rig.session->connectionClosed();
  rig.drain();
  CHECK(rig.pins.size() == 4);
  CHECK_FALSE(rig.pins.back().active);
  // not 4 digits: never shown
  rig.session->pin("12");
  rig.drain();
  CHECK(rig.pins.size() == 4);
}

TEST_CASE("an hls cast reaches the hooks with its url") {
  Rig rig;
  rig.session->hlsPlay("http://localhost:7100/master.m3u8", 12.5f);
  rig.session->hlsStop();
  rig.drain();
  REQUIRE(rig.hls.size() == 2);
  CHECK(rig.hls[0].op == HlsRequest::Op::Play);
  CHECK(rig.hls[0].url == "http://localhost:7100/master.m3u8");
  CHECK(rig.hls[0].value == 12.5f);
  CHECK(rig.hls[1].op == HlsRequest::Op::Stop);
}

TEST_CASE("a lost packet's gap is filled with silence, so the audio after it stays on the timeline") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->audioFormat(AudioCodec::Pcm, 352, false, true);
  rig.session->openOutput(rig.music);
  rig.session->outputLatency(50 * kMs);
  const auto now = rig.mono.load();
  const auto packetNs = 352LL * 1'000'000'000 / 44100;
  // ten packets, the fifth lost: libairplay hands over the sixth with the rtp time it has
  for (std::uint16_t i = 0; i < 10; ++i) {
    if (i == 4)
      continue;
    rig.sendRtp(now + 60 * kMs + i * packetNs, 352, 1000U + i * 352U, static_cast<std::uint16_t>(100 + i));
  }
  rig.session->tick();
  const auto stats = rig.session->takeStats();
  CHECK(stats.packets == 9);
  CHECK(stats.lost == 1);
  CHECK(stats.concealedFrames == 352);
  CHECK(stats.hardCuts == 0);
  // the start's 10 ms alone
  CHECK(stats.silenceFrames <= 441);
  // the whole timeline was queued: 10 packets' worth after the start's silence, the gap silent
  const auto heard = readAll(rig.music);
  const auto first = firstSound(heard);
  REQUIRE(first);
  const auto at48 = [](std::size_t frames44) { return frames44 * 48000 / 44100; };
  // well inside the lost packet, nothing sounds; well inside the next, it does
  const auto lostMid = *first + at48(4 * 352 + 176);
  const auto nextMid = *first + at48(5 * 352 + 176);
  REQUIRE(heard.size() / 2 > nextMid);
  CHECK(std::abs(heard[2 * lostMid]) < 0.01f);
  CHECK(std::abs(heard[2 * nextMid]) > 0.2f);
  // the window starts again
  CHECK(rig.session->takeStats().packets == 0);
}

TEST_CASE("a packet sent again after its successor's start is trimmed to what follows on") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->openOutput(rig.music);
  rig.session->outputLatency(50 * kMs);
  const auto now = rig.mono.load();
  rig.sendRtp(now + 100 * kMs, 352, 1000, 1);
  // starts 100 frames before the first one's end
  rig.sendRtp(now + 100 * kMs + 252LL * 1'000'000'000 / 44100, 352, 1252, 2);
  CHECK(rig.session->takeStats().overlapFrames == 100);
}

// the stick's output, modelled: aaudio's callback takes a 2050-frame burst every 42.7 ms of a clock 200 ppm fast,
// and the player refills its ring to three bursts in 960-frame periods, each read from the music input and told to
// the session as the playhead. the latency a period samples swings by a burst over that cycle; the session's heard
// estimate must not, or it cuts the audio it thinks is late or early
TEST_CASE("a bursty output with a drifting clock is followed by steering alone, never cut") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->audioFormat(AudioCodec::Pcm, 352, false, true);
  rig.session->openOutput(rig.music);
  constexpr double kDrift = 200e-6;
  constexpr double kDeviceRate = 48000.0 * (1.0 + kDrift);
  constexpr std::size_t kBurst = 2050;
  constexpr std::size_t kPeriod = 960;
  constexpr double kHeldFrames = 2000;  // what aaudio itself holds past the ring
  const auto start = rig.mono.load();
  const auto packetNs = 352.0 * 1e9 / 44100.0;
  double ring = 0;
  double nextCallbackNs = static_cast<double>(start);
  double nextPacketNs = static_cast<double>(start);
  std::uint64_t mixed = 0;
  std::uint32_t rtp = 0;
  std::uint16_t seq = 0;
  std::size_t underruns = 0;
  std::vector<float> period(kPeriod * 2);
  AudioStats settled{};
  for (std::int64_t t = start; t < start + 120'000 * kMs; t += kMs) {
    rig.mono = t;
    while (nextPacketNs <= static_cast<double>(t)) {
      // the sender asks for each packet to be heard 1.5 s after it sends it
      rig.sendRtp(static_cast<std::int64_t>(nextPacketNs) + 1'500 * kMs, 352, rtp, seq);
      rtp += 352;
      ++seq;
      nextPacketNs += packetNs;
    }
    if (static_cast<double>(t) >= nextCallbackNs) {
      ring = std::max(0.0, ring - kBurst);
      nextCallbackNs += kBurst / kDeviceRate * 1e9;
    }
    while (ring + kPeriod <= 3 * kBurst + kPeriod && ring < 3 * kBurst) {
      // when this period's first frame is heard, as the player computes it at the nominal rate
      const auto heardNs = t + static_cast<std::int64_t>((ring + kHeldFrames) * 1e9 / 48000.0);
      const auto read = rig.music.read(period);
      if (t > start + 2'000 * kMs && read < kPeriod)
        ++underruns;
      rig.session->playhead(Playhead{mixed, heardNs, heardNs - t, kPeriod, rig.music.readPosition()});
      mixed += kPeriod;
      ring += kPeriod;
    }
    if ((t - start) % (10 * kMs) == 0)
      rig.session->tick();
    if (t == start + 20'000 * kMs)
      rig.session->takeStats();
  }
  settled = rig.session->takeStats();
  CHECK(underruns == 0);
  CHECK(settled.hardCuts == 0);
  CHECK(settled.shifts == 0);
  CHECK(settled.silenceFrames == 0);
  CHECK(settled.droppedFrames == 0);
  // it plays as much slower as the output runs fast, within the error the gain leaves
  CHECK(settled.steerPpm <= -150);
  CHECK(settled.steerPpm >= -250);
  CHECK(std::abs(settled.errorNs) < 5 * kMs);
  CHECK(settled.leadMinNs > 0);
  CHECK(settled.late == 0);
}

TEST_CASE("a pushed frame is heard where the playhead's period and the input's positions put it, not its latency") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->openOutput(rig.music);
  const auto now = rig.mono.load();
  // the period read nothing yet and is heard 100 ms from now; its latency (sampled whenever it ran) says nothing
  rig.session->playhead(Playhead{0, now + 100 * kMs, 999 * kMs, 960, rig.music.status().readPos});
  // due 200 ms from now: after the 20 ms period, 80 ms of silence
  rig.sendAudio(now + 200 * kMs, 4410);
  const auto first = firstSound(readAll(rig.music));
  REQUIRE(first);
  CHECK(*first >= 3840 - 96);
  CHECK(*first <= 3840 + 96);
}

TEST_CASE("a playhead from before the session's clear is not used: the frames it counts were dropped") {
  Rig rig;
  rig.session->connectionOpened();
  rig.session->openOutput(rig.music);
  rig.session->outputLatency(50 * kMs);
  const auto now = rig.mono.load();
  rig.sendAudio(now + 60 * kMs, 4410);
  // the playhead the loop had, then the sender's flush
  rig.session->playhead(Playhead{0, now + 2'000 * kMs, 50 * kMs, 960, 0});
  rig.session->audioFlush();
  readAll(rig.music);
  rig.sendAudio(now + 120 * kMs, 4410);
  const auto first = firstSound(readAll(rig.music));
  REQUIRE(first);
  // by the latency: 70 ms of silence, not the stale playhead's 2 s
  CHECK(*first >= 3360 - 96);
  CHECK(*first <= 3360 + 96);
}
