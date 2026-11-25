#pragma once

#include "audiod/soloist-jail.h"
#include "audiod/soloist-scratch.h"
#include "support/audiod-harness.h"

#include <catch2/catch_test_macros.hpp>

#include <linux/capability.h>
#include <poll.h>
#include <signal.h>
#include <sys/stat.h>
#include <unistd.h>

#include <array>
#include <chrono>
#include <cstdint>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <sstream>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::test_support {

// the oom score a child started with target gets: target, or this process's own when that is higher already (a
// kubernetes pod's processes start near 1000, and a child is never made less killable than audiod)
inline std::string childOomScore(int target) {
  std::ifstream in("/proc/self/oom_score_adj");
  int own = 0;
  in >> own;
  return std::to_string(own > target ? own : target);
}

// root with CAP_SYS_ADMIN in its effective set, which the jail's namespace and mounts need
inline bool canJail() {
  if (::geteuid() != 0)
    return false;
  std::ifstream status("/proc/self/status");
  for (std::string line; std::getline(status, line);)
    if (line.starts_with("CapEff:"))
      return ((std::stoull(line.substr(7), nullptr, 16) >> CAP_SYS_ADMIN) & 1) != 0;
  return false;
}

// a test through the real jail skips without the privilege it needs; the privileged run sets CADENCE_JAIL_REQUIRED,
// so there a missing privilege fails instead of passing by skipping
inline void requireJail() {
  if (!audiod::kSoloistJailed || canJail())
    return;
  if (std::getenv("CADENCE_JAIL_REQUIRED") != nullptr)
    FAIL("the jail needs root with CAP_SYS_ADMIN");
  SKIP("the jail needs root with CAP_SYS_ADMIN: make media-test-privileged");
}

// the signals as audiod leaves them for the threads it starts (SignalStop): SIGTERM and SIGINT blocked on this thread,
// SIGPIPE ignored, until the guard goes. a program started meanwhile must still get them all back at their defaults
class AudiodSignals {
 public:
  AudiodSignals() {
    sigset_t stopping;
    sigemptyset(&stopping);
    sigaddset(&stopping, SIGTERM);
    sigaddset(&stopping, SIGINT);
    REQUIRE(::pthread_sigmask(SIG_BLOCK, &stopping, &mask_) == 0);
    pipe_ = ::signal(SIGPIPE, SIG_IGN);
    REQUIRE(pipe_ != SIG_ERR);
  }
  ~AudiodSignals() {
    ::signal(SIGPIPE, pipe_);
    ::pthread_sigmask(SIG_SETMASK, &mask_, nullptr);
  }
  AudiodSignals(const AudiodSignals&) = delete;
  AudiodSignals& operator=(const AudiodSignals&) = delete;

 private:
  sigset_t mask_{};
  sighandler_t pipe_ = SIG_DFL;
};

inline void writeFile(const std::string& path, std::string_view content, std::filesystem::perms mode) {
  std::ofstream(path) << content;
  std::filesystem::permissions(path, mode);
}

// what v1's runtime assembly leaves for the jail to bind over: its directories and empty placeholder files, beside a
// soloist scratch, a ca bundle and a resolv.conf, all in one temp dir
class JailRig {
 public:
  static constexpr auto kDirMode = std::filesystem::perms(0755);
  static constexpr auto kFileMode = std::filesystem::perms(0644);
  static constexpr std::array kRuntimeDirs{"dev", "proc", "etc/ssl/certs", "home/soloist", "run/cadence"};
  static constexpr std::array kPlaceholders{"soloist",  "dev/null",        "dev/urandom",
                                            "dev/zero", "etc/resolv.conf", "etc/ssl/certs/ca-certificates.crt"};
  static constexpr std::string_view kResolvLine = "nameserver 192.0.2.53";
  static constexpr std::string_view kCaLine = "fake ca bundle for the jail tests";

  JailRig() : root_(dir_.file("runtime")) {
    std::filesystem::create_directories(root_);
    std::filesystem::permissions(root_, kDirMode);
    for (const std::string_view dir : kRuntimeDirs) {
      std::filesystem::create_directories(root_ + "/" + std::string(dir));
      for (auto path = std::filesystem::path(root_) / dir; path != root_; path = path.parent_path())
        std::filesystem::permissions(path, kDirMode);
    }
    for (const std::string_view file : kPlaceholders)
      writeFile(root_ + "/" + std::string(file), "", kFileMode);
    writeFile(dir_.file("resolv.conf"), std::string(kResolvLine) + "\n", kFileMode);
    writeFile(dir_.file("ca.crt"), std::string(kCaLine) + "\n", kFileMode);
    writeFile(dir_.file("build"), "", std::filesystem::perms(0755));
    const auto scratch = audiod::prepareSoloistScratch(log_, dir_.file("scratch"));
    REQUIRE(scratch);
    scratch_ = *scratch;
  }

  JailRig(const JailRig&) = delete;
  JailRig& operator=(const JailRig&) = delete;

  // build is the soloist the plan runs: by default an empty file, which only the plan's own checks look at
  [[nodiscard]] audiod::JailPaths paths(std::string build = {}) const {
    return {.runtimeRoot = root_,
            .build = build.empty() ? dir_.file("build") : std::move(build),
            .resolvConf = dir_.file("resolv.conf"),
            .caFile = dir_.file("ca.crt"),
            .home = scratch_.home,
            .run = scratch_.run};
  }
  [[nodiscard]] audiod::JailPlan plan(std::string build = {}) const { return audiod::planJail(paths(std::move(build))); }

  [[nodiscard]] const std::string& root() const { return root_; }
  [[nodiscard]] std::string inRoot(std::string_view path) const { return root_ + std::string(path); }
  [[nodiscard]] std::string file(std::string_view name) const { return dir_.file(name); }
  [[nodiscard]] const audiod::SoloistScratch& scratch() const { return scratch_; }
  [[nodiscard]] const LogCapture& logs() const { return logs_; }
  [[nodiscard]] const audiod::Log& log() const { return log_; }

 private:
  TempDir dir_;
  LogCapture logs_;
  audiod::Log log_ = logs_.log();
  std::string root_;
  audiod::SoloistScratch scratch_;
};

// everything fd yields until its end, or what came within limit
inline std::string readToEnd(int fd, std::chrono::milliseconds limit = std::chrono::seconds(10)) {
  std::string text;
  const auto deadline = std::chrono::steady_clock::now() + limit;
  for (;;) {
    const auto left = std::chrono::duration_cast<std::chrono::milliseconds>(deadline - std::chrono::steady_clock::now());
    if (left.count() <= 0)
      return text;
    pollfd ready{.fd = fd, .events = POLLIN, .revents = 0};
    if (::poll(&ready, 1, static_cast<int>(left.count())) <= 0)
      continue;
    std::array<char, 4096> chunk{};
    const auto size = ::read(fd, chunk.data(), chunk.size());
    if (size == 0 || (size < 0 && errno != EAGAIN && errno != EINTR))
      return text;
    if (size > 0)
      text.append(chunk.data(), static_cast<std::size_t>(size));
  }
}

// reads until a line equal to line arrived
inline bool readLine(int fd, std::string_view line, std::chrono::milliseconds limit = std::chrono::seconds(10)) {
  std::string text;
  const auto deadline = std::chrono::steady_clock::now() + limit;
  while (std::chrono::steady_clock::now() < deadline) {
    pollfd ready{.fd = fd, .events = POLLIN, .revents = 0};
    if (::poll(&ready, 1, 10) <= 0)
      continue;
    char byte = 0;
    if (::read(fd, &byte, 1) != 1)
      return false;
    if (byte != '\n') {
      text.push_back(byte);
      continue;
    }
    if (text == line)
      return true;
    text.clear();
  }
  return false;
}

inline std::vector<std::string> splitLines(const std::string& text) {
  std::vector<std::string> lines;
  std::istringstream stream(text);
  for (std::string line; std::getline(stream, line);)
    lines.push_back(line);
  return lines;
}

}  // namespace cadence::test_support
