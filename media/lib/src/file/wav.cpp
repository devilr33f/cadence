#include "file/wav.h"

#include <algorithm>
#include <array>
#include <cstring>
#include <iterator>
#include <limits>

namespace cadence {

namespace {

constexpr std::size_t kHeaderBytes = 44;
constexpr std::uint16_t kPcm = 1;

std::uint16_t le16(const std::uint8_t* p) {
  return static_cast<std::uint16_t>(p[0] | (p[1] << 8));
}

std::uint32_t le32(const std::uint8_t* p) {
  return static_cast<std::uint32_t>(p[0]) | (static_cast<std::uint32_t>(p[1]) << 8) | (static_cast<std::uint32_t>(p[2]) << 16) |
         (static_cast<std::uint32_t>(p[3]) << 24);
}

void put16(std::uint8_t* p, std::uint16_t v) {
  p[0] = static_cast<std::uint8_t>(v & 0xff);
  p[1] = static_cast<std::uint8_t>(v >> 8);
}

void put32(std::uint8_t* p, std::uint32_t v) {
  for (int i = 0; i < 4; ++i)
    p[i] = static_cast<std::uint8_t>((v >> (8 * i)) & 0xff);
}

bool tag(const std::uint8_t* p, const char* name) {
  return std::memcmp(p, name, 4) == 0;
}

std::array<std::uint8_t, kHeaderBytes> header(const WavFormat& format, std::uint32_t dataBytes) {
  std::array<std::uint8_t, kHeaderBytes> h{};
  const auto channels = static_cast<std::uint16_t>(format.channels);
  const auto rate = static_cast<std::uint32_t>(format.sampleRate);
  const auto blockAlign = static_cast<std::uint16_t>(channels * 2);
  std::memcpy(&h[0], "RIFF", 4);
  put32(&h[4], 36 + dataBytes);
  std::memcpy(&h[8], "WAVEfmt ", 8);
  put32(&h[16], 16);
  put16(&h[20], kPcm);
  put16(&h[22], channels);
  put32(&h[24], rate);
  put32(&h[28], rate * blockAlign);
  put16(&h[32], blockAlign);
  put16(&h[34], 16);
  std::memcpy(&h[36], "data", 4);
  put32(&h[40], dataBytes);
  return h;
}

WavError malformed(std::string detail) {
  return WavError{WavErrorCode::Malformed, std::move(detail)};
}

}  // namespace

Result<WavReader, WavError> WavReader::parse(std::span<const std::uint8_t> bytes) {
  if (bytes.size() < 12 || !tag(bytes.data(), "RIFF") || !tag(bytes.data() + 8, "WAVE"))
    return malformed("not a riff wave file");

  std::optional<WavFormat> format;
  std::size_t pos = 12;
  while (pos + 8 <= bytes.size()) {
    const auto* chunk = bytes.data() + pos;
    const std::size_t declared = le32(chunk + 4);
    const std::size_t body = pos + 8;
    const std::size_t available = bytes.size() - body;

    if (tag(chunk, "fmt ")) {
      if (declared < 16 || declared > available)
        return malformed("fmt chunk size");
      const auto* f = bytes.data() + body;
      if (le16(f) != kPcm)
        return WavError{WavErrorCode::Unsupported, "not integer pcm"};
      if (le16(f + 14) != 16)
        return WavError{WavErrorCode::Unsupported, "not 16-bit"};
      const std::size_t channels = le16(f + 2);
      if (channels != 1 && channels != 2)
        return WavError{WavErrorCode::Unsupported, "not mono or stereo"};
      const auto rate = le32(f + 4);
      if (rate == 0 || rate > static_cast<std::uint32_t>(std::numeric_limits<int>::max()))
        return malformed("sample rate");
      format = WavFormat{static_cast<int>(rate), channels};
    } else if (tag(chunk, "data")) {
      if (!format)
        return malformed("data before fmt");
      // streaming writers leave the size at its maximum or unfinished, so what the file holds wins
      const std::size_t size = std::min(declared, available);
      const std::size_t blockBytes = format->channels * 2;
      const std::size_t usable = size - size % blockBytes;
      WavReader reader;
      reader.format_ = *format;
      reader.samples_.resize(usable / 2);
      for (std::size_t i = 0; i < reader.samples_.size(); ++i)
        reader.samples_[i] = static_cast<std::int16_t>(le16(bytes.data() + body + i * 2));
      return reader;
    }
    if (declared > available)
      return malformed("chunk size");
    // chunks are padded to even sizes
    pos = body + declared + (declared & 1);
  }
  return malformed(format ? "no data chunk" : "no fmt chunk");
}

Result<WavReader, WavError> WavReader::open(const std::string& path) {
  std::ifstream file(path, std::ios::binary);
  if (!file)
    return WavError{WavErrorCode::OpenFailed, "open"};
  const std::vector<std::uint8_t> bytes((std::istreambuf_iterator<char>(file)), std::istreambuf_iterator<char>());
  if (file.bad())
    return WavError{WavErrorCode::OpenFailed, "read"};
  return parse(bytes);
}

Result<WavWriter, WavError> WavWriter::create(const std::string& path, WavFormat format) {
  if (format.sampleRate <= 0 || (format.channels != 1 && format.channels != 2))
    return WavError{WavErrorCode::Unsupported, "format"};
  std::fstream file(path, std::ios::binary | std::ios::in | std::ios::out | std::ios::trunc);
  if (!file)
    return WavError{WavErrorCode::OpenFailed, "create"};
  const auto h = header(format, 0);
  file.write(reinterpret_cast<const char*>(h.data()), static_cast<std::streamsize>(h.size()));
  if (!file)
    return WavError{WavErrorCode::WriteFailed, "header"};
  return WavWriter(std::move(file), format);
}

WavWriter& WavWriter::operator=(WavWriter&& other) noexcept {
  if (this != &other) {
    close();
    file_ = std::move(other.file_);
    format_ = other.format_;
    samples_ = other.samples_;
  }
  return *this;
}

WavWriter::~WavWriter() {
  close();
}

std::optional<WavError> WavWriter::write(std::span<const std::int16_t> samples) {
  if (!file_.is_open())
    return WavError{WavErrorCode::WriteFailed, "closed"};
  if ((samples_ + samples.size()) * 2 > std::numeric_limits<std::uint32_t>::max() - 36)
    return WavError{WavErrorCode::TooLarge, "data"};
  std::vector<std::uint8_t> bytes(samples.size() * 2);
  for (std::size_t i = 0; i < samples.size(); ++i)
    put16(&bytes[i * 2], static_cast<std::uint16_t>(samples[i]));
  file_.write(reinterpret_cast<const char*>(bytes.data()), static_cast<std::streamsize>(bytes.size()));
  if (!file_)
    return WavError{WavErrorCode::WriteFailed, "data"};
  samples_ += samples.size();
  return std::nullopt;
}

std::optional<WavError> WavWriter::close() {
  if (!file_.is_open())
    return std::nullopt;
  const auto h = header(format_, static_cast<std::uint32_t>(samples_ * 2));
  file_.seekp(0);
  file_.write(reinterpret_cast<const char*>(h.data()), static_cast<std::streamsize>(h.size()));
  file_.flush();
  const bool failed = !file_;
  file_.close();
  if (failed || file_.fail())
    return WavError{WavErrorCode::WriteFailed, "finalize"};
  return std::nullopt;
}

}  // namespace cadence
