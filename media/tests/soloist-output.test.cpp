#include "audiod/soloist-output.h"
#include "audiod/sys.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <unistd.h>

#include <array>
#include <cstddef>
#include <span>
#include <string>
#include <string_view>

using namespace cadence::audiod;

namespace {

constexpr std::string_view kStamp = "2026-10-27 04:38:59.185: ";

std::string stamped(std::string_view message) { return std::string(kStamp) + std::string(message); }

std::size_t scanned(const std::string& input, std::size_t chunk) {
  OutputScanner scanner;
  std::size_t found = 0;
  for (std::size_t at = 0; at < input.size(); at += chunk)
    if (scanner.feed(std::span<const char>(input).subspan(at, std::min(chunk, input.size() - at))))
      ++found;
  if (scanner.finish())
    ++found;
  return found;
}

// one byte at a time is the worst split a pipe hands over; both splits must agree
std::size_t found(const std::string& input) {
  const auto trickled = scanned(input, 1);
  const auto whole = scanned(input, input.empty() ? 1 : input.size());
  CHECK(trickled == whole);
  return whole;
}

}  // namespace

TEST_CASE("login failed must start the message, bare or behind the stamp") {
  for (const std::string& line :
       {std::string("login failed"), std::string("login failed: make sure --api-key is valid"),
        std::string("login failed\r"), stamped("login failed"), stamped("login failed: make sure --api-key is valid"),
        std::string("1999-01-01 00:00:00.000: login failed"),
        // the line as a user's soloist wrote it (soloist issue #18)
        std::string("2026-10-19 08:18:40.695: login failed: make sure --api-key is valid")}) {
    INFO(line);
    CHECK(loginFailed(line));
  }
  CHECK(found("login failed: make sure\n") == 1);
  CHECK(found(stamped("login failed: make sure\r\n")) == 1);
  CHECK(found(stamped("login failed")) == 1);
  CHECK(found("2026-10-19 08:18:40.642: client expires in 86 days\n"
              "2026-10-19 08:18:40.686: running\n"
              "2026-10-19 08:18:40.695: login failed: make sure --api-key is valid\n") == 1);
}

TEST_CASE("login failed later in a line is no finding") {
  for (const std::string& line : {
           std::string("x login failed"),
           std::string("the login failed"),
           std::string("E 12:00 login failed: make sure --api-key is valid"),
           std::string(" login failed"),
           // a title or an artist that says it
           stamped("playing \"login failed\" by login failed"),
           stamped(" login failed"),
           stamped("x: login failed"),
           // not the logger's stamp: one more digit, a letter, a missing colon or space
           std::string("2026-10-27 04:38:59.1855: login failed"),
           std::string("2026-10-27 04:38:59.18: login failed"),
           std::string("2026-10-27 04:38:5x.185: login failed"),
           std::string("2026-10-27 04:38:59.185:login failed"),
           std::string("2026-10-27T04:38:59.185: login failed"),
           std::string("26-10-27 04:38:59.185: login failed"),
           // a stamp inside the line anchors nothing
           std::string("x 2026-10-27 04:38:59.185: login failed"),
       }) {
    INFO(line);
    CHECK_FALSE(loginFailed(line));
  }
  CHECK(found(stamped("track login failed\n")) == 0);
  CHECK(found("x login failed") == 0);
}

TEST_CASE("other lines find nothing") {
  for (const std::string& line :
       {std::string(), std::string("login"), std::string("login faile"), std::string("Login Failed"),
        std::string("login  failed"), std::string("playing Some Track by Some Artist for some_user"),
        std::string(kStamp), stamped("login faile"), stamped("Login Failed"),
        stamped("playing Some Track by Some Artist for some_user")}) {
    INFO(line);
    CHECK_FALSE(loginFailed(line));
  }
  CHECK(found("login\nfailed\nlogin fai\nled\n") == 0);
  CHECK(found("") == 0);
}

TEST_CASE("one finding comes per run") {
  CHECK(found("login failed\r\nlogin failed\n") == 1);
  CHECK(found("login failed\nother\nlogin failed") == 1);
  CHECK(found(stamped("login failed\n") + "login failed\n") == 1);
}

TEST_CASE("an over-long line counts by its first 4 KiB") {
  const std::string long_(kOutputLineCap, 'x');
  CHECK(found(long_ + " login failed\nother\n") == 0);
  CHECK(found(long_ + " login failed\n" + stamped("login failed\n")) == 1);
  for (const std::string& start : {std::string("login failed "), stamped("login failed ")}) {
    auto input = start;
    input.resize(kOutputLineCap + 10'000, 'x');
    CHECK(found(input + "\nnext\n") == 1);
  }
}

TEST_CASE("the pipe drains within the budget and says when it closed") {
  std::array<int, 2> fds{};
  REQUIRE(::pipe2(fds.data(), O_CLOEXEC | O_NONBLOCK) == 0);
  UniqueFd read(fds[0]);
  UniqueFd write(fds[1]);
  // more than one budget, within 8 KiB: the least a pipe holds (2 pages, once a user is past fs.pipe-user-pages-soft,
  // as root on a busy ci host), where one write of more comes back short
  const std::string input = "login failed\n" + std::string(8'000, 'x') + "\nlogin failed\n";
  REQUIRE(::write(write.get(), input.data(), input.size()) == static_cast<ssize_t>(input.size()));

  OutputScanner scanner;
  auto drained = drainOutput(read.get(), scanner, 4096);
  CHECK(drained.loginFailed);
  CHECK_FALSE(drained.closed);
  // the rest waits for the next call: once reported, the run only drains
  std::size_t calls = 1;
  for (; calls < 10 && !drained.closed; ++calls) {
    drained = drainOutput(read.get(), scanner, 4096);
    CHECK_FALSE(drained.loginFailed);
    if (calls == 3)
      write.reset();
  }
  CHECK(drained.closed);
  CHECK(calls >= 4);
}
