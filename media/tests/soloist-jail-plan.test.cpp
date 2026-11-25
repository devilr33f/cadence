#include "audiod/jailed-process.h"
#include "audiod/soloist-jail.h"
#include "support/jail-rig.h"

#include <catch2/catch_test_macros.hpp>

#include <sched.h>
#include <sys/mount.h>
#include <unistd.h>

#include <cerrno>
#include <filesystem>
#include <optional>
#include <set>
#include <string>
#include <string_view>
#include <vector>

using namespace cadence::audiod;
using namespace cadence::test_support;

namespace {

const JailPaths kPaths{.runtimeRoot = "/data/cadence/soloist/runtime",
                       .build = "/data/cadence/soloist/soloist",
                       .resolvConf = "/etc/resolv.conf",
                       .caFile = "/data/cadence/ca-certificates.crt",
                       .home = "/tmp/cadence/soloist/home",
                       .run = "/tmp/cadence/soloist/run"};

constexpr unsigned long kReadOnly = MS_REMOUNT | MS_BIND | MS_RDONLY;

JailMount bindMount(std::string_view name, std::string source, std::string target) {
  return {.name = name, .source = std::move(source), .target = std::move(target), .fstype = {}, .flags = MS_BIND};
}

JailMount remountReadOnly(std::string_view name, std::string target) {
  return {.name = name, .source = {}, .target = std::move(target), .fstype = {}, .flags = kReadOnly};
}

std::optional<JailRefusal> refusal(std::string_view reason, std::string_view mount) {
  return JailRefusal{.reason = reason, .mount = mount};
}

void replaceWithDirectory(const std::string& path) {
  std::filesystem::remove(path);
  std::filesystem::create_directory(path);
}

void replaceWithFile(const std::string& path) {
  std::filesystem::remove(path);
  writeFile(path, "", JailRig::kFileMode);
}

}  // namespace

TEST_CASE("the plan is v1's mount sequence: private propagation, then each bind with the remount a read-only one needs") {
  const auto plan = planJail(kPaths);
  const std::string root = "/data/cadence/soloist/runtime";
  const std::vector<JailMount> expected{
      {.name = "propagation", .source = {}, .target = "/", .fstype = {}, .flags = MS_REC | MS_PRIVATE},
      bindMount("root", root, root),
      remountReadOnly("root", root),
      bindMount("soloist", "/data/cadence/soloist/soloist", root + "/soloist"),
      remountReadOnly("soloist", root + "/soloist"),
      bindMount("dev_null", "/dev/null", root + "/dev/null"),
      bindMount("dev_urandom", "/dev/urandom", root + "/dev/urandom"),
      bindMount("dev_zero", "/dev/zero", root + "/dev/zero"),
      {.name = "proc", .source = "proc", .target = root + "/proc", .fstype = "proc",
       .flags = MS_NOSUID | MS_NODEV | MS_NOEXEC},
      bindMount("resolv_conf", "/etc/resolv.conf", root + "/etc/resolv.conf"),
      remountReadOnly("resolv_conf", root + "/etc/resolv.conf"),
      bindMount("ca", "/data/cadence/ca-certificates.crt", root + "/etc/ssl/certs/ca-certificates.crt"),
      remountReadOnly("ca", root + "/etc/ssl/certs/ca-certificates.crt"),
      bindMount("home", "/tmp/cadence/soloist/home", root + "/home/soloist"),
      bindMount("run", "/tmp/cadence/soloist/run", root + "/run/cadence"),
  };
  CHECK(plan.mounts == expected);
  CHECK(plan.unshareFlags == CLONE_NEWNS);
  CHECK(plan.root == root);
}

TEST_CASE("every target of a bind or of proc lies inside the runtime root") {
  const auto plan = planJail(kPaths);
  for (const auto& mount : plan.mounts) {
    if (mount.name == "propagation")
      continue;
    INFO(mount.name);
    CHECK((mount.target == plan.root || mount.target.starts_with(plan.root + "/")));
  }
}

TEST_CASE("the plan runs /soloist as nobody with only home, the ca bundle and path in its environment") {
  const auto plan = planJail(kPaths);
  CHECK(plan.program == "/soloist");
  CHECK(plan.build == "/data/cadence/soloist/soloist");
  CHECK(plan.env == std::vector<std::string>{"HOME=/home/soloist",
                                             "SPOTIFY_SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt", "PATH=/"});
  CHECK(plan.uid == 65534);
  CHECK(plan.gid == 65534);
}

TEST_CASE("the plan lowers soloist as v1's helper was: nice 5, oom score 500") {
  const auto plan = planJail(kPaths);
  CHECK(plan.nice == 5);
  CHECK(plan.oomScoreAdj == "500");
}

TEST_CASE("a child whose parent is gone is told apart from one whose parent lives") {
  CHECK(stillChildOf(::getppid()));
  CHECK_FALSE(stillChildOf(::getppid() + 1));
}

TEST_CASE("a runtime with every placeholder of the right kind holds the jail") {
  JailRig rig;
  CHECK_FALSE(checkJail(rig.plan()).has_value());
}

TEST_CASE("a missing root, placeholder or source is refused before the fork, naming its mount") {
  JailRig rig;

  SECTION("a placeholder") {
    std::filesystem::remove(rig.inRoot("/dev/zero"));
    CHECK(checkJail(rig.plan()) == refusal("missing", "dev_zero"));
  }
  SECTION("the first one missing is named") {
    std::filesystem::remove(rig.inRoot("/home/soloist"));
    std::filesystem::remove(rig.inRoot("/proc"));
    CHECK(checkJail(rig.plan()) == refusal("missing", "proc"));
  }
  SECTION("the build") {
    CHECK(checkJail(rig.plan(rig.file("no-such-build"))) == refusal("missing", "soloist"));
  }
  SECTION("the ca bundle") {
    std::filesystem::remove(rig.file("ca.crt"));
    CHECK(checkJail(rig.plan()) == refusal("missing", "ca"));
  }
  SECTION("a relative or empty source") {
    auto paths = rig.paths();
    paths.caFile = "ca.crt";
    CHECK(checkJail(planJail(paths)) == refusal("missing", "ca"));
    paths.caFile.clear();
    CHECK(checkJail(planJail(paths)) == refusal("missing", "ca"));
  }
  SECTION("the runtime root") {
    std::filesystem::remove_all(rig.root());
    CHECK(checkJail(rig.plan()) == refusal("missing", "root"));
  }
}

TEST_CASE("a target of the wrong kind is refused: a file bind needs a file, a directory bind and proc a directory") {
  JailRig rig;

  SECTION("a directory where a file is bound") {
    for (const auto& [placeholder, mount] : {std::pair{"/soloist", "soloist"}, std::pair{"/dev/urandom", "dev_urandom"},
                                             std::pair{"/etc/ssl/certs/ca-certificates.crt", "ca"}}) {
      INFO(placeholder);
      replaceWithDirectory(rig.inRoot(placeholder));
      CHECK(checkJail(rig.plan()) == refusal("kind", mount));
      replaceWithFile(rig.inRoot(placeholder));
    }
    CHECK_FALSE(checkJail(rig.plan()).has_value());
  }
  SECTION("a file where a directory or proc is mounted") {
    for (const auto& [placeholder, mount] : {std::pair{"/proc", "proc"}, std::pair{"/home/soloist", "home"},
                                             std::pair{"/run/cadence", "run"}}) {
      INFO(placeholder);
      std::filesystem::remove(rig.inRoot(placeholder));
      writeFile(rig.inRoot(placeholder), "", JailRig::kFileMode);
      CHECK(checkJail(rig.plan()) == refusal("kind", mount));
      replaceWithDirectory(rig.inRoot(placeholder));
    }
    CHECK_FALSE(checkJail(rig.plan()).has_value());
  }
  SECTION("a directory as the source of a file placeholder") {
    std::filesystem::create_directory(rig.file("ca-dir"));
    auto paths = rig.paths();
    paths.caFile = rig.file("ca-dir");
    CHECK(checkJail(planJail(paths)) == refusal("kind", "ca"));
  }
  SECTION("a runtime root that is a file") {
    std::filesystem::remove_all(rig.root());
    writeFile(rig.root(), "", JailRig::kFileMode);
    CHECK(checkJail(rig.plan()) == refusal("kind", "root"));
  }
}

TEST_CASE("a symlink anywhere on a source or a target path is refused, never followed") {
  JailRig rig;

  SECTION("a placeholder") {
    std::filesystem::remove(rig.inRoot("/etc/resolv.conf"));
    std::filesystem::create_symlink(rig.file("resolv.conf"), rig.inRoot("/etc/resolv.conf"));
    CHECK(checkJail(rig.plan()) == refusal("symlink", "resolv_conf"));
  }
  SECTION("a directory on a target's path") {
    std::filesystem::rename(rig.inRoot("/etc/ssl"), rig.file("ssl"));
    std::filesystem::create_directory_symlink(rig.file("ssl"), rig.inRoot("/etc/ssl"));
    CHECK(checkJail(rig.plan()) == refusal("symlink", "ca"));
  }
  SECTION("the source itself") {
    std::filesystem::create_symlink(rig.file("ca.crt"), rig.file("ca-link"));
    auto paths = rig.paths();
    paths.caFile = rig.file("ca-link");
    CHECK(checkJail(planJail(paths)) == refusal("symlink", "ca"));
  }
  SECTION("a directory on a source's path") {
    std::filesystem::create_directory_symlink(rig.scratch().root, rig.file("scratch-link"));
    auto paths = rig.paths();
    paths.run = rig.file("scratch-link") + "/run";
    CHECK(checkJail(planJail(paths)) == refusal("symlink", "run"));
  }
  SECTION("the runtime root") {
    std::filesystem::rename(rig.root(), rig.file("real-runtime"));
    std::filesystem::create_directory_symlink(rig.file("real-runtime"), rig.root());
    CHECK(checkJail(rig.plan()) == refusal("symlink", "root"));
  }
}

TEST_CASE("starting a jailed program refuses a plan the host cannot hold, before any fork, and logs why") {
  if constexpr (!kSoloistJailed)
    SKIP("a jail-off build mounts nothing, so it checks no mount");
  JailRig rig;
  std::filesystem::remove(rig.inRoot("/proc"));

  const auto started = JailedProcess::start(rig.log(), rig.plan(), {}, nullptr);

  REQUIRE_FALSE(started.ok());
  CHECK(started.error().refused);
  CHECK(started.error().reason == "missing");
  CHECK(started.error().mount == "proc");
  CHECK(rig.logs().count(R"("msg":"jail_refused","reason":"missing","mount":"proc")") == 1);
}

TEST_CASE("without CAP_SYS_ADMIN the child's failing step comes back through the report pipe") {
  if (!kSoloistJailed || canJail())
    SKIP("only a jailed build without the privilege fails at unshare");
  JailRig rig;

  const auto started = JailedProcess::start(rig.log(), rig.plan(), {}, nullptr);

  REQUIRE_FALSE(started.ok());
  CHECK_FALSE(started.error().refused);
  CHECK(started.error().reason == "unshare");
  CHECK(started.error().code == EPERM);
  CHECK(rig.logs().count(R"("msg":"jail_spawn_failed","step":"unshare","errno":1)") == 1);
}
