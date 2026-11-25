#pragma once

#include "audiod/log.h"
#include "audiod/soloist-supervisor.h"
#include "backend/pcm-input.h"
#include "support/eventually.h"
#include "support/jail-rig.h"
#include "support/timed-log.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <filesystem>
#include <fstream>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

// a supervisor of the fake soloist (support/fake-soloist.cpp), never the real one: tests never cast
namespace cadence::test_support {

inline constexpr std::string_view kFakeKey = "test-key-not-real";

// short enough for tests, in v1's proportions
inline audiod::SoloistTimings quickTimings() {
  audiod::SoloistTimings timings;
  timings.portTick = std::chrono::milliseconds(20);
  timings.tick = std::chrono::milliseconds(100);
  timings.portWait = std::chrono::milliseconds(400);
  timings.answerWait = std::chrono::milliseconds(1200);
  timings.reconnect = std::chrono::milliseconds(100);
  timings.firstBackoff = std::chrono::milliseconds(100);
  timings.lastBackoff = std::chrono::milliseconds(400);
  timings.steady = std::chrono::milliseconds(1500);
  timings.switchCheck = std::chrono::milliseconds(300);
  timings.probeLimit = std::chrono::milliseconds(2000);
  return timings;
}

// what the supervisor reported, on its thread
class SoloistReports {
 public:
  audiod::SoloistHooks hooks() {
    return {.status =
                [this](const audiod::SoloistStatus& status) {
                  std::scoped_lock lock(mutex_);
                  statuses_.push_back(status);
                  changed_.notify_all();
                },
            .probed =
                [this](const std::string& requestId, const audiod::SoloistProbe& probe) {
                  std::scoped_lock lock(mutex_);
                  probes_.emplace_back(requestId, probe);
                  changed_.notify_all();
                },
            .senderVolume =
                [this](std::uint8_t level) {
                  std::scoped_lock lock(mutex_);
                  senderVolumes_.push_back(level);
                },
            .post = {}};
  }

  // the deliberate slider moves it reported
  [[nodiscard]] std::vector<std::uint8_t> senderVolumes() const {
    std::scoped_lock lock(mutex_);
    return senderVolumes_;
  }

  // the latest status, once one matches
  std::optional<audiod::SoloistStatus> wait(const std::function<bool(const audiod::SoloistStatus&)>& matches,
                                            std::chrono::milliseconds limit = std::chrono::seconds(10)) {
    std::unique_lock lock(mutex_);
    std::optional<audiod::SoloistStatus> found;
    changed_.wait_for(lock, limit, [&] {
      if (!statuses_.empty() && matches(statuses_.back()))
        found = statuses_.back();
      return found.has_value();
    });
    return found;
  }
  std::optional<audiod::SoloistStatus> waitState(audiod::SoloistState state,
                                                 std::chrono::milliseconds limit = std::chrono::seconds(10)) {
    return wait([state](const audiod::SoloistStatus& status) { return status.state == state; }, limit);
  }

  std::optional<audiod::SoloistProbe> probe(const std::string& requestId,
                                            std::chrono::milliseconds limit = std::chrono::seconds(10)) {
    std::unique_lock lock(mutex_);
    std::optional<audiod::SoloistProbe> found;
    changed_.wait_for(lock, limit, [&] {
      for (const auto& [id, probe] : probes_)
        if (id == requestId)
          found = probe;
      return found.has_value();
    });
    return found;
  }

  [[nodiscard]] std::vector<audiod::SoloistStatus> statuses() const {
    std::scoped_lock lock(mutex_);
    return statuses_;
  }
  [[nodiscard]] audiod::SoloistStatus latest() const {
    std::scoped_lock lock(mutex_);
    return statuses_.empty() ? audiod::SoloistStatus{} : statuses_.back();
  }

 private:
  mutable std::mutex mutex_;
  std::condition_variable changed_;
  std::vector<audiod::SoloistStatus> statuses_;
  std::vector<std::pair<std::string, audiod::SoloistProbe>> probes_;
  std::vector<std::uint8_t> senderVolumes_;
};

// the fake's side: the files it reads and writes in the scratch's home
class FakeSoloist {
 public:
  explicit FakeSoloist(std::string home) : home_(std::move(home)) {}

  [[nodiscard]] std::string file(std::string_view name) const { return home_ + "/" + std::string(name); }

  // one line of its inbox: an event it sends as it is, or a !directive
  void send(std::string_view line) const {
    std::ofstream(file("fake-inbox"), std::ios::app) << line << '\n';
  }

  [[nodiscard]] std::vector<std::string> lines(std::string_view name) const {
    std::ifstream in(file(name));
    std::vector<std::string> found;
    for (std::string line; std::getline(in, line);)
      found.push_back(line);
    return found;
  }
  // the text frames it received
  [[nodiscard]] std::vector<std::string> received() const { return lines("fake-received"); }
  bool waitReceived(std::string_view text, std::chrono::milliseconds limit = std::chrono::seconds(5)) const {
    return eventually(
        [&] {
          for (const auto& line : received())
            if (line == text)
              return true;
          return false;
        },
        limit);
  }
  // its arguments and, after --env--, its environment
  [[nodiscard]] std::vector<std::string> argv() const { return lines("fake-argv"); }
  bool started(std::chrono::milliseconds limit = std::chrono::seconds(5)) const {
    return eventually([&] { return std::filesystem::exists(file("fake-argv")); }, limit);
  }
  // the bytes of tone the shim took
  [[nodiscard]] std::int64_t tone() const {
    const auto found = lines("fake-tone");
    return found.empty() ? 0 : std::stoll(found.front());
  }
  void forget() const {
    std::error_code ignored;
    for (const auto* name : {"fake-argv", "fake-received", "fake-inbox", "fake-tone"})
      std::filesystem::remove(file(name), ignored);
  }

 private:
  std::string home_;
};

// a supervisor over the jail rig's runtime, ca and resolv.conf, with the fake installed as <dir>/soloist and the
// scratch at <dir>/scratch. started at once; the gate stays shut until opened
class SoloistRig {
 public:
  // logs gets soloist's lines, as audiod's children's lines would
  explicit SoloistRig(audiod::SoloistTimings timings = quickTimings(), std::string fake = CADENCE_FAKE_SOLOIST,
                      audiod::ChildLogTarget logs = {})
      : fake_(std::move(fake)), dir_(std::filesystem::path(jail_.root()).parent_path().string()) {
    install();
    audiod::SoloistOptions options{
        .timings = timings, .resolvConf = jail_.file("resolv.conf"), .pcm = {}, .logs = std::move(logs)};
    supervisor_ = std::make_unique<audiod::SoloistSupervisor>(log_, reports_.hooks(), std::move(options));
    supervisor_->start();
  }
  ~SoloistRig() { supervisor_->stop(); }

  SoloistRig(const SoloistRig&) = delete;
  SoloistRig& operator=(const SoloistRig&) = delete;

  [[nodiscard]] audiod::SoloistSettings settings(std::string name = "Kitchen",
                                                 std::string key = std::string(kFakeKey)) const {
    return {.enabled = true,
            .apiKey = std::move(key),
            .deviceName = std::move(name),
            .dir = dir_,
            .scratchDir = dir_ + "/scratch",
            .caFile = jail_.file("ca.crt"),
            .initialVolume = 35};
  }
  // enabled with the gate open, ready once the fake answered
  void run(std::string name = "Kitchen", std::string key = std::string(kFakeKey)) {
    supervisor().configure(settings(std::move(name), std::move(key)));
    supervisor().gate(true);
  }

  void install() const {
    std::filesystem::copy_file(fake_, build(), std::filesystem::copy_options::overwrite_existing);
    std::filesystem::permissions(build(), std::filesystem::perms(0755));
  }
  void removeBuild() const { std::filesystem::remove(build()); }

  [[nodiscard]] audiod::SoloistSupervisor& supervisor() { return *supervisor_; }
  [[nodiscard]] SoloistReports& reports() { return reports_; }
  [[nodiscard]] const TimedLog& logs() const { return logs_; }
  [[nodiscard]] PcmInput& music() { return music_; }
  [[nodiscard]] const JailRig& jail() const { return jail_; }
  [[nodiscard]] const std::string& dir() const { return dir_; }
  [[nodiscard]] std::string build() const { return dir_ + "/soloist"; }
  [[nodiscard]] std::string home() const { return dir_ + "/scratch/home"; }
  [[nodiscard]] FakeSoloist fake() const { return FakeSoloist(home()); }

 private:
  JailRig jail_;
  std::string fake_;
  std::string dir_;
  TimedLog logs_;
  audiod::Log log_ = logs_.log();
  PcmInput music_;
  SoloistReports reports_;
  std::unique_ptr<audiod::SoloistSupervisor> supervisor_;
};

}  // namespace cadence::test_support
