#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/sys.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <sys/epoll.h>
#include <unistd.h>

#include <array>
#include <chrono>
#include <cstdint>
#include <future>
#include <optional>
#include <string_view>
#include <thread>
#include <vector>

using namespace cadence::audiod;
using namespace std::chrono_literals;

namespace {

Log quietLog() {
  return Log([](std::string_view) {});
}

struct Pipe {
  Pipe() {
    std::array<int, 2> fds{};
    REQUIRE(::pipe2(fds.data(), O_CLOEXEC | O_NONBLOCK) == 0);
    read = UniqueFd(fds[0]);
    write = UniqueFd(fds[1]);
  }
  UniqueFd read;
  UniqueFd write;
};

}  // namespace

TEST_CASE("timers fire in deadline order, never early") {
  const auto log = quietLog();
  auto loop = Loop::create(log);
  REQUIRE(loop);
  std::vector<int> fired;
  const auto start = Loop::Clock::now();
  loop->after(30ms, [&] {
    fired.push_back(3);
    loop->stop();
  });
  loop->after(10ms, [&] { fired.push_back(1); });
  loop->at(start + 20ms, [&] { fired.push_back(2); });

  REQUIRE_FALSE(loop->run().has_value());

  CHECK(fired == std::vector{1, 2, 3});
  CHECK(Loop::Clock::now() - start >= 30ms);
}

TEST_CASE("a cancelled timer never fires") {
  const auto log = quietLog();
  auto loop = Loop::create(log);
  REQUIRE(loop);
  bool cancelledFired = false;
  const auto cancelled = loop->after(5ms, [&] { cancelledFired = true; });
  loop->after(20ms, [&] { loop->stop(); });
  loop->cancel(cancelled);

  REQUIRE_FALSE(loop->run().has_value());

  CHECK_FALSE(cancelledFired);
}

TEST_CASE("callbacks posted from another thread run on the loop thread, and stop ends it from there") {
  const auto log = quietLog();
  auto loop = Loop::create(log);
  REQUIRE(loop);
  std::promise<std::thread::id> ranOn;
  auto ran = ranOn.get_future();
  std::thread runner([&] { (void)loop->run(); });
  const auto loopThread = runner.get_id();

  loop->post([&] { ranOn.set_value(std::this_thread::get_id()); });
  REQUIRE(ran.wait_for(2s) == std::future_status::ready);
  CHECK(ran.get() == loopThread);

  // the loop sleeps in epoll with nothing to do, so only the wake can end it
  loop->stop();
  runner.join();
}

TEST_CASE("a watch removed earlier in the same batch never fires") {
  const auto log = quietLog();
  auto loop = Loop::create(log);
  REQUIRE(loop);
  Pipe first;
  Pipe second;
  REQUIRE(::write(first.write.get(), "x", 1) == 1);
  REQUIRE(::write(second.write.get(), "x", 1) == 1);

  // both are readable before the loop starts, so one epoll batch reports both and whichever runs first removes
  // the other
  int calls = 0;
  std::optional<Loop::WatchId> firstWatch;
  std::optional<Loop::WatchId> secondWatch;
  auto handler = [&](const UniqueFd& fd, std::optional<Loop::WatchId>& other) {
    return [&fd, &other, &calls, &loop](std::uint32_t events) {
      CHECK((events & EPOLLIN) != 0);
      char byte = 0;
      CHECK(::read(fd.get(), &byte, 1) == 1);
      ++calls;
      loop->unwatch(*other);
    };
  };
  const auto watchedFirst = loop->watch(first.read.get(), EPOLLIN, handler(first.read, secondWatch));
  const auto watchedSecond = loop->watch(second.read.get(), EPOLLIN, handler(second.read, firstWatch));
  REQUIRE(watchedFirst.ok());
  REQUIRE(watchedSecond.ok());
  firstWatch = watchedFirst.value();
  secondWatch = watchedSecond.value();
  loop->after(50ms, [&] { loop->stop(); });

  REQUIRE_FALSE(loop->run().has_value());

  CHECK(calls == 1);
}
