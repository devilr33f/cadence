#include "audiod/stream-recorder.h"

#include "audio/opus-codec.h"

#include <algorithm>

namespace cadence::audiod {

namespace {

constexpr std::size_t kMaxStreamId = 64;

bool plain(char c) {
  return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-';
}

}  // namespace

bool safeStreamId(std::string_view id) {
  return !id.empty() && id.size() <= kMaxStreamId && std::all_of(id.begin(), id.end(), plain);
}

Result<StreamRecorder, std::string_view> StreamRecorder::create(const std::string& dir, std::string_view id) {
  auto wav = WavWriter::create(dir + "/" + std::string(id) + ".wav", WavFormat{kDecodeSampleRate, 1});
  if (!wav.ok())
    return std::string_view("wav_open");
  return StreamRecorder(std::move(wav.value()));
}

std::optional<std::string_view> StreamRecorder::write(std::span<const std::int16_t> pcm) {
  if (failed_)
    return std::nullopt;
  if (wav_.write(pcm)) {
    failed_ = true;
    return "wav_write";
  }
  return std::nullopt;
}

std::optional<std::string_view> StreamRecorder::finish() {
  if (finished_)
    return std::nullopt;
  finished_ = true;
  // a failed write was reported when it happened
  if (wav_.close().has_value() && !failed_)
    return "wav_close";
  return std::nullopt;
}

}  // namespace cadence::audiod
