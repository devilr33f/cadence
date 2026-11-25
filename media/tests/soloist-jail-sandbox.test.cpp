#include "audiod/jailed-process.h"
#include "audiod/soloist-jail.h"
#include "support/jail-rig.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <signal.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <chrono>
#include <future>
#include <map>
#include <set>
#include <string>
#include <string_view>
#include <vector>

using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

// a token that only exists in the host's fake config, outside every bind
constexpr std::string_view kToken = "fake-device-token-for-the-jail-test";
constexpr std::string_view kControlToken = "control-token-inside-the-jail";

// the probe's report: each fact's lines by their first word
class Report {
 public:
  explicit Report(const std::string& text) {
    for (const auto& line : splitLines(text)) {
      const auto space = line.find(' ');
      facts_[line.substr(0, space)].push_back(space == std::string::npos ? "" : line.substr(space + 1));
    }
  }

  [[nodiscard]] std::vector<std::string> all(const std::string& key) const {
    const auto found = facts_.find(key);
    return found == facts_.end() ? std::vector<std::string>{} : found->second;
  }
  [[nodiscard]] std::string one(const std::string& key) const {
    const auto values = all(key);
    return values.size() == 1 ? values.front() : "<" + std::to_string(values.size()) + " values>";
  }
  [[nodiscard]] std::set<std::string> set(const std::string& key) const {
    const auto values = all(key);
    return {values.begin(), values.end()};
  }

 private:
  std::map<std::string, std::vector<std::string>> facts_;
};

// what the probe reports from a long-lived start, the way soloist runs: stdout and stderr each into a pipe. the start
// meets what audiod has: an inherited descriptor without close-on-exec (the ipc socket), SIGTERM and SIGINT blocked
// and SIGPIPE ignored
Report run(const JailRig& rig, std::vector<std::string> args) {
  std::array<int, 2> inheritable{};
  REQUIRE(::pipe(inheritable.data()) == 0);
  const UniqueFd readEnd(inheritable[0]);
  const UniqueFd writeEnd(inheritable[1]);
  std::promise<JailedExit> exited;
  args.insert(args.begin(), "report");

  auto started = [&] {
    const AudiodSignals signals;
    return JailedProcess::start(rig.log(), rig.plan(CADENCE_JAIL_PROBE), std::move(args),
                                [&](JailedExit exit) { exited.set_value(exit); });
  }();

  REQUIRE(started.ok());
  const auto output = started.value()->takeOutput();
  const auto text = readToEnd(output.get());
  auto future = exited.get_future();
  REQUIRE(future.wait_for(10s) == std::future_status::ready);
  CHECK(future.get().code == 0);
  return Report(text);
}

// what holds in and out of the jail alike: the environment, the descriptors, the working directory, the flags, the
// signals and the priority
void checkProcessRules(const Report& report) {
  CHECK(report.all("env") == std::vector<std::string>{"HOME=/home/soloist",
                                                      "SPOTIFY_SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
                                                      "PATH=/"});
  CHECK(report.one("stdin") == "null");
  const auto stdoutLine = report.one("stdout");
  const auto stderrLine = report.one("stderr");
  CHECK(stdoutLine.starts_with("fifo "));
  CHECK(stderrLine.starts_with("fifo "));
  CHECK(stderrLine != stdoutLine);
  CHECK(report.one("stdio_nonblock") == "0");
  CHECK(report.all("fd").empty());
  CHECK(report.one("cwd") == "/");
  CHECK(report.one("NoNewPrivs") == "1");
  CHECK(report.one("pdeathsig") == std::to_string(SIGKILL));
  CHECK(report.one("SigBlk") == "0000000000000000");
  CHECK(report.one("SigIgn") == "0000000000000000");
  CHECK(report.one("nice") == "5");
}

}  // namespace

TEST_CASE("the jail runs soloist as nobody without privileges, with nothing of the host but its binds") {
  if constexpr (!kSoloistJailed)
    SKIP("a jail-off build has no jail");
  requireJail();
  JailRig rig;
  const auto config = rig.file("config.json");
  writeFile(config, R"({"deviceToken":")" + std::string(kToken) + "\"}\n", JailRig::kFileMode);
  const auto viaProc = "/proc/" + std::to_string(::getpid()) + "/root" + config;
  // the scan's own check: a token in the scratch, which the jail does show, is found
  writeFile(rig.scratch().run + "/control", kControlToken, JailRig::kFileMode);

  const auto report = run(rig, {"--open", config, "--open", viaProc, "--open", "/data", "--read", "/etc/resolv.conf",
                                "--read", "/etc/ssl/certs/ca-certificates.crt", "--walk", "--token",
                                std::string(kToken), "--token", std::string(kControlToken)});

  checkProcessRules(report);
  CHECK(report.one("oom_score_adj") == childOomScore(500));
  CHECK(report.one("uid") == "65534 65534 65534");
  CHECK(report.one("gid") == "65534 65534 65534");
  CHECK(report.one("groups") == "0");
  CHECK(report.one("CapEff") == "0000000000000000");

  // only the plan's mounts, the fresh proc among them, read-only where the plan remounts
  const std::set<std::string> points = [&] {
    std::set<std::string> found;
    for (const auto& mount : report.all("mount"))
      found.insert(mount.substr(0, mount.find(' ')));
    return found;
  }();
  CHECK(points == std::set<std::string>{"/", "/soloist", "/dev/null", "/dev/urandom", "/dev/zero", "/proc",
                                        "/etc/resolv.conf", "/etc/ssl/certs/ca-certificates.crt", "/home/soloist",
                                        "/run/cadence"});
  for (const auto& mount : report.all("mount")) {
    INFO(mount);
    const auto point = mount.substr(0, mount.find(' '));
    const bool readOnly = point == "/" || point == "/soloist" || point == "/etc/resolv.conf" ||
                          point == "/etc/ssl/certs/ca-certificates.crt";
    CHECK(mount.ends_with(readOnly ? " ro" : " rw"));
    if (point == "/proc")
      CHECK(mount == "/proc proc rw");
  }
  CHECK(report.all("read") ==
        std::vector<std::string>{"/etc/resolv.conf " + std::string(JailRig::kResolvLine),
                                 "/etc/ssl/certs/ca-certificates.crt " + std::string(JailRig::kCaLine)});

  // the host's config is out of reach, by path and through the jailer's /proc root; nothing visible holds its token
  CHECK(report.all("open") == std::vector<std::string>{config + " errno 2", viaProc + " errno 13", "/data errno 2"});
  CHECK(report.all("token") == std::vector<std::string>{std::string(kControlToken) + " /run/cadence/control"});
  CHECK(report.set("writable") == std::set<std::string>{"/home/soloist", "/run/cadence"});
}

TEST_CASE("a jail-off build runs the program with the jail's environment, descriptors and flags, as its own user") {
  if constexpr (kSoloistJailed)
    SKIP("a jailed build always jails");
  JailRig rig;

  const auto report = run(rig, {});

  checkProcessRules(report);
  const auto uid = std::to_string(::getuid());
  const auto gid = std::to_string(::getgid());
  CHECK(report.one("uid") == uid + " " + uid + " " + uid);
  CHECK(report.one("gid") == gid + " " + gid + " " + gid);
  // a jail-off build sets the oom score only as root
  if (::geteuid() == 0)
    CHECK(report.one("oom_score_adj") == childOomScore(500));
}
