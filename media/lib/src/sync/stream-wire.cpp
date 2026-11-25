#include "sync/stream-wire.h"

#include <pb_decode.h>
#include <pb_encode.h>

#include <algorithm>
#include <cstring>
#include <fcntl.h>
#include <unistd.h>

namespace cadence::sync {

namespace {

std::span<const std::uint8_t> bytesOf(std::string_view s) {
  return {reinterpret_cast<const std::uint8_t*>(s.data()), s.size()};
}

void append(std::vector<std::uint8_t>& out, std::span<const std::uint8_t> bytes) {
  out.insert(out.end(), bytes.begin(), bytes.end());
}

Sha256Digest labelled(std::span<const std::uint8_t> key, std::string_view label, std::span<const std::uint8_t> a,
                      std::span<const std::uint8_t> b, std::span<const std::uint8_t> c = {}) {
  std::vector<std::uint8_t> data;
  data.reserve(label.size() + a.size() + b.size() + c.size());
  append(data, bytesOf(label));
  append(data, a);
  append(data, b);
  append(data, c);
  return hmacSha256(key, data);
}

void putU32(std::uint8_t* p, std::uint32_t v) {
  p[0] = static_cast<std::uint8_t>(v >> 24);
  p[1] = static_cast<std::uint8_t>(v >> 16);
  p[2] = static_cast<std::uint8_t>(v >> 8);
  p[3] = static_cast<std::uint8_t>(v);
}

std::uint32_t getU32(const std::uint8_t* p) {
  return static_cast<std::uint32_t>(p[0]) << 24 | static_cast<std::uint32_t>(p[1]) << 16 |
         static_cast<std::uint32_t>(p[2]) << 8 | static_cast<std::uint32_t>(p[3]);
}

}  // namespace

StreamTag streamTag(std::span<const std::uint8_t> key, std::uint64_t counter, std::span<const std::uint8_t> body) {
  std::vector<std::uint8_t> data(8 + body.size());
  for (int i = 0; i < 8; ++i)
    data[static_cast<std::size_t>(i)] = static_cast<std::uint8_t>(counter >> (56 - 8 * i));
  std::copy(body.begin(), body.end(), data.begin() + 8);
  const auto mac = hmacSha256(key, data);
  StreamTag tag{};
  std::copy_n(mac.begin(), kStreamTagBytes, tag.begin());
  return tag;
}

Sha256Digest helloMac(std::span<const std::uint8_t> key, std::span<const std::uint8_t> leaderNonce,
                      std::span<const std::uint8_t> memberNonce, std::string_view memberId) {
  return labelled(key, "cadence-sync-hello", leaderNonce, memberNonce, bytesOf(memberId));
}

Sha256Digest welcomeMac(std::span<const std::uint8_t> key, std::span<const std::uint8_t> memberNonce,
                        std::span<const std::uint8_t> leaderNonce) {
  return labelled(key, "cadence-sync-welcome", memberNonce, leaderNonce);
}

std::vector<std::uint8_t> sessionKey(std::span<const std::uint8_t> key, std::span<const std::uint8_t> leaderNonce,
                                     std::span<const std::uint8_t> memberNonce) {
  const auto d = labelled(key, "cadence-sync-session", leaderNonce, memberNonce);
  return {d.begin(), d.end()};
}

bool randomNonce(StreamNonce& out) {
  const int fd = ::open("/dev/urandom", O_RDONLY | O_CLOEXEC);
  if (fd < 0)
    return false;
  std::size_t got = 0;
  while (got < out.size()) {
    const auto n = ::read(fd, out.data() + got, out.size() - got);
    if (n <= 0)
      break;
    got += static_cast<std::size_t>(n);
  }
  ::close(fd);
  return got == out.size();
}

void StreamSealer::rekey(std::vector<std::uint8_t> key) {
  key_ = std::move(key);
  counter_ = 0;
}

bool StreamSealer::seal(const SyncEnvelope& envelope, std::vector<std::uint8_t>& out) {
  std::size_t size = 0;
  if (!pb_get_encoded_size(&size, cadence_sync_v1_Envelope_fields, &envelope) || size > kStreamMaxMessageBytes)
    return false;
  body_.resize(size);
  auto stream = pb_ostream_from_buffer(body_.data(), body_.size());
  if (!pb_encode(&stream, cadence_sync_v1_Envelope_fields, &envelope))
    return false;
  const auto tag = streamTag(key_, counter_++, body_);
  const auto start = out.size();
  out.resize(start + kStreamLengthBytes);
  putU32(out.data() + start, static_cast<std::uint32_t>(size));
  append(out, body_);
  append(out, tag);
  return true;
}

void StreamOpener::rekey(std::vector<std::uint8_t> key) {
  key_ = std::move(key);
  counter_ = 0;
}

void StreamOpener::feed(std::span<const std::uint8_t> bytes) {
  if (refused_)
    return;
  // what was consumed goes once it is the larger part, so the buffer never grows past a few messages
  if (at_ > 0 && at_ * 2 >= buf_.size()) {
    buf_.erase(buf_.begin(), buf_.begin() + static_cast<std::ptrdiff_t>(at_));
    at_ = 0;
  }
  append(buf_, bytes);
}

OpenResult StreamOpener::next(SyncEnvelope& out) {
  if (refused_)
    return OpenResult::Refused;
  const auto have = buf_.size() - at_;
  if (have < kStreamLengthBytes)
    return OpenResult::Need;
  const auto size = getU32(buf_.data() + at_);
  if (size > kStreamMaxMessageBytes) {
    refused_ = true;
    return OpenResult::Refused;
  }
  const auto whole = kStreamLengthBytes + size + kStreamTagBytes;
  if (have < whole)
    return OpenResult::Need;
  const std::span<const std::uint8_t> body(buf_.data() + at_ + kStreamLengthBytes, size);
  const std::span<const std::uint8_t> tag(body.data() + size, kStreamTagBytes);
  const auto want = streamTag(key_, counter_, body);
  if (!equalConstantTime(want, tag)) {
    refused_ = true;
    return OpenResult::Refused;
  }
  out = cadence_sync_v1_Envelope_init_zero;
  auto stream = pb_istream_from_buffer(body.data(), body.size());
  if (!pb_decode(&stream, cadence_sync_v1_Envelope_fields, &out) || out.which_body == 0) {
    refused_ = true;
    return OpenResult::Refused;
  }
  ++counter_;
  at_ += whole;
  return OpenResult::Message;
}

}  // namespace cadence::sync
