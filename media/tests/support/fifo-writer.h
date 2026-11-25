#pragma once

#include "audiod/sys.h"
#include "support/eventually.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <poll.h>
#include <unistd.h>

#include <algorithm>
#include <cerrno>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

namespace cadence::test_support {

// shairport's side of a fifo: opened for writing without blocking, as shairport opens it, once a reader is there
inline audiod::UniqueFd fifoWriter(const std::string& path) {
  audiod::UniqueFd fd;
  REQUIRE(eventually(
      [&] {
        fd = audiod::UniqueFd(::open(path.c_str(), O_WRONLY | O_NONBLOCK | O_CLOEXEC));
        return fd.valid();
      },
      std::chrono::seconds(5)));
  return fd;
}

struct Written {
  std::chrono::milliseconds slowest{};
  std::chrono::milliseconds total{};
  // a chunk found no room for stallLimit: nothing reads the fifo, and the rest was not written
  bool stalled = false;
};

// 48 kHz s16 stereo frames of sample, in 20 ms chunks as fast as the fifo takes them
inline Written writeFrames(int fd, std::size_t frames, std::int16_t sample = 1000,
                           std::chrono::milliseconds stallLimit = std::chrono::seconds(1)) {
  using Clock = std::chrono::steady_clock;
  constexpr std::size_t kChunkFrames = 960;
  const std::vector<std::int16_t> chunk(kChunkFrames * 2, sample);
  Written written;
  const auto start = Clock::now();
  for (std::size_t done = 0; done < frames && !written.stalled; done += kChunkFrames) {
    const auto began = Clock::now();
    const auto* bytes = reinterpret_cast<const char*>(chunk.data());
    std::size_t left = chunk.size() * sizeof(std::int16_t);
    while (left > 0) {
      const auto sent = ::write(fd, bytes, left);
      if (sent > 0) {
        bytes += sent;
        left -= static_cast<std::size_t>(sent);
        continue;
      }
      REQUIRE((errno == EAGAIN || errno == EINTR));
      pollfd room{fd, POLLOUT, 0};
      if (::poll(&room, 1, static_cast<int>(stallLimit.count())) == 0) {
        written.stalled = true;
        break;
      }
    }
    written.slowest =
        std::max(written.slowest, std::chrono::duration_cast<std::chrono::milliseconds>(Clock::now() - began));
  }
  written.total = std::chrono::duration_cast<std::chrono::milliseconds>(Clock::now() - start);
  return written;
}

}  // namespace cadence::test_support
