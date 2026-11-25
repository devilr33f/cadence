#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <vector>

namespace cadence::ws {

enum class Opcode : std::uint8_t {
  Continuation = 0x0,
  Text = 0x1,
  Binary = 0x2,
  Close = 0x8,
  Ping = 0x9,
  Pong = 0xA,
};

inline constexpr std::uint16_t kCloseNormal = 1000;
inline constexpr std::uint16_t kCloseGoingAway = 1001;
inline constexpr std::uint16_t kCloseProtocolError = 1002;
// never on the wire: a close frame that carried no code
inline constexpr std::uint16_t kCloseNoStatus = 1005;
// never on the wire: the connection ended without a close frame
inline constexpr std::uint16_t kCloseAbnormal = 1006;
inline constexpr std::uint16_t kCloseTooBig = 1009;

inline constexpr std::size_t kMaxControlPayload = 125;

using MaskKey = std::array<std::uint8_t, 4>;

// one final frame from the client, masked with mask as rfc 6455 requires of every client frame
std::vector<std::uint8_t> encodeClientFrame(Opcode opcode, std::span<const std::uint8_t> payload, MaskKey mask);

// what the server's stream yields
struct Incoming {
  enum class Kind : std::uint8_t { Text, Ping, Close, Fail };
  Kind kind = Kind::Text;
  // a whole text message, or a ping's payload
  std::string data{};
  // a close: the server's code, kCloseNoStatus when it gave none; a failure: the code to close with
  std::uint16_t code = 0;
};

// the server's frames, parsed as they arrive: whole text messages out of their fragments, pings between them and the
// close (its reason is dropped); binary messages and pongs are read and dropped. a frame against rfc 6455 (masked,
// reserved bits or opcode, a fragmented or long control frame, a continuation out of place, a 64-bit length with its
// top bit set, a close code §7.4 does not allow) fails the stream with 1002, a message over maxMessage with 1009, known
// from the frame headers before any of its payload is buffered. after a close or a failure it yields nothing more.
//
// text is not checked for utf-8 (rfc 6455 would close with 1007): the one peer is soloist on the loopback, and its
// messages go to a json parser that takes invalid utf-8 for no event
class MessageReader {
 public:
  explicit MessageReader(std::size_t maxMessage);

  void append(std::span<const std::uint8_t> bytes);
  // nullopt while the next whole message or control frame has not arrived
  std::optional<Incoming> next();

 private:
  std::optional<Incoming> fail(std::uint16_t code);

  std::size_t maxMessage_;
  std::vector<std::uint8_t> buffer_;
  // where the unparsed bytes start in buffer_
  std::size_t offset_ = 0;
  // inside a fragmented message, and whether it is text (kept) or binary (only counted)
  bool fragmented_ = false;
  bool text_ = false;
  std::string message_;
  std::size_t messageBytes_ = 0;
  bool ended_ = false;
};

}  // namespace cadence::ws
