#pragma once

#include <cstdint>

namespace cadence::url {

// lines ffmpeg logged since the sink went in, by level; never their text
struct FfmpegLogCounts {
  // panic and fatal
  std::uint64_t fatal = 0;
  std::uint64_t error = 0;
  std::uint64_t warning = 0;
  std::uint64_t info = 0;
  // verbose, debug and trace
  std::uint64_t debug = 0;
};

// ffmpeg's messages name urls, hosts and stream metadata (rule 8), so av_log goes to a sink that only counts them:
// nothing reaches stderr or any log. installed once per process, whoever calls first; call it before ffmpeg runs
void installFfmpegLogSink();
FfmpegLogCounts ffmpegLogCounts();

}  // namespace cadence::url
