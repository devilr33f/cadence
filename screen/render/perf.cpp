#include "render/perf.h"

#include <algorithm>
#include <chrono>

namespace cadence::screen::render::perf {

namespace {

Spans& current() {
  static Spans s{};
  return s;
}

Counters& currentCounters() {
  static Counters c{};
  return c;
}

Quantiles quantiles(std::vector<double> v) {
  Quantiles q;
  if (v.empty()) return q;
  std::sort(v.begin(), v.end());
  const auto at = [&](double f) {
    const auto i = static_cast<std::size_t>(f * static_cast<double>(v.size() - 1) + 0.5);
    return v[std::min(i, v.size() - 1)];
  };
  q.p50 = at(0.5);
  q.p95 = at(0.95);
  q.p99 = at(0.99);
  q.max = v.back();
  return q;
}

double ms(std::int64_t ns) { return static_cast<double>(ns) / 1e6; }

std::int64_t cpuNs(const Turn& t) {
  std::int64_t sum = 0;
  for (std::size_t i = 0; i < kPhases; ++i) {
    if (i != static_cast<std::size_t>(Phase::Swap) && i != static_cast<std::size_t>(Phase::Gpu)) sum += t.ns[i];
  }
  return sum;
}

}  // namespace

std::int64_t nowNs() {
  return std::chrono::duration_cast<std::chrono::nanoseconds>(std::chrono::steady_clock::now().time_since_epoch())
      .count();
}

void add(Span span, std::int64_t ns, std::uint32_t count) {
  auto& s = current()[static_cast<std::size_t>(span)];
  s.ns += ns;
  s.count += count;
}

void count(Counter counter, std::uint64_t n) { currentCounters()[static_cast<std::size_t>(counter)] += n; }

Counters takeCounters() {
  Counters out = currentCounters();
  currentCounters() = {};
  return out;
}

Spans take() {
  Spans out = current();
  current() = {};
  return out;
}

Recorder::Recorder(std::size_t window, double refreshHz) : window_(std::max<std::size_t>(1, window)) {
  setRefreshHz(refreshHz);
  frames_.reserve(window_);
}

void Recorder::setRefreshHz(double hz) { periodNs_ = 1e9 / (hz > 1 ? hz : 60.0); }

void Recorder::reset(std::int64_t nowNs) {
  frames_.clear();
  next_ = 0;
  sinceNs_ = nowNs;
  turns_ = presented_ = idleTurns_ = chained_ = late_ = 0;
  chainedNs_ = 0;
  spanTotals_ = {};
  lastEndNs_ = 0;
}

void Recorder::add(const Turn& t) {
  if (sinceNs_ == 0) sinceNs_ = t.endNs;
  turns_++;
  for (std::size_t i = 0; i < spanTotals_.size(); ++i) {
    spanTotals_[i].ns += t.spans[i].ns;
    spanTotals_[i].count += t.spans[i].count;
  }
  if (!t.presented) {
    if (cpuNs(t) > 0 && !t.uiDirty) idleTurns_++;
    return;
  }
  presented_++;
  Turn rec = t;
  if (t.chained && lastEndNs_ > 0) {
    const auto interval = static_cast<double>(t.endNs - lastEndNs_);
    chained_++;
    chainedNs_ += interval;
    rec.late = interval > 1.5 * periodNs_;
    if (rec.late) late_++;
  }
  lastEndNs_ = t.endNs;
  if (frames_.size() < window_) {
    frames_.push_back(rec);
  } else {
    frames_[next_] = rec;
    next_ = (next_ + 1) % window_;
  }
}

Summary Recorder::summary(std::int64_t nowNs) const {
  Summary s;
  s.seconds = sinceNs_ > 0 && nowNs > sinceNs_ ? static_cast<double>(nowNs - sinceNs_) / 1e9 : 0;
  s.turns = turns_;
  s.frames = presented_;
  s.idleTurns = idleTurns_;
  if (s.seconds > 0) {
    s.turnsPerS = static_cast<double>(turns_) / s.seconds;
    s.fps = static_cast<double>(presented_) / s.seconds;
  }
  s.chained = chained_;
  s.late = late_;
  if (chainedNs_ > 0) s.chainedFps = static_cast<double>(chained_) / (chainedNs_ / 1e9);
  std::vector<double> cpu, frame;
  std::array<std::vector<double>, kPhases> phases;
  std::array<std::vector<double>, static_cast<std::size_t>(Span::Count)> spans;
  std::array<std::vector<double>, kCounters> counters;
  for (const auto& f : frames_) {
    for (std::size_t i = 0; i < kCounters; ++i) counters[i].push_back(static_cast<double>(f.counters[i]));
    const auto c = cpuNs(f);
    cpu.push_back(ms(c));
    frame.push_back(ms(c + f.ns[static_cast<std::size_t>(Phase::Gpu)] + f.ns[static_cast<std::size_t>(Phase::Swap)]));
    for (std::size_t i = 0; i < kPhases; ++i) phases[i].push_back(ms(f.ns[i]));
    for (std::size_t i = 0; i < spans.size(); ++i) spans[i].push_back(ms(f.spans[i].ns));
  }
  s.cpuMs = quantiles(std::move(cpu));
  s.frameMs = quantiles(std::move(frame));
  for (std::size_t i = 0; i < kPhases; ++i) s.phaseMs[i] = quantiles(std::move(phases[i]));
  for (std::size_t i = 0; i < kCounters; ++i) s.counters[i] = quantiles(std::move(counters[i]));
  for (std::size_t i = 0; i < spans.size(); ++i) {
    s.spanMs[i] = quantiles(std::move(spans[i]));
    s.spanTotalMs[i] = ms(spanTotals_[i].ns);
    s.spanCount[i] = spanTotals_[i].count;
  }
  return s;
}

std::vector<Recorder::Bar> Recorder::recent(std::size_t n) const {
  std::vector<Bar> out;
  const auto size = frames_.size();
  n = std::min(n, size);
  out.reserve(n);
  // oldest first: the ring's start is next_ once it is full
  const std::size_t start = size < window_ ? 0 : next_;
  for (std::size_t i = size - n; i < size; ++i) {
    const auto& f = frames_[(start + i) % size];
    const auto swap = f.ns[static_cast<std::size_t>(Phase::Swap)] + f.ns[static_cast<std::size_t>(Phase::Gpu)];
    out.push_back({static_cast<float>(ms(cpuNs(f))), static_cast<float>(ms(swap)), f.late});
  }
  return out;
}

std::vector<Turn> Recorder::worst(std::size_t n) const {
  std::vector<Turn> out(frames_.begin(), frames_.end());
  std::sort(out.begin(), out.end(), [](const Turn& a, const Turn& b) { return cpuNs(a) > cpuNs(b); });
  if (out.size() > n) out.resize(n);
  return out;
}

const char* phaseName(Phase p) {
  switch (p) {
    case Phase::Events:
      return "events";
    case Phase::Link:
      return "link";
    case Phase::Update:
      return "update";
    case Phase::Draw:
      return "draw";
    case Phase::Gpu:
      return "gpu";
    case Phase::Swap:
      return "swap";
    case Phase::Count:
      break;
  }
  return "?";
}

const char* counterName(Counter c) {
  switch (c) {
    case Counter::Draws:
      return "draws";
    case Counter::ClipMasks:
      return "clip_masks";
    case Counter::Layers:
      return "layers";
    case Counter::FillKpx:
      return "fill_kpx";
    case Counter::StencilClearKpx:
      return "stencil_clear_kpx";
    case Counter::Gradients:
      return "gradients";
    case Counter::GradientKpx:
      return "gradient_kpx";
    case Counter::Count:
      break;
  }
  return "?";
}

const char* spanName(Span s) {
  switch (s) {
    case Span::RmlUpdate:
      return "rml_update";
    case Span::RmlRender:
      return "rml_render";
    case Span::Upload:
      return "upload";
    case Span::ImageScan:
      return "image_scan";
    case Span::Decode:
      return "decode";
    case Span::Count:
      break;
  }
  return "?";
}

}  // namespace cadence::screen::render::perf
