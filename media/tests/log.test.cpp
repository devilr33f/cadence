#include "audiod/log.h"
#include "audiod/sys.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <unistd.h>

#include <array>
#include <cerrno>
#include <chrono>
#include <future>
#include <string>

using namespace cadence::audiod;
using namespace std::chrono_literals;

namespace {

struct Pipe {
  Pipe() {
    std::array<int, 2> fds{};
    REQUIRE(::pipe2(fds.data(), O_CLOEXEC) == 0);
    read = UniqueFd(fds[0]);
    write = UniqueFd(fds[1]);
  }
  UniqueFd read;
  UniqueFd write;
};

std::string drain(int fd) {
  REQUIRE(::fcntl(fd, F_SETFL, ::fcntl(fd, F_GETFL) | O_NONBLOCK) == 0);
  std::string text;
  std::array<char, 4096> chunk{};
  for (;;) {
    const auto size = ::read(fd, chunk.data(), chunk.size());
    if (size <= 0)
      return text;
    text.append(chunk.data(), static_cast<std::size_t>(size));
  }
}

}  // namespace

TEST_CASE("LogDropsLinesWhileItsPipeIsFull") {
  Pipe pipe;
  const auto log = Log::toDescriptor(pipe.write.get());
  // the log made its pipe non-blocking, so filling it ends instead of hanging
  const std::array<char, 4096> filler{};
  while (::write(pipe.write.get(), filler.data(), filler.size()) > 0) {
  }
  REQUIRE(errno == EAGAIN);

  auto logged = std::async(std::launch::async, [&] { log.info("first"); });
  REQUIRE(logged.wait_for(2s) == std::future_status::ready);

  const auto stalled = drain(pipe.read.get());
  CHECK(stalled.find("first") == std::string::npos);
  log.info("second");
  CHECK(drain(pipe.read.get()) ==
        "{\"level\":\"WARN\",\"msg\":\"log_dropped\",\"count\":1}\n{\"level\":\"INFO\",\"msg\":\"second\"}\n");
}
