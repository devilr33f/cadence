#pragma once

#include <concepts>
#include <cstdint>
#include <functional>
#include <initializer_list>
#include <string_view>
#include <utility>
#include <variant>

namespace cadence::audiod {

// one key of a log line: a number or a fixed token, never audio, payloads or text from a peer
class LogField {
 public:
  using Value = std::variant<std::int64_t, std::uint64_t, std::string_view>;

  LogField(std::string_view key, std::string_view token) : key_(key), value_(token) {}
  LogField(std::string_view key, const char* token) : key_(key), value_(std::string_view(token)) {}
  template <std::signed_integral T>
  LogField(std::string_view key, T number) : key_(key), value_(static_cast<std::int64_t>(number)) {}
  template <std::unsigned_integral T>
  LogField(std::string_view key, T number) : key_(key), value_(static_cast<std::uint64_t>(number)) {}

  [[nodiscard]] std::string_view key() const { return key_; }
  [[nodiscard]] const Value& value() const { return value_; }

 private:
  std::string_view key_;
  Value value_;
};

// json lines like cadenced's own ({"level":"INFO","msg":"ipc_client_connected",...}); cadenced forwards
// audiod's stderr as its logs. the sink gets one whole line per event, newline included, and may be called
// from any thread that logs
class Log {
 public:
  using Sink = std::function<void(std::string_view line)>;

  explicit Log(Sink sink) : sink_(std::move(sink)) {}

  // a pipe or socket becomes non-blocking: a line that finds it full is dropped, never waited for, and the
  // next line that fits is preceded by log_dropped with the count
  static Log toDescriptor(int fd);
  static Log toStderr();

  void debug(std::string_view msg, std::initializer_list<LogField> fields = {}) const;
  void info(std::string_view msg, std::initializer_list<LogField> fields = {}) const;
  void warn(std::string_view msg, std::initializer_list<LogField> fields = {}) const;
  void error(std::string_view msg, std::initializer_list<LogField> fields = {}) const;

 private:
  void write(std::string_view level, std::string_view msg, std::initializer_list<LogField> fields) const;

  Sink sink_;
};

}  // namespace cadence::audiod
