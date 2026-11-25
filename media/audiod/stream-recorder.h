#pragma once

#include "file/wav.h"
#include "result.h"

#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <utility>

namespace cadence::audiod {

// stream ids name files, so they are plain names: 1 to 64 of [A-Za-z0-9_-]
bool safeStreamId(std::string_view id);

// the file backend's record of one stream, as the sim's checks read it: the decoded audio as
// <dir>/<stream_id>.wav (48 kHz mono 16-bit). the stream's summary is the sim's own, written by cadenced
class StreamRecorder {
 public:
  // id must be safe; the error is a reason token
  static Result<StreamRecorder, std::string_view> create(const std::string& dir, std::string_view id);

  // one decoded packet. a reason token for the write that failed; the audio after it is not recorded
  [[nodiscard]] std::optional<std::string_view> write(std::span<const std::int16_t> pcm);
  // closes the wav. later calls do nothing; a reason token when it could not be finished
  [[nodiscard]] std::optional<std::string_view> finish();

 private:
  explicit StreamRecorder(WavWriter wav) : wav_(std::move(wav)) {}

  WavWriter wav_;
  bool failed_ = false;
  bool finished_ = false;
};

}  // namespace cadence::audiod
