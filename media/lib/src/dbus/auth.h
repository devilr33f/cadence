#pragma once

#include <cstddef>
#include <cstdint>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::dbus {

// the socket path of the first unix:path= address in a server address list (values unescaped as the specification
// says); nullopt when there is none
std::optional<std::string> unixSocketPath(std::string_view address);
// the unix:path= address of path, every byte outside the optionally-escaped set (backslash included) escaped as %xx
std::string unixAddress(std::string_view path);

// the client's side of the authentication protocol: SASL EXTERNAL with a uid and nothing else; unix fd passing is never
// negotiated. pure: the connection moves its bytes
class AuthClient {
 public:
  enum class State : std::uint8_t { Waiting, Authenticated, Failed };

  // the server's lines are short; a longer one is a broken peer
  static constexpr std::size_t kMaxLine = 512;

  explicit AuthClient(std::uint32_t uid) : uid_(uid) {}

  // the nul byte and the AUTH line, sent once connected
  [[nodiscard]] std::string greeting() const;
  // the server's bytes as they arrive; what to send back: BEGIN after OK, ERROR for a command it does not know
  std::string receive(std::string_view bytes);

  [[nodiscard]] State state() const { return state_; }
  // the server's guid, once authenticated
  [[nodiscard]] const std::string& guid() const { return guid_; }
  // what came after the OK line: the first bytes of the message stream
  [[nodiscard]] const std::string& rest() const { return pending_; }

 private:
  std::string line(std::string_view line);

  std::uint32_t uid_;
  State state_ = State::Waiting;
  std::string pending_;
  std::string guid_;
};

}  // namespace cadence::dbus
