#include "audiod/jailed-process.h"
#include "audiod/soloist-jail.h"
#include "support/eventually.h"
#include "support/jail-rig.h"

#include <catch2/catch_test_macros.hpp>

#include <signal.h>
#include <sys/types.h>
#include <unistd.h>

#include <atomic>
#include <cerrno>
#include <chrono>
#include <future>
#include <memory>
#include <optional>
#include <string>
#include <thread>
#include <vector>

using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;
using Clock = std::chrono::steady_clock;

namespace {

// a jailed run of the probe whose exit the test can wait for
struct Probe {
  explicit Probe(const JailRig& rig, std::vector<std::string> args) {
    auto started = JailedProcess::start(rig.log(), rig.plan(CADENCE_JAIL_PROBE), std::move(args),
                                        [this](JailedExit exit) { exited.set_value(exit); });
    REQUIRE(started.ok());
    process = std::move(started.value());
    output = process->takeOutput();
    REQUIRE(output);
  }

  [[nodiscard]] JailedExit exit() {
    auto future = exited.get_future();
    REQUIRE(future.wait_for(10s) == std::future_status::ready);
    return future.get();
  }

  std::promise<JailedExit> exited;
  std::unique_ptr<JailedProcess> process;
  UniqueFd output;
};

bool alive(pid_t pid) { return ::kill(pid, 0) == 0; }

}  // namespace

TEST_CASE("the exit comes back through the callback, with its code or the signal that ended it") {
  requireJail();
  JailRig rig;

  SECTION("a code") {
    Probe probe(rig, {"exit", "7"});
    const auto exit = probe.exit();
    CHECK(exit.code == 7);
    CHECK(exit.signal == 0);
  }
  SECTION("a signal") {
    Probe probe(rig, {"wait"});
    REQUIRE(readLine(probe.output.get(), "ready"));
    probe.process->kill(SIGUSR1);
    const auto exit = probe.exit();
    CHECK(exit.code == -1);
    CHECK(exit.signal == SIGUSR1);
  }
}

TEST_CASE("stop asks with SIGTERM and kills once the grace is over, and returns the reaped exit") {
  requireJail();
  JailRig rig;

  SECTION("a program that ends on SIGTERM, started where audiod blocks it") {
    std::unique_ptr<Probe> probe;
    {
      const AudiodSignals signals;
      probe = std::make_unique<Probe>(rig, std::vector<std::string>{"wait"});
    }
    REQUIRE(readLine(probe->output.get(), "ready"));
    const auto begun = Clock::now();
    const auto exit = probe->process->stop(5s);
    REQUIRE(exit);
    CHECK(exit->signal == SIGTERM);
    CHECK(Clock::now() - begun < 3s);
  }
  SECTION("a program that ignores it") {
    Probe probe(rig, {"ignore-term"});
    REQUIRE(readLine(probe.output.get(), "ready"));
    const auto begun = Clock::now();
    const auto exit = probe.process->stop(300ms);
    const auto took = Clock::now() - begun;
    REQUIRE(exit);
    CHECK(exit->signal == SIGKILL);
    CHECK(took >= 300ms);
    CHECK(took < 4s);
    CHECK(probe.exit().signal == SIGKILL);
  }
}

TEST_CASE("destroying a running process kills and reaps it, and its callback never comes") {
  requireJail();
  JailRig rig;
  std::atomic<int> calls{0};
  auto started = JailedProcess::start(rig.log(), rig.plan(CADENCE_JAIL_PROBE), {"wait"},
                                      [&](JailedExit /*exit*/) { ++calls; });
  REQUIRE(started.ok());
  auto process = std::move(started.value());
  const auto output = process->takeOutput();
  REQUIRE(readLine(output.get(), "ready"));
  const pid_t pid = process->pid();

  process.reset();

  CHECK_FALSE(alive(pid));
  std::this_thread::sleep_for(100ms);
  CHECK(calls == 0);
}

// the death signal follows the thread that forked: a process started from a thread that ends right after must live on
TEST_CASE("the program outlives the thread that started it") {
  requireJail();
  JailRig rig;
  std::unique_ptr<Probe> probe;

  std::thread([&] { probe = std::make_unique<Probe>(rig, std::vector<std::string>{"wait"}); }).join();

  REQUIRE(probe);
  REQUIRE(readLine(probe->output.get(), "ready"));
  std::this_thread::sleep_for(300ms);
  CHECK(alive(probe->process->pid()));
  const auto exit = probe->process->stop(5s);
  REQUIRE(exit);
  CHECK(exit->signal == SIGTERM);
}

TEST_CASE("a capture returns what the program printed and how it ended") {
  requireJail();
  JailRig rig;
  const std::string line = "soloist 1.3.10 build 1767225600 (20260101) test";

  const auto captured = JailedProcess::runCapture(rig.log(), rig.plan(CADENCE_JAIL_PROBE), {"print", line});

  REQUIRE(captured.ok());
  CHECK(captured.value().output == line + "\n");
  REQUIRE(captured.value().exit);
  CHECK(captured.value().exit->code == 0);
  CHECK_FALSE(captured.value().timedOut);
  CHECK_FALSE(captured.value().truncated);
}

TEST_CASE("a capture keeps at most 4 KiB of stdout") {
  requireJail();
  JailRig rig;
  STATIC_CHECK(kCaptureBytes == 4096);

  const auto captured = JailedProcess::runCapture(rig.log(), rig.plan(CADENCE_JAIL_PROBE), {"spew", "10000"});

  REQUIRE(captured.ok());
  CHECK(captured.value().output == std::string(4096, 'x'));
  CHECK(captured.value().truncated);
  CHECK_FALSE(captured.value().timedOut);
  CHECK(captured.value().exit);
}

TEST_CASE("a capture gives up at its limit, 10 s unless told otherwise, and kills the program") {
  requireJail();
  JailRig rig;
  STATIC_CHECK(kCaptureLimit == 10s);
  const auto begun = Clock::now();

  const auto captured = JailedProcess::runCapture(rig.log(), rig.plan(CADENCE_JAIL_PROBE), {"hang"}, 300ms);

  const auto took = Clock::now() - begun;
  REQUIRE(captured.ok());
  CHECK(captured.value().timedOut);
  CHECK(captured.value().output.empty());
  REQUIRE(captured.value().exit);
  CHECK(captured.value().exit->signal == SIGKILL);
  CHECK(took >= 300ms);
  CHECK(took < 4s);
}

TEST_CASE("a build without an execute bit fails the start at the exec step, with its errno") {
  requireJail();
  JailRig rig;
  writeFile(rig.file("not-executable"), "#!/bin/sh\n", JailRig::kFileMode);

  const auto started = JailedProcess::start(rig.log(), rig.plan(rig.file("not-executable")), {}, nullptr);

  REQUIRE_FALSE(started.ok());
  CHECK_FALSE(started.error().refused);
  CHECK(started.error().reason == "exec");
  CHECK(started.error().code == EACCES);
  CHECK(rig.logs().count(R"("msg":"jail_spawn_failed","step":"exec","errno":13)") == 1);
}

TEST_CASE("a missing build fails a jail-off start at the exec step") {
  if constexpr (kSoloistJailed)
    SKIP("the jail refuses a missing build before the fork");
  JailRig rig;

  const auto started = JailedProcess::start(rig.log(), rig.plan(rig.file("missing")), {}, nullptr);

  REQUIRE_FALSE(started.ok());
  CHECK(started.error().reason == "exec");
  CHECK(started.error().code == ENOENT);
  CHECK(rig.logs().count(R"("msg":"jail_spawn_failed","step":"exec","errno":2)") == 1);
}
