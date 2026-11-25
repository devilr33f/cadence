#include "playback/jitter-buffer.h"

#include <algorithm>
#include <utility>

namespace cadence {

JitterBuffer::JitterBuffer(JitterBufferConfig config) : config_(config), ring_(config.maxSamples) {}

bool JitterBuffer::begin(std::string_view responseId) {
  const std::lock_guard lock(mutex_);
  if (retired(responseId) || queued(responseId))
    return false;
  if (!responses_.empty() && !responses_.back().ended) {
    dropAll();
    ++counters_.generation;
  }
  responses_.push_back(Response{std::string(responseId), 0, false, false});
  return true;
}

PushResult JitterBuffer::push(std::string_view responseId, std::span<const std::int16_t> pcm) {
  const std::lock_guard lock(mutex_);
  if (!acceptsPackets(responseId)) {
    ++counters_.staleRejected;
    return PushResult::Stale;
  }
  if (pcm.size() > ring_.size() - queued_) {
    ++counters_.overflowDropped;
    return PushResult::Overflow;
  }
  if (pcm.empty())
    return PushResult::Accepted;

  const auto tail = (head_ + queued_) % ring_.size();
  const auto first = std::min(pcm.size(), ring_.size() - tail);
  std::copy_n(pcm.begin(), first, ring_.begin() + static_cast<std::ptrdiff_t>(tail));
  std::copy(pcm.begin() + static_cast<std::ptrdiff_t>(first), pcm.end(), ring_.begin());
  queued_ += pcm.size();
  responses_.back().samples += pcm.size();
  return PushResult::Accepted;
}

bool JitterBuffer::end(std::string_view responseId) {
  const std::lock_guard lock(mutex_);
  if (!acceptsPackets(responseId))
    return false;
  responses_.back().ended = true;
  return true;
}

bool JitterBuffer::watch(std::string_view responseId) {
  const std::lock_guard lock(mutex_);
  if (!queued(responseId))
    return false;
  watched_ = std::string(responseId);
  return true;
}

bool JitterBuffer::empty() const {
  const std::lock_guard lock(mutex_);
  return responses_.empty();
}

bool JitterBuffer::cancel(std::optional<std::string_view> responseId) {
  const std::lock_guard lock(mutex_);
  if (responseId && !queued(*responseId)) {
    if (watched_ && *watched_ == *responseId)
      watched_.reset();
    retire(std::string(*responseId));
    return false;
  }
  dropAll();
  ++counters_.generation;
  return true;
}

bool JitterBuffer::cancelFrom(std::string_view responseId) {
  const std::lock_guard lock(mutex_);
  const auto named = std::find_if(responses_.begin(), responses_.end(),
                                  [responseId](const Response& response) { return response.id == responseId; });
  if (named == responses_.end()) {
    retire(std::string(responseId));
    return false;
  }
  bool stale = false;
  // what is queued sits in order, so the dropped responses' audio is the ring's tail
  for (auto response = named; response != responses_.end(); ++response) {
    const bool watched = watched_ && *watched_ == response->id;
    if (watched)
      watched_.reset();
    stale = stale || response->read || watched;
    queued_ -= response->samples;
    retire(std::move(response->id));
  }
  if (named == responses_.begin()) {
    head_ = 0;
    playing_ = false;
  }
  responses_.erase(named, responses_.end());
  if (stale)
    ++counters_.generation;
  return stale;
}

JitterRead JitterBuffer::read(std::span<std::int16_t> output) {
  const std::lock_guard lock(mutex_);
  JitterRead result;
  result.generation = counters_.generation;
  while (result.samples < output.size() && !responses_.empty()) {
    auto& front = responses_.front();
    if (!playing_ && (front.samples >= config_.targetSamples || front.ended))
      playing_ = true;
    if (!playing_)
      break;

    const auto copied = std::min(front.samples, output.size() - result.samples);
    if (copied > 0) {
      if constexpr (trace::kEnabled) {
        if (!front.read && result.trace.starts < JitterTrace::kStarts) {
          result.trace.startOffsets[result.trace.starts] = result.samples;
          result.trace.startIds[result.trace.starts].assign(front.id);
          ++result.trace.starts;
        }
      }
      const auto first = std::min(copied, ring_.size() - head_);
      const auto out = output.begin() + static_cast<std::ptrdiff_t>(result.samples);
      std::copy_n(ring_.begin() + static_cast<std::ptrdiff_t>(head_), first, out);
      std::copy_n(ring_.begin(), copied - first, out + static_cast<std::ptrdiff_t>(first));
      head_ = (head_ + copied) % ring_.size();
      queued_ -= copied;
      front.samples -= copied;
      front.read = true;
      result.samples += copied;
    }
    if (front.samples > 0)
      break;
    if (front.ended) {
      result.drained = true;
      ++counters_.drained;
      if (watched_ && *watched_ == front.id) {
        result.marker = result.samples;
        watched_.reset();
      }
      retire(std::move(front.id));
      responses_.pop_front();
      playing_ = false;
      continue;
    }
    if (result.samples < output.size()) {
      // wait for the target again rather than play the rest in fragments
      result.underrun = true;
      ++counters_.underruns;
      if constexpr (trace::kEnabled)
        result.trace.underrun.assign(front.id);
      playing_ = false;
    }
    break;
  }
  std::fill(output.begin() + static_cast<std::ptrdiff_t>(result.samples), output.end(), std::int16_t{0});
  return result;
}

std::uint64_t JitterBuffer::generation() const {
  const std::lock_guard lock(mutex_);
  return counters_.generation;
}

JitterBufferStatus JitterBuffer::status() const {
  const std::lock_guard lock(mutex_);
  auto status = counters_;
  if (!responses_.empty()) {
    status.activeResponse = responses_.back().id;
    status.ended = responses_.back().ended;
  }
  status.playing = playing_;
  status.queuedSamples = queued_;
  return status;
}

void JitterBuffer::retire(std::string id) {
  if (config_.retiredIds == 0 || retired(id))
    return;
  retired_.push_back(std::move(id));
  while (retired_.size() > config_.retiredIds)
    retired_.pop_front();
}

bool JitterBuffer::retired(std::string_view id) const {
  return std::find(retired_.begin(), retired_.end(), id) != retired_.end();
}

bool JitterBuffer::queued(std::string_view id) const {
  return std::any_of(responses_.begin(), responses_.end(), [id](const Response& response) { return response.id == id; });
}

bool JitterBuffer::acceptsPackets(std::string_view id) const {
  return !responses_.empty() && responses_.back().id == id && !responses_.back().ended;
}

void JitterBuffer::dropAll() {
  for (auto& response : responses_)
    retire(std::move(response.id));
  responses_.clear();
  head_ = 0;
  queued_ = 0;
  playing_ = false;
  watched_.reset();
}

}  // namespace cadence
