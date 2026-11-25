#include "audiod/soloist-jail.h"
#include "audiod/soloist-launch.h"
#include "support/audiod-harness.h"
#include "support/jail-rig.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <filesystem>
#include <string>
#include <variant>
#include <vector>

using namespace cadence::audiod;
using namespace cadence::test_support;
namespace fs = std::filesystem;

namespace {

// a config over the jail rig's runtime (its parent dir), ca bundle and scratch
SoloistSettings settingsFor(const JailRig& rig, std::string caFile) {
  return {.enabled = true,
          .apiKey = "test-key-not-real",
          .deviceName = "Kitchen",
          .dir = fs::path(rig.root()).parent_path().string(),
          .scratchDir = rig.scratch().root,
          .caFile = std::move(caFile),
          .initialVolume = 50};
}

}  // namespace

TEST_CASE("a key is missing when empty, and invalid past 256 bytes or with a byte outside 0x21-0x7e") {
  CHECK(keyProblem("") == "missing");
  CHECK(keyProblem(std::string(257, 'k')) == "invalid");
  CHECK(keyProblem("with space") == "invalid");
  CHECK(keyProblem("del\x7f") == "invalid");
  CHECK(keyProblem("caf\xc3\xa9") == "invalid");
  CHECK_FALSE(keyProblem(std::string(256, 'k')));
  CHECK_FALSE(keyProblem("!~"));
}

TEST_CASE("a symlinked resolv.conf and ca bundle are resolved through realpath, not refused") {
  JailRig rig;
  const auto links = rig.file("links");
  fs::create_directory(links);
  fs::create_symlink(rig.file("resolv.conf"), links + "/resolv.conf");
  fs::create_symlink(rig.file("ca.crt"), links + "/ca.crt");
  // a symlinked directory on the way too, as a board's /etc may hold one
  fs::create_directory_symlink(links, rig.file("etc"));

  const auto settings = settingsFor(rig, rig.file("etc") + "/ca.crt");
  const auto paths =
      resolveJail(rig.log(), settings, rig.file("build"), rig.file("etc") + "/resolv.conf", rig.scratch());
  REQUIRE(paths.ok());
  CHECK(paths.value().resolvConf == fs::canonical(rig.file("resolv.conf")).string());
  CHECK(paths.value().caFile == fs::canonical(rig.file("ca.crt")).string());
  CHECK(paths.value().runtimeRoot == fs::canonical(rig.root()).string());
  CHECK_FALSE(checkJail(planJail(paths.value())));

  // as given, the plan would refuse them
  auto raw = rig.paths();
  raw.resolvConf = links + "/resolv.conf";
  const auto refused = checkJail(planJail(raw));
  REQUIRE(refused);
  CHECK(refused->reason == "symlink");
  CHECK(refused->mount == "resolv_conf");
}

TEST_CASE("a missing ca bundle or runtime part is the start's code, and logged with its mount") {
  JailRig rig;
  const auto missingCa = resolveJail(rig.log(), settingsFor(rig, rig.file("no-such-ca.crt")), rig.file("build"),
                                     rig.file("resolv.conf"), rig.scratch());
  REQUIRE_FALSE(missingCa.ok());
  CHECK(missingCa.error() == "ca_missing");
  CHECK(rig.logs().count(R"("msg":"soloist_unavailable","reason":"ca","mount":"ca")") == 1);

  fs::remove(rig.inRoot("/dev/zero"));
  const auto missingRuntime = resolveJail(rig.log(), settingsFor(rig, rig.file("ca.crt")), rig.file("build"),
                                          rig.file("resolv.conf"), rig.scratch());
  // a part only the jail mounts: an unjailed build never looks for it
  if constexpr (kSoloistJailed) {
    REQUIRE_FALSE(missingRuntime.ok());
    CHECK(missingRuntime.error() == "runtime_missing");
    CHECK(rig.logs().count(R"("msg":"soloist_unavailable","reason":"runtime","mount":"dev_zero")") == 1);
  } else {
    CHECK(missingRuntime.ok());
  }
}

TEST_CASE("soloist's command line names it Cadence when the config gives no name") {
  JailRig rig;
  auto settings = settingsFor(rig, rig.file("ca.crt"));
  settings.deviceName.clear();
  const std::vector<std::string> expected{"--device-name", "Cadence",        "--api-key",      "test-key-not-real",
                                          "--data-dir",    "/home/soloist",  "--cache-dir",    "/home/soloist/cache",
                                          "--cache-size",  "100",            "--initial-volume", "42",
                                          "--ws",          "127.0.0.1:0"};
  CHECK(soloistArgs(settings, "/home/soloist", 42) == expected);
}

bool hasEnv(const std::vector<std::string>& env, const std::string& variable) {
  return std::find(env.begin(), env.end(), variable) != env.end();
}

// the plain plan an unjailed build runs (android: no namespace, no chroot): the runtime's loader with --library-path
// when the runtime brings one, the build itself when it does not (the sim's fake soloist beside a placeholder
// runtime), and the scratch's own paths for the pcm socket, the ca bundle and the resolv preload
TEST_CASE("a plain plan runs through the runtime's loader when it brings one") {
  JailRig rig;
  // the rig's runtime holds placeholders only: no loader, so the build runs as itself
  const auto empty = findRuntime(rig.root());
  CHECK(empty.loader.empty());
  CHECK(empty.libraryPath.empty());
  CHECK(empty.preload.empty());

  const auto paths = rig.paths();
  const auto plan = planPlain(paths);
  CHECK(plan.plan.program == paths.build);
  CHECK(plan.build == paths.build);
  CHECK(hasEnv(plan.plan.env, "HOME=" + paths.home));
  CHECK(hasEnv(plan.plan.env, "SPOTIFY_SSL_CERT_FILE=" + paths.caFile));
  CHECK(hasEnv(plan.plan.env, "PULSE_SERVER=unix:" + paths.run + "/pcm.sock"));
  CHECK(hasEnv(plan.plan.env, "PATH=/nonexistent"));
  CHECK(hasEnv(plan.plan.env, "TMPDIR=" + paths.home));
  for (const char* prefix : {"LD_PRELOAD=", "CADENCE_RESOLV_SOCK="})
    CHECK_FALSE(std::any_of(plan.plan.env.begin(), plan.plan.env.end(),
                            [&](const std::string& v) { return v.starts_with(prefix); }));
  // argv[0] is the program, the build itself: the line is its args alone
  CHECK(plainLine(plan.runtime, paths.build, {"--device-name", "Kitchen"}) ==
        std::vector<std::string>{"--device-name", "Kitchen"});
}

TEST_CASE("a runtime's loader, libraries and resolv preload make the plain plan's line and env") {
  JailRig rig;
  const std::string triple = "aarch64-linux-gnu";
  fs::create_directories(rig.inRoot("/lib"));
  fs::create_directories(rig.inRoot("/usr/lib/" + triple));
  writeFile(rig.inRoot("/lib/ld-linux-aarch64.so.1"), "loader", fs::perms(0755));
  writeFile(rig.inRoot("/usr/lib/" + triple + "/libcadence-resolv.so"), "resolv", fs::perms(0644));

  const auto runtime = findRuntime(rig.root());
  REQUIRE(runtime.loader == rig.inRoot("/lib/ld-linux-aarch64.so.1"));
  CHECK(runtime.libraryPath == rig.inRoot("/lib/" + triple) + ":" + rig.inRoot("/usr/lib/" + triple));
  CHECK(runtime.preload == rig.inRoot("/usr/lib/" + triple + "/libcadence-resolv.so"));

  const auto paths = rig.paths();
  const auto plan = planPlain(paths);
  CHECK(plan.plan.program == runtime.loader);
  CHECK(hasEnv(plan.plan.env, "LD_PRELOAD=" + runtime.preload));
  CHECK(hasEnv(plan.plan.env, "CADENCE_RESOLV_SOCK=" + paths.run + "/resolv.sock"));
  CHECK(plainLine(plan.runtime, paths.build, {"--ws", "127.0.0.1:0"}) ==
        std::vector<std::string>{"--library-path", runtime.libraryPath, paths.build, "--ws", "127.0.0.1:0"});

  // a loader without the exec bit is none: the runtime is there for the jail's checks, not for a plain launch
  fs::permissions(runtime.loader, fs::perms(0644));
  const auto noExec = findRuntime(rig.root());
  CHECK(noExec.loader.empty());
}

// the plan a build makes of the same paths: the jail's in a jailed build, the plain one in an unjailed build, and
// the probe runs the plan's own form
TEST_CASE("soloistPlan is the build's form, and probePlan runs it") {
  JailRig rig;
  const auto plan = soloistPlan(rig.paths());
  if constexpr (kSoloistJailed) {
    REQUIRE(std::holds_alternative<JailPlan>(plan));
    CHECK(std::get<JailPlan>(plan).root == rig.root());
  } else {
    REQUIRE(std::holds_alternative<SoloistPlainPlan>(plan));
    CHECK(std::get<SoloistPlainPlan>(plan).build == rig.paths().build);
  }
}
