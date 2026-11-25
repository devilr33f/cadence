#pragma once

#include <cstddef>
#include <functional>
#include <span>
#include <string>
#include <string_view>

namespace cadence::audiod {

// soloist's stdout and stderr (M4c-D13, reference §8.1, v1's soloist_output.rs): matched line by line against one
// fixed phrase. its lines carry track, artist and user names, so nothing read here is ever logged or kept; the
// supervisor hands the same bytes to soloist's lines (M6b-D7), which go to cadenced alone

// a line counts by its first 4 KiB: the rest of a longer one is skipped unread
inline constexpr std::size_t kOutputLineCap = 4096;

// whether the message starts with "login failed", bare or right behind the logger's stamp
// (2026-10-27 04:38:59.185: ): a track, artist or user name later in a line may say anything
bool loginFailed(std::string_view line);

// assembles one run's lines; at most one finding per run, after which it only drains
class OutputScanner {
 public:
  // true the first time a line says login failed
  bool feed(std::span<const char> bytes);
  // the last line, which ended without a newline
  bool finish();

 private:
  bool endLine();
  bool report(bool found);

  std::string line_;
  bool skipping_ = false;
  bool reported_ = false;
};

struct Drained {
  bool loginFailed = false;
  // the pipe's write end closed: every process that held it is gone
  bool closed = false;
};

// reads what the non-blocking pipe fd holds, at most budget bytes, so a flood never holds the caller; soloist never
// waits on a full pipe as long as the caller polls it. each chunk read also goes to also, if any
Drained drainOutput(int fd, OutputScanner& scanner, std::size_t budget = 64 * 1024,
                    const std::function<void(std::span<const char>)>& also = {});

}  // namespace cadence::audiod
