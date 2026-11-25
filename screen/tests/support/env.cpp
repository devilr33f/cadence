#include "support/env.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdlib>
#include <fstream>
#include <sstream>
#include <unistd.h>

namespace cadence::screen::test {

std::string fixture(const std::string& name) {
  const std::string path = std::string(CADENCE_SCREEN_FIXTURES) + "/" + name + ".binpb";
  std::ifstream in(path, std::ios::binary);
  INFO("fixture " << path);
  REQUIRE(in.good());
  std::stringstream s;
  s << in.rdbuf();
  return s.str();
}

std::string clip(const std::string& name) {
  const char* dir = std::getenv("CADENCE_SCREEN_CLIPS");
  INFO("CADENCE_SCREEN_CLIPS names the clips' directory (the test image sets it)");
  REQUIRE(dir != nullptr);
  const std::string path = std::string(dir) + "/" + name;
  INFO("clip " << path);
  REQUIRE(std::ifstream(path).good());
  return path;
}

std::string socketPath(const std::string& tag) {
  char tmpl[] = "/tmp/cadence-screen-test-XXXXXX";
  const char* dir = ::mkdtemp(tmpl);
  REQUIRE(dir != nullptr);
  return std::string(dir) + "/" + tag + ".sock";
}

}  // namespace cadence::screen::test
