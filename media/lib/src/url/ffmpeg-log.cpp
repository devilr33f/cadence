#include "url/ffmpeg-log.h"

#include "url/ffmpeg.h"

#include <atomic>
#include <cstdarg>
#include <mutex>

namespace cadence::url {

namespace {

std::atomic<std::uint64_t> fatal{0};
std::atomic<std::uint64_t> errors{0};
std::atomic<std::uint64_t> warnings{0};
std::atomic<std::uint64_t> infos{0};
std::atomic<std::uint64_t> debugs{0};

// the message and its arguments are never formatted, let alone kept
void count(void* /*context*/, int level, const char* /*format*/, va_list /*arguments*/) {
  auto& counter = level <= AV_LOG_FATAL     ? fatal
                  : level <= AV_LOG_ERROR   ? errors
                  : level <= AV_LOG_WARNING ? warnings
                  : level <= AV_LOG_INFO    ? infos
                                            : debugs;
  counter.fetch_add(1, std::memory_order_relaxed);
}

}  // namespace

void installFfmpegLogSink() {
  static std::once_flag once;
  std::call_once(once, [] { av_log_set_callback(count); });
}

FfmpegLogCounts ffmpegLogCounts() {
  return {.fatal = fatal.load(std::memory_order_relaxed),
          .error = errors.load(std::memory_order_relaxed),
          .warning = warnings.load(std::memory_order_relaxed),
          .info = infos.load(std::memory_order_relaxed),
          .debug = debugs.load(std::memory_order_relaxed)};
}

}  // namespace cadence::url
