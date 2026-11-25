#pragma once

#include "cadence/screen/v1/screen.pb.h"

#include <cstdint>
#include <functional>
#include <initializer_list>
#include <mutex>
#include <string>
#include <string_view>
#include <utility>
#include <variant>
#include <vector>

namespace cadence::screen::ipc {

// a token value: only [a-z0-9_.:-]{1,64} survives, anything else is logged as "invalid", so a title or a url can never
// reach a log line through one (rule 8)
class Token {
 public:
  Token(const char* value) : Token(std::string_view(value)) {}  // NOLINT(google-explicit-constructor)
  Token(std::string_view value);                                 // NOLINT(google-explicit-constructor)
  [[nodiscard]] const std::string& value() const { return value_; }

 private:
  std::string value_;
};

struct Field {
  Field(const char* k, Token v) : key(k), value(std::move(v)) {}
  // without it a literal would bind to bool
  Field(const char* k, const char* v) : key(k), value(Token(v)) {}
  Field(const char* k, std::int64_t v) : key(k), value(v) {}
  Field(const char* k, int v) : key(k), value(static_cast<std::int64_t>(v)) {}
  Field(const char* k, unsigned v) : key(k), value(static_cast<std::int64_t>(v)) {}
  Field(const char* k, std::uint64_t v) : key(k), value(static_cast<std::int64_t>(v)) {}
#if defined(__APPLE__)
  // darwin's uint64_t is unsigned long long, so a size_t (unsigned long) fits no exact overload above
  Field(const char* k, unsigned long v) : key(k), value(static_cast<std::int64_t>(v)) {}
#endif
  Field(const char* k, double v) : key(k), value(v) {}
  Field(const char* k, bool v) : key(k), value(v) {}

  const char* key;
  std::variant<Token, std::int64_t, double, bool> value;
};

enum class Level { Debug, Info, Warn, Error };

// a log line as it ships: screen/v1's LogLine fields
struct LogRecord {
  std::string source;
  Level level = Level::Info;
  std::string text;
  bool truncated = false;
  bool continued = false;
};

// "event key=value ...": the event and the keys are code's own literals, values are numbers and tokens
std::string formatLine(std::string_view event, std::initializer_list<Field> fields);

// a line cut into LogLine-sized pieces (4096 bytes, on utf-8 boundaries): the first marked truncated when it was cut,
// each later one continued, and truncated while more follow
std::vector<LogRecord> splitRecord(LogRecord record, std::size_t limit = 4096);

// the screen's log: stderr always, and the link once one is set. safe from any thread
class Log {
 public:
  using Sink = std::function<void(const LogRecord&)>;

  static Log& get();

  void setSink(Sink sink);
  void setMinLevel(Level level);
  void event(Level level, std::string_view event, std::initializer_list<Field> fields = {});
  // a library's line, scrubbed (scrubThirdParty) before it goes anywhere
  void thirdParty(std::string_view source, Level level, std::string_view line);

 private:
  void emit(LogRecord record);

  std::mutex mu_;
  Sink sink_;
  Level min_ = Level::Info;
};

cadence_screen_v1_LogLevel toProto(Level level);

}  // namespace cadence::screen::ipc

#define CADENCE_LOG(level, ...) ::cadence::screen::ipc::Log::get().event(::cadence::screen::ipc::Level::level, __VA_ARGS__)
