#pragma once

#include "support/temp-dir.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <poll.h>
#include <signal.h>
#include <spawn.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

#include <chrono>
#include <fstream>
#include <string>
#include <vector>

extern char** environ;

namespace cadence::test_support {

// a private dbus-daemon for one test, its socket in a fresh directory, allowing everything to its own uid; killed with
// the object. binary is the daemon (CADENCE_DBUS_DAEMON); the tests skip when the build found none
class PrivateBus {
 public:
  explicit PrivateBus(const std::string& binary) {
    const auto config = dir_.file("bus.conf");
    std::ofstream(config) << "<!DOCTYPE busconfig PUBLIC \"-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN\"\n"
                             " \"http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd\">\n"
                             "<busconfig>\n"
                             "  <listen>unix:path=" << dir_.file("bus.sock") << "</listen>\n"
                             "  <auth>EXTERNAL</auth>\n"
                             "  <policy context=\"default\">\n"
                             "    <allow send_destination=\"*\" eavesdrop=\"true\"/>\n"
                             "    <allow eavesdrop=\"true\"/>\n"
                             "    <allow own=\"*\"/>\n"
                             "  </policy>\n"
                             "</busconfig>\n";

    // the daemon prints its address on fd 3 once it listens
    int fds[2];
    REQUIRE(::pipe2(fds, O_CLOEXEC) == 0);
    const int readEnd = fds[0];
    // dup2 onto itself would keep close-on-exec
    int writeEnd = fds[1];
    if (writeEnd == 3) {
      writeEnd = ::fcntl(fds[1], F_DUPFD_CLOEXEC, 4);
      ::close(fds[1]);
      REQUIRE(writeEnd >= 0);
    }
    posix_spawn_file_actions_t actions;
    REQUIRE(::posix_spawn_file_actions_init(&actions) == 0);
    ::posix_spawn_file_actions_adddup2(&actions, writeEnd, 3);
    ::posix_spawn_file_actions_addopen(&actions, STDIN_FILENO, "/dev/null", O_RDONLY, 0);
    ::posix_spawn_file_actions_addopen(&actions, STDOUT_FILENO, "/dev/null", O_WRONLY, 0);
    std::vector<std::string> args{binary, "--config-file=" + config, "--nofork", "--nopidfile", "--nosyslog",
                                  "--print-address=3"};
    std::vector<char*> argv;
    for (auto& arg : args)
      argv.push_back(arg.data());
    argv.push_back(nullptr);
    const int spawned = ::posix_spawn(&pid_, binary.c_str(), &actions, nullptr, argv.data(), environ);
    ::posix_spawn_file_actions_destroy(&actions);
    ::close(writeEnd);
    if (spawned != 0)
      ::close(readEnd);
    REQUIRE(spawned == 0);

    const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(10);
    char byte = 0;
    while (std::chrono::steady_clock::now() < deadline) {
      pollfd ready{readEnd, POLLIN, 0};
      if (::poll(&ready, 1, 100) <= 0)
        continue;
      if (::read(readEnd, &byte, 1) != 1 || byte == '\n')
        break;
      address_ += byte;
    }
    ::close(readEnd);
    REQUIRE(byte == '\n');
    REQUIRE(address_.starts_with("unix:path="));
  }

  ~PrivateBus() { kill(); }
  PrivateBus(const PrivateBus&) = delete;
  PrivateBus& operator=(const PrivateBus&) = delete;

  [[nodiscard]] const std::string& address() const { return address_; }

  void kill() {
    if (pid_ <= 0)
      return;
    ::kill(pid_, SIGKILL);
    ::waitpid(pid_, nullptr, 0);
    pid_ = -1;
  }

 private:
  TempDir dir_;
  pid_t pid_ = -1;
  std::string address_;
};

}  // namespace cadence::test_support
