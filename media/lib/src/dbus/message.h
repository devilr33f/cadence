#pragma once

#include "dbus/marshal.h"
#include "result.h"

#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::dbus {

// any other value is a type of a later specification, which a receiver ignores
enum class MessageType : std::uint8_t { MethodCall = 1, MethodReturn = 2, Error = 3, Signal = 4 };

inline constexpr std::uint8_t kFlagNoReplyExpected = 0x1;

struct Message {
  MessageType type = MessageType::MethodCall;
  std::uint8_t flags = 0;
  // set by the connection that sends it
  std::uint32_t serial = 0;
  // empty when absent
  std::string path;
  std::string interface;
  std::string member;
  std::string errorName;
  std::optional<std::uint32_t> replySerial;
  std::string destination;
  std::string sender;
  std::vector<Value> body;

  // the body's signature
  [[nodiscard]] std::string signature() const;

  static Message methodCall(std::string destination, std::string path, std::string interface, std::string member,
                            std::vector<Value> body = {});
  static Message signal(std::string path, std::string interface, std::string member, std::vector<Value> body = {});
  static Message methodReturn(const Message& call, std::vector<Value> body = {});
  static Message error(const Message& call, std::string name, std::string text);
};

// interface names, which error names follow too
bool validInterfaceName(std::string_view name);
bool validMemberName(std::string_view name);
// unique (":1.5") and well-known names
bool validBusName(std::string_view name);

// the whole message, little-endian, under its serial; refused when it breaks a rule of the specification or is over
// kMaxMessage
Result<std::vector<std::uint8_t>, WireError> encodeMessage(const Message& message);
// exactly one whole message of either byte order. a known header field of the wrong type, a required one missing, an
// invalid name and UNIX_FDS are refused; unknown header fields are ignored
Result<Message, WireError> decodeMessage(std::span<const std::uint8_t> bytes);
// the bytes the message that starts with these takes, from its first 16; Truncated while fewer are there
Result<std::size_t, WireError> messageSize(std::span<const std::uint8_t> bytes);

// the peer's stream cut into messages, each decoded once whole; one over kMaxMessage is refused from its first 16
// bytes. after a failure it yields nothing more
class MessageReader {
 public:
  void append(std::span<const std::uint8_t> bytes);
  // nullopt while no whole message is there, or after a failure
  std::optional<Message> next();
  // whether next() has something to hand out or a failure to find without more bytes
  [[nodiscard]] bool ready() const;
  [[nodiscard]] std::optional<WireError> failure() const { return failure_; }

 private:
  std::vector<std::uint8_t> buffer_;
  // where the unread bytes start in buffer_
  std::size_t offset_ = 0;
  std::optional<WireError> failure_;
};

}  // namespace cadence::dbus
