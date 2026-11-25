#include "pcm_timeline.h"

#include <algorithm>
#include <cmath>

namespace cadence::render::viz {

PcmTimeline::PcmTimeline(std::size_t frames, std::uint32_t rate) : ring_(std::max<std::size_t>(frames, 2)), rate_(rate) {}

std::int64_t PcmTimeline::index_at(std::int64_t t_ns) const {
  // to the nearest frame: a stamp truncated to whole ns must not move its snapshot a frame early
  return std::llround(static_cast<double>(t_ns - origin_ns_) * static_cast<double>(rate_) / 1e9);
}

void PcmTimeline::push(std::span<const float> interleaved, std::uint32_t channels, std::uint32_t rate,
                       std::int64_t mono_ns) {
  if (channels == 0 || rate == 0) return;
  std::unique_lock lock(mu_, std::try_to_lock);
  if (!lock.owns_lock()) {
    dropped_.fetch_add(1, std::memory_order_relaxed);
    return;
  }
  const auto size = static_cast<std::int64_t>(ring_.size());
  std::int64_t first = started_ && rate == rate_ ? index_at(mono_ns) : 0;
  if (!started_ || rate != rate_ || first > newest_ + static_cast<std::int64_t>(rate) ||
      first < newest_ - static_cast<std::int64_t>(rate)) {
    rate_ = rate;
    origin_ns_ = mono_ns;
    first = 0;
    newest_ = 0;
    std::fill(ring_.begin(), ring_.end(), 0.0f);
    if (started_) ++stats_.resets;
    ++epoch_;
    started_ = true;
  }
  ++stats_.pushes;
  if (first > newest_) stats_.gap_frames += static_cast<std::uint64_t>(first - newest_);
  const auto frames = static_cast<std::int64_t>(interleaved.size() / channels);
  // a gap since the newest frame is silence
  for (std::int64_t i = newest_; i < first; ++i) ring_[static_cast<std::size_t>(((i % size) + size) % size)] = 0;
  for (std::int64_t f = 0; f < frames; ++f) {
    float s = 0;
    for (std::uint32_t c = 0; c < channels; ++c) s += interleaved[static_cast<std::size_t>(f) * channels + c];
    s /= static_cast<float>(channels);
    if (!std::isfinite(s)) s = 0;
    const std::int64_t i = first + f;
    if (i < newest_ - size) continue;
    ring_[static_cast<std::size_t>(((i % size) + size) % size)] = s;
  }
  newest_ = std::max(newest_, first + frames);
}

double PcmTimeline::ahead_ms(std::int64_t now_ns) {
  std::lock_guard lock(mu_);
  if (!started_) return 0;
  return static_cast<double>(newest_ - index_at(now_ns)) * 1000.0 / static_cast<double>(rate_);
}

bool PcmTimeline::window(std::int64_t now_ns, std::span<float> out) {
  std::lock_guard lock(mu_);
  if (!started_) {
    std::fill(out.begin(), out.end(), 0.0f);
    return false;
  }
  const auto size = static_cast<std::int64_t>(ring_.size());
  // the frame heard now is the window's last
  const std::int64_t end = index_at(now_ns) + 1;
  const auto n = static_cast<std::int64_t>(out.size());
  for (std::int64_t k = 0; k < n; ++k) {
    const std::int64_t i = end - n + k;
    const bool have = i >= 0 && i < newest_ && i >= newest_ - size;
    if (!have) ++stats_.missing_frames;
    out[static_cast<std::size_t>(k)] = have ? ring_[static_cast<std::size_t>(i % size)] : 0.0f;
  }
  return true;
}

std::int64_t PcmTimeline::index_of(std::int64_t t_ns) {
  std::lock_guard lock(mu_);
  return started_ ? index_at(t_ns) : 0;
}

std::int64_t PcmTimeline::newest() {
  std::lock_guard lock(mu_);
  return newest_;
}

std::uint64_t PcmTimeline::epoch() {
  std::lock_guard lock(mu_);
  return epoch_;
}

std::int64_t PcmTimeline::read(std::int64_t from, std::int64_t to, std::vector<float>& out) {
  std::lock_guard lock(mu_);
  out.clear();
  const auto size = static_cast<std::int64_t>(ring_.size());
  from = std::max({from, newest_ - size, std::int64_t{0}});
  to = std::min(to, newest_);
  for (std::int64_t i = from; i < to; ++i) out.push_back(ring_[static_cast<std::size_t>(i % size)]);
  return from;
}

PcmTimeline::Stats PcmTimeline::take_stats() {
  std::lock_guard lock(mu_);
  Stats s = stats_;
  s.dropped = dropped_.exchange(0, std::memory_order_relaxed);
  stats_ = {};
  return s;
}

}  // namespace cadence::render::viz
