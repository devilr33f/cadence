#pragma once

#include "result.h"

#include <cstddef>
#include <cstdint>
#include <fstream>
#include <optional>
#include <span>
#include <string>
#include <vector>

namespace cadence {

enum class WavErrorCode {
  OpenFailed,
  // not a riff/wave file, or its chunks are inconsistent
  Malformed,
  // a valid wav that is not 16-bit integer pcm of one or two channels
  Unsupported,
  WriteFailed,
  // the data chunk would pass the 4 GiB a wav header can describe
  TooLarge,
};

// detail names the step, never the audio
struct WavError {
  WavErrorCode code;
  std::string detail;
};

struct WavFormat {
  int sampleRate = 0;
  std::size_t channels = 0;
};

// a whole 16-bit pcm wav, read into memory
class WavReader {
 public:
  static Result<WavReader, WavError> open(const std::string& path);
  static Result<WavReader, WavError> parse(std::span<const std::uint8_t> bytes);

  [[nodiscard]] const WavFormat& format() const { return format_; }
  // interleaved
  [[nodiscard]] const std::vector<std::int16_t>& samples() const { return samples_; }
  [[nodiscard]] std::size_t frames() const { return samples_.size() / format_.channels; }

 private:
  WavFormat format_;
  std::vector<std::int16_t> samples_;
};

// writes a 16-bit pcm wav; the header sizes are patched when the file is closed
class WavWriter {
 public:
  static Result<WavWriter, WavError> create(const std::string& path, WavFormat format);

  WavWriter(WavWriter&&) noexcept = default;
  WavWriter& operator=(WavWriter&& other) noexcept;
  WavWriter(const WavWriter&) = delete;
  WavWriter& operator=(const WavWriter&) = delete;
  // finalizes like close
  ~WavWriter();

  // interleaved
  [[nodiscard]] std::optional<WavError> write(std::span<const std::int16_t> samples);
  // patches the header and closes the file; later calls do nothing
  std::optional<WavError> close();

  [[nodiscard]] std::uint64_t frames() const { return samples_ / format_.channels; }

 private:
  WavWriter(std::fstream file, WavFormat format) : file_(std::move(file)), format_(format) {}

  std::fstream file_;
  WavFormat format_;
  std::uint64_t samples_ = 0;
};

}  // namespace cadence
