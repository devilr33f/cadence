#include "support/http-server.h"
#include "url/ffmpeg-log.h"
#include "url/ffmpeg-options.h"
#include "url/ffmpeg.h"
#include "url/url-input.h"

#include <catch2/catch_test_macros.hpp>

#include <stdio.h>
#include <unistd.h>

#include <cstdio>
#include <cstdlib>
#include <fstream>
#include <iterator>
#include <string>

using namespace cadence;
using namespace cadence::url;
using namespace cadence::test_support;

namespace {

// what the process wrote to stderr while fn ran
template <typename Fn>
std::string stderrOf(Fn fn) {
  char path[] = "/tmp/cadence-stderr-XXXXXX";
  const int file = ::mkstemp(path);
  REQUIRE(file >= 0);
  std::fflush(stderr);
  const int saved = ::dup(STDERR_FILENO);
  REQUIRE(::dup2(file, STDERR_FILENO) == STDERR_FILENO);
  fn();
  std::fflush(stderr);
  ::dup2(saved, STDERR_FILENO);
  ::close(saved);
  ::close(file);
  std::ifstream in(path);
  std::string text{std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
  ::unlink(path);
  return text;
}

}  // namespace

// rule 8: ffmpeg's own messages name urls and hosts; its log only counts them
TEST_CASE("FfmpegLogNeverPrints") {
  installFfmpegLogSink();
  HttpServer server;
  const auto total = [](const FfmpegLogCounts& counts) {
    return counts.fatal + counts.error + counts.warning + counts.info + counts.debug;
  };

  const auto before = ffmpegLogCounts();
  auto printed = stderrOf([] {
    for (const int level : {AV_LOG_PANIC, AV_LOG_FATAL, AV_LOG_ERROR, AV_LOG_WARNING, AV_LOG_INFO, AV_LOG_VERBOSE,
                            AV_LOG_DEBUG, AV_LOG_TRACE})
      av_log(nullptr, level, "opening http://secret.example/private?token=1\n");
  });
  const auto counted = ffmpegLogCounts();
  CHECK(printed.empty());
  CHECK(counted.fatal == before.fatal + 2);
  CHECK(counted.error == before.error + 1);
  CHECK(counted.warning == before.warning + 1);
  CHECK(counted.info == before.info + 1);
  CHECK(counted.debug == before.debug + 3);

  // an open that fails: ffmpeg logs on its way, the url among it
  printed = stderrOf([&] {
    const auto opened = UrlInput::open(server.url("/missing"), ffmpegOptions(""), [] { return false; });
    CHECK_FALSE(opened.ok());
  });
  CHECK(printed.empty());
  CHECK(total(ffmpegLogCounts()) > total(counted));
}
