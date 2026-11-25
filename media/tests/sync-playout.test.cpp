// a group's playout on a fake device: frames placed by the playhead formula, steered onto a dac that runs off its
// nominal rate, paused on a sample, its placer's corrections, and what it does when a thread stalls
#include "support/group-harness.h"
#include "sync/group-placer.h"
#include "sync/group-playout.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <utility>
#include <vector>

using namespace cadence;
using namespace cadence::sync;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

std::vector<std::uint8_t> pcmFrame(std::uint64_t k) {
  std::vector<float> pcm;
  rampFrame(k, pcm);
  MusicEncoder enc(MusicCodec::PcmS16, 0);
  std::vector<std::uint8_t> bytes;
  enc.encode(pcm, bytes);
  return bytes;
}

// how soon after a stall the playout is back within 100 us: a rejoin's margin (100 ms), its preroll and a few frames
constexpr std::int64_t kRecoverNs = 500'000'000;

// a leader in the test's own process: frames of one epoch from t0, fed ahead by a feeder thread, and a pump thread
// that rejoins as the leader's own playout does (from a margin ahead, out of what was fed). each thread can be stalled:
// the pump between pumps, the pump while it holds a playhead (it pushes by a stale one), the feeder (the producer
// late), and the dac before a period
struct Rig {
  PcmInput input;
  FakePlayback dac;
  StallPlan pumpStalls;
  StallPlan staleStalls;
  StallPlan feedStalls;
  GroupPlayout playout;
  std::int64_t t0 = 0;
  std::int64_t feedLeadNs = 1'000'000'000;
  std::atomic<bool> stop{false};
  std::atomic<std::uint64_t> fed{0};
  std::atomic<int> rejoins{0};
  std::atomic<int> errorRejoins{0};
  std::thread feeder;
  std::thread pumper;

  // the leader's own rejoin margin
  static constexpr std::int64_t kRejoinMarginNs = 100'000'000;

  Rig(double dacPpm, GroupPlayoutConfig config)
      : dac(input, dacPpm),
        playout(
            input,
            [this] {
              const auto playhead = dac.playhead();
              staleStalls.point();
              return playhead;
            },
            config) {}

  [[nodiscard]] std::int64_t frameT(std::uint64_t k) const { return t0 + static_cast<std::int64_t>(k) * kGroupFrameNs; }

  void start(std::int64_t startNs, std::uint64_t frames) {
    t0 = startNs;
    playout.format(1, MusicCodec::PcmS16);
    dac.start();
    feeder = std::thread([this, frames] {
      while (!stop) {
        feedStalls.point();
        // the leader's timeline runs feedLeadNs ahead of real time
        while (fed < frames && frameT(fed) < monoNs() + feedLeadNs) {
          const auto k = fed.load();
          playout.frame(1, k, frameT(k), k * 20, pcmFrame(k));
          ++fed;
        }
        std::this_thread::sleep_for(2ms);
      }
    });
    pumper = std::thread([this] {
      const ClockMapping identity{};
      while (!stop) {
        pumpStalls.point();
        const auto reason = playout.pump(monoNs(), identity);
        if (reason != RejoinReason::None) {
          ++rejoins;
          if (reason == RejoinReason::Error)
            ++errorRejoins;
          const auto from = monoNs() + kRejoinMarginNs;
          playout.rejoinFrom(from);
          const auto upTo = fed.load();
          for (std::uint64_t k = 0; k < upTo; ++k)
            if (frameT(k) >= from)
              playout.frame(1, k, frameT(k), k * 20, pcmFrame(k));
        }
        std::this_thread::sleep_for(2ms);
      }
    });
  }
  void finish() {
    stop = true;
    if (feeder.joinable())
      feeder.join();
    if (pumper.joinable())
      pumper.join();
    dac.stop();
  }
  ~Rig() { finish(); }
};

}  // namespace

TEST_CASE("an idle trim jumps") {
  PcmInput input;
  GroupPlayout idle(input, {}, {});
  CHECK(idle.setTrim(1'000'000) == GroupTrimMode::Jump);
}

TEST_CASE("a trim change mid-play stays gapless and inside the slew's placement bound") {
  GroupPlayoutConfig config;
  Rig rig(0, config);
  const auto t0 = monoNs() + 300'000'000;
  rig.start(t0, 300);
  std::this_thread::sleep_for(1200ms);
  const auto changed = monoNs();
  CHECK(rig.playout.setTrim(2'000'000) == GroupTrimMode::Slew);
  std::this_thread::sleep_for(3600ms);
  rig.finish();
  const auto placed = rig.playout.placed();
  std::vector<double> dues(300, 0.0);
  for (const auto& f : placed)
    if (f.epoch == 1 && f.seq < dues.size())
      dues[f.seq] = static_cast<double>(f.dueMonoNs);
  // every frame after the change was still placed, at its slewed due time: no cut, no rejoin, no relink
  REQUIRE(placed.size() > 150);
  for (std::size_t i = 1; i <= placed.back().seq && i < dues.size(); ++i)
    REQUIRE(dues[i] != 0.0);
  const auto due = [&](std::uint64_t n) {
    return dues[n / kGroupFrameSamples] + static_cast<double>(n % kGroupFrameSamples) * 1e9 / 48000.0;
  };
  const auto heard = rig.dac.heard();
  const auto errors = placementErrors(heard, due, t0 + 300'000'000,
                                      windowsAfter(rig.dac.events(), kHostHiccupNs));
  const auto stats = rig.playout.stats();
  INFO("changed " << changed - t0 << " ns after t0; max " << errors.maxAbsNs << " ns; cuts " << stats.cuts
                  << "; rejoins " << stats.rejoins);
  CHECK(errors.samples > 48000 * 2);
  CHECK(errors.maxAbsNs < 500'000);
  CHECK(stats.cuts == 0);
  CHECK(rig.errorRejoins == 0);
  CHECK(rig.playout.trimNs() == 2'000'000);
}

TEST_CASE("a trim past 50 ms jumps even while playing") {
  Rig rig(0, {});
  rig.start(monoNs() + 300'000'000, 100);
  std::this_thread::sleep_for(800ms);
  CHECK(rig.playout.setTrim(60'000'000) == GroupTrimMode::Jump);
  rig.finish();
}

TEST_CASE("the placer steers small errors, cuts past 5 ms and rejoins past 200 ms") {
  GroupPlacer placer;
  CHECK(placer.place(20'000, 20'000'000).action == GroupPlacement::Action::Steer);
  GroupPlacer banded(GroupPlacerConfig{.deadBandNs = 50'000});
  CHECK(banded.place(20'000, 20'000'000).action == GroupPlacement::Action::Steer);
  // inside the dead band only the integral moves
  CHECK(std::abs(banded.steerPpm()) <= 1);
  // late: faster
  CHECK(placer.place(200'000, 20'000'000).steerPpm > 100);
  placer.reset();
  CHECK(placer.place(-200'000, 20'000'000).steerPpm < -100);
  CHECK(placer.place(-900'000, 20'000'000).steerPpm == -500);
  const auto cut = placer.place(6'000'000, 20'000'000);
  CHECK(cut.action == GroupPlacement::Action::Cut);
  CHECK(cut.cutNs == 6'000'000);
  CHECK(placer.place(-250'000'000, 20'000'000).action == GroupPlacement::Action::Rejoin);
  // a constant error is integrated away: the steering keeps growing while it lasts
  placer.reset();
  std::int32_t last = 0;
  for (int i = 0; i < 100; ++i)
    last = placer.place(40'000, 20'000'000).steerPpm;
  CHECK(last >= 1);
}

TEST_CASE("frames land within 100 us of their due time on a dac on time or 80 ppm fast, and a trim moves them") {
  const std::int64_t trim = GENERATE(0, 3'000'000);
  const double dacPpm = GENERATE(0.0, 80.0);
  GroupPlayoutConfig config;
  config.trimNs = trim;
  Rig rig(dacPpm, config);
  const auto t0 = monoNs() + 300'000'000;
  rig.start(t0, 400);
  std::this_thread::sleep_for(6500ms);
  rig.finish();
  const auto due = [&](std::uint64_t n) { return static_cast<double>(t0 + trim) + static_cast<double>(n) * 1e9 / 48000.0; };
  const auto heard = rig.dac.heard();
  const auto events = rig.dac.events();
  const auto from = t0 + 1'000'000'000;
  // the algorithm settled, not the host: the 100 us hold everywhere but just after the dac overslept or the input ran
  // short (a busy host's), and every sample within a millisecond even there
  const auto errors = placementErrors(heard, due, from, windowsAfter(events, kHostHiccupNs));
  const auto whole = placementErrors(heard, due, from);
  const auto stats = rig.playout.stats();
  INFO("dac " << dacPpm << " trim " << trim << ": max " << errors.maxAbsNs << " ns, mean " << errors.meanNs
              << " ns over " << errors.samples << " samples, " << errors.excluded << " excluded after "
              << events.size() << " dac events (" << whole.maxAbsNs << " ns with them), " << errors.faded
              << " faded; steer " << stats.steerPpm << ", rejoins " << stats.rejoins << ", underruns "
              << stats.underruns << ", cuts " << stats.cuts);
  INFO("story" << (errors.maxAbsNs >= 100'000 ? describeErrors(heard, due, from, events) : std::string()));
  CHECK(errors.samples + errors.excluded > 48000 * 4);
  CHECK(errors.samples > 48000);
  CHECK(errors.maxAbsNs < 100'000);
  CHECK(whole.maxAbsNs < 1'000'000);
  // a rejoin only for an underrun a stalled host made, never for an error
  CHECK(rig.errorRejoins == 0);
  CHECK(stats.rejoins <= stats.underruns);
  CHECK(stats.cuts == 0);
  // a dac that reads faster than nominal: the stream is slowed onto it
  if (dacPpm > 0 && stats.rejoins == 0) {
    CHECK(stats.steerPpm < -40);
    CHECK(stats.steerPpm > -130);
  }
}

TEST_CASE("a pause stops on its sample after a fade, and nothing after it is heard") {
  Rig rig(0, {});
  const auto t0 = monoNs() + 300'000'000;
  rig.start(t0, 300);
  // a pause 1.01 s into the epoch: mid-frame
  const auto pauseAt = t0 + 1'010'000'000;
  std::this_thread::sleep_for(500ms);
  rig.playout.pause(1, pauseAt);
  std::this_thread::sleep_for(1200ms);
  rig.finish();
  CHECK(rig.playout.stats().state == PlayoutState::Paused);
  const auto heard = rig.dac.heard();
  REQUIRE(!heard.empty());
  for (std::size_t i = heard.size() > 3 ? heard.size() - 3 : 0; i < heard.size(); ++i)
    UNSCOPED_INFO("tail " << heard[i].heardNs - pauseAt << " " << heard[i].left);
  // the last sound ends at the pause, give or take a sample, and the 10 ms before it fade
  CHECK(std::llabs(heard.back().heardNs - pauseAt) < 50'000);
  const auto due = [&](std::uint64_t n) { return static_cast<double>(t0) + static_cast<double>(n) * 1e9 / 48000.0; };
  const auto errors = placementErrors(heard, due, t0 + 200'000'000);
  CHECK(errors.maxAbsNs < 100'000);
}

TEST_CASE("a left channel member plays the left channel on both outputs, and a lost frame is concealed") {
  GroupPlayoutConfig config;
  config.channel = GroupChannelMap::Left;
  PcmInput input;
  FakePlayback dac(input, 0);
  GroupPlayout playout(input, [&] { return dac.playhead(); }, config);
  playout.format(1, MusicCodec::PcmS16);
  const auto t0 = monoNs() + 200'000'000;
  for (std::uint64_t k = 0; k < 40; ++k)
    if (k != 10)
      playout.frame(1, k, t0 + static_cast<std::int64_t>(k) * kGroupFrameNs, 0, pcmFrame(k));
  dac.start();
  const ClockMapping identity{};
  const auto end = monoNs() + 1'200'000'000;
  while (monoNs() < end) {
    const auto r = playout.pump(monoNs(), identity);
    INFO("at " << (monoNs() - t0) / 1000 << " us, state " << static_cast<int>(playout.stats().state));
    CHECK(r == RejoinReason::None);
    std::this_thread::sleep_for(2ms);
  }
  dac.stop();
  const auto heard = dac.heard();
  REQUIRE(heard.size() > 30 * kGroupFrameSamples);
  for (const auto& s : heard)
    CHECK(s.left == s.right);
  CHECK(playout.stats().concealed == 1);
}

TEST_CASE("after a stall of the dac, the pump or the producer nothing is heard 1 ms off, the playout is back within "
          "100 us in 500 ms, and an underrun is told") {
  // the pump stalled between pumps or while it holds a playhead (it pushes by a stale one), the dac before a period,
  // and the producer: the frames come late
  const std::string kind = GENERATE("dac", "pump", "stale", "producer");
  // the probe tells of every frame: a frame it tells of was heard, where it says
  std::mutex probedMutex;
  std::vector<std::pair<std::uint64_t, std::int64_t>> probed;
  GroupPlayoutConfig config;
  config.heardEvery = 1;
  config.heard = [&](std::uint32_t, std::uint64_t seq, std::int64_t ns) {
    std::lock_guard lock(probedMutex);
    probed.emplace_back(seq, ns);
  };
  Rig rig(40, config);
  const auto t0 = monoNs() + 300'000'000;
  const auto at = [&](std::int64_t ms) { return t0 + ms * 1'000'000; };
  StallPlan& plan = kind == "dac"     ? rig.dac.stalls()
                    : kind == "pump"  ? rig.pumpStalls
                    : kind == "stale" ? rig.staleStalls
                                      : rig.feedStalls;
  // ms after t0, ms long: 40 ms ones back to back while the playout waits for its first frame and starts (about 100
  // ms before t0: the start is placed by a playhead the stall made stale, onto an input it let run dry), then 5, 15,
  // 40 ms (within the 60 ms lead) and 100 ms (past it: an underrun) while it plays; the producer is fed 150 ms ahead
  // and stalls within it, then past it
  std::vector<std::pair<std::int64_t, std::int64_t>> schedule{{-170, 40}, {-130, 40}, {-90, 40}, {-50, 40},
                                                              {1000, 5},  {2000, 15},  {3000, 40}, {4000, 100}};
  if (kind == "producer") {
    rig.feedLeadNs = 150'000'000;
    schedule = {{1000, 100}, {2000, 300}, {3500, 600}};
  }
  for (const auto& [ms, len] : schedule)
    plan.add(at(ms), len * 1'000'000);
  rig.start(t0, 300);
  std::this_thread::sleep_for(std::chrono::nanoseconds(at(5400) - monoNs()));
  rig.finish();

  const auto due = [&](std::uint64_t n) { return static_cast<double>(t0) + static_cast<double>(n) * 1e9 / 48000.0; };
  const auto heard = rig.dac.heard();
  const auto events = rig.dac.events();
  const auto stalls = plan.stalls();
  // what a stall may disturb: from its start until 500 ms after its end; and what the host's own hiccups may
  Windows windows = windowsAfter(events, kHostHiccupNs);
  for (const auto& stall : stalls)
    if (stall.beganNs != 0)
      windows.emplace_back(stall.beganNs, std::max(stall.endedNs, stall.beganNs) + kRecoverNs);
  std::sort(windows.begin(), windows.end());
  const auto whole = placementErrors(heard, due, t0, {}, 1'000'000);
  const auto settled = placementErrors(heard, due, t0 + 200'000'000, windows);
  const auto stats = rig.playout.stats();
  std::string story;
  for (const auto& stall : stalls)
    story += " stall@" + std::to_string((stall.beganNs - t0) / 1'000'000) + "ms for " +
             std::to_string((stall.endedNs - stall.beganNs) / 1000) + "us";
  INFO(kind << ":" << story);
  INFO("whole: max " << whole.maxAbsNs << " ns over " << whole.samples << " samples, " << whole.faded
                     << " faded or spliced, " << whole.wrap << " at the ramp's wrap");
  INFO("settled: max " << settled.maxAbsNs << " ns over " << settled.samples << " samples, " << settled.excluded
                       << " excluded");
  INFO("rejoins " << stats.rejoins << ", underruns " << stats.underruns << ", cuts " << stats.cuts << ", concealed "
                  << stats.concealed << ", dac events " << events.size());
  INFO("story" << (whole.maxAbsNs >= 1'000'000 ? describeErrors(heard, due, t0, events, 1'000'000)
                   : settled.maxAbsNs >= 100'000 ? describeErrors(heard, due, t0 + 200'000'000, events)
                                                 : std::string()));
  REQUIRE(std::all_of(stalls.begin(), stalls.end(), [](const auto& stall) { return stall.endedNs != 0; }));
  // late or early by a stall never: what was placed is heard where it was meant to be, or not at all
  CHECK(whole.maxAbsNs < 1'000'000);
  // back within 100 us once a stall is 500 ms past
  CHECK(settled.maxAbsNs < 100'000);
  CHECK(settled.samples > 48000);
  // a stall never makes a placement error the placer has to cut or rejoin for: the playout notices the underrun
  CHECK(stats.cuts == 0);
  CHECK(rig.errorRejoins == 0);
  CHECK(stats.rejoins <= stats.underruns);
  if (kind == "pump" || kind == "stale")
    CHECK(stats.underruns >= 1);
  // a frame a clear dropped before it was heard is never told of, nor one read across an underrun
  std::lock_guard lock(probedMutex);
  CHECK(probed.size() > 100);
  double worstProbe = 0;
  for (const auto& [seq, ns] : probed)
    worstProbe = std::max(worstProbe, std::abs(static_cast<double>(ns - rig.frameT(seq))));
  INFO("probe: " << probed.size() << " frames, worst " << worstProbe << " ns");
  CHECK(worstProbe < 1'000'000);
}

TEST_CASE("a start made while the dac or the pump stalls is heard on time, or not at all") {
  // the dac stalled and then catching up in a burst, or the pump holding a playhead the burst made stale: the start
  // used to be placed by a playhead the input then ran dry after, unseen as nothing was attached yet, and its frames
  // were heard late by what the burst read dry (ms, steered out at 500 ppm for seconds, or cut)
  const std::string kind = GENERATE("dac", "stale");
  PcmInput input;
  FakePlayback dac(input, 40);
  StallPlan staleStalls;
  GroupPlayout playout(
      input,
      [&] {
        const auto playhead = dac.playhead();
        staleStalls.point();
        return playhead;
      },
      {});
  StallPlan& plan = kind == "dac" ? dac.stalls() : staleStalls;
  std::atomic<bool> stop{false};
  std::atomic<int> rejoins{0};
  dac.start();
  std::thread pumper([&] {
    const ClockMapping identity{};
    while (!stop) {
      if (playout.pump(monoNs(), identity) != RejoinReason::None)
        ++rejoins;
      std::this_thread::sleep_for(2ms);
    }
  });
  // each epoch: 400 ms of frames from 300 ms on, and 40 ms stalls back to back over the 170 ms before it, which the
  // start falls in
  constexpr int kEpochs = 6;
  constexpr std::uint64_t kFrames = 20;
  std::vector<std::int64_t> starts;
  for (int e = 1; e <= kEpochs; ++e) {
    const auto t = monoNs() + 300'000'000;
    starts.push_back(t);
    playout.format(static_cast<std::uint32_t>(e), MusicCodec::PcmS16);
    for (std::uint64_t k = 0; k < kFrames; ++k)
      playout.frame(static_cast<std::uint32_t>(e), k, t + static_cast<std::int64_t>(k) * kGroupFrameNs, k * 20,
                    pcmFrame(k));
    for (int j = 0; j < 4; ++j)
      plan.add(t - 170'000'000 + j * 40'000'000, 40'000'000);
    std::this_thread::sleep_for(std::chrono::nanoseconds(t + 550'000'000 - monoNs()));
  }
  stop = true;
  pumper.join();
  dac.stop();

  const auto heard = dac.heard();
  const auto stats = playout.stats();
  INFO(kind << ": rejoins " << stats.rejoins << ", underruns " << stats.underruns << ", cuts " << stats.cuts);
  for (int e = 0; e < kEpochs; ++e) {
    const auto t = starts[static_cast<std::size_t>(e)];
    const auto due = [t](std::uint64_t n) { return static_cast<double>(t) + static_cast<double>(n) * 1e9 / 48000.0; };
    // from before the epoch's start: a start placed early is caught too
    const auto from = t - 100'000'000;
    std::vector<HeardSample> window;
    for (const auto& s : heard)
      if (s.heardNs >= from && s.heardNs < t + static_cast<std::int64_t>(kFrames) * kGroupFrameNs)
        window.push_back(s);
    const auto errors = placementErrors(window, due, from, {}, 1'000'000);
    INFO("epoch " << e + 1 << ": max " << errors.maxAbsNs << " ns over " << errors.samples << " samples, "
                  << errors.faded << " faded");
    INFO("story" << (errors.maxAbsNs >= 1'000'000 ? describeErrors(window, due, from, dac.events(), 1'000'000)
                                                  : std::string()));
    CHECK(errors.maxAbsNs < 1'000'000);
    // a start that went is made again from the queue: most of the epoch is heard
    CHECK(errors.samples > 48000 / 5);
  }
  // nothing a placement error would need: no cut, no rejoin
  CHECK(stats.cuts == 0);
  CHECK(rejoins == 0);
}

TEST_CASE("an epoch that ends on an output of long periods fades before its input runs dry: no underrun, no rejoin") {
  // a tv's output reads 43 ms a period: the run-out used to wait until 30 ms past the latency were left, so the next
  // period read the input short, counted an underrun and rejoined at the end of every track
  PcmInput input;
  FakePlayback dac(input, 0, 60'000'000, 2064);
  GroupPlayout playout(input, [&] { return dac.playhead(); }, {});
  std::atomic<bool> stop{false};
  std::atomic<int> rejoins{0};
  dac.start();
  std::thread pumper([&] {
    const ClockMapping identity{};
    while (!stop) {
      if (playout.pump(monoNs(), identity) != RejoinReason::None)
        ++rejoins;
      std::this_thread::sleep_for(2ms);
    }
  });
  // epochs of different lengths, so their ends fall on every phase of the output's periods
  constexpr int kEpochs = 8;
  for (int e = 1; e <= kEpochs; ++e) {
    const auto t = monoNs() + 300'000'000;
    const auto frames = static_cast<std::uint64_t>(17 + e);
    playout.format(static_cast<std::uint32_t>(e), MusicCodec::PcmS16);
    for (std::uint64_t k = 0; k < frames; ++k)
      playout.frame(static_cast<std::uint32_t>(e), k, t + static_cast<std::int64_t>(k) * kGroupFrameNs, k * 20,
                    pcmFrame(k));
    std::this_thread::sleep_for(
        std::chrono::nanoseconds(t + static_cast<std::int64_t>(frames) * kGroupFrameNs + 400'000'000 - monoNs()));
    INFO("epoch " << e);
    CHECK(playout.stats().state == PlayoutState::Buffering);
  }
  stop = true;
  pumper.join();
  dac.stop();
  const auto stats = playout.stats();
  INFO("rejoins " << stats.rejoins << ", underruns " << stats.underruns << ", dac events " << dac.events().size());
  CHECK(stats.underruns == 0);
  CHECK(stats.rejoins == 0);
  CHECK(rejoins == 0);
  // every epoch was heard
  CHECK(dac.heard().size() > static_cast<std::size_t>(kEpochs) * 15 * kGroupFrameSamples);
}

TEST_CASE("a playhead missing for a few pumps while it plays leaves the playout playing") {
  // the backend's playhead can read none for a moment (its seqlock busy, the clock between periods): the state used
  // to turn buffering and stay so while the audio played on, as only a start sets playing
  PcmInput input;
  FakePlayback dac(input, 0);
  std::atomic<bool> blind{false};
  GroupPlayout playout(
      input, [&]() -> std::optional<Playhead> { return blind ? std::nullopt : dac.playhead(); }, {});
  playout.format(1, MusicCodec::PcmS16);
  const auto t0 = monoNs() + 200'000'000;
  for (std::uint64_t k = 0; k < 100; ++k)
    playout.frame(1, k, t0 + static_cast<std::int64_t>(k) * kGroupFrameNs, k * 20, pcmFrame(k));
  dac.start();
  const ClockMapping identity{};
  const auto pumpUntil = [&](std::int64_t end) {
    while (monoNs() < end) {
      CHECK(playout.pump(monoNs(), identity) == RejoinReason::None);
      std::this_thread::sleep_for(2ms);
    }
  };
  pumpUntil(t0 + 600'000'000);
  REQUIRE(playout.stats().state == PlayoutState::Playing);
  blind = true;
  pumpUntil(monoNs() + 8'000'000);
  blind = false;
  pumpUntil(monoNs() + 300'000'000);
  dac.stop();
  const auto stats = playout.stats();
  CHECK(stats.state == PlayoutState::Playing);
  CHECK(stats.rejoins == 0);
}

TEST_CASE("a rejoin tells why: a placement past 200 ms, an underrun, the clock lost, with the output's delay") {
  // what audiod's group_rejoin line says: the reason's token, the error, and the output's delay then and over the run
  const std::string kind = GENERATE("placement", "underrun", "clock_lost");
  PcmInput input;
  FakePlayback dac(input, 0, 20'000'000);
  GroupPlayout playout(input, [&] { return dac.playhead(); }, {});
  playout.format(1, MusicCodec::PcmS16);
  const auto t0 = monoNs() + 200'000'000;
  for (std::uint64_t k = 0; k < 200; ++k)
    playout.frame(1, k, t0 + static_cast<std::int64_t>(k) * kGroupFrameNs, k * 20, pcmFrame(k));
  dac.start();
  const ClockMapping identity{};
  auto reason = RejoinReason::None;
  const auto pumpUntil = [&](std::int64_t end, const std::optional<ClockMapping>& mapping) {
    while (monoNs() < end && reason == RejoinReason::None) {
      reason = playout.pump(monoNs(), mapping);
      std::this_thread::sleep_for(2ms);
    }
  };
  pumpUntil(t0 + 500'000'000, identity);
  REQUIRE(reason == RejoinReason::None);
  REQUIRE(playout.stats().state == PlayoutState::Playing);
  REQUIRE(playout.lastDrop().reason == RejoinReason::None);
  if (kind == "placement") {
    // the leader's clock read 250 ms ahead on the same generation: every frame is due 250 ms earlier than it is heard
    ClockMapping ahead;
    ahead.offsetNs = 250'000'000;
    pumpUntil(monoNs() + 200'000'000, ahead);
  } else if (kind == "underrun") {
    // the pump sleeps past the lead and the output's delay: the input runs dry
    std::this_thread::sleep_for(150ms);
    pumpUntil(monoNs() + 200'000'000, identity);
  } else {
    pumpUntil(monoNs() + 200'000'000, std::nullopt);
  }
  dac.stop();
  const auto drop = playout.lastDrop();
  INFO("reason " << rejoinToken(reason) << ", error " << drop.errorNs << " ns, delay " << drop.delayFrames);
  CHECK(rejoinToken(reason) == kind);
  CHECK(drop.reason == reason);
  CHECK(rejoinToken(drop.reason) == kind);
  CHECK(playout.stats().state == PlayoutState::Rejoining);
  // the fake dac's 20 ms delay: 960 frames, steady over the run
  CHECK(drop.delayFrames == 960);
  CHECK(drop.delayMinFrames == 960);
  CHECK(drop.delayMaxFrames == 960);
  CHECK(drop.playedNs > 100'000'000);
  if (kind == "placement")
    CHECK(drop.errorNs > 200'000'000);
  if (kind == "underrun")
    CHECK(playout.stats().underruns >= 1);
}
