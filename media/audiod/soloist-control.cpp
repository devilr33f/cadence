#include "audiod/soloist-control.h"

#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <array>
#include <cerrno>
#include <utility>

namespace cadence::audiod::soloist {

namespace {

constexpr const char* kPortFile = "ws.port";
constexpr const char* kAddressFile = "ws.addr";
// a port is at most five digits: a longer file is none (v1's bound, which leading zeros may fill)
constexpr std::size_t kPortFileMax = 16;
constexpr unsigned kMaxPort = 65535;

// rust's trim_ascii, which v1 read the file with
bool asciiSpace(char c) { return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'; }

std::string_view trim(std::string_view text) {
  while (!text.empty() && asciiSpace(text.front()))
    text.remove_prefix(1);
  while (!text.empty() && asciiSpace(text.back()))
    text.remove_suffix(1);
  return text;
}

}  // namespace

std::optional<std::uint16_t> readPort(const std::string& home) {
  const UniqueFd dir(::open(home.c_str(), O_RDONLY | O_DIRECTORY | O_CLOEXEC));
  if (!dir)
    return std::nullopt;
  // soloist owns the file: a link is never followed and a fifo never waited on
  const UniqueFd file(::openat(dir.get(), kPortFile, O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_NOCTTY | O_CLOEXEC));
  if (!file)
    return std::nullopt;
  struct stat info {};
  if (::fstat(file.get(), &info) != 0 || !S_ISREG(info.st_mode))
    return std::nullopt;
  std::array<char, kPortFileMax + 1> buffer{};
  std::size_t used = 0;
  while (used < buffer.size()) {
    const auto got = ::read(file.get(), buffer.data() + used, buffer.size() - used);
    if (got < 0 && errno == EINTR)
      continue;
    if (got < 0)
      return std::nullopt;
    if (got == 0)
      break;
    used += static_cast<std::size_t>(got);
  }
  // read whole or refused: nothing may hide behind the bytes that were read
  if (used > kPortFileMax)
    return std::nullopt;
  const auto text = trim(std::string_view(buffer.data(), used));
  if (text.empty())
    return std::nullopt;
  unsigned port = 0;
  for (const char c : text) {
    if (c < '0' || c > '9')
      return std::nullopt;
    port = port * 10 + static_cast<unsigned>(c - '0');
    if (port > kMaxPort)
      return std::nullopt;
  }
  if (port == 0)
    return std::nullopt;
  return static_cast<std::uint16_t>(port);
}

std::optional<SysError> clearPort(const std::string& home) {
  const UniqueFd dir(::open(home.c_str(), O_RDONLY | O_DIRECTORY | O_CLOEXEC));
  if (!dir)
    return SysError{.call = "open", .code = errno};
  for (const char* name : {kPortFile, kAddressFile})
    if (::unlinkat(dir.get(), name, 0) != 0 && errno != ENOENT)
      return SysError{.call = "unlinkat", .code = errno};
  return std::nullopt;
}

Result<std::unique_ptr<Connection>, std::string_view> connect(const Log& log, std::uint16_t port,
                                                              ControlOptions options) {
  auto client = ws::Client::connect(port, options.client);
  if (!client.ok())
    return ws::connectErrorToken(client.error());
  return std::make_unique<Connection>(log, std::move(client.value()), std::move(options));
}

Connection::Connection(const Log& log, ws::Client client, ControlOptions options)
    : log_(log), client_(std::move(client)), options_(std::move(options)) {
  log_.info("soloist_ws_connected");
}

Connection::~Connection() {
  client_.close(ws::kCloseGoingAway);
  noteClosed();
}

std::optional<Event> Connection::next() {
  auto text = client_.next();
  if (!text) {
    noteClosed();
    return std::nullopt;
  }
  ++counts_.messages;
  auto event = decode(*text);
  if (auto* error = std::get_if<Error>(&event)) {
    ++counts_.errors;
    error->unsupported = answered();
    if (error->unsupported)
      unsupported(*error->unsupported);
  } else if (auto* answer = std::get_if<Answer>(&event)) {
    answer->control = answered(answer->name);
  }
  return event;
}

std::string_view Connection::control(Command command, bool loggedIn) {
  // logged out, soloist would only answer an error
  if (!loggedIn || !send(commandText(command), command)) {
    unsupported(command);
    return kControlUnsupported;
  }
  ++counts_.controls;
  log_.info("soloist_command", {{"command", commandName(command)}, {"code", "ok"}});
  return kMediaOk;
}

bool Connection::setVolume(unsigned level) {
  if (!send(setVolumeText(level), std::nullopt))
    return false;
  ++counts_.volumes;
  return true;
}

// a failed send drops the connection
bool Connection::send(const std::string& text, std::optional<Command> control) {
  if (!client_.send(text)) {
    noteClosed();
    return false;
  }
  lastSent_ = Sent{.control = control, .at = options_.now()};
  return true;
}

std::optional<Command> Connection::answered(std::string_view name) {
  if (!lastSent_ || name.empty() || name != (lastSent_->control ? commandName(*lastSent_->control) : kSetVolumeName))
    return std::nullopt;
  return answered();
}

std::optional<Command> Connection::answered() {
  const auto sent = std::exchange(lastSent_, std::nullopt);
  if (!sent || !sent->control || options_.now() - sent->at >= options_.controlAnswer)
    return std::nullopt;
  return sent->control;
}

void Connection::unsupported(Command command) {
  ++counts_.unsupported;
  log_.info("soloist_command", {{"command", commandName(command)}, {"code", kControlUnsupported}});
}

void Connection::noteClosed() {
  if (client_.open() || closeLogged_)
    return;
  closeLogged_ = true;
  lastSent_.reset();
  log_.info("soloist_ws_closed", {{"code", client_.closeCode()},
                                  {"messages", counts_.messages},
                                  {"controls", counts_.controls},
                                  {"volumes", counts_.volumes},
                                  {"errors", counts_.errors}});
}

}  // namespace cadence::audiod::soloist
