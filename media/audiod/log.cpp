#include "audiod/log.h"

#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cerrno>
#include <cstddef>
#include <memory>
#include <mutex>
#include <string>

namespace cadence::audiod {

namespace {

template <typename... Ts>
struct Overloaded : Ts... {
  using Ts::operator()...;
};

// tokens are ascii by contract; anything else still cannot break the line or the json
void appendEscaped(std::string& line, std::string_view text) {
  for (const char c : text) {
    const auto byte = static_cast<unsigned char>(c);
    if (c == '"' || c == '\\') {
      line.push_back('\\');
      line.push_back(c);
    } else {
      line.push_back(byte >= 0x20 && byte < 0x7f ? c : '?');
    }
  }
}

void appendString(std::string& line, std::string_view text) {
  line.push_back('"');
  appendEscaped(line, text);
  line.push_back('"');
}

// a pipe is the one sink that fills when its reader (cadenced) stalls, and a blocked write would stall the
// loop or an audio thread with it. a terminal or a file shares its open file description with others, so it
// keeps blocking writes
bool fillsUp(int fd) {
  struct stat info {};
  return ::fstat(fd, &info) == 0 && (S_ISFIFO(info.st_mode) || S_ISSOCK(info.st_mode));
}

// whole line or nothing: lines are far shorter than PIPE_BUF, so a pipe never takes part of one
bool writeLine(int fd, std::string_view line) {
  while (!line.empty()) {
    const auto written = ::write(fd, line.data(), line.size());
    if (written < 0 && errno == EINTR)
      continue;
    if (written <= 0)
      return false;
    line.remove_prefix(static_cast<std::size_t>(written));
  }
  return true;
}

// lines that found the pipe full are counted, and the count goes out ahead of the next line that fits
class DescriptorSink {
 public:
  explicit DescriptorSink(int fd) : fd_(fd) {
    if (fillsUp(fd))
      ::fcntl(fd, F_SETFL, ::fcntl(fd, F_GETFL) | O_NONBLOCK);
  }

  void write(std::string_view line) {
    std::scoped_lock lock(mutex_);
    if (dropped_ > 0) {
      const auto note = R"({"level":"WARN","msg":"log_dropped","count":)" + std::to_string(dropped_) + "}\n";
      if (!writeLine(fd_, note)) {
        ++dropped_;
        return;
      }
      dropped_ = 0;
    }
    if (!writeLine(fd_, line))
      ++dropped_;
  }

 private:
  const int fd_;
  std::mutex mutex_;
  std::uint64_t dropped_ = 0;
};

}  // namespace

Log Log::toDescriptor(int fd) {
  auto sink = std::make_shared<DescriptorSink>(fd);
  return Log([sink](std::string_view line) { sink->write(line); });
}

Log Log::toStderr() { return toDescriptor(STDERR_FILENO); }

void Log::debug(std::string_view msg, std::initializer_list<LogField> fields) const { write("DEBUG", msg, fields); }
void Log::info(std::string_view msg, std::initializer_list<LogField> fields) const { write("INFO", msg, fields); }
void Log::warn(std::string_view msg, std::initializer_list<LogField> fields) const { write("WARN", msg, fields); }
void Log::error(std::string_view msg, std::initializer_list<LogField> fields) const { write("ERROR", msg, fields); }

void Log::write(std::string_view level, std::string_view msg, std::initializer_list<LogField> fields) const {
  std::string line = R"({"level":")";
  line.append(level);
  line.append(R"(","msg":)");
  appendString(line, msg);
  for (const auto& field : fields) {
    line.push_back(',');
    appendString(line, field.key());
    line.push_back(':');
    std::visit(Overloaded{
                   [&](std::string_view token) { appendString(line, token); },
                   [&](auto number) { line.append(std::to_string(number)); },
               },
               field.value());
  }
  line.append("}\n");
  sink_(line);
}

}  // namespace cadence::audiod
