#pragma once

// a fake device for the group tests: a playback thread that reads a music input on a perfect dac whose rate is off by
// ppm, publishing the playhead as the real backend does, and records when each sample it read is heard; plus the ramp
// a group test plays, whose sample values say which sample of the epoch they are, so where it was heard is measured
// from the output itself. a stall plan pauses a thread at chosen times, as a loaded host does, and the dac records
// when it read late or the input came up short, so a test tells the host's hiccups from the algorithm's errors
#include "audio/music-codec.h"
#include "backend/pcm-input.h"
#include "backend/presentation-clock.h"
#include "sync/clock-socket.h"

#include <atomic>
#include <chrono>
#include <cmath>
#include <cstdint>
#include <functional>
#include <limits>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <utility>
#include <vector>

namespace cadence::test_support {

// sample n of the ramp: left climbs one s16 step a sample and wraps every 65536, right is its negation
inline void rampFrame(std::uint64_t frame, std::vector<float>& out) {
  out.resize(kGroupFrameSamples * 2);
  for (std::size_t i = 0; i < kGroupFrameSamples; ++i) {
    const auto n = frame * kGroupFrameSamples + i;
    const auto v = static_cast<float>(static_cast<std::int64_t>(n % 65536) - 32768) / 32768.0f;
    out[2 * i] = v;
    out[2 * i + 1] = -v;
  }
}

// stalls a thread at chosen times: each stall sleeps once, the first time its thread passes its point at or after the
// stall's time, and records when it really began and ended (a loaded host oversleeps too)
class StallPlan {
 public:
  struct Stall {
    std::int64_t atNs = 0;
    std::int64_t forNs = 0;
    std::int64_t beganNs = 0;
    std::int64_t endedNs = 0;
  };

  void add(std::int64_t atMono, std::int64_t forNs) {
    std::lock_guard lock(mutex_);
    stalls_.push_back({atMono, forNs});
  }
  // the thread's point: sleeps out the earliest stall that is due
  void point() {
    const auto now = cadence::sync::monoNs();
    std::size_t index = 0;
    std::int64_t forNs = 0;
    {
      std::lock_guard lock(mutex_);
      for (; index < stalls_.size(); ++index)
        if (stalls_[index].beganNs == 0 && stalls_[index].atNs <= now)
          break;
      if (index == stalls_.size())
        return;
      forNs = stalls_[index].forNs;
      stalls_[index].beganNs = now;
    }
    std::this_thread::sleep_for(std::chrono::nanoseconds(forNs));
    std::lock_guard lock(mutex_);
    stalls_[index].endedNs = cadence::sync::monoNs();
  }
  [[nodiscard]] std::vector<Stall> stalls() const {
    std::lock_guard lock(mutex_);
    return stalls_;
  }

 private:
  mutable std::mutex mutex_;
  std::vector<Stall> stalls_;
};

struct HeardSample {
  std::int64_t heardNs = 0;
  float left = 0;
  float right = 0;
};

// what the fake dac saw go wrong at one period: it read late (its thread overslept, or a planned stall), or the input
// came up short (an underrun: its producer did not keep up)
struct DacEvent {
  // when the period's first sample is heard
  std::int64_t heardNs = 0;
  // how long after its time the period was read
  std::int64_t lateNs = 0;
  std::size_t read = 0;
  bool shortRead = false;
};

// a period read this much after its time counts as an oversleep: a period, past the poll's 500 us and the few ms a
// busy host's scheduler adds to any sleep
inline constexpr std::int64_t kDacLateNs = 5'000'000;

class FakePlayback {
 public:
  FakePlayback(PcmInput& input, double dacPpm, std::int64_t latencyNs = 20'000'000, std::size_t period = 240)
      : input_(input), dacPpm_(dacPpm), latencyNs_(latencyNs), period_(period) {}
  ~FakePlayback() { stop(); }

  void start() {
    stop_ = false;
    thread_ = std::thread([this] { run(); });
  }
  void stop() {
    stop_ = true;
    if (thread_.joinable())
      thread_.join();
  }
  [[nodiscard]] std::optional<Playhead> playhead() const { return clock_.playhead(); }
  [[nodiscard]] std::vector<HeardSample> heard() const {
    std::lock_guard lock(mutex_);
    return heard_;
  }
  void forget() {
    std::lock_guard lock(mutex_);
    heard_.clear();
  }
  // the dac thread's stalls: taken right before a period is read
  StallPlan& stalls() { return stalls_; }
  [[nodiscard]] std::vector<DacEvent> events() const {
    std::lock_guard lock(mutex_);
    return events_;
  }

 private:
  void run() {
    const double sample = 1e9 / 48000.0 / (1.0 + dacPpm_ * 1e-6);
    const auto base = cadence::sync::monoNs() + latencyNs_;
    std::vector<float> buf(period_ * 2);
    std::size_t lastRead = 0;
    for (std::uint64_t m = 0; !stop_; ++m) {
      const auto heardNs = base + static_cast<std::int64_t>(std::llround(static_cast<double>(m * period_) * sample));
      while (!stop_ && cadence::sync::monoNs() < heardNs - latencyNs_)
        std::this_thread::sleep_for(std::chrono::microseconds(500));
      stalls_.point();
      const auto lateNs = cadence::sync::monoNs() - (heardNs - latencyNs_);
      const auto read = input_.read(buf);
      clock_.period(m * period_, heardNs, latencyNs_, period_, input_.readPosition());
      // a short read after a full one: an input that stays empty is idle, not news
      const bool shortRead = read < period_ && (read > 0 || lastRead > 0);
      lastRead = read;
      std::lock_guard lock(mutex_);
      if (lateNs > kDacLateNs || shortRead)
        events_.push_back({heardNs, lateNs, read, shortRead});
      for (std::size_t i = 0; i < period_; ++i) {
        if (buf[2 * i] == 0.0f && buf[2 * i + 1] == 0.0f)
          continue;
        heard_.push_back({heardNs + static_cast<std::int64_t>(std::llround(static_cast<double>(i) * sample)),
                          buf[2 * i], buf[2 * i + 1]});
      }
    }
  }

  PcmInput& input_;
  double dacPpm_;
  std::int64_t latencyNs_;
  std::size_t period_;
  PresentationClock clock_;
  std::thread thread_;
  std::atomic<bool> stop_{false};
  StallPlan stalls_;
  mutable std::mutex mutex_;
  std::vector<HeardSample> heard_;
  std::vector<DacEvent> events_;
};

// what a host's hiccup (a dac that overslept, an input that ran short) may disturb: a rejoin's margin (100 ms), its
// preroll and a few frames
inline constexpr std::int64_t kHostHiccupNs = 300'000'000;

// heard-time windows a measurement leaves out: [from, to)
using Windows = std::vector<std::pair<std::int64_t, std::int64_t>>;

// the windows after each of the dac's events (an oversleep, a short read): what a host's hiccup may disturb while the
// playout notices and recovers. from the event's period on, for windowNs
inline Windows windowsAfter(const std::vector<DacEvent>& events, std::int64_t windowNs) {
  Windows out;
  for (const auto& e : events) {
    if (!out.empty() && e.heardNs <= out.back().second)
      out.back().second = std::max(out.back().second, e.heardNs + windowNs);
    else
      out.emplace_back(e.heardNs, e.heardNs + windowNs);
  }
  return out;
}

struct PlacementErrors {
  // ramp samples measured
  std::size_t samples = 0;
  double maxAbsNs = 0;
  double meanNs = 0;
  // samples outlierNs or more off, the first of them and the last
  std::size_t outliers = 0;
  std::int64_t firstOutlierNs = 0;
  double firstOutlierLeft = 0;
  double firstOutlierError = 0;
  std::int64_t lastOutlierAt = 0;
  // sounding samples off the ramp, or too near a sample that is (a fade in or out, a splice's crossfade): they say
  // nothing of placement
  std::size_t faded = 0;
  // the same around the ramp's wrap, where the resampler rings
  std::size_t wrap = 0;
  // ramp samples left out by the windows
  std::size_t excluded = 0;
};

// a sample is measured only inside a run of this many ramp steps on either side of it: a band-limited resampler rings
// around the ramp's wrap (a jump of two full scales) and a fade, and a ringing sample one step above its neighbour
// would otherwise be taken for a ramp sample far off its place (the ms "errors" a group test saw were such samples, at
// the wrap); ringing never climbs one step a sample for long
inline constexpr std::size_t kRampRun = 16;

// each ramp sample heard after fromNs, against when it was due: dueMono(n) for sample n of the epoch. a sample not
// inside a run of ramp steps says nothing and is left out, counted as faded or wrap; a sample heard in one of the
// windows is left out too, counted as excluded
inline PlacementErrors placementErrors(const std::vector<HeardSample>& heard,
                                       const std::function<double(std::uint64_t n)>& dueMono, std::int64_t fromNs,
                                       const Windows& windows = {}, double outlierNs = 1e5) {
  PlacementErrors out;
  const double sample = 1e9 / 48000.0;
  const double zero = dueMono(0);
  // bad[i]: how many of the steps into samples 0..i-1 are off the ramp; wraps[i] the same for the wrap's drop
  std::vector<std::uint32_t> bad(heard.size() + 1, 0);
  std::vector<std::uint32_t> wraps(heard.size() + 1, 0);
  for (std::size_t i = 0; i < heard.size(); ++i) {
    bool good = false;
    bool wrapped = false;
    if (i > 0) {
      const double step = (heard[i].left - heard[i - 1].left) * 32768.0;
      const auto dt = heard[i].heardNs - heard[i - 1].heardNs;
      // the ramp's zero is silence to the dac, so a step of two over it
      good = (std::abs(step - 1.0) <= 0.25 && dt <= 30'000) || (std::abs(step - 2.0) <= 0.25 && dt <= 50'000);
      wrapped = step < -32768.0;
    }
    bad[i + 1] = bad[i] + (good ? 0 : 1);
    wraps[i + 1] = wraps[i] + (wrapped ? 1 : 0);
  }
  // steps into samples [from, to)
  const auto count = [&](const std::vector<std::uint32_t>& c, std::size_t from, std::size_t to) {
    from = std::min(from, heard.size());
    to = std::min(to, heard.size());
    return from < to ? c[to] - c[from] : 0u;
  };
  // the resampler's reach either side of the wrap's drop, past the run
  constexpr std::size_t kWrapReach = 256;
  double sum = 0;
  std::size_t window = 0;
  for (std::size_t i = 0; i < heard.size(); ++i) {
    const auto& s = heard[i];
    if (s.heardNs < fromNs)
      continue;
    // the steps into i-kRampRun+1 .. i+kRampRun
    if (i < kRampRun || i + kRampRun >= heard.size() || count(bad, i - kRampRun + 1, i + kRampRun + 1) > 0) {
      const auto lo = i > kRampRun + kWrapReach ? i - kRampRun - kWrapReach : 0;
      if (count(wraps, lo, i + kRampRun + kWrapReach + 1) > 0)
        ++out.wrap;
      else
        ++out.faded;
      continue;
    }
    while (window < windows.size() && windows[window].second <= s.heardNs)
      ++window;
    if (window < windows.size() && windows[window].first <= s.heardNs) {
      ++out.excluded;
      continue;
    }
    const double mod = s.left * 32768.0 + 32768.0;
    const double estimate = (static_cast<double>(s.heardNs) - zero) / sample;
    const double n = mod + 65536.0 * std::round((estimate - mod) / 65536.0);
    if (n < 0)
      continue;
    // the index is fractional through the resampler: due by interpolation
    const auto whole = static_cast<std::uint64_t>(std::floor(n));
    const double due = dueMono(whole) + (n - static_cast<double>(whole)) * sample;
    const double error = static_cast<double>(s.heardNs) - due;
    out.maxAbsNs = std::max(out.maxAbsNs, std::abs(error));
    if (std::abs(error) >= outlierNs) {
      if (out.outliers++ == 0) {
        out.firstOutlierNs = s.heardNs - fromNs;
        out.firstOutlierLeft = s.left;
        out.firstOutlierError = error;
      }
      out.lastOutlierAt = s.heardNs;
    }
    sum += error;
    ++out.samples;
  }
  out.meanNs = out.samples > 0 ? sum / static_cast<double>(out.samples) : 0;
  return out;
}

// a failure's story, in ms from fromNs: the 10 ms stretches whose worst sample is outlierNs or more off (their first
// and worst error, how many samples were off, the first one's value), then the dac's events within 200 ms of the
// first of them
inline std::string describeErrors(const std::vector<HeardSample>& heard,
                                  const std::function<double(std::uint64_t n)>& dueMono, std::int64_t fromNs,
                                  const std::vector<DacEvent>& events, double outlierNs = 1e5) {
  std::string out;
  constexpr std::int64_t kBucketNs = 10'000'000;
  std::optional<std::int64_t> firstBad;
  std::size_t i = 0;
  for (std::int64_t at = fromNs; !heard.empty() && at <= heard.back().heardNs && out.size() < 3000; at += kBucketNs) {
    // the stretch with the runs around it, so its samples are judged as in the whole
    constexpr std::size_t kMargin = 2 * (kRampRun + 256);
    const auto first = i > kMargin ? i - kMargin : 0;
    while (i < heard.size() && heard[i].heardNs < at + kBucketNs)
      ++i;
    const auto last = std::min(i + kMargin, heard.size());
    const std::vector<HeardSample> slice(heard.begin() + static_cast<std::ptrdiff_t>(first),
                                         heard.begin() + static_cast<std::ptrdiff_t>(last));
    const Windows after{{at + kBucketNs, std::numeric_limits<std::int64_t>::max()}};
    const auto errors = placementErrors(slice, dueMono, at, after, outlierNs);
    if (errors.maxAbsNs < outlierNs)
      continue;
    if (!firstBad)
      firstBad = at;
    out += " err@" + std::to_string((at - fromNs) / 1'000'000) + "ms(first " +
           std::to_string(static_cast<std::int64_t>(errors.firstOutlierError / 1000)) + "us at +" +
           std::to_string(errors.firstOutlierNs / 1000) + "us, max " +
           std::to_string(static_cast<std::int64_t>(errors.maxAbsNs / 1000)) + "us, " +
           std::to_string(errors.outliers) + "/" + std::to_string(errors.samples) + " off, value " +
           std::to_string(errors.firstOutlierLeft) + ")";
  }
  if (firstBad)
    for (const auto& e : events)
      if (e.heardNs >= *firstBad - 200'000'000 && e.heardNs <= *firstBad + 200'000'000)
        out += " dac@" + std::to_string((e.heardNs - fromNs) / 1'000'000) + "ms(late " +
               std::to_string(e.lateNs / 1000) + "us read " + std::to_string(e.read) + ")";
  return out;
}

}  // namespace cadence::test_support
