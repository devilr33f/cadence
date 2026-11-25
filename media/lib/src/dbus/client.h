#pragma once

#include "dbus/connection.h"
#include "dbus/io.h"
#include "dbus/marshal.h"
#include "dbus/message.h"
#include "result.h"

#include <chrono>
#include <cstdint>
#include <functional>
#include <map>
#include <optional>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

namespace cadence::dbus {

inline constexpr std::chrono::milliseconds kDefaultCallTimeout{2000};

enum class CallStatus : std::uint8_t {
  Ok,
  Error,         // the peer answered with an error
  Timeout,       // no answer within the call's timeout
  Disconnected,  // the connection closed first
  Invalid,       // the call broke a rule and was not sent
  Stopped,       // callAndWait gave up when the stop fd turned readable
};

struct Reply {
  CallStatus status = CallStatus::Disconnected;
  // an error's name
  std::string errorName;
  // a return's values, an error's arguments: the peer's text, never to be logged
  std::vector<Value> body;
};

using ReplyHandler = std::function<void(Reply reply)>;
using SignalHandler = std::function<void(const Message& signal)>;

// what an exported method answers: its values, or an error when errorName is set
struct MethodResult {
  std::vector<Value> values;
  std::string errorName;
  std::string errorText;
};

using MethodHandler = std::function<MethodResult(const Message& call)>;

struct Property {
  std::function<Value()> get;
  // empty for a read-only property; false refuses the value
  std::function<bool(const Value& value)> set;
};

// an interface of an exported object: the minimum a stand-in service needs
struct Interface {
  std::string name;
  std::map<std::string, MethodHandler, std::less<>> methods;
  std::map<std::string, Property, std::less<>> properties;
};

// a client of a message bus: Hello, method calls matched to their replies by REPLY_SERIAL with a timeout each, signals
// to one handler, and objects it exports with their properties (org.freedesktop.DBus.Properties answered for them).
// poll-driven like its connection, no threads: the owner polls fd() for reading until nextDeadline(), then calls
// process(), which reads, dispatches and times out; while hasBuffered() it calls process() without polling (right after
// connect, the bus's first signals may already be read). handlers run inside process() (or callAndWait()); they may
// call and send but must not destroy the client or call callAndWait
class Client {
 public:
  // the connection and the Hello, within the options' connectTimeout
  static Result<Client, ConnectError> connect(std::string_view address, ConnectionOptions options = {});

  Client(Client&&) noexcept = default;
  Client& operator=(Client&&) noexcept = default;
  Client(const Client&) = delete;
  Client& operator=(const Client&) = delete;
  ~Client() = default;

  [[nodiscard]] int fd() const { return connection_.fd(); }
  [[nodiscard]] bool open() const { return connection_.open(); }
  [[nodiscard]] CloseCode closeCode() const { return connection_.closeCode(); }
  [[nodiscard]] std::optional<WireError> wireError() const { return connection_.wireError(); }
  // the unique name Hello gave
  [[nodiscard]] const std::string& uniqueName() const { return uniqueName_; }
  // whole messages already read wait for process(): the owner must not wait on fd() first
  [[nodiscard]] bool hasBuffered() const { return connection_.hasBuffered(); }

  // sends a method call; its handler gets the reply, the timeout or the disconnect, always from a later process(). the
  // call's serial; 0 when it was not sent (open() says whether the connection closed), and the handler is dropped
  std::uint32_t call(Message call, ReplyHandler handler, std::chrono::milliseconds timeout = kDefaultCallTimeout);
  // sends a method call and processes the connection until its reply, the timeout, the disconnect or the stop fd;
  // everything else that arrives meanwhile is dispatched as process() would
  Reply callAndWait(Message call, std::chrono::milliseconds timeout = kDefaultCallTimeout);
  // a signal, or a call that expects no reply; false when it was not sent
  bool send(Message message);
  void setSignalHandler(SignalHandler handler) { signalHandler_ = std::move(handler); }

  // serves interface at path: its methods, and Get, GetAll and Set for its properties; a second one with the same name
  // replaces the first
  void exportInterface(std::string path, Interface interface);
  bool emitPropertiesChanged(std::string path, std::string interface,
                             std::vector<std::pair<std::string, Value>> changed,
                             std::vector<std::string> invalidated = {});

  // reads what is there (one drain), dispatches it and times out calls; after a disconnect every pending call gets
  // Disconnected
  void process();
  // the earliest call timeout, when one is pending
  [[nodiscard]] std::optional<Clock::time_point> nextDeadline() const;
  void close();

 private:
  struct Pending {
    ReplyHandler handler;
    Clock::time_point deadline;
  };

  explicit Client(Connection connection) : connection_(std::move(connection)) {}

  void dispatch(Message message);
  void answer(const Message& call);
  MethodResult properties(const Message& call, const std::vector<Interface>& interfaces);
  void expire(Clock::time_point now);
  void failPending();

  Connection connection_;
  std::string uniqueName_;
  std::map<std::uint32_t, Pending> pending_;
  SignalHandler signalHandler_;
  std::map<std::string, std::vector<Interface>, std::less<>> objects_;
};

}  // namespace cadence::dbus
