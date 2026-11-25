#pragma once

#include "audiod/airplay-stack.h"
#include "audiod/airplay-supervisor.h"
#include "audiod/log.h"
#include "audiod/sys.h"
#include "backend/pcm-input.h"
#include "support/eventually.h"
#include "support/timed-log.h"

#include <catch2/catch_test_macros.hpp>

#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdlib.h>
#include <sys/socket.h>

#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <filesystem>
#include <fstream>
#include <functional>
#include <map>
#include <memory>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <system_error>
#include <thread>
#include <utility>
#include <vector>

// the airplay supervisor over a runtime of fakes (support/fake-airplay.cpp) and the builder's real dbus-daemon, never
// the real receiver: tests never cast
namespace cadence::test_support {

// empty when the build found no dbus-daemon
inline constexpr std::string_view kAirplayBus = CADENCE_DBUS_DAEMON;

#define REQUIRE_AIRPLAY_BUS()                                                                            \
  do {                                                                                                   \
    if (cadence::test_support::kAirplayBus.empty())                                                      \
      SKIP("no dbus-daemon was found when this was configured (the builder's test stage installs one)"); \
  } while (false)

// for what only a build that runs avahi and nqptp does, or only a sim build without them (kAirplayLan)
#define REQUIRE_AIRPLAY_LAN()                                                    \
  do {                                                                           \
    if (!cadence::audiod::kAirplayLan)                                           \
      SKIP("this sim build runs no avahi or nqptp (CADENCE_MEDIA_AIRPLAY_LAN)"); \
  } while (false)
#define REQUIRE_NO_AIRPLAY_LAN()                                                 \
  do {                                                                           \
    if (cadence::audiod::kAirplayLan)                                            \
      SKIP("this build runs avahi and nqptp as well");                           \
  } while (false)

// the fakes an attempt starts, in order: avahi and nqptp only where the build runs them
inline std::vector<std::string> fakePrograms() {
  if (audiod::kAirplayLan)
    return {"avahi-daemon", "nqptp", "shairport-sync"};
  return {"shairport-sync"};
}

// the daemons an attempt logs as it starts them, in order
inline std::vector<std::string> startedDaemons() {
  if (audiod::kAirplayLan)
    return {"dbus", "avahi", "nqptp", "shairport"};
  return {"dbus", "shairport"};
}

// short enough for tests
inline audiod::AirplayTimings quickAirplayTimings() {
  audiod::AirplayTimings timings;
  timings.resourceWait = std::chrono::milliseconds(3000);
  timings.receiverWait = std::chrono::milliseconds(3000);
  timings.firstBackoff = std::chrono::milliseconds(100);
  timings.lastBackoff = std::chrono::milliseconds(400);
  timings.steady = std::chrono::milliseconds(1500);
  return timings;
}

// the loader this process runs under: the runtime's own stands in for it
inline std::string hostLoader() {
  std::ifstream maps("/proc/self/maps");
  for (std::string line; std::getline(maps, line);) {
    const auto at = line.find('/');
    if (at != std::string::npos && line.find("/ld-linux", at) != std::string::npos)
      return line.substr(at);
  }
  FAIL("no loader in /proc/self/maps");
  return {};
}

inline std::uint16_t freeUdpPort() {
  const audiod::UniqueFd fd(::socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0));
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_addr.s_addr = htonl(INADDR_ANY);
  socklen_t length = sizeof address;
  REQUIRE(::bind(fd.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
  REQUIRE(::getsockname(fd.get(), reinterpret_cast<sockaddr*>(&address), &length) == 0);
  return ntohs(address.sin_port);
}

// what a fake wrote about its start: each key's values, in order
using FakeStart = std::multimap<std::string, std::string>;

inline std::vector<std::string> values(const FakeStart& start, const std::string& key) {
  std::vector<std::string> found;
  const auto [first, last] = start.equal_range(key);
  for (auto it = first; it != last; ++it)
    found.push_back(it->second);
  return found;
}

inline std::string value(const FakeStart& start, const std::string& key) {
  const auto found = values(start, key);
  return found.empty() ? std::string() : found.front();
}

// a board's airplay dir with the fakes and the real dbus-daemon in runtime/bin and this process's loader as the
// runtime's, a scratch, avahi's base, the timing path, an interface eth0 and free ports, and the fakes' rig in fake/
class AirplayRuntime {
 public:
  AirplayRuntime() {
    namespace fs = std::filesystem;
    // short, for the bus socket's path; a TempDir of either harness would clash with the other's
    auto pattern = (fs::temp_directory_path() / "cadence-airplay-XXXXXX").string();
    REQUIRE(::mkdtemp(pattern.data()) != nullptr);
    root_ = pattern;
    for (const auto* sub : {"runtime/bin", "runtime/lib", "fake", "net/eth0", "net/lo", "shm", "avahi-parent"})
      fs::create_directories(file(sub));
    install(hostLoader(), "runtime/lib/" + std::string(audiod::airplay::kLoader));
    install(std::string(kAirplayBus), "runtime/bin/dbus-daemon");
    for (const auto* program : {"avahi-daemon", "nqptp", "shairport-sync"})
      install(CADENCE_FAKE_AIRPLAY, "runtime/bin/" + std::string(program));
    std::ofstream(file("net/eth0/flags")) << "0x1003\n";
    std::ofstream(file("net/eth0/address")) << "02:00:00:00:00:01\n";
    std::ofstream(file("net/lo/flags")) << "0x9\n";
    std::ofstream(file("net/lo/address")) << "00:00:00:00:00:00\n";
    options_.avahiBase = file("avahi-parent/avahi");
    options_.timingPath = file("shm/nqptp");
    options_.interfaces = file("net");
    options_.ports = {.event = freeUdpPort(), .general = freeUdpPort(), .control = freeUdpPort()};
    std::ofstream(fake("timing")) << options_.ports.control << ' ' << options_.timingPath << '\n';
  }

  [[nodiscard]] audiod::AirplayOptions options(audiod::AirplayTimings timings = quickAirplayTimings()) const {
    auto options = options_;
    options.timings = timings;
    return options;
  }
  [[nodiscard]] audiod::AirplaySettings settings(std::string name = "Kitchen") const {
    return {.enabled = true, .name = std::move(name), .dir = dir(), .scratchDir = scratch()};
  }

  ~AirplayRuntime() {
    std::error_code ignored;
    std::filesystem::remove_all(root_, ignored);
  }
  AirplayRuntime(const AirplayRuntime&) = delete;
  AirplayRuntime& operator=(const AirplayRuntime&) = delete;

  [[nodiscard]] std::string dir() const { return root_.string(); }
  [[nodiscard]] std::string file(const std::string& name) const { return (root_ / name).string(); }
  [[nodiscard]] std::string scratch() const { return file("scratch"); }
  [[nodiscard]] std::string runtime() const { return file("runtime"); }
  [[nodiscard]] std::string fake(const std::string& name) const { return file("fake/" + name); }
  [[nodiscard]] const audiod::AirplayOptions& layout() const { return options_; }

  // with, run as the runtime's program in place of its fake
  void replace(const std::string& program, const std::string& with) const { install(with, "runtime/bin/" + program); }

  // what the fake does at its next start
  void mode(const std::string& program, const std::string& mode) const {
    std::ofstream(fake(program + ".mode"), std::ios::trunc) << mode << '\n';
  }
  [[nodiscard]] bool started(const std::string& program) const {
    return std::filesystem::exists(fake(program + ".start"));
  }
  bool waitStarted(const std::string& program, std::chrono::milliseconds limit = std::chrono::seconds(10)) const {
    return eventually([&] { return started(program); }, limit);
  }
  // its last start
  [[nodiscard]] FakeStart start(const std::string& program) const {
    FakeStart found;
    std::ifstream in(fake(program + ".start"));
    for (std::string line; std::getline(in, line);) {
      const auto equals = line.find('=');
      if (equals != std::string::npos)
        found.emplace(line.substr(0, equals), line.substr(equals + 1));
    }
    return found;
  }
  // the programs in the order they started, with their pids
  [[nodiscard]] std::vector<std::pair<std::string, long>> starts() const {
    std::vector<std::pair<std::string, long>> found;
    std::ifstream in(fake("starts"));
    std::string name;
    long pid = 0;
    while (in >> name >> pid)
      found.emplace_back(name, pid);
    return found;
  }
  // what the fakes in a term mode did with a SIGTERM, in order: "<name> term", "<name> exit"
  [[nodiscard]] std::vector<std::string> events() const {
    std::vector<std::string> found;
    std::ifstream in(fake("events"));
    for (std::string line; std::getline(in, line);)
      found.push_back(line);
    return found;
  }

 private:
  void install(const std::string& from, const std::string& to) const {
    std::filesystem::copy_file(from, file(to), std::filesystem::copy_options::overwrite_existing);
    std::filesystem::permissions(file(to), std::filesystem::perms(0755));
  }

  std::filesystem::path root_;
  audiod::AirplayOptions options_;
};

// what the supervisor reported, on its thread
class AirplayReports {
 public:
  audiod::AirplayHooks hooks() {
    return {.status = [this](const audiod::AirplayStatus& status) {
      std::scoped_lock lock(mutex_);
      statuses_.push_back(status);
      changed_.notify_all();
    }};
  }

  // the first status of state it reported, however briefly it held
  std::optional<audiod::AirplayStatus> waitState(audiod::AirplayState state,
                                                 std::chrono::milliseconds limit = std::chrono::seconds(15)) {
    return wait([state](const audiod::AirplayStatus& status) { return status.state == state; }, false, limit);
  }
  // the latest status, once it is of state
  std::optional<audiod::AirplayStatus> waitLatest(audiod::AirplayState state,
                                                  std::chrono::milliseconds limit = std::chrono::seconds(15)) {
    return wait([state](const audiod::AirplayStatus& status) { return status.state == state; }, true, limit);
  }
  [[nodiscard]] audiod::AirplayStatus latest() const {
    std::scoped_lock lock(mutex_);
    return statuses_.empty() ? audiod::AirplayStatus{} : statuses_.back();
  }
  [[nodiscard]] std::vector<audiod::AirplayStatus> statuses() const {
    std::scoped_lock lock(mutex_);
    return statuses_;
  }

 private:
  std::optional<audiod::AirplayStatus> wait(const std::function<bool(const audiod::AirplayStatus&)>& matches,
                                            bool latest, std::chrono::milliseconds limit) {
    std::unique_lock lock(mutex_);
    std::optional<audiod::AirplayStatus> found;
    changed_.wait_for(lock, limit, [&] {
      if (latest && !statuses_.empty() && matches(statuses_.back()))
        found = statuses_.back();
      for (const auto& status : statuses_)
        if (!latest && !found && matches(status))
          found = status;
      return found.has_value();
    });
    return found;
  }

  mutable std::mutex mutex_;
  std::condition_variable changed_;
  std::vector<audiod::AirplayStatus> statuses_;
};

// what the receiver's source would see, on the supervisor's thread
class FakeReceiver final : public audiod::AirplayReceiver {
 public:
  // before the supervisor starts: each attach ends in that failure, or asks for a tick after that wait
  void failOnAttach(std::string code) { failOnAttach_ = std::move(code); }
  void tickAfter(std::chrono::milliseconds wait) { tickAfter_ = wait; }

  void attached(dbus::Client& client, const std::string& owner) override {
    std::scoped_lock lock(mutex_);
    owners_.push_back(owner);
    attachedOpen_ = client.open();
    thread_ = std::this_thread::get_id();
    attachedAt_ = Clock::now();
    failure_ = failOnAttach_;
    if (tickAfter_)
      deadline_ = attachedAt_ + *tickAfter_;
  }
  void signal(const dbus::Message& /*message*/) override {}
  void detached() override {
    std::scoped_lock lock(mutex_);
    ++detached_;
    failure_.clear();
    deadline_.reset();
  }
  void metadata(const audiod::MediaMetadata& metadata) override {
    std::scoped_lock lock(mutex_);
    metadata_.push_back(metadata);
  }
  [[nodiscard]] std::optional<Clock::time_point> deadline() const override {
    std::scoped_lock lock(mutex_);
    return deadline_;
  }
  void tick(Clock::time_point now) override {
    std::scoped_lock lock(mutex_);
    if (deadline_ && now >= *deadline_) {
      deadline_.reset();
      ticks_.push_back(now - attachedAt_);
    }
  }
  // only ever read on the supervisor's thread, which alone writes it
  [[nodiscard]] std::string_view failure() const override { return failure_; }

  [[nodiscard]] std::thread::id thread() const {
    std::scoped_lock lock(mutex_);
    return thread_;
  }
  // when each due tick came, after its attach
  [[nodiscard]] std::vector<Clock::duration> ticks() const {
    std::scoped_lock lock(mutex_);
    return ticks_;
  }
  [[nodiscard]] std::vector<audiod::MediaMetadata> metadataSeen() const {
    std::scoped_lock lock(mutex_);
    return metadata_;
  }

  [[nodiscard]] std::vector<std::string> owners() const {
    std::scoped_lock lock(mutex_);
    return owners_;
  }
  [[nodiscard]] int detachedCount() const {
    std::scoped_lock lock(mutex_);
    return detached_;
  }
  [[nodiscard]] bool attachedOpen() const {
    std::scoped_lock lock(mutex_);
    return attachedOpen_;
  }

 private:
  std::string failOnAttach_;
  std::optional<std::chrono::milliseconds> tickAfter_;
  std::string failure_;

  mutable std::mutex mutex_;
  std::vector<std::string> owners_;
  int detached_ = 0;
  bool attachedOpen_ = false;
  std::thread::id thread_;
  Clock::time_point attachedAt_{};
  std::optional<Clock::time_point> deadline_;
  std::vector<Clock::duration> ticks_;
  std::vector<audiod::MediaMetadata> metadata_;
};

// a supervisor over the runtime, started at once; the gate stays shut until opened
class AirplayRig {
 public:
  // logs gets the daemons' lines, as audiod's children's lines would
  explicit AirplayRig(audiod::AirplayTimings timings = quickAirplayTimings(), audiod::ChildLogTarget logs = {})
      : timings_(timings), lines_(std::move(logs)) {}
  ~AirplayRig() {
    if (supervisor_)
      supervisor_->stop();
  }

  AirplayRig(const AirplayRig&) = delete;
  AirplayRig& operator=(const AirplayRig&) = delete;

  // after the runtime is set up as the test wants it
  audiod::AirplaySupervisor& supervisor() {
    if (!supervisor_) {
      auto options = runtime_.options(timings_);
      options.logs = lines_;
      supervisor_ = std::make_unique<audiod::AirplaySupervisor>(log_, reports_.hooks(), std::move(options));
      supervisor_->receiver(receiver_);
      supervisor_->start();
    }
    return *supervisor_;
  }
  // enabled with the gate open
  void run(std::string name = "Kitchen") {
    supervisor().configure(runtime_.settings(std::move(name)));
    supervisor().gate(true);
  }

  [[nodiscard]] const AirplayRuntime& runtime() const { return runtime_; }
  [[nodiscard]] AirplayReports& reports() { return reports_; }
  [[nodiscard]] const TimedLog& logs() const { return logs_; }
  [[nodiscard]] PcmInput& music() { return music_; }
  [[nodiscard]] FakeReceiver& receiver() { return *receiver_; }

 private:
  AirplayRuntime runtime_;
  audiod::AirplayTimings timings_;
  audiod::ChildLogTarget lines_;
  TimedLog logs_;
  audiod::Log log_ = logs_.log();
  PcmInput music_;
  AirplayReports reports_;
  std::shared_ptr<FakeReceiver> receiver_ = std::make_shared<FakeReceiver>();
  std::unique_ptr<audiod::AirplaySupervisor> supervisor_;
};

}  // namespace cadence::test_support
