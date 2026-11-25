#include "file/file-capture.h"

#include <algorithm>

namespace cadence {

namespace {

constexpr int kRate = 16000;
constexpr std::size_t kFrameSamples = kRate / 50;
constexpr std::chrono::nanoseconds kFrameDuration = std::chrono::milliseconds(20);

}  // namespace

FileCapture::FileCapture(std::string path, FileClock& clock) : path_(std::move(path)), clock_(clock) {}

Result<CaptureFormat, CaptureError> FileCapture::open(const CaptureFormat& requested) {
  close();
  auto loaded = WavReader::open(path_);
  if (!loaded.ok())
    return CaptureError{CaptureErrorCode::OpenFailed, "wav " + std::to_string(static_cast<int>(loaded.error().code)) + " " + loaded.error().detail};
  if (loaded.value().format().sampleRate != kRate || loaded.value().format().channels != 1)
    return CaptureError{CaptureErrorCode::FormatMismatch, "wav is not 16 kHz mono"};
  reader_ = loaded.value();
  position_ = 0;
  start_ = clock_.now();
  open_ = true;
  CaptureFormat granted = requested;
  granted.sampleRate = kRate;
  granted.channels = 1;
  if (granted.periodFrames == 0)
    granted.periodFrames = kFrameSamples;
  if (granted.bufferFrames == 0)
    granted.bufferFrames = kFrameSamples * 4;
  return granted;
}

bool FileCapture::finished() const {
  return position_ >= reader_.samples().size();
}

CaptureRead FileCapture::read(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) {
  if (!open_)
    return CaptureRead{CaptureRead::Kind::Failed, 0, CaptureError{CaptureErrorCode::NotOpen, "file capture"}};

  const auto total = reader_.samples().size();
  const auto limit = clock_.now() + timeout;
  // the frames whose 20 ms have fully passed
  const auto due = [&] {
    const auto elapsed = std::max(clock_.now() - start_, FileClock::TimePoint::duration::zero());
    const auto frames = static_cast<std::size_t>(elapsed / kFrameDuration);
    return std::min(frames * kFrameSamples, total);
  };

  if (finished()) {
    clock_.waitUntil(limit);
    return CaptureRead{};
  }
  auto ready = due();
  if (ready <= position_) {
    const auto frames = (position_ / kFrameSamples) + 1;
    const auto next = start_ + kFrameDuration * static_cast<std::int64_t>(frames);
    clock_.waitUntil(std::min(next, limit));
    ready = due();
  }
  const auto count = std::min(ready - position_, interleaved.size());
  if (count == 0)
    return CaptureRead{};
  std::copy_n(reader_.samples().begin() + static_cast<std::ptrdiff_t>(position_), count, interleaved.begin());
  position_ += count;
  return CaptureRead{CaptureRead::Kind::Frames, count, std::nullopt};
}

void FileCapture::close() {
  open_ = false;
  reader_ = WavReader();
  position_ = 0;
}

}  // namespace cadence
