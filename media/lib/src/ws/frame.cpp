#include "ws/frame.h"

#include <algorithm>
#include <utility>

namespace cadence::ws {

namespace {

constexpr std::uint8_t kFin = 0x80;
constexpr std::uint8_t kReserved = 0x70;
constexpr std::uint8_t kMasked = 0x80;

bool known(unsigned opcode) {
  switch (static_cast<Opcode>(opcode)) {
    case Opcode::Continuation:
    case Opcode::Text:
    case Opcode::Binary:
    case Opcode::Close:
    case Opcode::Ping:
    case Opcode::Pong:
      return true;
  }
  return false;
}

// rfc 6455 §7.4: the codes a close frame may carry; 1012-1014 are registered with iana since
bool allowedCloseCode(std::uint16_t code) {
  return (code >= 1000 && code <= 1003) || (code >= 1007 && code <= 1014) || (code >= 3000 && code <= 4999);
}

std::uint64_t bigEndian(std::span<const std::uint8_t> bytes) {
  std::uint64_t value = 0;
  for (const auto byte : bytes)
    value = (value << 8) | byte;
  return value;
}

std::string text(std::span<const std::uint8_t> bytes) {
  return {reinterpret_cast<const char*>(bytes.data()), bytes.size()};
}

}  // namespace

std::vector<std::uint8_t> encodeClientFrame(Opcode opcode, std::span<const std::uint8_t> payload, MaskKey mask) {
  // the header first, then one allocation of the exact size (push_back after reserve trips gcc 14's
  // -Wfree-nonheap-object at -O3)
  std::array<std::uint8_t, 14> header{};
  std::size_t used = 0;
  header[used++] = static_cast<std::uint8_t>(kFin | static_cast<std::uint8_t>(opcode));
  if (payload.size() < 126) {
    header[used++] = static_cast<std::uint8_t>(kMasked | payload.size());
  } else if (payload.size() <= 0xFFFF) {
    header[used++] = kMasked | 126;
    header[used++] = static_cast<std::uint8_t>(payload.size() >> 8);
    header[used++] = static_cast<std::uint8_t>(payload.size());
  } else {
    header[used++] = kMasked | 127;
    for (int shift = 56; shift >= 0; shift -= 8)
      header[used++] = static_cast<std::uint8_t>(static_cast<std::uint64_t>(payload.size()) >> shift);
  }
  for (const auto byte : mask)
    header[used++] = byte;
  std::vector<std::uint8_t> frame(used + payload.size());
  std::copy_n(header.begin(), used, frame.begin());
  for (std::size_t i = 0; i < payload.size(); ++i)
    frame[used + i] = static_cast<std::uint8_t>(payload[i] ^ mask[i % mask.size()]);
  return frame;
}

MessageReader::MessageReader(std::size_t maxMessage) : maxMessage_(maxMessage) {}

void MessageReader::append(std::span<const std::uint8_t> bytes) {
  if (ended_)
    return;
  // what was parsed goes once it is half of the buffer, so a message read in many pieces is not moved for each
  if (offset_ == buffer_.size()) {
    buffer_.clear();
    offset_ = 0;
  } else if (offset_ > 0 && offset_ >= buffer_.size() / 2) {
    buffer_.erase(buffer_.begin(), buffer_.begin() + static_cast<std::ptrdiff_t>(offset_));
    offset_ = 0;
  }
  buffer_.insert(buffer_.end(), bytes.begin(), bytes.end());
}

std::optional<Incoming> MessageReader::next() {
  while (!ended_) {
    const auto data = std::span<const std::uint8_t>(buffer_).subspan(offset_);
    if (data.size() < 2)
      return std::nullopt;
    const bool fin = (data[0] & kFin) != 0;
    const unsigned opcode = data[0] & 0x0FU;
    const bool control = (opcode & 0x08U) != 0;
    if ((data[0] & kReserved) != 0 || (data[1] & kMasked) != 0 || !known(opcode))
      return fail(kCloseProtocolError);
    std::uint64_t length = data[1] & 0x7FU;
    std::size_t header = 2;
    if (control && (!fin || length > kMaxControlPayload))
      return fail(kCloseProtocolError);
    if (length == 126) {
      if (data.size() < 4)
        return std::nullopt;
      length = bigEndian(data.subspan(2, 2));
      header = 4;
    } else if (length == 127) {
      if (data.size() < 10)
        return std::nullopt;
      length = bigEndian(data.subspan(2, 8));
      header = 10;
      if ((length >> 63) != 0)
        return fail(kCloseProtocolError);
    }
    const bool continuation = static_cast<Opcode>(opcode) == Opcode::Continuation;
    if (!control) {
      if (continuation != fragmented_)
        return fail(kCloseProtocolError);
      const std::size_t before = continuation ? messageBytes_ : 0;
      if (length > maxMessage_ - before)
        return fail(kCloseTooBig);
    }
    if (data.size() - header < length)
      return std::nullopt;
    const auto payload = data.subspan(header, static_cast<std::size_t>(length));
    offset_ += header + payload.size();
    switch (static_cast<Opcode>(opcode)) {
      case Opcode::Ping:
        return Incoming{.kind = Incoming::Kind::Ping, .data = text(payload), .code = 0};
      case Opcode::Pong:
        continue;
      case Opcode::Close: {
        if (payload.size() == 1)
          return fail(kCloseProtocolError);
        const auto code = payload.empty() ? kCloseNoStatus : static_cast<std::uint16_t>(bigEndian(payload.first(2)));
        if (!payload.empty() && !allowedCloseCode(code))
          return fail(kCloseProtocolError);
        ended_ = true;
        buffer_.clear();
        offset_ = 0;
        return Incoming{.kind = Incoming::Kind::Close, .data = {}, .code = code};
      }
      case Opcode::Text:
      case Opcode::Binary:
        text_ = static_cast<Opcode>(opcode) == Opcode::Text;
        message_.clear();
        messageBytes_ = 0;
        break;
      case Opcode::Continuation:
        break;
    }
    messageBytes_ += payload.size();
    if (text_)
      message_.append(reinterpret_cast<const char*>(payload.data()), payload.size());
    fragmented_ = !fin;
    if (fin && text_)
      return Incoming{.kind = Incoming::Kind::Text, .data = std::exchange(message_, {}), .code = 0};
  }
  return std::nullopt;
}

std::optional<Incoming> MessageReader::fail(std::uint16_t code) {
  ended_ = true;
  buffer_.clear();
  offset_ = 0;
  message_.clear();
  return Incoming{.kind = Incoming::Kind::Fail, .data = {}, .code = code};
}

}  // namespace cadence::ws
