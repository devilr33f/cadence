#include "audiod/soloist-scratch.h"
#include "support/audiod-harness.h"
#include "support/jail-rig.h"

#include <catch2/catch_test_macros.hpp>

#include <sys/stat.h>
#include <unistd.h>

#include <filesystem>
#include <optional>
#include <string>
#include <utility>

using namespace cadence::audiod;
using namespace cadence::test_support;
namespace fs = std::filesystem;

namespace {

struct stat entry(const std::string& path) {
  struct stat info {};
  REQUIRE(::lstat(path.c_str(), &info) == 0);
  return info;
}

unsigned mode(const std::string& path) { return entry(path).st_mode & 07777; }

std::pair<uid_t, gid_t> owner(const std::string& path) {
  const auto info = entry(path);
  return {info.st_uid, info.st_gid};
}

std::size_t count(const std::string& dir) {
  return static_cast<std::size_t>(std::distance(fs::directory_iterator(dir), fs::directory_iterator()));
}

// an outside directory a symlink in the scratch points at, which a prepare must leave as it is
struct Outside {
  explicit Outside(const TempDir& dir) : path(dir.file("outside")) {
    fs::create_directory(path);
    writeFile(path + "/kept", "kept", fs::perms(0644));
    fs::permissions(path, fs::perms(0750));
  }
  void checkUntouched() const {
    CHECK(count(path) == 1);
    CHECK(fs::exists(path + "/kept"));
    CHECK(mode(path) == 0750);
  }
  std::string path;
};

}  // namespace

TEST_CASE("the scratch is home, run and a read-only crashpad directory, owned as soloist's jail needs") {
  TempDir dir;
  LogCapture logs;
  const auto log = logs.log();

  const auto scratch = prepareSoloistScratch(log, dir.file("soloist"));

  REQUIRE(scratch);
  CHECK(scratch->root == dir.file("soloist"));
  CHECK(scratch->home == dir.file("soloist") + "/home");
  CHECK(scratch->run == dir.file("soloist") + "/run");
  const auto crashpad = scratch->home + "/crashpad";
  CHECK(mode(scratch->root) == 0755);
  CHECK(mode(scratch->home) == 01770);
  CHECK(mode(scratch->run) == 0700);
  CHECK(S_ISDIR(entry(crashpad).st_mode));
  CHECK(mode(crashpad) == 0555);
  CHECK(count(scratch->home) == 1);
  CHECK(count(scratch->run) == 0);
  CHECK(count(crashpad) == 0);
  if (::geteuid() == 0) {
    CHECK(owner(scratch->root) == std::pair<uid_t, gid_t>{0, 0});
    CHECK(owner(scratch->home) == std::pair<uid_t, gid_t>{0, 65534});
    CHECK(owner(scratch->run) == std::pair<uid_t, gid_t>{65534, 65534});
    CHECK(owner(crashpad) == std::pair<uid_t, gid_t>{0, 0});
  }
}

TEST_CASE("a prepare removes what the last run left, crashpad's contents included") {
  TempDir dir;
  LogCapture logs;
  const auto log = logs.log();
  const auto first = prepareSoloistScratch(log, dir.file("soloist"));
  REQUIRE(first);
  fs::create_directories(first->home + "/cache/ab");
  writeFile(first->home + "/cache/ab/track", "stale", fs::perms(0600));
  writeFile(first->home + "/ws.port", "40123", fs::perms(0600));
  writeFile(first->run + "/pcm.sock", "stale", fs::perms(0600));
  fs::permissions(first->home + "/crashpad", fs::perms(0755));
  writeFile(first->home + "/crashpad/dump", "stale", fs::perms(0600));
  fs::permissions(first->home + "/crashpad", fs::perms(0555));

  const auto second = prepareSoloistScratch(log, dir.file("soloist"));

  REQUIRE(second);
  CHECK(count(second->home) == 1);
  CHECK(count(second->home + "/crashpad") == 0);
  CHECK(count(second->run) == 0);
  CHECK(mode(second->home) == 01770);
}

TEST_CASE("a symlink in the old tree is removed, never followed: its target stays as it was") {
  TempDir dir;
  LogCapture logs;
  const auto log = logs.log();
  const Outside outside(dir);
  const auto path = dir.file("soloist");

  SECTION("in the scratch's own place") {
    fs::create_directory_symlink(outside.path, path);
    const auto scratch = prepareSoloistScratch(log, path);
    REQUIRE(scratch);
    CHECK(S_ISDIR(entry(path).st_mode));
  }
  SECTION("below it") {
    const auto first = prepareSoloistScratch(log, path);
    REQUIRE(first);
    fs::remove(first->run);
    fs::create_directory_symlink(outside.path, first->run);
    fs::create_directory_symlink(outside.path, first->home + "/link");
    const auto scratch = prepareSoloistScratch(log, path);
    REQUIRE(scratch);
    CHECK(S_ISDIR(entry(scratch->run).st_mode));
    CHECK_FALSE(fs::exists(fs::symlink_status(scratch->home + "/link")));
  }
  SECTION("at crashpad") {
    const auto first = prepareSoloistScratch(log, path);
    REQUIRE(first);
    fs::permissions(first->home + "/crashpad", fs::perms(0755));
    fs::remove(first->home + "/crashpad");
    fs::create_directory_symlink(outside.path, first->home + "/crashpad");
    const auto scratch = prepareSoloistScratch(log, path);
    REQUIRE(scratch);
    CHECK(S_ISDIR(entry(scratch->home + "/crashpad").st_mode));
    CHECK(mode(scratch->home + "/crashpad") == 0555);
  }
  outside.checkUntouched();
}

TEST_CASE("a file in the scratch's place or at crashpad is replaced by the directory") {
  TempDir dir;
  LogCapture logs;
  const auto log = logs.log();
  const auto path = dir.file("soloist");

  SECTION("the scratch") {
    writeFile(path, "in the way", fs::perms(0644));
  }
  SECTION("crashpad, which soloist's create_directories would throw on") {
    const auto first = prepareSoloistScratch(log, path);
    REQUIRE(first);
    fs::permissions(first->home + "/crashpad", fs::perms(0755));
    fs::remove(first->home + "/crashpad");
    writeFile(first->home + "/crashpad", "blocked", fs::perms(0644));
  }
  const auto scratch = prepareSoloistScratch(log, path);
  REQUIRE(scratch);
  CHECK(S_ISDIR(entry(scratch->home + "/crashpad").st_mode));
  CHECK(mode(scratch->home + "/crashpad") == 0555);
}

TEST_CASE("a scratch whose parent is missing fails, logged with the call") {
  TempDir dir;
  LogCapture logs;
  const auto log = logs.log();

  CHECK_FALSE(prepareSoloistScratch(log, dir.file("missing") + "/soloist").has_value());
  CHECK(logs.count(R"("msg":"soloist_scratch_failed","call":"open","errno":2)") == 1);
  CHECK_FALSE(prepareSoloistScratch(log, "relative/soloist").has_value());
  CHECK(logs.count(R"("msg":"soloist_scratch_failed","call":"path","errno":0)") == 1);
}

TEST_CASE("home alone is made again, a ws.port made a directory with it, and run stays as it is") {
  TempDir dir;
  LogCapture logs;
  const auto log = logs.log();
  const Outside outside(dir);
  const auto scratch = prepareSoloistScratch(log, dir.file("soloist"));
  REQUIRE(scratch);
  fs::create_directories(scratch->home + "/ws.port/inner");
  writeFile(scratch->home + "/ws.port/inner/file", "x", fs::perms(0600));
  fs::create_directory_symlink(outside.path, scratch->home + "/link");
  writeFile(scratch->run + "/pcm.sock", "kept", fs::perms(0600));

  REQUIRE(resetSoloistHome(log, *scratch));

  CHECK(count(scratch->home) == 1);
  CHECK(mode(scratch->home) == 01770);
  CHECK(mode(scratch->home + "/crashpad") == 0555);
  CHECK(fs::exists(scratch->run + "/pcm.sock"));
  outside.checkUntouched();
  if (::geteuid() == 0)
    CHECK(owner(scratch->home) == std::pair<uid_t, gid_t>{0, 65534});

  CHECK_FALSE(resetSoloistHome(log, SoloistScratch{.root = dir.file("gone"), .home = {}, .run = {}}));
  CHECK(logs.count(R"("msg":"soloist_scratch_failed","call":"open")") == 1);
}
