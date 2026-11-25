// the airplay mirror presenter's rules (stream j): config frames first, keyframes after a config, the presentation
// clock's release times, late frames dropped, MirrorStop's end; and the presenter's thread over a fake decoder
#include "player/mirror-presenter.h"
#include "player/mirror-schedule.h"

#include <catch2/catch_test_macros.hpp>

#include <atomic>
#include <chrono>
#include <map>
#include <mutex>
#include <thread>

using namespace cadence::screen::player;
using Kind = MirrorSchedule::Release::Kind;
using Input = MirrorSchedule::Input;

namespace {

const std::vector<std::uint8_t> kH264Config = {0, 0, 0, 1, 0x67, 0x64, 0, 0x28, 0, 0, 0, 1, 0x68, 0xee};
const std::vector<std::uint8_t> kH265Config = {0, 0, 0, 1, 0x40, 0x01, 0x0c, 0, 0, 0, 1, 0x42, 0x01};
const std::vector<std::uint8_t> kIdr = {0, 0, 0, 1, 0x65, 0x88, 0x84};
const std::vector<std::uint8_t> kP = {0, 0, 0, 1, 0x41, 0x9a};

constexpr std::int64_t ms = 1'000'000;

template <class F>
bool eventually(F f, int timeoutMs = 3000) {
  const auto until = std::chrono::steady_clock::now() + std::chrono::milliseconds(timeoutMs);
  while (std::chrono::steady_clock::now() < until) {
    if (f()) return true;
    std::this_thread::sleep_for(std::chrono::milliseconds(2));
  }
  return false;
}

}  // namespace

TEST_CASE("mirror: nothing decodes before a config, nor after one before a keyframe") {
  MirrorSchedule s;
  CHECK(s.frame("m-1", false, true, kIdr) == Input::Skip);
  CHECK(s.frame("m-1", false, false, kP) == Input::Skip);
  CHECK(s.frame("m-1", true, false, kH264Config) == Input::Configure);
  CHECK(s.frame("m-1", false, false, kP) == Input::Skip);
  CHECK(s.frame("m-1", false, true, kIdr) == Input::Decode);
  CHECK(s.frame("m-1", false, false, kP) == Input::Decode);
  // the sender repeats its config before keyframes: the same bytes change nothing
  CHECK(s.frame("m-1", true, false, kH264Config) == Input::Skip);
  CHECK(s.frame("m-1", false, false, kP) == Input::Decode);
  // a rotation or a new size: other bytes make the decoder again, and wait for its keyframe
  CHECK(s.frame("m-1", true, false, kH265Config) == Input::Configure);
  CHECK(s.frame("m-1", false, false, kP) == Input::Skip);
  CHECK(s.frame("m-1", false, true, kIdr) == Input::Decode);
  // frames were lost to an overflow: the next keyframe again
  s.lost();
  CHECK(s.frame("m-1", false, false, kP) == Input::Skip);
  CHECK(s.frame("m-1", false, true, kIdr) == Input::Decode);
  // an empty config or session never reaches a decoder
  CHECK(s.frame("m-1", true, false, {}) == Input::Skip);
  CHECK(s.frame("", true, false, kH264Config) == Input::Skip);
}

TEST_CASE("mirror: outputs are released on the presentation clock, late ones dropped") {
  MirrorSchedule s;
  // no clock yet: shown at once
  CHECK(s.output(5 * ms, 1000 * ms).kind == Kind::RenderNow);
  // media 1 s sounds at mono 10 s (the output latency included)
  s.clock(1000 * ms, 10'000 * ms);
  REQUIRE(s.anchored());
  // pts 1.040 s is due at mono 10.040 s
  auto r = s.output(1040 * ms, 10'000 * ms);
  CHECK(r.kind == Kind::Render);
  CHECK(r.atNs == 10'040 * ms);
  // far ahead of its time: held, the decoder keeps it
  CHECK(s.output(1200 * ms, 10'000 * ms).kind == Kind::Hold);
  CHECK(s.output(1000 * ms + MirrorSchedule::kLeadNs + 1, 10'000 * ms).kind == Kind::Hold);
  // a little late is still shown, now
  r = s.output(995 * ms, 10'000 * ms);
  CHECK(r.kind == Kind::Render);
  CHECK(r.atNs == 10'000 * ms);
  // later than that: dropped
  CHECK(s.output(1000 * ms - MirrorSchedule::kLateNs - 1, 10'000 * ms).kind == Kind::Drop);
  CHECK(s.output(500 * ms, 10'000 * ms).kind == Kind::Drop);
}

TEST_CASE("mirror: MirrorStop ends a session until MirrorStart names it again; a new session needs its config") {
  MirrorSchedule s;
  REQUIRE(s.frame("m-1", true, false, kH264Config) == Input::Configure);
  REQUIRE(s.frame("m-1", false, true, kIdr) == Input::Decode);
  CHECK(s.active());
  // another session's stop leaves this one
  CHECK_FALSE(s.stop("m-0"));
  CHECK(s.frame("m-1", false, false, kP) == Input::Decode);
  CHECK(s.stop("m-1"));
  CHECK_FALSE(s.active());
  // frames still in flight on the tap after the stop
  CHECK(s.frame("m-1", false, false, kP) == Input::Skip);
  CHECK(s.frame("m-1", true, false, kH264Config) == Input::Skip);
  // a sender that reconnected: its new session's frames start it, config first
  CHECK(s.frame("m-2", false, true, kIdr) == Input::Skip);
  CHECK(s.frame("m-2", true, false, kH264Config) == Input::Configure);
  CHECK(s.frame("m-2", false, true, kIdr) == Input::Decode);
  // its frames before MirrorStart were shown; MirrorStart of an ended one opens it again
  s.start("m-1");
  CHECK(s.frame("m-1", true, false, kH264Config) == Input::Configure);
  // an empty stop ends whatever is shown
  CHECK(s.stop(""));
  CHECK(s.frame("m-1", false, true, kIdr) == Input::Skip);
}

TEST_CASE("mirror: the codec from the config's first nal") {
  CHECK(codecOfConfig(kH264Config) == MirrorCodec::H264);
  CHECK(codecOfConfig(kH265Config) == MirrorCodec::H265);
  const std::vector<std::uint8_t> threeByte = {0, 0, 1, 0x27, 0x42};
  CHECK(codecOfConfig(threeByte) == MirrorCodec::H264);
  CHECK(codecOfConfig(kIdr) == MirrorCodec::Unknown);
  CHECK(codecOfConfig({}) == MirrorCodec::Unknown);
}

namespace {

// a decoder that "decodes" each queued unit into one output at once, with a configurable number of free inputs
struct FakeDecoder final : MirrorDecoder {
  struct Shared {
    std::mutex mu;
    std::vector<std::string> calls;
    std::vector<Output> ready;
    std::vector<std::pair<std::int64_t, std::int64_t>> rendered;  // pts, at
    std::vector<std::int64_t> dropped;
    std::map<std::int64_t, std::int64_t> ptsOf;
    MirrorCodec codec = MirrorCodec::Unknown;
    int width = 0;
    bool refuse = false;
    bool noCodec = false;
    int freeInputs = 1000;
    std::vector<std::int64_t> queuedPts;
    std::int64_t next = 1;
  };
  std::shared_ptr<Shared> s = std::make_shared<Shared>();

  bool configure(MirrorCodec c, int width, int, std::span<const std::uint8_t>) override {
    std::lock_guard lock(s->mu);
    s->calls.push_back("configure");
    s->codec = c;
    s->width = width;
    return !s->refuse;
  }
  Queued queue(std::span<const std::uint8_t>, std::int64_t ptsNs, bool keyframe) override {
    std::lock_guard lock(s->mu);
    if (s->noCodec) return Queued::Dropped;
    if (s->freeInputs == 0) return Queued::Full;
    s->freeInputs--;
    s->calls.push_back(keyframe ? "key" : "unit");
    s->queuedPts.push_back(ptsNs);
    const auto h = s->next++;
    // the outputs carry the decoder's pts; the test's records are the sender's, put back by the presenter
    s->ptsOf[h] = ptsNs;
    s->ready.push_back(Output{h, ptsNs});
    return Queued::Taken;
  }
  void outputs(std::vector<Output>& out, std::int64_t) override {
    {
      std::lock_guard lock(s->mu);
      out.insert(out.end(), s->ready.begin(), s->ready.end());
      s->ready.clear();
    }
    std::this_thread::sleep_for(std::chrono::milliseconds(1));
  }
  void release(std::int64_t handle, bool render, std::int64_t atNs) override {
    std::lock_guard lock(s->mu);
    if (render)
      s->rendered.emplace_back(s->ptsOf[handle], atNs);
    else
      s->dropped.push_back(s->ptsOf[handle]);
  }
  void close() override {
    std::lock_guard lock(s->mu);
    s->calls.push_back("close");
  }
};

}  // namespace

TEST_CASE("mirror presenter: frames reach the decoder in order and are shown at their time") {
  auto decoder = std::make_unique<FakeDecoder>();
  auto shared = decoder->s;
  std::atomic<std::int64_t> now{10'000 * ms};
  MirrorPresenter p(std::move(decoder), [&] { return now.load(); });
  p.clock(1000 * ms, 10'000 * ms);
  p.start("m-1", 2, 1280, 720);
  p.frame("m-1", 0, false, false, kP);
  p.frame("m-1", 0, true, false, kH264Config);
  p.frame("m-1", 1000 * ms, false, true, kIdr);
  p.frame("m-1", 1020 * ms, false, false, kP);
  // 300 ms ahead: held until its time
  p.frame("m-1", 1300 * ms, false, false, kP);
  // late: dropped
  p.frame("m-1", 900 * ms, false, false, kP);
  REQUIRE(eventually([&] {
    std::lock_guard lock(shared->mu);
    return shared->rendered.size() == 2 && shared->dropped.size() == 1;
  }));
  {
    std::lock_guard lock(shared->mu);
    // MirrorStart's codec wins over the config's nal (h265 here), and its size is used
    CHECK(shared->codec == MirrorCodec::H265);
    CHECK(shared->width == 1280);
    REQUIRE(shared->calls.size() >= 3);
    CHECK(shared->calls[0] == "configure");
    CHECK(shared->calls[1] == "key");
    CHECK(shared->rendered[0] == std::pair<std::int64_t, std::int64_t>{1000 * ms, 10'000 * ms});
    CHECK(shared->rendered[1] == std::pair<std::int64_t, std::int64_t>{1020 * ms, 10'020 * ms});
    CHECK(shared->dropped[0] == 900 * ms);
  }
  now = 10'280 * ms;
  REQUIRE(eventually([&] {
    std::lock_guard lock(shared->mu);
    return shared->rendered.size() == 3;
  }));
  CHECK(p.active());
  p.stop("m-1");
  CHECK_FALSE(p.active());
  REQUIRE(eventually([&] {
    std::lock_guard lock(shared->mu);
    return !shared->calls.empty() && shared->calls.back() == "close";
  }));
  const auto c = p.counts();
  CHECK(c.frames == 6);
  CHECK(c.configs == 1);
  CHECK(c.rendered == 3);
  CHECK(c.late == 1);
  CHECK(c.skipped == 1);
}

TEST_CASE("mirror presenter: a full decoder keeps the next frame; a stop drops what is queued and held") {
  auto decoder = std::make_unique<FakeDecoder>();
  auto shared = decoder->s;
  shared->freeInputs = 1;
  std::atomic<std::int64_t> now{10'000 * ms};
  MirrorPresenter p(std::move(decoder), [&] { return now.load(); });
  p.clock(1000 * ms, 10'000 * ms);
  p.frame("m-1", 0, true, false, kH264Config);
  // held: due in 500 ms
  p.frame("m-1", 1500 * ms, false, true, kIdr);
  p.frame("m-1", 1520 * ms, false, false, kP);
  REQUIRE(eventually([&] { return p.counts().decoded == 1; }));
  std::this_thread::sleep_for(std::chrono::milliseconds(20));
  CHECK(p.counts().decoded == 1);
  {
    std::lock_guard lock(shared->mu);
    shared->freeInputs = 1;
  }
  REQUIRE(eventually([&] { return p.counts().decoded == 2; }));
  p.stop("");
  REQUIRE(eventually([&] {
    std::lock_guard lock(shared->mu);
    return shared->dropped.size() == 2 && shared->calls.back() == "close";
  }));
  std::lock_guard lock(shared->mu);
  CHECK(shared->rendered.empty());
}

TEST_CASE("mirror presenter: a refused codec skips the session's frames; an overflow waits for a keyframe") {
  auto decoder = std::make_unique<FakeDecoder>();
  auto shared = decoder->s;
  shared->refuse = true;
  MirrorPresenter p(std::move(decoder), [] { return std::int64_t{0}; });
  p.frame("m-1", 0, true, false, kH264Config);
  p.frame("m-1", 0, false, true, kIdr);
  REQUIRE(eventually([&] { return p.counts().refused == 1; }));
  std::this_thread::sleep_for(std::chrono::milliseconds(20));
  {
    std::lock_guard lock(shared->mu);
    CHECK(shared->calls == std::vector<std::string>{"configure"});
  }
  // an unknown codec is refused without a decoder
  p.frame("m-2", 0, true, false, kIdr);
  REQUIRE(eventually([&] { return p.counts().refused == 2; }));
  CHECK(p.counts().configs == 0);
}

TEST_CASE("mirror presenter: the queue is bounded, keeping configs and keyframes") {
  // a decoder that never takes input: everything queues
  auto decoder = std::make_unique<FakeDecoder>();
  decoder->s->freeInputs = 0;
  MirrorPresenter p(std::move(decoder), [] { return std::int64_t{0}; });
  p.frame("m-1", 0, true, false, kH264Config);
  for (std::size_t i = 0; i < MirrorPresenter::kMaxQueued + 10; i++) p.frame("m-1", 0, false, i == 0, kP);
  CHECK(p.counts().overflows >= 1);
  CHECK(p.counts().frames == MirrorPresenter::kMaxQueued + 11);
}

TEST_CASE("mirror: a clock that cannot be the video's shows frames at once rather than never (rule 6)") {
  MirrorSchedule s;
  s.clock(1000 * ms, 10'000 * ms);
  // more than a second ahead: no sender's latency is that long, and held it would starve the decoder
  auto r = s.output(1000 * ms + MirrorSchedule::kMaxAheadNs + 1, 10'000 * ms);
  CHECK(r.kind == Kind::RenderNow);
  CHECK(r.unsynced);
  // a second ahead at most is still the clock's
  CHECK(s.output(1000 * ms + MirrorSchedule::kMaxAheadNs, 10'000 * ms).kind == Kind::Hold);
  // a clock behind the video: a run of late frames is dropped, then they are shown as they come
  for (int i = 0; i < MirrorSchedule::kMaxLateRun; i++) CHECK(s.output(500 * ms, 10'000 * ms).kind == Kind::Drop);
  r = s.output(500 * ms, 10'000 * ms);
  CHECK(r.kind == Kind::RenderNow);
  CHECK(r.unsynced);
  CHECK(s.output(500 * ms, 10'000 * ms).kind == Kind::RenderNow);
  // one on time ends the run: late ones are dropped again
  CHECK(s.output(1000 * ms, 10'000 * ms).kind == Kind::Render);
  CHECK(s.output(500 * ms, 10'000 * ms).kind == Kind::Drop);
}

TEST_CASE("mirror presenter: the decoder sees small pts from the session's start, the clock the sender's") {
  // regression (the stick: 13371 frames, rendered=0): a sender's ntp-epoch pts went to MediaCodec as is
  auto decoder = std::make_unique<FakeDecoder>();
  auto shared = decoder->s;
  std::atomic<std::int64_t> now{10'000 * ms};
  MirrorPresenter p(std::move(decoder), [&] { return now.load(); });
  constexpr std::int64_t ntp = 1'763'000'000'000'000'000;
  p.clock(ntp, 10'000 * ms);
  p.frame("m-1", 0, true, false, kH264Config);
  p.frame("m-1", ntp, false, true, kIdr);
  p.frame("m-1", ntp + 33 * ms, false, false, kP);
  REQUIRE(eventually([&] {
    std::lock_guard lock(shared->mu);
    return shared->rendered.size() == 2;
  }));
  std::lock_guard lock(shared->mu);
  CHECK(shared->queuedPts == std::vector<std::int64_t>{MirrorPresenter::kDecoderPtsStartNs,
                                                       MirrorPresenter::kDecoderPtsStartNs + 33 * ms});
  CHECK(shared->rendered[0].second == 10'000 * ms);
  CHECK(shared->rendered[1].second == 10'033 * ms);
}

TEST_CASE("mirror presenter: a clock far off still shows every frame") {
  auto decoder = std::make_unique<FakeDecoder>();
  auto shared = decoder->s;
  std::atomic<std::int64_t> now{10'000 * ms};
  MirrorPresenter p(std::move(decoder), [&] { return now.load(); });
  // the video's pts 5 s after what the clock says sounds now
  p.clock(1000 * ms, 10'000 * ms);
  p.frame("m-1", 0, true, false, kH264Config);
  for (int i = 0; i < 10; i++) p.frame("m-1", (6000 + 33 * i) * ms, false, i == 0, i == 0 ? kIdr : kP);
  REQUIRE(eventually([&] { return p.counts().rendered == 10; }));
  CHECK(p.counts().unsynced == 10);
  CHECK(p.counts().late == 0);
}

TEST_CASE("mirror presenter: at most kMaxHeld outputs wait for their time; the rest go with their release time") {
  auto decoder = std::make_unique<FakeDecoder>();
  auto shared = decoder->s;
  std::atomic<std::int64_t> now{10'000 * ms};
  MirrorPresenter p(std::move(decoder), [&] { return now.load(); });
  p.clock(1000 * ms, 10'000 * ms);
  p.frame("m-1", 0, true, false, kH264Config);
  // 300 ms of latency ahead of every frame: all would be held
  constexpr int n = 10;
  for (int i = 0; i < n; i++) p.frame("m-1", (1300 + 33 * i) * ms, false, i == 0, i == 0 ? kIdr : kP);
  REQUIRE(eventually([&] { return p.counts().decoded == n; }));
  REQUIRE(eventually([&] { return p.counts().early == n - MirrorPresenter::kMaxHeld; }));
  std::this_thread::sleep_for(std::chrono::milliseconds(20));
  CHECK(p.counts().rendered == n - MirrorPresenter::kMaxHeld);
  {
    std::lock_guard lock(shared->mu);
    // the oldest first, each at its own time on the clock
    REQUIRE(shared->rendered.size() == n - MirrorPresenter::kMaxHeld);
    for (std::size_t i = 0; i < shared->rendered.size(); i++) {
      CHECK(shared->rendered[i].second == (10'300 + 33 * static_cast<std::int64_t>(i)) * ms);
    }
  }
  // their time comes: the held ones (due 10.498 s to 10.597 s) are shown, none late
  now = 10'500 * ms;
  REQUIRE(eventually([&] { return p.counts().rendered == n - 2; }));
  now = 10'560 * ms;
  REQUIRE(eventually([&] { return p.counts().rendered == n; }));
  CHECK(p.counts().late == 0);
}

TEST_CASE("mirror presenter: units a decoder without a codec drops are counted") {
  auto decoder = std::make_unique<FakeDecoder>();
  auto shared = decoder->s;
  shared->noCodec = true;
  MirrorPresenter p(std::move(decoder), [] { return std::int64_t{0}; });
  p.frame("m-1", 0, true, false, kH264Config);
  p.frame("m-1", 0, false, true, kIdr);
  p.frame("m-1", 0, false, false, kP);
  REQUIRE(eventually([&] { return p.counts().dropped == 2; }));
  CHECK(p.counts().decoded == 0);
}

TEST_CASE("mirror: a clock carrying a group's lead holds units before the decoder until they are near their time "
          "(rule 7)") {
  MirrorSchedule s;
  // a device's own output latency: units decode as they come, however far ahead (rule 6 shows one past a second)
  s.clock(1000 * ms, 10'000 * ms, 40 * ms);
  CHECK(s.waitBeforeDecode(1500 * ms, 10'000 * ms) == 0);
  CHECK(s.waitBeforeDecode(7000 * ms, 10'000 * ms) == 0);
  // a tv that leads a group: media 1 s sounds 1.5 s later than its own output would play it (latency 40 ms + lead)
  s.clock(1000 * ms, 11'500 * ms, 1'540 * ms);
  // pts 1 s is due at 11.5 s: it waits until kDecodeAheadNs before that
  CHECK(s.waitBeforeDecode(1000 * ms, 10'000 * ms) == 1500 * ms - MirrorSchedule::kDecodeAheadNs);
  CHECK(s.waitBeforeDecode(1000 * ms, 11'500 * ms - MirrorSchedule::kDecodeAheadNs) == 0);
  CHECK(s.waitBeforeDecode(900 * ms, 11'400 * ms) == 0);
  // decoded then, it is held and shown at its time, never at once (rule 6's second bound is the decoder's alone)
  CHECK(s.output(1000 * ms, 11'300 * ms).kind == Kind::Hold);
  const auto r = s.output(1000 * ms, 11'480 * ms);
  CHECK(r.kind == Kind::Render);
  CHECK(r.atNs == 11'500 * ms);
  CHECK_FALSE(r.unsynced);
  // no group's lead is longer than kMaxWaitNs: such a unit decodes at once, and rule 6 shows it
  CHECK(s.waitBeforeDecode(1000 * ms + MirrorSchedule::kMaxWaitNs, 11'500 * ms - MirrorSchedule::kMaxWaitNs) == 0);
  // no clock: nothing waits
  MirrorSchedule unanchored;
  CHECK(unanchored.waitBeforeDecode(1000 * ms, 0) == 0);
}

TEST_CASE("mirror presenter: under a group's lead units wait before the decoder and are shown at the group's time") {
  auto decoder = std::make_unique<FakeDecoder>();
  auto shared = decoder->s;
  std::atomic<std::int64_t> now{10'000 * ms};
  MirrorPresenter p(std::move(decoder), [&] { return now.load(); });
  // media 1 s sounds at 11.5 s: the group's 1.5 s lead past the output's 40 ms
  p.clock(1000 * ms, 11'500 * ms, 1'540 * ms);
  p.frame("m-1", 0, true, false, kH264Config);
  p.frame("m-1", 1000 * ms, false, true, kIdr);
  p.frame("m-1", 1033 * ms, false, false, kP);
  REQUIRE(eventually([&] {
    std::lock_guard lock(shared->mu);
    return !shared->calls.empty() && shared->calls[0] == "configure";
  }));
  // 1.5 s ahead: not in the decoder yet, whose few outputs could not hold it
  std::this_thread::sleep_for(std::chrono::milliseconds(30));
  CHECK(p.counts().decoded == 0);
  // within kDecodeAheadNs of its time: decoded, held until it is due
  now = 11'350 * ms;
  REQUIRE(eventually([&] { return p.counts().decoded == 2; }));
  CHECK(p.counts().rendered == 0);
  now = 11'480 * ms;
  REQUIRE(eventually([&] { return p.counts().rendered == 1; }));
  now = 11'500 * ms;
  REQUIRE(eventually([&] { return p.counts().rendered == 2; }));
  {
    std::lock_guard lock(shared->mu);
    REQUIRE(shared->rendered.size() == 2);
    CHECK(shared->rendered[0] == std::pair<std::int64_t, std::int64_t>{1000 * ms, 11'500 * ms});
    CHECK(shared->rendered[1] == std::pair<std::int64_t, std::int64_t>{1033 * ms, 11'533 * ms});
  }
  const auto c = p.counts();
  CHECK(c.unsynced == 0);
  CHECK(c.late == 0);
  CHECK(c.early == 0);
}
