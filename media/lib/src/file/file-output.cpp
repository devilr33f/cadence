#include "file/file-output.h"

#include <algorithm>
#include <cstdio>

namespace cadence {

FileOutput::FileOutput(std::string dir, std::string prefix, FileClock& clock, FileSilence silence)
    : dir_(std::move(dir)), prefix_(std::move(prefix)), clock_(clock), silence_(silence) {}

FileOutput::~FileOutput() {
  close();
}

FileOutput::Duration FileOutput::frameTime(std::size_t frames, bool roundUp) const {
  const auto rate = static_cast<std::int64_t>(format_.sampleRate);
  const auto scaled = static_cast<std::int64_t>(frames) * 1'000'000'000;
  const auto nanos = roundUp ? (scaled + rate - 1) / rate : scaled / rate;
  return std::chrono::duration_cast<Duration>(std::chrono::nanoseconds(nanos));
}

Result<PlaybackFormat, PlaybackError> FileOutput::open(const PlaybackFormat& requested) {
  close();
  if (requested.sampleFormat != SampleFormat::S16Le || requested.sampleRate <= 0)
    return PlaybackError{PlaybackErrorCode::FormatMismatch, "file output writes s16 only"};
  char name[32];
  std::snprintf(name, sizeof name, "-%03zu.wav", files_.size());
  auto path = dir_ + "/" + prefix_ + name;
  auto created = WavWriter::create(path, WavFormat{requested.sampleRate, requested.channels});
  if (!created.ok())
    return PlaybackError{created.error().code == WavErrorCode::Unsupported ? PlaybackErrorCode::FormatMismatch
                                                                           : PlaybackErrorCode::OpenFailed,
                         "wav " + created.error().detail};
  files_.push_back(std::move(path));
  writer_.emplace(std::move(created.value()));
  fault_.reset();
  format_ = requested;
  channels_ = requested.channels;
  capacityFrames_ = requested.fillFrames != 0 ? requested.fillFrames : requested.bufferFrames;
  buffer_.clear();
  played_ = clock_.now();
  return format_;
}

std::optional<PlaybackError> FileOutput::flush(std::size_t frames) {
  if (frames == 0)
    return std::nullopt;
  const auto samples = frames * channels_;
  std::vector<std::int16_t> chunk(buffer_.begin(), buffer_.begin() + static_cast<std::ptrdiff_t>(samples));
  buffer_.erase(buffer_.begin(), buffer_.begin() + static_cast<std::ptrdiff_t>(samples));
  if (silence_ == FileSilence::Drop &&
      std::all_of(chunk.begin(), chunk.end(), [](std::int16_t sample) { return sample == 0; }))
    return std::nullopt;
  if (const auto error = writer_->write(chunk))
    return PlaybackError{PlaybackErrorCode::WriteFailed, "wav " + error->detail};
  return std::nullopt;
}

void FileOutput::advance() {
  const auto now = clock_.now();
  const auto elapsed = std::max(now - played_, Duration::zero());
  const auto nanos = std::chrono::duration_cast<std::chrono::nanoseconds>(elapsed).count();
  const auto due = static_cast<std::size_t>(nanos * format_.sampleRate / 1'000'000'000);
  const auto count = std::min(due, bufferedFrames());
  // a device that ran dry has nothing to play, so its time only restarts with the next write
  played_ = bufferedFrames() <= due ? now : played_ + frameTime(count);
  if (auto error = flush(count))
    fault_ = std::move(error);
}

PlaybackWrite FileOutput::write(std::span<const std::int16_t> interleaved, std::chrono::milliseconds timeout) {
  if (!writer_)
    return PlaybackWrite{PlaybackWrite::Kind::Failed, 0, PlaybackError{PlaybackErrorCode::NotOpen, "file output"}};
  if (fault_)
    return PlaybackWrite{PlaybackWrite::Kind::Failed, 0, fault_};
  const auto limit = clock_.now() + timeout;
  const auto wanted = interleaved.size() / channels_;
  const auto need = std::min(wanted, std::max<std::size_t>(format_.periodFrames, 1));
  advance();
  if (capacityFrames_ != 0 && capacityFrames_ - std::min(capacityFrames_, bufferedFrames()) < need) {
    // wait until the device has played enough to take a period
    const auto excess = bufferedFrames() + need - capacityFrames_;
    clock_.waitUntil(std::min(played_ + frameTime(excess, true), limit));
    advance();
  }
  const auto room = capacityFrames_ == 0 ? wanted : capacityFrames_ - std::min(capacityFrames_, bufferedFrames());
  const auto count = std::min(wanted, room);
  if (count == 0)
    return PlaybackWrite{};
  if (bufferedFrames() == 0)
    played_ = clock_.now();
  buffer_.insert(buffer_.end(), interleaved.begin(), interleaved.begin() + static_cast<std::ptrdiff_t>(count * channels_));
  return PlaybackWrite{PlaybackWrite::Kind::Frames, count, std::nullopt};
}

Result<std::size_t, PlaybackError> FileOutput::rewind() {
  if (!writer_)
    return PlaybackError{PlaybackErrorCode::NotOpen, "file output"};
  advance();
  const auto frames = bufferedFrames();
  buffer_.clear();
  return frames;
}

Result<std::size_t, PlaybackError> FileOutput::delayFrames() {
  if (!writer_)
    return PlaybackError{PlaybackErrorCode::NotOpen, "file output"};
  advance();
  return bufferedFrames();
}

std::optional<PlaybackError> FileOutput::drop() {
  if (!writer_)
    return PlaybackError{PlaybackErrorCode::NotOpen, "file output"};
  return std::nullopt;
}

void FileOutput::close() {
  if (!writer_)
    return;
  flush(bufferedFrames());
  writer_->close();
  writer_.reset();
}

}  // namespace cadence
