#include "support/fake-pcm.h"

#include <algorithm>
#include <utility>

namespace cadence::fake_pcm {

namespace {

// nothing real to wait for; a short idle keeps the capture thread quick to notice stop
constexpr auto kIdleWait = std::chrono::milliseconds(2);

}  // namespace

void FakePcmSource::push(Step step) {
  {
    const std::lock_guard lock(mutex_);
    steps_.push_back(std::move(step));
  }
  queued_.notify_all();
}

void FakePcmSource::pushFrames(std::vector<std::int16_t> interleaved) {
  push({CaptureRead::Kind::Frames, std::move(interleaved)});
}

void FakePcmSource::pushRecovered() { push({CaptureRead::Kind::Recovered, {}}); }

void FakePcmSource::pushFailure() { push({CaptureRead::Kind::Failed, {}}); }

void FakePcmSource::holdReads(bool held) {
  {
    const std::lock_guard lock(mutex_);
    held_ = held;
  }
  queued_.notify_all();
}

bool FakePcmSource::drained() const {
  const std::lock_guard lock(mutex_);
  return steps_.empty();
}

bool FakePcmSource::waitForFirstRead(std::chrono::milliseconds limit) {
  std::unique_lock lock(mutex_);
  return firstRead_.wait_for(lock, limit, [this] { return read_; });
}

Result<CaptureFormat, CaptureError> FakePcmSource::open(const CaptureFormat& format) {
  ++opens;
  requested = format;
  if (openError)
    return *openError;
  const auto granted = grant.value_or(format);
  channels_ = granted.channels;
  return granted;
}

std::size_t FakePcmSource::reads() const {
  const std::lock_guard lock(mutex_);
  return reads_;
}

CaptureRead FakePcmSource::read(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) {
  std::unique_lock lock(mutex_);
  read_ = true;
  ++reads_;
  firstRead_.notify_all();
  queued_.wait_for(lock, std::min(timeout, kIdleWait), [this] { return !held_ && !steps_.empty(); });
  if (held_ || steps_.empty())
    return {};

  auto& step = steps_.front();
  if (step.kind == CaptureRead::Kind::Recovered) {
    steps_.pop_front();
    return {CaptureRead::Kind::Recovered, 0, CaptureError{CaptureErrorCode::Overrun, "fake overrun"}};
  }
  if (step.kind == CaptureRead::Kind::Failed) {
    steps_.pop_front();
    return {CaptureRead::Kind::Failed, 0, CaptureError{CaptureErrorCode::ReadFailed, "fake read failure"}};
  }

  const auto frames = std::min((step.samples.size() - step.offset) / channels_, interleaved.size() / channels_);
  std::copy_n(step.samples.data() + step.offset, frames * channels_, interleaved.data());
  step.offset += frames * channels_;
  if (step.offset == step.samples.size())
    steps_.pop_front();
  return {CaptureRead::Kind::Frames, frames, std::nullopt};
}

void FakePcmSource::close() { ++closes; }

}  // namespace cadence::fake_pcm
