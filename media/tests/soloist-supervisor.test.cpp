#include "audiod/soloist-supervisor.h"
#include "support/child-lines.h"
#include "support/eventually.h"
#include "support/soloist-rig.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

#include <arpa/inet.h>
#include <time.h>
#include <netinet/in.h>
#include <sys/socket.h>

#include <algorithm>
#include <chrono>
#include <filesystem>
#include <fstream>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;
using State = SoloistState;
using Clock = std::chrono::steady_clock;

namespace {

bool listening(std::uint16_t port) {
  const UniqueFd fd(::socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0));
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_port = htons(port);
  address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  return ::connect(fd.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0;
}

std::uint16_t portOf(const FakeSoloist& fake) {
  const auto lines = fake.lines("ws.port");
  REQUIRE_FALSE(lines.empty());
  return static_cast<std::uint16_t>(std::stoul(lines.front()));
}

std::chrono::milliseconds between(Clock::time_point from, Clock::time_point to) {
  return std::chrono::duration_cast<std::chrono::milliseconds>(to - from);
}

// what this process's threads have used of a cpu, the supervisor's among them
std::chrono::nanoseconds processCpu() {
  timespec used{};
  REQUIRE(::clock_gettime(CLOCK_PROCESS_CPUTIME_ID, &used) == 0);
  return std::chrono::seconds(used.tv_sec) + std::chrono::nanoseconds(used.tv_nsec);
}

constexpr std::string_view kUp = R"({"type":"auth_state","logged_in":true,"is_active":true})";

std::string setVolume(int level) {
  return R"({"type":"command","command":"set_volume","volume":)" + std::to_string(level) + "}";
}

std::size_t countOf(const std::vector<std::string>& lines, const std::string& line) {
  return static_cast<std::size_t>(std::count(lines.begin(), lines.end(), line));
}

void writeScript(const std::string& path, std::string_view body) {
  std::ofstream(path) << "#!/bin/sh\n" << body << "\n";
  std::filesystem::permissions(path, std::filesystem::perms(0755));
}

}  // namespace

TEST_CASE("the schedule is v1's") {
  const SoloistTimings timings;
  CHECK(timings.portTick == 100ms);
  CHECK(timings.tick == 1s);
  CHECK(timings.portWait == 10s);
  CHECK(timings.answerWait == 30s);
  CHECK(timings.reconnect == 1s);
  CHECK(timings.firstBackoff == 1s);
  CHECK(timings.lastBackoff == 60s);
  CHECK(timings.steady == 60s);
  CHECK(timings.switchCheck == 60s);
  CHECK(timings.probeLimit == 10s);
  CHECK(SoloistOptions{}.resolvConf == "/etc/resolv.conf");
}

TEST_CASE("soloist waits for a config that enables it and an open gate") {
  SoloistRig rig;
  CHECK(rig.reports().waitState(State::Off));
  rig.supervisor().gate(true);
  CHECK_FALSE(rig.fake().started(300ms));
  rig.supervisor().gate(false);

  rig.supervisor().configure(rig.settings());
  CHECK(rig.reports().waitState(State::Starting));
  // the gate is shut: no media child before a volume held (rule 7)
  CHECK_FALSE(rig.fake().started(400ms));
  CHECK(rig.logs().count(msg("soloist_start")) == 0);

  rig.supervisor().gate(true);
  CHECK(rig.reports().waitState(State::Ready));
  CHECK(rig.fake().started());
  // a gate that shuts again leaves it running
  rig.supervisor().gate(false);
  std::this_thread::sleep_for(200ms);
  CHECK(listening(portOf(rig.fake())));
  CHECK(rig.reports().latest().state == State::Ready);
}

TEST_CASE("a config that disables it stops it, and an identical one changes nothing") {
  SoloistRig rig;
  rig.run();
  REQUIRE(rig.reports().waitState(State::Ready));
  const auto port = portOf(rig.fake());

  rig.supervisor().configure(rig.settings());
  auto louder = rig.settings();
  louder.initialVolume = 80;
  rig.supervisor().configure(louder);
  std::this_thread::sleep_for(300ms);
  CHECK(rig.logs().count(msg("soloist_start")) == 1);
  CHECK(rig.logs().count(msg("soloist_configured")) == 2);

  auto off = rig.settings();
  off.enabled = false;
  rig.supervisor().configure(off);
  CHECK(rig.reports().waitState(State::Off));
  CHECK(eventually([&] { return !listening(port); }));
  CHECK(rig.logs().count(msg("soloist_start")) == 1);
}

TEST_CASE("a change of the name or the key restarts it") {
  SoloistRig rig;
  rig.run();
  REQUIRE(rig.logs().eventually(msg("soloist_answered")));
  rig.supervisor().configure(rig.settings("Kitchen", "another-test-key"));
  CHECK(rig.logs().eventually(msg("soloist_answered"), 2));
  rig.supervisor().configure(rig.settings("Hall", "another-test-key"));
  CHECK(rig.logs().eventually(msg("soloist_answered"), 3));
  CHECK(rig.logs().count(msg("soloist_start")) == 3);
}

TEST_CASE("a key that is missing or invalid never starts it") {
  struct Case {
    std::string key;
    std::string code;
  };
  const auto [key, code] = GENERATE(Case{"", "missing"}, Case{"has space", "invalid"},
                                    Case{std::string(257, 'k'), "invalid"}, Case{"tab\tkey", "invalid"},
                                    Case{"caf\xc3\xa9", "invalid"});
  SoloistRig rig;
  rig.run("Kitchen", key);
  const auto status = rig.reports().waitState(State::KeyMissing);
  REQUIRE(status);
  CHECK(status->code == code);
  CHECK(rig.logs().count("\"reason\":\"" + code + "\"") == 1);
  CHECK_FALSE(rig.fake().started(300ms));

  rig.supervisor().configure(rig.settings("Kitchen", std::string(256, 'k')));
  CHECK(rig.reports().waitState(State::Ready));
}

TEST_CASE("a missing build waits for a switch") {
  SoloistRig rig;
  rig.removeBuild();
  rig.run();
  CHECK(rig.reports().waitState(State::NoBuild));
  CHECK(rig.logs().count(msg("soloist_missing")) == 1);
  rig.install();
  std::this_thread::sleep_for(300ms);
  CHECK(rig.reports().latest().state == State::NoBuild);
  rig.supervisor().switchBuild();
  CHECK(rig.reports().waitState(State::Ready));
}

TEST_CASE("a missing runtime or ca bundle fails the start") {
  SoloistRig rig;
  SECTION("runtime") {
    std::filesystem::remove(rig.jail().inRoot("/proc"));
    rig.run();
    // a part only the jail mounts: an unjailed build never looks for it
    if constexpr (!kSoloistJailed) {
      CHECK(rig.reports().waitState(State::Ready));
      return;
    }
    const auto status = rig.reports().waitState(State::Failed);
    REQUIRE(status);
    CHECK(status->code == "runtime_missing");
    CHECK(rig.logs().count("\"mount\":\"proc\"") == 1);
    CHECK_FALSE(rig.fake().started(100ms));
    std::filesystem::create_directory(rig.jail().inRoot("/proc"));
    rig.supervisor().switchBuild();
    CHECK(rig.reports().waitState(State::Ready));
  }
  SECTION("ca") {
    auto settings = rig.settings();
    settings.caFile = rig.dir() + "/no-such-ca.crt";
    rig.supervisor().configure(settings);
    rig.supervisor().gate(true);
    const auto status = rig.reports().waitState(State::Failed);
    REQUIRE(status);
    CHECK(status->code == "ca_missing");
    CHECK_FALSE(rig.fake().started(100ms));
  }
}

TEST_CASE("its command line and environment are v1's") {
  SoloistRig rig;
  rig.run();
  REQUIRE(rig.fake().started());
  // a jail-off build gives the scratch's own home, as nothing is bound at /home/soloist
  const std::string home = kSoloistJailed ? "/home/soloist" : rig.home();
  // the jail's environment, or a plain start's: the scratch's home and pcm socket and the host's ca bundle
  const std::vector<std::string> env =
      kSoloistJailed ? std::vector<std::string>{"HOME=/home/soloist",
                                                "SPOTIFY_SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt", "PATH=/"}
                     : std::vector<std::string>{"HOME=" + home, "SPOTIFY_SSL_CERT_FILE=" + rig.settings().caFile,
                                                "PULSE_SERVER=unix:" + rig.dir() + "/scratch/run/pcm.sock",
                                                "PATH=/nonexistent", "TMPDIR=" + home};
  std::vector<std::string> expected{"--device-name",
                                          "Kitchen",
                                          "--api-key",
                                          std::string(kFakeKey),
                                          "--data-dir",
                                          home,
                                          "--cache-dir",
                                          home + "/cache",
                                          "--cache-size",
                                          "100",
                                          "--initial-volume",
                                          "35",
                                          "--ws",
                                          "127.0.0.1:0",
                                          "--env--"};
  expected.insert(expected.end(), env.begin(), env.end());
  CHECK(rig.fake().argv() == expected);
}

TEST_CASE("a name left empty is Cadence, and a level beyond 100 starts at 50") {
  SoloistRig rig;
  auto settings = rig.settings("");
  settings.initialVolume = 101;
  rig.supervisor().configure(settings);
  rig.supervisor().gate(true);
  REQUIRE(rig.fake().started());
  const auto argv = rig.fake().argv();
  REQUIRE(argv.size() > 11);
  CHECK(argv[1] == "Cadence");
  CHECK(argv[11] == "50");
}

TEST_CASE("the running build's version reaches the status") {
  SoloistRig rig;
  rig.run();
  const auto status = rig.reports().waitState(State::Ready);
  REQUIRE(status);
  CHECK(status->version == "1.3.10.18");
  CHECK(status->buildEpoch == 1'793'059'327);
  CHECK(rig.logs().count(msg("soloist_version")) == 1);
}

TEST_CASE("a start that writes no port in time is silent: killed, counted and started again") {
  SoloistRig rig;
  rig.run("silent");
  const auto status = rig.reports().wait([](const SoloistStatus& s) { return s.silentStarts >= 2; });
  REQUIRE(status);
  CHECK(status->state == State::Starting);
  const auto starts = rig.logs().times(msg("soloist_start"));
  const auto silents = rig.logs().times(msg("soloist_silent"));
  REQUIRE(starts.size() >= 2);
  REQUIRE(silents.size() >= 2);
  CHECK(between(starts[0], silents[0]) >= 400ms);
  CHECK(between(starts[0], silents[0]) < 1000ms);
  CHECK(between(silents[0], starts[1]) >= 100ms);
}

TEST_CASE("a start that never answers its websocket is silent") {
  SoloistRig rig;
  rig.run("mute");
  const auto status = rig.reports().wait([](const SoloistStatus& s) { return s.silentStarts >= 1; });
  REQUIRE(status);
  const auto starts = rig.logs().times(msg("soloist_start"));
  const auto silents = rig.logs().times(msg("soloist_silent"));
  REQUIRE_FALSE(silents.empty());
  CHECK(between(starts[0], silents[0]) >= 1200ms);
  CHECK(rig.logs().count(msg("soloist_answered")) == 0);
}

TEST_CASE("an answer resets the silent starts") {
  SoloistRig rig;
  rig.run("silent");
  REQUIRE(rig.reports().wait([](const SoloistStatus& s) { return s.silentStarts >= 2; }));
  rig.supervisor().configure(rig.settings("Kitchen"));
  const auto status = rig.reports().waitState(State::Ready);
  REQUIRE(status);
  CHECK(status->silentStarts == 0);
}

TEST_CASE("an exit starts it again after waits doubling up to the last") {
  SoloistRig rig;
  rig.run("exit-3");
  REQUIRE(rig.logs().eventually(msg("soloist_start"), 6));
  const auto starts = rig.logs().times(msg("soloist_start"));
  const std::vector<std::chrono::milliseconds> waits{100ms, 200ms, 400ms, 400ms, 400ms};
  for (std::size_t i = 0; i < waits.size(); ++i) {
    INFO("restart " << i + 1);
    CHECK(between(starts[i], starts[i + 1]) >= waits[i]);
    CHECK(between(starts[i], starts[i + 1]) < waits[i] + 300ms);
  }
  const auto status = rig.reports().latest();
  CHECK(status.exitCode == 3);
  CHECK(status.state == State::Starting);
  CHECK(rig.logs().count("\"msg\":\"soloist_exit\",\"code\":3") >= 5);
}

TEST_CASE("a steady run starts the waits over") {
  SoloistRig rig;
  rig.run();
  const auto restart = [&](std::size_t run, std::chrono::milliseconds ranFor) {
    REQUIRE(rig.logs().eventually(msg("soloist_answered"), run));
    std::this_thread::sleep_for(ranFor);
    rig.fake().send("!exit 3");
    REQUIRE(rig.logs().eventually(msg("soloist_start"), run + 1));
    const auto exits = rig.logs().times(msg("soloist_exit"));
    const auto starts = rig.logs().times(msg("soloist_start"));
    return between(exits.at(run - 1), starts.at(run));
  };
  CHECK(restart(1, 0ms) < 200ms);
  CHECK(restart(2, 0ms) >= 200ms);
  CHECK(restart(3, 0ms) >= 400ms);
  // a run of at least steady: from the first wait again
  CHECK(restart(4, 1600ms) < 200ms);
}

TEST_CASE("an expired build waits for a switch") {
  SoloistRig rig;
  rig.run("exit-10");
  const auto status = rig.reports().waitState(State::Expired);
  REQUIRE(status);
  CHECK(status->exitCode == 10);
  CHECK(rig.logs().eventually(msg("soloist_expired")));
  std::this_thread::sleep_for(500ms);
  CHECK(rig.logs().count(msg("soloist_start")) == 1);
  rig.supervisor().switchBuild();
  CHECK(rig.logs().eventually(msg("soloist_start"), 2));
  CHECK(rig.logs().eventually(msg("soloist_expired"), 2));
}

TEST_CASE("a switch starts the installed build at once when nothing plays") {
  SoloistRig rig;
  rig.run();
  REQUIRE(rig.logs().eventually(msg("soloist_answered")));
  rig.supervisor().switchBuild();
  CHECK(rig.logs().eventually(msg("soloist_switch")));
  CHECK(rig.logs().eventually(msg("soloist_answered"), 2));
  CHECK(rig.logs().count(msg("soloist_start")) == 2);
  CHECK(rig.reports().latest().state == State::Ready);
}

TEST_CASE("a switch waits while music plays") {
  SoloistRig rig;
  rig.run();
  REQUIRE(rig.logs().eventually(msg("soloist_answered")));
  // music plays only in a session that is logged in
  rig.fake().send(R"({"type":"auth_state","logged_in":true,"is_active":true})");
  rig.fake().send(R"({"type":"playback_state","status":"playing"})");
  std::this_thread::sleep_for(300ms);
  rig.supervisor().switchBuild();
  CHECK(rig.logs().eventually(msg("soloist_switch_held")));
  // looked at again every switch check, and held while it plays
  std::this_thread::sleep_for(800ms);
  CHECK(rig.logs().count(msg("soloist_switch")) == 0);
  CHECK(rig.logs().count(msg("soloist_start")) == 1);

  rig.fake().send(R"({"type":"playback_changed","status":"paused"})");
  CHECK(rig.logs().eventually(msg("soloist_switch")));
  CHECK(rig.logs().eventually(msg("soloist_answered"), 2));
}

TEST_CASE("a websocket that closes while soloist runs is connected again") {
  SoloistRig rig;
  rig.run();
  REQUIRE(rig.logs().eventually(msg("soloist_answered")));
  rig.fake().send("!hangup");
  CHECK(rig.logs().eventually(msg("soloist_ws_closed")));
  CHECK(rig.logs().eventually(msg("soloist_session_lost")));
  CHECK(rig.logs().eventually(msg("soloist_ws_connected"), 2));
  CHECK(rig.logs().count(msg("soloist_start")) == 1);
  CHECK(rig.logs().count(msg("soloist_answered")) == 1);
  CHECK(rig.reports().latest().state == State::Ready);
}

TEST_CASE("a login failed at the start of a line fails the run, once") {
  SoloistRig rig;
  rig.run("Kitchen", "bad");
  const auto status = rig.reports().waitState(State::Failed);
  REQUIRE(status);
  CHECK(status->code == "auth_failed");
  // the websocket answered all the same: the failure stays for the run
  REQUIRE(rig.logs().eventually(msg("soloist_answered")));
  rig.fake().send("!print 2026-10-19 08:18:41.000: login failed: make sure --api-key is valid");
  std::this_thread::sleep_for(300ms);
  CHECK(rig.logs().count(msg("soloist_auth_failed")) == 1);
  CHECK(rig.reports().latest().state == State::Failed);
}

// M6b-D7: what soloist prints goes out as its lines, stdout's INFO and stderr's WARN, login failed among them as it
// still fails the run; audiod's own log keeps none of it
TEST_CASE("soloist's lines go to its logs, and its own log keeps none of them") {
  ChildLines lines;
  SoloistRig rig(quickTimings(), CADENCE_FAKE_SOLOIST, lines.target());
  rig.run("Kitchen", "bad");
  REQUIRE(rig.reports().waitState(State::Failed));
  REQUIRE(rig.logs().eventually(msg("soloist_answered")));
  rig.fake().send("!print 2026-10-27 04:38:59.185: playing a track");
  rig.fake().send("!eprint crashpad: no database");
  const auto login = lines.find("login failed");
  const auto playing = lines.find("playing a track");
  const auto crashpad = lines.find("crashpad: no database");
  REQUIRE(login);
  REQUIRE(playing);
  REQUIRE(crashpad);
  for (const auto* line : {&*login, &*playing, &*crashpad})
    CHECK(line->source == "soloist");
  CHECK(playing->text == "2026-10-27 04:38:59.185: playing a track");
  CHECK(playing->level == cadence_audiod_v1_LogLevel_LOG_LEVEL_INFO);
  CHECK(crashpad->level == cadence_audiod_v1_LogLevel_LOG_LEVEL_WARN);
  CHECK(rig.logs().count(msg("soloist_auth_failed")) == 1);
  CHECK(rig.logs().all().find("playing") == std::string::npos);
  CHECK(rig.logs().all().find("crashpad") == std::string::npos);
}

// what soloist prints as it exits is still read when its exit is seen first: its last lines go out, and a login failed
// among them fails the run, which stays failed until the next start
TEST_CASE("soloist's last lines and a login failed survive its exit") {
  ChildLines lines;
  SoloistRig rig(quickTimings(), CADENCE_FAKE_SOLOIST, lines.target());
  rig.run("mute");
  REQUIRE(rig.logs().eventually(msg("soloist_start")));
  // the port read, the supervisor waits on a handshake that never comes: what soloist prints and its exit meanwhile are
  // both there when it looks again, and the exit is taken first
  std::this_thread::sleep_for(300ms);
  rig.fake().send("!print 2026-11-11 10:00:00.000: login failed: late\n"
                  "!print 2026-11-11 10:00:00.001: last words\n"
                  "!exit 3");
  REQUIRE(rig.logs().eventually(msg("soloist_exit")));
  const auto last = lines.find("last words");
  REQUIRE(last);
  CHECK(last->source == "soloist");
  CHECK(rig.logs().count(msg("soloist_auth_failed")) == 1);
  const auto failed = rig.reports().wait([](const SoloistStatus& status) {
    return status.state == State::Failed && status.code == "auth_failed" && status.exitCode == 3;
  });
  CHECK(failed);
}

TEST_CASE("a login failed later in a line is no failure") {
  SoloistRig rig;
  rig.run();
  REQUIRE(rig.reports().waitState(State::Ready));
  rig.fake().send(R"(!print 2026-10-27 04:38:59.185: playing "login failed" by login failed)");
  rig.fake().send("!print x login failed");
  std::this_thread::sleep_for(300ms);
  CHECK(rig.logs().count(msg("soloist_auth_failed")) == 0);
  CHECK(rig.reports().latest().state == State::Ready);
  // nothing soloist prints is logged
  CHECK(rig.logs().all().find("playing") == std::string::npos);
  CHECK(rig.logs().all().find("client expires") == std::string::npos);
}

TEST_CASE("a probe answers ok, version or io") {
  SoloistRig rig;
  SECTION("before any config") {
    rig.supervisor().probe("p0", rig.build());
    const auto probe = rig.reports().probe("p0");
    REQUIRE(probe);
    CHECK(probe->code == "io");
  }
  SECTION("with one") {
    auto off = rig.settings();
    off.enabled = false;
    rig.supervisor().configure(off);

    rig.supervisor().probe("p1", rig.build());
    const auto ok = rig.reports().probe("p1");
    REQUIRE(ok);
    CHECK(ok->code.empty());
    REQUIRE(ok->build);
    CHECK(*ok->build == SoloistBuild{.version = "1.3.10.18", .epoch = 1'793'059'327, .date = "20261027"});

    writeScript(rig.dir() + "/garbage", "echo Segmentation fault");
    rig.supervisor().probe("p2", rig.dir() + "/garbage");
    const auto garbage = rig.reports().probe("p2");
    REQUIRE(garbage);
    CHECK(garbage->code == "version");
    CHECK_FALSE(garbage->build);

    rig.supervisor().probe("p3", rig.dir() + "/no-such-build");
    const auto missing = rig.reports().probe("p3");
    REQUIRE(missing);
    CHECK(missing->code == "io");

    // the jail's PATH is /: nothing is found by name
    writeScript(rig.dir() + "/stuck", "exec /bin/sleep 30");
    const auto asked = Clock::now();
    rig.supervisor().probe("p4", rig.dir() + "/stuck");
    const auto stuck = rig.reports().probe("p4");
    REQUIRE(stuck);
    CHECK(stuck->code == "io");
    CHECK(between(asked, Clock::now()) >= 2000ms);
    CHECK(rig.logs().count("\"msg\":\"soloist_probe\",\"code\":\"ok\"") == 1);
    CHECK_FALSE(rig.fake().started(100ms));
  }
}

TEST_CASE("a ws.port soloist made a directory is cleared with its home") {
  SoloistRig rig;
  rig.run();
  REQUIRE(rig.logs().eventually(msg("soloist_answered")));
  const auto home = rig.home();
  std::filesystem::remove(home + "/ws.port");
  std::filesystem::create_directories(home + "/ws.port/inner");
  std::ofstream(home + "/ws.port/inner/file") << "x";
  rig.fake().send("!exit 3");
  CHECK(rig.logs().eventually(msg("soloist_home_reset")));
  CHECK(rig.logs().eventually(msg("soloist_answered"), 2));
  CHECK(std::filesystem::is_directory(home + "/crashpad"));
}

TEST_CASE("the key reaches no log and no status") {
  const std::string key = "secret-test-key-0123456789";
  SoloistRig rig;
  rig.run("Kitchen", key);
  REQUIRE(rig.reports().waitState(State::Ready));
  rig.fake().send("!exit 3");
  REQUIRE(rig.logs().eventually(msg("soloist_answered"), 2));
  rig.supervisor().configure(rig.settings("Kitchen", key + " invalid"));
  REQUIRE(rig.reports().waitState(State::KeyMissing));
  CHECK(rig.logs().all().find("secret-test-key") == std::string::npos);
  for (const auto& status : rig.reports().statuses()) {
    CHECK(status.code.find("secret") == std::string::npos);
    CHECK(status.version.find("secret") == std::string::npos);
  }
}

TEST_CASE("a control without a connection or a login is control_unsupported, and goes out once logged in") {
  struct Answers {
    std::mutex mutex;
    std::vector<std::string> codes;
    ControlAnswer answer() {
      return [this](std::string_view code) {
        std::scoped_lock lock(mutex);
        codes.emplace_back(code);
      };
    }
    std::optional<std::string> wait(std::size_t count) {
      std::optional<std::string> found;
      eventually([&] {
        std::scoped_lock lock(mutex);
        if (codes.size() >= count)
          found = codes[count - 1];
        return found.has_value();
      });
      return found;
    }
  } answers;
  SoloistRig rig;
  rig.supervisor().control(soloist::Command::Pause, answers.answer());
  CHECK(answers.wait(1) == "control_unsupported");

  rig.run();
  REQUIRE(rig.logs().eventually(msg("soloist_answered")));
  rig.supervisor().control(soloist::Command::Pause, answers.answer());
  CHECK(answers.wait(2) == "control_unsupported");
  rig.fake().send(R"({"type":"auth_state","logged_in":true,"is_active":true})");
  std::this_thread::sleep_for(200ms);
  rig.supervisor().control(soloist::Command::SkipNext, answers.answer());
  CHECK(answers.wait(3) == "");
  CHECK(rig.fake().received() == std::vector<std::string>{R"({"type":"command","command":"skip_next"})"});
  CHECK(rig.logs().count(R"("msg":"soloist_command","command":"pause","code":"control_unsupported")") == 2);
}

TEST_CASE("a restart that comes due while the gate is shut waits without a busy loop, and starts as the gate opens") {
  SoloistRig rig;
  rig.run();
  REQUIRE(rig.logs().eventually(msg("soloist_answered")));
  rig.supervisor().gate(false);
  rig.fake().send("!exit 3");
  REQUIRE(rig.logs().eventually(msg("soloist_exit")));
  // past the first restart wait
  std::this_thread::sleep_for(300ms);
  const auto used = processCpu();
  std::this_thread::sleep_for(500ms);
  CHECK(processCpu() - used < 50ms);
  CHECK(rig.logs().count(msg("soloist_start")) == 1);

  const auto opened = Clock::now();
  rig.supervisor().gate(true);
  REQUIRE(rig.logs().eventually(msg("soloist_start"), 2));
  CHECK(between(opened, rig.logs().times(msg("soloist_start")).back()) < 2s);
}

TEST_CASE("a probe that takes long holds no echo back: ours coming back one off is still no request") {
  auto timings = quickTimings();
  timings.probeLimit = 15s;
  SoloistRig rig(timings);
  rig.run();
  REQUIRE(rig.logs().eventually(msg("soloist_answered")));
  rig.supervisor().speakerLevel(30);
  rig.fake().send(kUp);
  REQUIRE(rig.fake().waitReceived(setVolume(30)));
  // the app's stored level: dropped and answered
  rig.fake().send(R"({"type":"volume_changed","volume":80})");
  REQUIRE(eventually([&] { return countOf(rig.fake().received(), setVolume(30)) >= 2; }));
  rig.fake().send("!echo 1");
  std::this_thread::sleep_for(200ms);

  writeScript(rig.dir() + "/slow", "/bin/sleep 12\necho soloist 1.3.10.18 build 1793059327 '(20261027)'");
  rig.supervisor().speakerLevel(40);
  rig.supervisor().probe("slow", rig.dir() + "/slow");
  CHECK(rig.fake().waitReceived(setVolume(40)));
  const auto probe = rig.reports().probe("slow", 20s);
  REQUIRE(probe);
  CHECK(probe->code.empty());
  // 41 came back while the probe ran: judged then, within the echo window, and not once the probe is over
  std::this_thread::sleep_for(500ms);
  CHECK(rig.reports().senderVolumes().empty());
  CHECK(rig.logs().count(msg("soloist_sender_volume")) == 0);
}

TEST_CASE("a send that fails outside a read drops the connection, which is connected again") {
  SoloistRig rig;
  // it stops reading after the handshake
  rig.run("deaf");
  REQUIRE(rig.logs().eventually(msg("soloist_answered")));
  rig.fake().send(kUp);
  std::this_thread::sleep_for(300ms);
  // set_volume after set_volume fills its socket until one is not out within the send timeout
  for (int i = 0; i < 400'000 && rig.logs().count(msg("soloist_ws_closed")) == 0; i += 1000) {
    for (int j = 0; j < 1000; ++j)
      rig.supervisor().speakerLevel(static_cast<std::uint8_t>((i + j) % 100));
    std::this_thread::sleep_for(10ms);
  }
  REQUIRE(rig.logs().eventually(msg("soloist_ws_closed"), 1, 20s));
  // a new server that reads, on a new port
  rig.fake().send("!hangup");
  CHECK(rig.logs().eventually(msg("soloist_session_lost")));
  CHECK(rig.logs().eventually(msg("soloist_ws_connected"), 2, 20s));
  CHECK(rig.logs().count(msg("soloist_start")) == 1);
}

TEST_CASE("an expired build stays expired through a config that changes it") {
  SoloistRig rig;
  rig.run("exit-10");
  REQUIRE(rig.reports().waitState(State::Expired));
  rig.supervisor().configure(rig.settings("Hall"));
  REQUIRE(rig.logs().eventually(msg("soloist_configured"), 2));
  std::this_thread::sleep_for(300ms);
  CHECK(rig.reports().latest().state == State::Expired);
  CHECK(rig.logs().count(msg("soloist_start")) == 1);
}

TEST_CASE("a switch starts the silent starts over") {
  SoloistRig rig;
  rig.run("silent");
  REQUIRE(rig.reports().wait([](const SoloistStatus& s) { return s.silentStarts >= 2; }));
  rig.supervisor().switchBuild();
  CHECK(rig.reports().wait([](const SoloistStatus& s) { return s.silentStarts == 0; }, 3s));
}

TEST_CASE("what comes during a start's probe goes first: a shut gate stops the start, a level is its initial one") {
  SoloistRig rig;
  // the build answers its --version slowly, then is the fake
  writeScript(rig.build(), "if [ \"$1\" = --version ]; then /bin/sleep 2; fi\nexec " +
                               std::string(CADENCE_FAKE_SOLOIST) + " \"$@\"");
  SECTION("the gate shuts") {
    rig.run();
    std::this_thread::sleep_for(300ms);
    rig.supervisor().gate(false);
    REQUIRE(rig.logs().eventually(msg("soloist_version")));
    std::this_thread::sleep_for(500ms);
    CHECK(rig.logs().count(msg("soloist_start")) == 0);
    CHECK_FALSE(rig.fake().started(100ms));
    // the probe is cached: the start follows the gate at once
    rig.supervisor().gate(true);
    CHECK(rig.logs().eventually(msg("soloist_start"), 1, 1s));
    CHECK(rig.logs().count(msg("soloist_version")) == 1);
  }
  SECTION("the speaker's level comes") {
    rig.run();
    std::this_thread::sleep_for(300ms);
    rig.supervisor().speakerLevel(70);
    REQUIRE(rig.fake().started());
    const auto argv = rig.fake().argv();
    REQUIRE(argv.size() > 11);
    CHECK(argv[10] == "--initial-volume");
    CHECK(argv[11] == "70");
  }
}
