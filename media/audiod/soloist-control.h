#pragma once

#include "audiod/log.h"
#include "audiod/soloist-wire.h"
#include "audiod/sys.h"
#include "result.h"
#include "ws/client.h"

#include <chrono>
#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::audiod::soloist {

// reference §4.1's schedule, which the supervisor's run loop keeps: ws.port is polled every kPortTick until it
// appears, then every kTick; a start with no port by kPortWait, or no answer by kAnswerWait, is silent; a connection
// that closed while soloist runs is tried again kReconnect later
inline constexpr std::chrono::milliseconds kPortTick{100};
inline constexpr std::chrono::seconds kTick{1};
inline constexpr std::chrono::seconds kPortWait{10};
inline constexpr std::chrono::seconds kAnswerWait{30};
inline constexpr std::chrono::seconds kReconnect{1};

// a control command soloist cannot take: refused here (not logged in, no connection, a failed send), or answered with
// an error within controlAnswer
inline constexpr std::string_view kControlUnsupported = "control_unsupported";

// the port soloist wrote to <home>/ws.port: a regular file, never followed through a link nor waited on as a fifo,
// of at most 16 bytes whose text, ascii whitespace around it aside, is digits of 1-65535
std::optional<std::uint16_t> readPort(const std::string& home);
// removes ws.port and ws.addr before each start, so a stale port is never taken for the new one's; a link is removed,
// not followed. nothing there is no error
std::optional<SysError> clearPort(const std::string& home);

struct ControlOptions {
  ws::ClientOptions client{};
  // an error this soon after a control command is that command's answer
  std::chrono::milliseconds controlAnswer{2000};
  std::function<std::chrono::steady_clock::time_point()> now = [] { return std::chrono::steady_clock::now(); };
};

struct ControlCounts {
  // text messages received
  std::uint64_t messages = 0;
  // commands sent
  std::uint64_t controls = 0;
  std::uint64_t volumes = 0;
  // soloist's errors, and the controls that answered control_unsupported
  std::uint64_t errors = 0;
  std::uint64_t unsupported = 0;
};

// one connection to soloist's websocket, driven on its owner's thread like the client under it (poll fd(), then
// next() until nullopt). soloist starts its state over on each connection, so the owner starts its session over too.
//
// a control command is refused here when the owner says soloist is not logged in; soloist answers commands in order,
// so an error within controlAnswer of the latest command, when that was a control, is its answer, and so is a
// command_result that names it: the Error or Answer event carries the command. an error after a volume is only
// counted, and a result that names another command than the latest one answers none.
//
// logs soloist_ws_connected, soloist_ws_closed code=… with the counts, and soloist_command command=… code=ok|
// control_unsupported for each control: never an event's payload. destroying it closes with 1001 (going away), which
// waits on a stalled socket no longer than the send timeout or the client's stop fd allows
class Connection {
 public:
  Connection(const Log& log, ws::Client client, ControlOptions options);
  ~Connection();

  Connection(const Connection&) = delete;
  Connection& operator=(const Connection&) = delete;

  // -1 once closed
  [[nodiscard]] int fd() const { return client_.fd(); }
  [[nodiscard]] bool open() const { return client_.open(); }
  [[nodiscard]] const ControlCounts& counts() const { return counts_; }

  // the next event, read without blocking; nullopt when none is there yet or the connection closed
  std::optional<Event> next();
  // kMediaOk once sent, else kControlUnsupported
  std::string_view control(Command command, bool loggedIn);
  // set_volume with min(level, 100); false when it could not be sent
  bool setVolume(unsigned level);

 private:
  struct Sent {
    // empty for a volume
    std::optional<Command> control;
    std::chrono::steady_clock::time_point at;
  };

  bool send(const std::string& text, std::optional<Command> control);
  // the latest command, once, when it was a control sent within controlAnswer
  std::optional<Command> answered();
  // the same, for a command_result: only when it names the latest command
  std::optional<Command> answered(std::string_view name);
  void unsupported(Command command);
  // logs the close once
  void noteClosed();

  const Log& log_;
  ws::Client client_;
  const ControlOptions options_;
  ControlCounts counts_;
  std::optional<Sent> lastSent_;
  bool closeLogged_ = false;
};

// one try at ws://127.0.0.1:<port>/; a completed handshake is soloist's answer. the error is a fixed token (see
// ws::connectErrorToken), never logged here: the owner retries on its schedule
Result<std::unique_ptr<Connection>, std::string_view> connect(const Log& log, std::uint16_t port,
                                                              ControlOptions options = {});

}  // namespace cadence::audiod::soloist
