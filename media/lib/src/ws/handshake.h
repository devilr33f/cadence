#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <string_view>

namespace cadence::ws {

// the random bytes behind Sec-WebSocket-Key
inline constexpr std::size_t kKeyBytes = 16;
// the longest response head read before the handshake is refused
inline constexpr std::size_t kMaxResponseHead = 8192;

// the opening request for ws://127.0.0.1:<port>/; key is the base64 of kKeyBytes random bytes
std::string handshakeRequest(std::uint16_t port, std::string_view key);
// what the server answers for key: base64(sha1(key + the guid of rfc 6455))
std::string acceptFor(std::string_view key);

enum class HandshakeStatus : std::uint8_t { Incomplete, Accepted, Refused };

struct HandshakeCheck {
  HandshakeStatus status = HandshakeStatus::Incomplete;
  // when accepted, the length of the response head: what follows it are the server's first frames
  std::size_t headBytes = 0;
};

// the response received so far, judged once its head is whole (rfc 6455 §4.1): status 101, Upgrade websocket, a
// Connection with the upgrade token, the accept of key, and neither an extension nor a subprotocol, as none was asked
// for. a head longer than kMaxResponseHead is refused
HandshakeCheck checkResponse(std::string_view received, std::string_view key);

}  // namespace cadence::ws
