#include "audiod/airplay-config.h"
#include "audiod/airplay-supervisor.h"
#include "support/airplay-rig.h"
#include "support/child-lines.h"
#include "support/eventually.h"
#include "support/fifo-writer.h"
#include "support/jail-rig.h"

#include <catch2/catch_test_macros.hpp>

#include <arpa/inet.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <signal.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <chrono>
#include <cstdint>
#include <filesystem>
#include <fstream>
#include <iterator>
#include <mutex>
#include <set>
#include <sstream>
#include <string>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;
using Clock = std::chrono::steady_clock;

namespace {

constexpr auto kReady = AirplayState::Ready;
constexpr auto kFailed = AirplayState::Failed;

std::string contents(const std::string& path) {
  std::ifstream in(path);
  return {std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
}

mode_t modeOf(const std::string& path) {
  struct stat info {};
  REQUIRE(::lstat(path.c_str(), &info) == 0);
  return info.st_mode;
}

// a process with pid neither runs nor waits to be reaped
bool gone(long pid) { return !std::filesystem::exists("/proc/" + std::to_string(pid)); }

// a pid the supervisor logged for a daemon it started: the last one
long loggedPid(const TimedLog& logs, std::string_view daemon) {
  const auto text = logs.all();
  const auto key = "\"daemon\":\"" + std::string(daemon) + "\",\"pid\":";
  const auto at = text.rfind(key);
  REQUIRE(at != std::string::npos);
  return std::stol(text.substr(at + key.size()));
}

std::vector<std::string> nulSeparated(const std::string& path) {
  std::vector<std::string> parts;
  std::istringstream in(contents(path));
  for (std::string part; std::getline(in, part, '\0');)
    parts.push_back(part);
  return parts;
}

std::vector<std::string> sorted(std::vector<std::string> items) {
  std::sort(items.begin(), items.end());
  return items;
}

std::vector<std::string> baseEnv(const AirplayRuntime& runtime) {
  const auto scratch = runtime.scratch();
  return {"PATH=/nonexistent", "HOME=" + scratch, "GIO_MODULE_DIR=" + scratch + "/empty", "GIO_USE_VFS=local",
          "GCONV_PATH=" + scratch + "/empty"};
}

std::vector<std::string> busEnv(const AirplayRuntime& runtime) {
  auto env = baseEnv(runtime);
  env.push_back("DBUS_SYSTEM_BUS_ADDRESS=unix:path=" + runtime.scratch() + "/bus.sock");
  return env;
}

// the gaps between each log line holding from and the next one holding to
std::vector<std::chrono::milliseconds> gaps(const TimedLog& logs, std::string_view from, std::string_view to) {
  const auto ends = logs.times(from);
  const auto starts = logs.times(to);
  std::vector<std::chrono::milliseconds> found;
  for (const auto end : ends) {
    const auto next = std::find_if(starts.begin(), starts.end(), [&](Clock::time_point at) { return at >= end; });
    if (next != starts.end())
      found.push_back(std::chrono::duration_cast<std::chrono::milliseconds>(*next - end));
  }
  return found;
}

// written without a stall, no chunk held for long, with a loaded machine's slack
bool drainedQuickly(const Written& written) { return !written.stalled && written.slowest < 750ms; }

}  // namespace

TEST_CASE("nothing starts until a config enables the receiver and the gate opens") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  rig.supervisor().gate(true);
  CHECK(rig.reports().waitLatest(AirplayState::Off));
  rig.supervisor().configure({.enabled = false, .name = "Kitchen", .dir = rig.runtime().dir(),
                              .scratchDir = rig.runtime().scratch()});
  rig.supervisor().gate(false);
  rig.supervisor().configure(rig.runtime().settings());
  CHECK(rig.reports().waitState(AirplayState::Starting));
  std::this_thread::sleep_for(300ms);
  CHECK(rig.logs().count(msg("airplay_daemon_started")) == 0);
  CHECK(rig.runtime().starts().empty());

  rig.supervisor().gate(true);
  const auto ready = rig.reports().waitState(kReady);
  REQUIRE(ready);
  CHECK(ready->code.empty());
}

TEST_CASE("the daemons start in order, each once the one before is ready, and the receiver gets the bus") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  rig.run();
  REQUIRE(rig.reports().waitState(kReady));
  const auto daemons = startedDaemons();
  REQUIRE(rig.logs().times(msg("airplay_daemon_started")).size() == daemons.size());
  const auto log = rig.logs().all();
  std::size_t previous = 0;
  for (const auto& daemon : daemons) {
    INFO(daemon);
    const auto at = log.find("\"daemon\":\"" + daemon + "\"");
    REQUIRE(at != std::string::npos);
    CHECK(at >= previous);
    previous = at;
  }
  std::vector<std::string> started;
  for (const auto& [name, pid] : rig.runtime().starts())
    started.push_back(name);
  CHECK(started == fakePrograms());

  // shairport's unique name on the private bus, and a connection still open
  REQUIRE(eventually([&] { return !rig.receiver().owners().empty(); }));
  CHECK(rig.receiver().owners().front().starts_with(":"));
  CHECK(rig.receiver().attachedOpen());
  CHECK(rig.receiver().detachedCount() == 0);
}

TEST_CASE("a daemon that never gets ready fails the attempt, and the next one does not start") {
  REQUIRE_AIRPLAY_BUS();
  auto timings = quickAirplayTimings();
  timings.resourceWait = 400ms;
  timings.receiverWait = 600ms;
  for (const auto& program : fakePrograms()) {
    INFO(program);
    AirplayRig rig(timings);
    rig.runtime().mode(program, "silent");
    rig.run();
    const auto failed = rig.reports().waitState(kFailed);
    REQUIRE(failed);
    CHECK(failed->code == "timeout");
    const auto starts = rig.runtime().starts();
    REQUIRE_FALSE(starts.empty());
    CHECK(starts.back().first == program);
  }
}

TEST_CASE("a daemon that exits while it starts fails the attempt at once") {
  REQUIRE_AIRPLAY_BUS();
  auto timings = quickAirplayTimings();
  timings.receiverWait = 10s;
  AirplayRig rig(timings);
  rig.runtime().mode("shairport-sync", "exit");
  const auto began = Clock::now();
  rig.run();
  const auto failed = rig.reports().waitState(kFailed);
  // a spawn that failed names its step and errno there
  INFO("audiod's log:\n" << rig.logs().all());
  REQUIRE(failed);
  CHECK(failed->code == "exited");
  CHECK(Clock::now() - began < 5s);
}

TEST_CASE("every daemon runs through the runtime's loader with exactly its closed environment") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  {
    // what audiod has as it starts them, in force on the threads that fork: a descriptor without close-on-exec (the
    // ipc socket), SIGTERM and SIGINT blocked and SIGPIPE ignored
    std::array<int, 2> inheritable{};
    REQUIRE(::pipe(inheritable.data()) == 0);
    const UniqueFd readEnd(inheritable[0]);
    const UniqueFd writeEnd(inheritable[1]);
    const AudiodSignals signals;
    rig.run();
    REQUIRE(rig.reports().waitState(kReady));
  }
  const auto& runtime = rig.runtime();
  const auto scratch = runtime.scratch();
  const auto bin = runtime.runtime() + "/bin/";
  for (const auto& name : fakePrograms()) {
    INFO(name);
    const auto start = runtime.start(name);
    CHECK(sorted(values(start, "env")) == sorted(name == "nqptp" ? baseEnv(runtime) : busEnv(runtime)));
    CHECK(value(start, "ppid") == std::to_string(::getpid()));
    CHECK(value(start, "nice") == "5");
    CHECK(value(start, "pdeathsig") == std::to_string(SIGKILL));
    CHECK(value(start, "nonewprivs") == "1");
    CHECK(value(start, "sigblk") == "0000000000000000");
    CHECK(value(start, "sigign") == "0000000000000000");
    // nothing of audiod's reaches it, and what it prints goes nowhere
    CHECK(value(start, "fds") == "0,1,2");
    CHECK(value(start, "stdin") == "/dev/null");
    CHECK(value(start, "stdout") == "/dev/null");
    CHECK(value(start, "stderr") == "/dev/null");
    if (::geteuid() == 0)
      CHECK(value(start, "oom") == childOomScore(500));
    CHECK(values(start, "arg").front() == bin + name);
  }
  if (kAirplayLan) {
    CHECK(values(runtime.start("avahi-daemon"), "arg") ==
          std::vector<std::string>{bin + "avahi-daemon", "--file=" + scratch + "/avahi.conf", "--no-drop-root",
                                   "--no-chroot", "--no-rlimits"});
    CHECK(values(runtime.start("nqptp"), "arg") == std::vector<std::string>{bin + "nqptp"});
  }
  CHECK(values(runtime.start("shairport-sync"), "arg") ==
        std::vector<std::string>{bin + "shairport-sync", "--configfile", scratch + "/shairport.conf"});

  // the real dbus-daemon: the loader's command line and the base environment
  const auto bus = loggedPid(rig.logs(), "dbus");
  const auto proc = "/proc/" + std::to_string(bus);
  CHECK(nulSeparated(proc + "/cmdline") ==
        std::vector<std::string>{runtime.runtime() + "/lib/" + std::string(airplay::kLoader), "--library-path",
                                 runtime.runtime() + "/lib", bin + "dbus-daemon", "--nofork", "--nosyslog",
                                 "--nopidfile", "--config-file=" + scratch + "/dbus.conf"});
  CHECK(sorted(nulSeparated(proc + "/environ")) == sorted(baseEnv(runtime)));
}

// M6b-D7: with logs, each daemon's stdout and stderr are pipes its lines come from, stdout's INFO and stderr's WARN,
// under the name cadenced knows it by; audiod's own log keeps none of them
TEST_CASE("the daemons' output goes out as their lines, and audiod's own log keeps none of it") {
  REQUIRE_AIRPLAY_BUS();
  ChildLines lines;
  AirplayRig rig(quickAirplayTimings(), lines.target());
  rig.run();
  REQUIRE(rig.reports().waitState(kReady));
  std::vector<std::pair<std::string, std::string>> daemons{{"shairport-sync", "shairport-sync"}};
  if (kAirplayLan) {
    daemons.emplace_back("avahi-daemon", "avahi");
    daemons.emplace_back("nqptp", "nqptp");
  }
  for (const auto& [program, source] : daemons) {
    INFO(program);
    const auto up = lines.find(program + ": up");
    const auto complaint = lines.find(program + ": complaint");
    REQUIRE(up);
    REQUIRE(complaint);
    CHECK(up->source == source);
    CHECK(up->level == cadence_audiod_v1_LogLevel_LOG_LEVEL_INFO);
    CHECK(complaint->source == source);
    CHECK(complaint->level == cadence_audiod_v1_LogLevel_LOG_LEVEL_WARN);
    const auto start = rig.runtime().start(program);
    CHECK(value(start, "stdout").starts_with("pipe:"));
    CHECK(value(start, "stderr").starts_with("pipe:"));
    CHECK(value(start, "stdout") != value(start, "stderr"));
  }
  CHECK(rig.logs().all().find("complaint") == std::string::npos);
}

TEST_CASE("the configs are written afresh, private, and avahi's base made without its etc") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  const auto& runtime = rig.runtime();
  const auto base = runtime.layout().avahiBase;
  std::filesystem::create_directories(base + "/etc/avahi/services");
  std::filesystem::create_directories(runtime.scratch());
  std::ofstream(runtime.scratch() + "/shairport.conf") << "stale";
  rig.run("Kit\"chen ${HOME}");
  REQUIRE(rig.reports().waitState(kReady));
  const auto scratch = runtime.scratch();
  const auto expected = airplay::shairportConfig("Kit\"chen ${HOME}", 0x020000000001, scratch + "/run/audio.fifo",
                                                 scratch + "/run/metadata.fifo");
  REQUIRE(expected);
  CHECK(contents(scratch + "/shairport.conf") == *expected);
  CHECK(contents(scratch + "/shairport.conf").find("name = \"Kit\\\"chen $${HOME}\";") != std::string::npos);
  CHECK(contents(scratch + "/dbus.conf") == airplay::busConfig(scratch + "/bus.sock", ::geteuid()));
  for (const auto* file : {"shairport.conf", "dbus.conf"})
    CHECK((modeOf(scratch + "/" + file) & 07777) == 0600);
  for (const auto& dir : {scratch, scratch + "/run", scratch + "/empty"})
    CHECK((modeOf(dir) & 07777) == 0700);
  CHECK(std::filesystem::is_empty(scratch + "/empty"));
  if (!kAirplayLan) {
    // a sim build that runs no avahi writes none of its config and leaves its base alone
    CHECK_FALSE(std::filesystem::exists(scratch + "/avahi.conf"));
    CHECK(std::filesystem::is_directory(base + "/etc/avahi/services"));
    return;
  }
  CHECK(contents(scratch + "/avahi.conf") == airplay::avahiConfig(0x020000000001));
  CHECK((modeOf(scratch + "/avahi.conf") & 07777) == 0600);
  for (const auto& dir : {base, base + "/run"})
    CHECK((modeOf(dir) & 07777) == 0700);
  CHECK_FALSE(std::filesystem::exists(base + "/etc"));
}

TEST_CASE("both fifos are made 0600, and a regular file at a fifo's path is refused") {
  REQUIRE_AIRPLAY_BUS();
  {
    AirplayRig rig;
    rig.run();
    REQUIRE(rig.reports().waitState(kReady));
    for (const auto* fifo : {"/run/audio.fifo", "/run/metadata.fifo"}) {
      const auto mode = modeOf(rig.runtime().scratch() + fifo);
      CHECK(S_ISFIFO(mode));
      CHECK((mode & 07777) == 0600);
    }
  }
  AirplayRig rig;
  std::filesystem::create_directories(rig.runtime().scratch() + "/run");
  const auto path = rig.runtime().scratch() + "/run/audio.fifo";
  std::ofstream(path) << "shairport would write here";
  rig.run();
  const auto failed = rig.reports().waitState(kFailed);
  REQUIRE(failed);
  CHECK(failed->code == "scratch_failed");
  CHECK_FALSE(rig.runtime().started("shairport-sync"));
  CHECK(contents(path) == "shairport would write here");
}

TEST_CASE("a port nqptp needs held by another fails the attempt, and the holder lives on") {
  REQUIRE_AIRPLAY_BUS();
  REQUIRE_AIRPLAY_LAN();
  AirplayRig rig;
  // the holder: a process of its own, as a foreign timing daemon would be
  UniqueFd held(::socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0));
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_addr.s_addr = htonl(INADDR_ANY);
  address.sin_port = htons(rig.runtime().layout().ports.general);
  REQUIRE(::bind(held.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
  const pid_t holder = ::fork();
  REQUIRE(holder >= 0);
  if (holder == 0) {
    for (;;)
      ::pause();
  }
  held.reset();
  struct Reaper {
    pid_t pid;
    ~Reaper() {
      ::kill(pid, SIGKILL);
      ::waitpid(pid, nullptr, 0);
    }
  } reaper{holder};

  rig.run();
  const auto failed = rig.reports().waitState(kFailed);
  REQUIRE(failed);
  CHECK(failed->code == "ports_busy");
  CHECK_FALSE(rig.runtime().started("nqptp"));
  CHECK(::waitpid(holder, nullptr, WNOHANG) == 0);
  CHECK(::kill(holder, 0) == 0);

  // once it is gone, a later attempt takes the ports
  ::kill(holder, SIGKILL);
  CHECK(rig.reports().waitState(kReady));
}

TEST_CASE("a stale timing interface is removed, and anything but a regular file there is refused") {
  REQUIRE_AIRPLAY_BUS();
  REQUIRE_AIRPLAY_LAN();
  {
    AirplayRig rig;
    const auto path = rig.runtime().layout().timingPath;
    const std::uint16_t version = airplay::kTimingVersion;
    std::ofstream(path, std::ios::binary).write(reinterpret_cast<const char*>(&version), sizeof version);
    rig.run();
    REQUIRE(rig.reports().waitState(kReady));
    // nqptp started with none there, and made its own
    CHECK(value(rig.runtime().start("nqptp"), "timing_at_start") == "0");
    CHECK(std::filesystem::is_regular_file(path));
  }
  for (const auto kind : {"symlink", "directory"}) {
    INFO(kind);
    AirplayRig rig;
    const auto path = rig.runtime().layout().timingPath;
    const auto target = rig.runtime().file("someone-elses");
    std::filesystem::create_directories(target);
    std::ofstream(target + "/keep") << "x";
    if (std::string_view(kind) == "symlink")
      std::filesystem::create_directory_symlink(target, path);
    else
      std::filesystem::create_directories(path);
    rig.run();
    const auto failed = rig.reports().waitState(kFailed);
    REQUIRE(failed);
    CHECK(failed->code == "foreign_service");
    CHECK_FALSE(rig.runtime().started("nqptp"));
    CHECK(std::filesystem::exists(target + "/keep"));
  }
}

TEST_CASE("a runtime without its programs fails at once, before anything starts") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  std::filesystem::remove(rig.runtime().runtime() + "/bin/shairport-sync");
  rig.run();
  const auto failed = rig.reports().waitState(kFailed);
  REQUIRE(failed);
  CHECK(failed->code == "runtime_missing");
  CHECK(rig.logs().count(msg("airplay_daemon_started")) == 0);
}

TEST_CASE("failed attempts are retried after a wait that doubles, from the first again after a steady run") {
  REQUIRE_AIRPLAY_BUS();
  // a cap far above the first wait, so that the upper bounds below leave a loaded machine its slack and still tell a
  // wait that doubled on, or did not start over, from one that did right
  auto timings = quickAirplayTimings();
  timings.lastBackoff = 1600ms;
  AirplayRig rig(timings);
  rig.runtime().mode("shairport-sync", "exit");
  rig.run();
  REQUIRE(rig.logs().eventually(msg("airplay_failed"), 6, 30s));
  // the steady run: up past 1.5 s, then gone
  rig.runtime().mode("shairport-sync", "exit-after=1700");
  REQUIRE(rig.logs().eventually(msg("airplay_session_lost"), 1, 30s));
  REQUIRE(rig.logs().eventually(msg("airplay_start"), 8, 30s));

  const auto waits = gaps(rig.logs(), msg("airplay_failed"), msg("airplay_start"));
  REQUIRE(waits.size() >= 6);
  CHECK(waits[0] >= 100ms);
  CHECK(waits[1] >= 200ms);
  CHECK(waits[2] >= 400ms);
  CHECK(waits[3] >= 800ms);
  // capped: doubled once more, it would be 3.2 s
  for (const auto capped : {waits[4], waits[5]}) {
    CHECK(capped >= 1600ms);
    CHECK(capped < 3200ms);
  }
  // from the first again: left at the cap, it would be 1.6 s
  const auto afterSteady = gaps(rig.logs(), msg("airplay_session_lost"), msg("airplay_start"));
  REQUIRE_FALSE(afterSteady.empty());
  CHECK(afterSteady.front() >= 100ms);
  CHECK(afterSteady.front() < 1000ms);
}

TEST_CASE("a daemon that exits ends the receiver, and every daemon is reaped before the next attempt") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  rig.run();
  REQUIRE(rig.reports().waitState(kReady));
  const auto first = rig.runtime().starts();
  const auto bus = loggedPid(rig.logs(), "dbus");
  const auto programs = fakePrograms();
  REQUIRE(first.size() == programs.size());
  ::kill(static_cast<pid_t>(first[0].second), SIGKILL);

  REQUIRE(rig.logs().eventually(msg("airplay_session_lost")));
  CHECK(rig.logs().count("\"reason\":\"exited\"") == 1);
  REQUIRE(eventually([&] { return rig.receiver().detachedCount() == 1; }));
  REQUIRE(eventually([&] { return rig.runtime().starts().size() == 2 * programs.size(); }, 10s));
  REQUIRE(rig.reports().waitLatest(kReady));
  // the second attempt's first fake found the first attempt's daemons all reaped
  CHECK(value(rig.runtime().start(programs.front()), "clean") == "1");
  for (const auto& [name, pid] : first) {
    INFO(name);
    CHECK(gone(pid));
  }
  CHECK(gone(bus));
  REQUIRE(eventually([&] { return rig.receiver().owners().size() == 2; }));
}

TEST_CASE("shairport letting its name go ends the receiver") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  rig.runtime().mode("shairport-sync", "release-after=1000");
  rig.run();
  REQUIRE(rig.reports().waitState(kReady));
  REQUIRE(rig.logs().eventually(msg("airplay_session_lost")));
  CHECK(rig.logs().count("\"reason\":\"owner\"") == 1);
  const auto lost = rig.reports().waitState(kFailed);
  REQUIRE(lost);
  CHECK(lost->code == "session_lost");
}

TEST_CASE("disabled, the receiver stops and every daemon is reaped") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  rig.run();
  REQUIRE(rig.reports().waitState(kReady));
  const auto starts = rig.runtime().starts();
  const auto bus = loggedPid(rig.logs(), "dbus");
  auto settings = rig.runtime().settings();
  settings.enabled = false;
  rig.supervisor().configure(settings);
  REQUIRE(rig.reports().waitLatest(AirplayState::Off));
  for (const auto& [name, pid] : starts) {
    INFO(name);
    CHECK(gone(pid));
  }
  CHECK(gone(bus));
  CHECK(rig.receiver().detachedCount() == 1);
  std::this_thread::sleep_for(300ms);
  CHECK(rig.runtime().starts().size() == starts.size());
}

TEST_CASE("a stop gives shairport, then avahi, a grace for their goodbyes, and kills the rest at once") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  // each says goodbye for 200 ms after its SIGTERM, well within the grace
  for (const auto& program : fakePrograms())
    rig.runtime().mode(program, "linger-term=200");
  rig.run();
  REQUIRE(rig.reports().waitState(kReady));
  const auto starts = rig.runtime().starts();
  auto settings = rig.runtime().settings();
  settings.enabled = false;
  rig.supervisor().configure(settings);
  REQUIRE(rig.reports().waitLatest(AirplayState::Off));
  for (const auto& [name, pid] : starts) {
    INFO(name);
    CHECK(gone(pid));
  }
  const auto events = rig.runtime().events();
  const auto at = [&](std::string_view event) {
    return std::distance(events.begin(), std::find(events.begin(), events.end(), event));
  };
  const auto last = std::ssize(events);
  std::string listed;
  for (const auto& event : events)
    listed += event + "; ";
  INFO(listed);
  CHECK(at("shairport-sync term") < at("shairport-sync exit"));
  CHECK(at("shairport-sync exit") < last);
  if (kAirplayLan) {
    // shairport's goodbye goes out through avahi, which is stopped after it
    CHECK(at("shairport-sync exit") < at("avahi-daemon term"));
    CHECK(at("avahi-daemon term") < at("avahi-daemon exit"));
    CHECK(at("avahi-daemon exit") < last);
    // nqptp says nothing to phones: killed with no grace
    CHECK(at("nqptp exit") == last);
  }
}

TEST_CASE("a daemon still there when its stop grace is up is killed") {
  REQUIRE_AIRPLAY_BUS();
  auto timings = quickAirplayTimings();
  timings.stopGrace = 300ms;
  AirplayRig rig(timings);
  rig.runtime().mode("shairport-sync", "hold-term");
  rig.run();
  REQUIRE(rig.reports().waitState(kReady));
  const auto shairport = rig.runtime().starts().back();
  REQUIRE(shairport.first == "shairport-sync");
  auto settings = rig.runtime().settings();
  settings.enabled = false;
  const auto began = Clock::now();
  rig.supervisor().configure(settings);
  REQUIRE(rig.reports().waitLatest(AirplayState::Off));
  CHECK(Clock::now() - began >= 300ms);
  CHECK(gone(shairport.second));
  CHECK(rig.runtime().events() == std::vector<std::string>{"shairport-sync term"});
}

TEST_CASE("a new config restarts the receiver with it") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  rig.run("Kitchen");
  REQUIRE(rig.reports().waitState(kReady));
  rig.supervisor().configure(rig.runtime().settings("Kitchen"));
  rig.supervisor().configure(rig.runtime().settings("Hall"));
  REQUIRE(eventually([&] { return rig.runtime().starts().size() == 2 * fakePrograms().size(); }, 10s));
  REQUIRE(rig.reports().waitLatest(kReady));
  CHECK(contents(rig.runtime().scratch() + "/shairport.conf").find("name = \"Hall\";") != std::string::npos);
  // the same config again changed nothing
  CHECK(rig.logs().count(msg("airplay_start")) == 2);
}

TEST_CASE("a stop while an attempt waits ends it at once, every daemon reaped") {
  REQUIRE_AIRPLAY_BUS();
  auto timings = quickAirplayTimings();
  timings.receiverWait = 30s;
  AirplayRig rig(timings);
  rig.runtime().mode("shairport-sync", "silent");
  rig.run();
  REQUIRE(rig.runtime().waitStarted("shairport-sync"));
  const auto starts = rig.runtime().starts();
  const auto began = Clock::now();
  rig.supervisor().stop();
  CHECK(Clock::now() - began < 3s);
  for (const auto& [name, pid] : starts) {
    INFO(name);
    CHECK(gone(pid));
  }
}

TEST_CASE("with the gate shut the receiver runs on, its audio drained unheard, and the writer never waits") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  rig.run();
  REQUIRE(rig.reports().waitState(kReady));
  rig.supervisor().openOutput(rig.music());
  // as the receiver releases it while it genuinely plays
  rig.supervisor().holdOutput(false);
  const auto writer = fifoWriter(rig.runtime().scratch() + "/run/audio.fifo");
  writeFrames(writer.get(), 4800);
  REQUIRE(eventually([&] { return rig.music().status().framesIn >= 4800; }));

  rig.supervisor().gate(false);
  const auto in = rig.music().status().framesIn;
  // seconds of audio at once, far more than the fifo holds: only a reader that keeps reading lets it through
  CHECK(drainedQuickly(writeFrames(writer.get(), 3 * 48000)));
  std::this_thread::sleep_for(100ms);
  CHECK(rig.music().status().framesIn == in);
  CHECK(rig.reports().latest().state == kReady);
  CHECK(rig.receiver().detachedCount() == 0);

  // and while another source owns the session, the same
  rig.supervisor().gate(true);
  rig.supervisor().closeOutput();
  CHECK(drainedQuickly(writeFrames(writer.get(), 3 * 48000)));
  std::this_thread::sleep_for(100ms);
  CHECK(rig.music().status().framesIn == in);
  rig.supervisor().openOutput(rig.music());
  writeFrames(writer.get(), 4800);
  CHECK(eventually([&] { return rig.music().status().framesIn >= in + 4800; }));
}

TEST_CASE("a receiver that fails ends the receiver, Failed with its code, and it starts again") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  rig.receiver().failOnAttach("invalid_state");
  rig.run();
  const auto failed = rig.reports().waitState(kFailed);
  REQUIRE(failed);
  CHECK(failed->code == "invalid_state");
  CHECK(rig.logs().count("\"reason\":\"receiver\"") >= 1);
  REQUIRE(eventually([&] { return rig.receiver().detachedCount() >= 1; }));
  CHECK(eventually([&] { return rig.receiver().owners().size() >= 2; }, 10s));
}

TEST_CASE("a receiver's deadline wakes the supervisor for its tick, and its tasks run on the supervisor's thread") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  rig.receiver().tickAfter(300ms);
  rig.run();
  REQUIRE(rig.reports().waitState(kReady));
  REQUIRE(eventually([&] { return !rig.receiver().ticks().empty(); }));
  CHECK(rig.receiver().ticks().front() >= 300ms);
  CHECK(rig.receiver().ticks().front() < 1s);

  std::mutex mutex;
  std::vector<std::pair<int, std::thread::id>> ran;
  for (int task = 1; task <= 3; ++task)
    rig.supervisor().post([&, task] {
      std::scoped_lock lock(mutex);
      ran.emplace_back(task, std::this_thread::get_id());
    });
  REQUIRE(eventually([&] {
    std::scoped_lock lock(mutex);
    return ran.size() == 3;
  }));
  for (int task = 1; task <= 3; ++task) {
    CHECK(ran[static_cast<std::size_t>(task - 1)].first == task);
    CHECK(ran[static_cast<std::size_t>(task - 1)].second == rig.receiver().thread());
  }
  CHECK(rig.receiver().thread() != std::this_thread::get_id());
}

TEST_CASE("what the metadata pipe says reaches the attached receiver") {
  REQUIRE_AIRPLAY_BUS();
  AirplayRig rig;
  rig.run();
  REQUIRE(rig.reports().waitState(kReady));
  REQUIRE(eventually([&] { return !rig.receiver().owners().empty(); }));
  const auto writer = fifoWriter(rig.runtime().scratch() + "/run/metadata.fifo");
  const std::string items =
      "<item><type>636f7265</type><code>6d696e6d</code><length>5</length>\n"
      "<data encoding=\"base64\">\nVGl0bGU=</data></item>\n";
  REQUIRE(::write(writer.get(), items.data(), items.size()) == static_cast<ssize_t>(items.size()));
  REQUIRE(eventually([&] { return !rig.receiver().metadataSeen().empty(); }));
  CHECK(rig.receiver().metadataSeen().back().title == "Title");
}

TEST_CASE("a sim build without the lan daemons runs the bus and shairport alone, and never looks at nqptp's ports") {
  REQUIRE_AIRPLAY_BUS();
  REQUIRE_NO_AIRPLAY_LAN();
  AirplayRig rig;
  const auto& runtime = rig.runtime();
  // a runtime without either is whole for it, a port nqptp would need is held, and its interface's path is what an
  // attempt that ran nqptp would refuse
  for (const auto* program : {"avahi-daemon", "nqptp"})
    std::filesystem::remove(runtime.runtime() + "/bin/" + program);
  UniqueFd held(::socket(AF_INET, SOCK_DGRAM | SOCK_CLOEXEC, 0));
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_addr.s_addr = htonl(INADDR_ANY);
  address.sin_port = htons(runtime.layout().ports.general);
  REQUIRE(::bind(held.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
  std::filesystem::create_directories(runtime.layout().timingPath);

  rig.run();
  REQUIRE(rig.reports().waitState(kReady));
  CHECK(rig.logs().count(msg("airplay_daemon_started")) == 2);
  CHECK(rig.logs().count("\"daemon\":\"avahi\"") == 0);
  CHECK(rig.logs().count("\"daemon\":\"nqptp\"") == 0);
  REQUIRE(rig.runtime().starts().size() == 1);
  CHECK(rig.runtime().starts().front().first == "shairport-sync");
  CHECK_FALSE(std::filesystem::exists(runtime.scratch() + "/avahi.conf"));
  CHECK_FALSE(std::filesystem::exists(runtime.layout().avahiBase));
  CHECK(std::filesystem::is_directory(runtime.layout().timingPath));
}
