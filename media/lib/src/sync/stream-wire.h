#pragma once

#include "sync/sha256.h"

#include "cadence/sync/v1/sync.pb.h"

#include <array>
#include <cstddef>
#include <cstdint>
#include <span>
#include <string_view>
#include <vector>

// the group stream's framing and authentication (proto/cadence/sync/v1/sync.proto's header): a big-endian u32
// length, the Envelope, then the first 16 bytes of hmac-sha256(key, u64 counter big-endian || envelope), the counter
// per direction from 0. the handshake is tagged under the group key, everything after Welcome under the session key,
// the counters starting again at 0. a bad tag, a counter out of order or a message over 64 KiB refuses the stream
namespace cadence::sync {

using SyncEnvelope = cadence_sync_v1_Envelope;

inline constexpr std::uint32_t kStreamProtocol = 1;
inline constexpr std::size_t kStreamMaxMessageBytes = 64 * 1024;
inline constexpr std::size_t kStreamTagBytes = 16;
inline constexpr std::size_t kStreamNonceBytes = 16;
inline constexpr std::size_t kStreamLengthBytes = 4;

using StreamTag = std::array<std::uint8_t, kStreamTagBytes>;
using StreamNonce = std::array<std::uint8_t, kStreamNonceBytes>;

StreamTag streamTag(std::span<const std::uint8_t> key, std::uint64_t counter, std::span<const std::uint8_t> body);
// hmac-sha256(group_key, "cadence-sync-hello" || leader_nonce || member_nonce || member_id)
Sha256Digest helloMac(std::span<const std::uint8_t> key, std::span<const std::uint8_t> leaderNonce,
                      std::span<const std::uint8_t> memberNonce, std::string_view memberId);
// hmac-sha256(group_key, "cadence-sync-welcome" || member_nonce || leader_nonce)
Sha256Digest welcomeMac(std::span<const std::uint8_t> key, std::span<const std::uint8_t> memberNonce,
                        std::span<const std::uint8_t> leaderNonce);
// hmac-sha256(group_key, "cadence-sync-session" || leader_nonce || member_nonce)
std::vector<std::uint8_t> sessionKey(std::span<const std::uint8_t> key, std::span<const std::uint8_t> leaderNonce,
                                     std::span<const std::uint8_t> memberNonce);
// from the system's entropy; false when it gave none
bool randomNonce(StreamNonce& out);

// one direction's sealing, one thread
class StreamSealer {
 public:
  explicit StreamSealer(std::vector<std::uint8_t> key = {}) : key_(std::move(key)) {}
  // the session key after Welcome: the counter starts again at 0
  void rekey(std::vector<std::uint8_t> key);
  // appends the length, the envelope and its tag; false when it does not encode (out unchanged)
  bool seal(const SyncEnvelope& envelope, std::vector<std::uint8_t>& out);
  [[nodiscard]] std::uint64_t counter() const { return counter_; }

 private:
  std::vector<std::uint8_t> key_;
  std::uint64_t counter_ = 0;
  std::vector<std::uint8_t> body_;
};

enum class OpenResult : std::uint8_t { Need, Message, Refused };

// the other direction: bytes as they arrive, messages out one at a time (so a caller rekeys between two), one thread.
// once refused it stays refused
class StreamOpener {
 public:
  explicit StreamOpener(std::vector<std::uint8_t> key = {}) : key_(std::move(key)) {}
  void rekey(std::vector<std::uint8_t> key);
  void feed(std::span<const std::uint8_t> bytes);
  // the next whole message, decoded into out; Refused for a bad tag, a length over the bound or an envelope that
  // does not decode
  OpenResult next(SyncEnvelope& out);
  [[nodiscard]] std::uint64_t counter() const { return counter_; }

 private:
  std::vector<std::uint8_t> key_;
  std::uint64_t counter_ = 0;
  std::vector<std::uint8_t> buf_;
  std::size_t at_ = 0;
  bool refused_ = false;
};

}  // namespace cadence::sync
