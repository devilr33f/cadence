#pragma once

#include "audiod/sys.h"
#include "support/eventually.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <signal.h>
#include <spawn.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

#include <array>
#include <string>
#include <vector>

namespace cadence::test_support {

// the cadence-audiod binary as a process (CADENCE_AUDIOD_BINARY), whose stdout and stderr go to one pipe; killed if a
// failed check leaves it running
class Child {
 public:
  explicit Child(std::vector<std::string> args) {
    std::array<int, 2> fds{};
    REQUIRE(::pipe2(fds.data(), O_CLOEXEC) == 0);
    output_ = audiod::UniqueFd(fds[0]);
    const audiod::UniqueFd writeEnd(fds[1]);

    posix_spawn_file_actions_t actions;
    REQUIRE(::posix_spawn_file_actions_init(&actions) == 0);
    ::posix_spawn_file_actions_adddup2(&actions, writeEnd.get(), STDOUT_FILENO);
    ::posix_spawn_file_actions_adddup2(&actions, writeEnd.get(), STDERR_FILENO);
    std::string binary = CADENCE_AUDIOD_BINARY;
    std::vector<char*> argv{binary.data()};
    for (auto& arg : args)
      argv.push_back(arg.data());
    argv.push_back(nullptr);
    const int spawned = ::posix_spawn(&pid_, binary.c_str(), &actions, nullptr, argv.data(), environ);
    ::posix_spawn_file_actions_destroy(&actions);
    REQUIRE(spawned == 0);
  }

  ~Child() {
    if (pid_ > 0) {
      ::kill(pid_, SIGKILL);
      ::waitpid(pid_, nullptr, 0);
    }
  }

  Child(const Child&) = delete;
  Child& operator=(const Child&) = delete;

  void signal(int signo) const { REQUIRE(::kill(pid_, signo) == 0); }

  // the wait status once it exited
  int wait() {
    int status = 0;
    REQUIRE(eventually([&] { return ::waitpid(pid_, &status, WNOHANG) == pid_; }));
    pid_ = -1;
    return status;
  }

  // everything it wrote, read after it exited
  [[nodiscard]] std::string output() const {
    std::string text;
    std::array<char, 4096> chunk{};
    for (;;) {
      const auto size = ::read(output_.get(), chunk.data(), chunk.size());
      if (size <= 0)
        return text;
      text.append(chunk.data(), static_cast<std::size_t>(size));
    }
  }

 private:
  pid_t pid_ = -1;
  audiod::UniqueFd output_;
};

}  // namespace cadence::test_support
