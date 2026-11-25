#include "audiod/prompts.h"

#include "support/audiod-harness.h"
#include "support/ogg-file.h"

#include <catch2/catch_test_macros.hpp>

#include <filesystem>
#include <fstream>
#include <string>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;

namespace {

void write(const std::string& path, const OggBytes& bytes) {
  std::filesystem::create_directories(std::filesystem::path(path).parent_path());
  std::ofstream out(path, std::ios::binary);
  out.write(reinterpret_cast<const char*>(bytes.data()), static_cast<std::streamsize>(bytes.size()));
}

}  // namespace

TEST_CASE("a prompt under the directory decodes once and is kept") {
  TempDir dir;
  write(dir.file("ru/failed.opus"), oggOpusFile(25));
  Prompts prompts;
  prompts.setDir(dir.file(""));
  const auto first = prompts.get("ru", "failed");
  REQUIRE(first.pcm != nullptr);
  CHECK(first.pcm->size() == 25 * 960 - kPreSkip);
  std::filesystem::remove(dir.file("ru/failed.opus"));
  CHECK(prompts.get("ru", "failed").pcm == first.pcm);
}

TEST_CASE("a missing, unreadable or unknown prompt plays nothing, and says why") {
  TempDir dir;
  write(dir.file("ru/stereo.opus"), oggOpusFile(10, 2));
  write(dir.file("ru/garbage.opus"), OggBytes{'n', 'o', 'p', 'e'});
  std::filesystem::create_directories(dir.file("ru/folder.opus"));
  Prompts prompts;
  CHECK(prompts.get("ru", "failed").reason == "unconfigured");
  prompts.setDir(dir.file(""));
  CHECK(prompts.get("ru", "offline").reason == "missing");
  CHECK(prompts.get("en", "stereo").reason == "missing");
  CHECK(prompts.get("ru", "stereo").reason == "channels");
  CHECK(prompts.get("ru", "garbage").reason == "not_ogg");
  CHECK(prompts.get("ru", "folder").reason == "read");
  for (const auto* id : {"../x", "Failed", "", "a/b", "abcdefghijklmnopqrstuvwxyz_abcdefg"})
    CHECK(prompts.get("ru", id).reason == "invalid");
  for (const auto* lang : {"r", "russ", "RU", "../"})
    CHECK(prompts.get(lang, "failed").reason == "invalid");
}

TEST_CASE("a symlinked prompt is not followed") {
  TempDir dir;
  write(dir.file("elsewhere.opus"), oggOpusFile(10));
  std::filesystem::create_directories(dir.file("ru"));
  std::filesystem::create_symlink(dir.file("elsewhere.opus"), dir.file("ru/failed.opus"));
  Prompts prompts;
  prompts.setDir(dir.file(""));
  CHECK(prompts.get("ru", "failed").reason == "read");
}
