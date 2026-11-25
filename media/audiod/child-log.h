#pragma once

#include "cadence/audiod/v1/audiod.pb.h"

#include <atomic>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <span>
#include <string>
#include <string_view>

namespace cadence::audiod {

using LogLevel = cadence_audiod_v1_LogLevel;

// a child's line as LogLine carries it (M6b-D7)
struct ChildLine {
  // a fixed token of static storage
  std::string_view source;
  LogLevel level = cadence_audiod_v1_LogLevel_LOG_LEVEL_INFO;
  // valid utf-8 without a nul, at most kChildLineBytes
  std::string text;
  // cut: past kChildLineBytes, sent after kChildPartialWait without its newline, or ended by its stream (or the
  // child's end) without one; text then ends with …
  bool truncated = false;
  // the rest of a line sent before after kChildPartialWait
  bool continued = false;
};

inline constexpr std::size_t kChildLineBytes = 4096;
inline constexpr std::uint32_t kChildLinesPerSecond = 200;
inline constexpr std::chrono::milliseconds kChildPartialWait{1000};

// LogsSet's switch: whether the children's lines go out, on until cadenced says otherwise. any thread
class ChildLogSwitch {
 public:
  void set(bool on) { on_.store(on, std::memory_order_relaxed); }
  [[nodiscard]] bool on() const { return on_.load(std::memory_order_relaxed); }

 private:
  std::atomic<bool> on_{true};
};

// where a child's lines go, and whether they go; a target without both forwards nothing
struct ChildLogTarget {
  std::shared_ptr<const ChildLogSwitch> forward;
  // called on the thread that reads the child; never blocks
  std::function<void(ChildLine line)> sink;

  explicit operator bool() const { return forward && sink; }
};

// the level a line names at its very start: DEBUG, INFO, WARN, WARNING or ERROR in any case, then no letter or digit
std::optional<LogLevel> leadingLevel(std::string_view line);

// one child's stdout and stderr made into lines (M6b-D7): a line ends at its newline, after kChildPartialWait without
// one (cut, and its rest continued), or at its stream's end (cut); past kChildLineBytes it is cut with …, the rest of
// it skipped. stdout's lines are INFO
// and stderr's WARN, unless a line starts with a level. while the switch is on, at most kChildLinesPerSecond lines go
// out each second; the rest are counted, and once that second ended one WARN "dropped N lines" says how many. while it
// is off every line is read and dropped, uncounted. what it reads is never logged. one thread feeds it
class ChildLog {
 public:
  using Clock = std::chrono::steady_clock;
  enum class Stream : std::uint8_t { Out, Err };

  ChildLog(std::string_view source, ChildLogTarget target);

  void feed(Stream stream, std::span<const char> bytes, Clock::time_point now);
  // the stream ended: the line it did not end goes out cut, as it may end in a secret's start
  void finish(Stream stream, Clock::time_point now);
  // the child is gone: both streams finish, and the drops counted so far are reported
  void close(Clock::time_point now);
  // sends the partial lines that waited long enough and reports a second's drops once it ended
  void tick(Clock::time_point now);
  // when tick has something to do
  [[nodiscard]] std::optional<Clock::time_point> deadline() const;

 private:
  struct Pending {
    std::string bytes;
    // when its first byte came
    std::optional<Clock::time_point> since;
    // the line was cut: the rest of it, up to its newline, is dropped
    bool skipping = false;
    // what comes is the rest of a line sent after kChildPartialWait
    bool continued = false;
  };

  Pending& of(Stream stream) { return stream == Stream::Out ? out_ : err_; }
  void send(Stream stream, Pending& line, bool cut, Clock::time_point now);
  // the window's second ended: its drops are reported, and the next line starts another
  void roll(Clock::time_point now, bool force);

  std::string_view source_;
  ChildLogTarget target_;
  Pending out_;
  Pending err_;
  std::optional<Clock::time_point> window_;
  std::uint32_t sent_ = 0;
  std::uint64_t dropped_ = 0;
};

}  // namespace cadence::audiod
