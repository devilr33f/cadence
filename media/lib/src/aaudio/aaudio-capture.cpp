#include "aaudio/aaudio-capture.h"

#include <algorithm>
#include <string>
#include <thread>

namespace cadence {

namespace {

constexpr std::chrono::milliseconds kPoll(2);
constexpr std::size_t kMinBursts = 4;
constexpr int kMinRingMs = 200;

}  // namespace

AAudioCapture::AAudioCapture(aaudio::Api& api) : api_(api) {}

AAudioCapture::~AAudioCapture() { close(); }

Result<CaptureFormat, CaptureError> AAudioCapture::open(const CaptureFormat& requested) {
  if (open_)
    return CaptureError{CaptureErrorCode::OpenFailed, "aaudio already open"};
  if (requested.sampleRate <= 0 || requested.channels == 0)
    return CaptureError{CaptureErrorCode::ConfigureFailed, "aaudio needs a rate and channels"};
  format_ = requested;
  if (auto error = openStream())
    return *error;
  open_ = true;
  return format_;
}

std::optional<CaptureError> AAudioCapture::openStream() {
  aaudio::StreamRequest request;
  request.direction = aaudio::Direction::Input;
  request.sampleRate = format_.sampleRate;
  request.channels = static_cast<int>(format_.channels);
  request.lowLatency = true;
  dead_.store(false);
  auto opened = api_.open(
      request, [this](std::span<std::int16_t> in) { onData(in); },
      [this](int) { dead_.store(true, std::memory_order_release); });
  if (!opened.ok())
    return CaptureError{CaptureErrorCode::OpenFailed, "aaudio open: " + opened.error()};
  auto stream = std::move(opened.value());
  const auto info = stream->info();
  // the vqe and the encoder are built for one rate and layout: never resampled or remixed here
  if (info.sampleRate != format_.sampleRate || info.channels != static_cast<int>(format_.channels))
    return CaptureError{CaptureErrorCode::FormatMismatch,
                        "aaudio granted " + std::to_string(info.sampleRate) + " Hz x " + std::to_string(info.channels)};
  const auto burst = static_cast<std::size_t>(std::max(info.framesPerBurst, 1));
  const std::size_t capacity =
      std::max({format_.bufferFrames, kMinBursts * burst, static_cast<std::size_t>(format_.sampleRate * kMinRingMs / 1000)});
  format_.bufferFrames = capacity;
  ring_.reset(capacity, format_.channels);
  stream_ = std::move(stream);
  if (!stream_->start()) {
    stream_.reset();
    return CaptureError{CaptureErrorCode::OpenFailed, "aaudio start"};
  }
  return std::nullopt;
}

void AAudioCapture::onData(std::span<std::int16_t> in) {
  const std::size_t channels = ring_.channels();
  const std::size_t frames = in.size() / channels;
  // a full ring keeps what it holds and drops what does not fit; the reader learns of the gap and skips past it
  if (const auto room = ring_.room(); room < frames) {
    overruns_.fetch_add(1, std::memory_order_relaxed);
    lost_.store(true, std::memory_order_release);
  }
  ring_.write(in);
}

CaptureRead AAudioCapture::read(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) {
  if (!open_)
    return {CaptureRead::Kind::Failed, 0, CaptureError{CaptureErrorCode::NotOpen, {}}};
  if (dead_.load(std::memory_order_acquire) || !stream_) {
    if (stream_) {
      stream_->stop();
      stream_.reset();
    }
    if (auto error = openStream())
      return {CaptureRead::Kind::Failed, 0, CaptureError{CaptureErrorCode::ReadFailed, error->detail}};
    return {CaptureRead::Kind::Recovered, 0, CaptureError{CaptureErrorCode::Suspended, "aaudio stream reopened"}};
  }
  if (lost_.exchange(false, std::memory_order_acq_rel)) {
    // what is left is older than the gap: dropped, so reads continue from audio after it
    ring_.skip(ring_.fill());
    return {CaptureRead::Kind::Recovered, 0, CaptureError{CaptureErrorCode::Overrun, "aaudio ring overflowed"}};
  }
  const auto deadline = std::chrono::steady_clock::now() + timeout;
  for (;;) {
    if (ring_.fill() > 0)
      return {CaptureRead::Kind::Frames, ring_.read(interleaved), std::nullopt};
    const auto now = std::chrono::steady_clock::now();
    if (now >= deadline)
      return {CaptureRead::Kind::Idle, 0, std::nullopt};
    std::this_thread::sleep_for(std::min<std::chrono::steady_clock::duration>(kPoll, deadline - now));
  }
}

void AAudioCapture::close() {
  if (stream_) {
    stream_->stop();
    stream_.reset();
  }
  open_ = false;
}

}  // namespace cadence
