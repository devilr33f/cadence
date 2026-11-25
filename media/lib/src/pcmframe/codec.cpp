#include "pcmframe/codec.h"

#include <array>

namespace cadence::pcmframe {

namespace {

constexpr std::uint8_t kFormat = 1;
constexpr std::uint8_t kPcm = 2;
constexpr std::uint8_t kCork = 3;
constexpr std::uint8_t kFlush = 4;
constexpr std::uint8_t kPlayed = 0x81;

constexpr std::array kToServer{kFormat, kPcm, kCork, kFlush};
constexpr std::array kToShim{kPlayed};

// the payload length a type requires, if it has a fixed one
std::optional<std::size_t> fixedLength(std::uint8_t kind) {
  switch (kind) {
    case kFormat:
      return 6;
    case kCork:
      return 1;
    case kFlush:
      return 0;
    case kPlayed:
      return 8;
    default:
      return std::nullopt;
  }
}

void putLe(std::vector<std::uint8_t>& out, std::uint64_t value, std::size_t bytes) {
  for (std::size_t i = 0; i < bytes; ++i)
    out.push_back(static_cast<std::uint8_t>(value >> (8 * i)));
}

std::uint64_t getLe(std::span<const std::uint8_t> bytes) {
  std::uint64_t value = 0;
  for (std::size_t i = bytes.size(); i > 0; --i)
    value = value << 8 | bytes[i - 1];
  return value;
}

void frame(std::vector<std::uint8_t>& out, std::uint8_t kind, std::span<const std::uint8_t> payload) {
  out.push_back(kind);
  putLe(out, payload.size(), 4);
  out.insert(out.end(), payload.begin(), payload.end());
}

std::optional<Encoding> encodingFrom(std::uint8_t wire) {
  switch (wire) {
    case 1:
      return Encoding::F32Le;
    case 2:
      return Encoding::S16Le;
    default:
      return std::nullopt;
  }
}

}  // namespace

bool Format::valid() const {
  return (encoding == Encoding::F32Le || encoding == Encoding::S16Le) && (channels == 1 || channels == 2) &&
         (rate == 44100 || rate == 48000);
}

std::size_t Format::frameBytes() const { return (encoding == Encoding::F32Le ? 4U : 2U) * channels; }

bool encode(const ToServer& message, std::vector<std::uint8_t>& out) {
  if (const auto* format = std::get_if<Format>(&message)) {
    std::vector<std::uint8_t> payload{static_cast<std::uint8_t>(format->encoding), format->channels};
    putLe(payload, format->rate, 4);
    frame(out, kFormat, payload);
  } else if (const auto* pcm = std::get_if<Pcm>(&message)) {
    if (pcm->bytes.size() > kMaxPayload)
      return false;
    frame(out, kPcm, pcm->bytes);
  } else if (const auto* cork = std::get_if<Cork>(&message)) {
    const std::array<std::uint8_t, 1> payload{static_cast<std::uint8_t>(cork->on ? 1 : 0)};
    frame(out, kCork, payload);
  } else {
    frame(out, kFlush, {});
  }
  return true;
}

void encode(Played message, std::vector<std::uint8_t>& out) {
  std::vector<std::uint8_t> payload;
  putLe(payload, message.total, 8);
  frame(out, kPlayed, payload);
}

void Decoder::push(std::span<const std::uint8_t> bytes) {
  buffer_.erase(buffer_.begin(), buffer_.begin() + static_cast<std::ptrdiff_t>(start_));
  start_ = 0;
  buffer_.insert(buffer_.end(), bytes.begin(), bytes.end());
}

Result<std::optional<Decoder::Frame>, FrameError> Decoder::nextFrame(std::span<const std::uint8_t> known) const {
  const auto pending = std::span<const std::uint8_t>(buffer_).subspan(start_);
  if (pending.empty())
    return std::optional<Frame>{};
  const auto kind = pending[0];
  if (std::ranges::find(known, kind) == known.end())
    return FrameError::Unknown;
  if (pending.size() < kHeaderBytes)
    return std::optional<Frame>{};
  const auto length = getLe(pending.subspan(1, 4));
  if (length > kMaxPayload)
    return FrameError::TooLong;
  if (const auto fixed = fixedLength(kind); fixed && *fixed != length)
    return FrameError::Malformed;
  if (pending.size() < kHeaderBytes + length)
    return std::optional<Frame>{};
  return std::optional<Frame>{Frame{kind, pending.subspan(kHeaderBytes, static_cast<std::size_t>(length))}};
}

Result<std::optional<ToServer>, FrameError> Decoder::nextToServer() {
  const auto next = nextFrame(kToServer);
  if (!next.ok())
    return next.error();
  if (!next.value())
    return std::optional<ToServer>{};
  const auto [kind, payload] = *next.value();
  ToServer message;
  switch (kind) {
    case kFormat: {
      const auto encoding = encodingFrom(payload[0]);
      if (!encoding)
        return FrameError::Malformed;
      const Format format{*encoding, payload[1], static_cast<std::uint32_t>(getLe(payload.subspan(2, 4)))};
      if (!format.valid())
        return FrameError::Malformed;
      message = format;
      break;
    }
    case kPcm:
      message = Pcm{payload};
      break;
    case kCork:
      if (payload[0] > 1)
        return FrameError::Malformed;
      message = Cork{payload[0] == 1};
      break;
    default:
      message = Flush{};
      break;
  }
  consume(payload.size());
  return std::optional<ToServer>{message};
}

Result<std::optional<Played>, FrameError> Decoder::nextToShim() {
  const auto next = nextFrame(kToShim);
  if (!next.ok())
    return next.error();
  if (!next.value())
    return std::optional<Played>{};
  const auto payload = next.value()->payload;
  consume(payload.size());
  return std::optional<Played>{Played{getLe(payload)}};
}

}  // namespace cadence::pcmframe
