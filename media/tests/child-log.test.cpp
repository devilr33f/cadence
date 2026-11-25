#include "audiod/child-log.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <memory>
#include <span>
#include <string>
#include <string_view>
#include <vector>

using namespace cadence::audiod;
using namespace std::chrono_literals;
using Clock = ChildLog::Clock;
using Stream = ChildLog::Stream;

namespace {

constexpr auto kDebug = cadence_audiod_v1_LogLevel_LOG_LEVEL_DEBUG;
constexpr auto kInfo = cadence_audiod_v1_LogLevel_LOG_LEVEL_INFO;
constexpr auto kWarn = cadence_audiod_v1_LogLevel_LOG_LEVEL_WARN;
constexpr auto kError = cadence_audiod_v1_LogLevel_LOG_LEVEL_ERROR;
constexpr std::string_view kEllipsis = "\xe2\x80\xa6";

// what a ChildLog sent
struct Lines {
  std::shared_ptr<ChildLogSwitch> forward = std::make_shared<ChildLogSwitch>();
  std::vector<ChildLine> got;

  ChildLogTarget target() {
    return {.forward = forward, .sink = [this](ChildLine line) { got.push_back(std::move(line)); }};
  }
};

void feed(ChildLog& log, Stream stream, std::string_view text, Clock::time_point now) {
  log.feed(stream, std::span<const char>(text.data(), text.size()), now);
}

const Clock::time_point t0 = Clock::now();

}  // namespace

TEST_CASE("stdout's lines are INFO and stderr's WARN, each line whole, the newline and a carriage return gone") {
  Lines lines;
  ChildLog log("shairport-sync", lines.target());
  feed(log, Stream::Out, "one\r\ntw", t0);
  feed(log, Stream::Err, "bad\n", t0);
  feed(log, Stream::Out, "o\n\n", t0);
  REQUIRE(lines.got.size() == 3);
  CHECK(lines.got[0].source == "shairport-sync");
  CHECK(lines.got[0].text == "one");
  CHECK(lines.got[0].level == kInfo);
  CHECK(lines.got[1].text == "bad");
  CHECK(lines.got[1].level == kWarn);
  CHECK(lines.got[2].text == "two");
  for (const auto& line : lines.got)
    CHECK_FALSE(line.truncated);
}

TEST_CASE("a level at the start of a line is its level, in any case") {
  CHECK(leadingLevel("ERROR: no output") == kError);
  CHECK(leadingLevel("warning: clock drift") == kWarn);
  CHECK(leadingLevel("Warn x") == kWarn);
  CHECK(leadingLevel("debug") == kDebug);
  CHECK(leadingLevel("INFO") == kInfo);
  CHECK(leadingLevel("[ERROR] bracketed") == std::nullopt);
  CHECK(leadingLevel("Information follows") == std::nullopt);
  CHECK(leadingLevel("errors=3") == std::nullopt);
  CHECK(leadingLevel(" ERROR leading space") == std::nullopt);
  CHECK(leadingLevel("") == std::nullopt);

  Lines lines;
  ChildLog log("avahi", lines.target());
  feed(log, Stream::Err, "debug: probe\nINFO: up\n", t0);
  feed(log, Stream::Out, "Error: down\n", t0);
  REQUIRE(lines.got.size() == 3);
  CHECK(lines.got[0].level == kDebug);
  CHECK(lines.got[1].level == kInfo);
  CHECK(lines.got[2].level == kError);
}

TEST_CASE("a line past 4096 bytes is cut with an ellipsis and the rest of it skipped") {
  Lines lines;
  ChildLog log("soloist", lines.target());
  const std::string exact(kChildLineBytes, 'a');
  feed(log, Stream::Out, exact + "\n", t0);
  feed(log, Stream::Out, std::string(kChildLineBytes + 500, 'b') + "\nnext\n", t0);
  REQUIRE(lines.got.size() == 3);
  CHECK(lines.got[0].text == exact);
  CHECK_FALSE(lines.got[0].truncated);
  CHECK(lines.got[1].text.size() == kChildLineBytes);
  CHECK(lines.got[1].text == std::string(kChildLineBytes - kEllipsis.size(), 'b') + std::string(kEllipsis));
  CHECK(lines.got[1].truncated);
  CHECK(lines.got[2].text == "next");
}

TEST_CASE("text that is no utf-8, or holds a nul, is made valid and still fits") {
  Lines lines;
  ChildLog log("nqptp", lines.target());
  feed(log, Stream::Out, std::string("ok\xff\xfe") + '\0' + "ok\n", t0);
  // two-byte characters past the cap: cut on a character
  std::string wide;
  for (std::size_t i = 0; i < kChildLineBytes / 2; ++i)
    wide += "\xc3\xa9";
  feed(log, Stream::Out, wide + "\n", t0);
  // replacements that grow a line of invalid bytes past the cap
  feed(log, Stream::Out, std::string(2000, '\x80') + "\n", t0);
  REQUIRE(lines.got.size() == 3);
  CHECK(lines.got[0].text == "ok\xef\xbf\xbd\xef\xbf\xbd\xef\xbf\xbdok");
  CHECK_FALSE(lines.got[1].truncated);
  CHECK(lines.got[1].text == wide);
  CHECK(lines.got[2].truncated);
  CHECK(lines.got[2].text.size() <= kChildLineBytes);
  CHECK(lines.got[2].text.ends_with(kEllipsis));
}

TEST_CASE("a line without its newline goes out cut after a second, its rest continued, and cut at its end") {
  Lines lines;
  ChildLog log("dbus", lines.target());
  feed(log, Stream::Out, "waiting", t0);
  CHECK(log.deadline() == t0 + kChildPartialWait);
  log.tick(t0 + 999ms);
  CHECK(lines.got.empty());
  log.tick(t0 + 1s);
  REQUIRE(lines.got.size() == 1);
  CHECK(lines.got[0].text == std::string("waiting") + std::string(kEllipsis));
  CHECK(lines.got[0].truncated);
  CHECK_FALSE(lines.got[0].continued);
  CHECK_FALSE(log.deadline());

  // the rest is a line of its own, which says it continues one: its start may end a secret
  feed(log, Stream::Out, " for\nlast", t0 + 2s);
  REQUIRE(lines.got.size() == 2);
  CHECK(lines.got[1].text == " for");
  CHECK(lines.got[1].continued);
  CHECK_FALSE(lines.got[1].truncated);
  // a line its stream ended without a newline may end in a secret's start too
  log.finish(Stream::Out, t0 + 2s);
  REQUIRE(lines.got.size() == 3);
  CHECK(lines.got[2].text == std::string("last") + std::string(kEllipsis));
  CHECK(lines.got[2].truncated);
  CHECK_FALSE(lines.got[2].continued);

  // a slow line sent twice before its newline: its middle continues one and is cut itself
  feed(log, Stream::Err, "a", t0 + 3s);
  log.tick(t0 + 4s);
  feed(log, Stream::Err, "b", t0 + 4s);
  log.tick(t0 + 5s);
  feed(log, Stream::Err, "c\n", t0 + 5s);
  REQUIRE(lines.got.size() == 6);
  CHECK((lines.got[3].truncated && !lines.got[3].continued));
  CHECK((lines.got[4].truncated && lines.got[4].continued));
  CHECK((!lines.got[5].truncated && lines.got[5].continued));
  CHECK(lines.got[5].text == "c");
}

TEST_CASE("at most 200 lines a second go out, then one line says how many were dropped") {
  Lines lines;
  ChildLog log("shairport-sync", lines.target());
  for (int i = 0; i < 250; ++i)
    feed(log, Stream::Out, "line " + std::to_string(i) + "\n", t0 + std::chrono::milliseconds(i));
  CHECK(lines.got.size() == kChildLinesPerSecond);
  CHECK(lines.got.back().text == "line 199");
  CHECK(log.deadline() == t0 + 1s);
  log.tick(t0 + 999ms);
  CHECK(lines.got.size() == kChildLinesPerSecond);
  log.tick(t0 + 1s);
  REQUIRE(lines.got.size() == kChildLinesPerSecond + 1);
  CHECK(lines.got.back().text == "dropped 50 lines");
  CHECK(lines.got.back().level == kWarn);
  log.tick(t0 + 3s);
  CHECK(lines.got.size() == kChildLinesPerSecond + 1);

  // the next second starts over, and its own drops are told once it ended, here by the line after it
  for (int i = 0; i < 203; ++i)
    feed(log, Stream::Err, "again\n", t0 + 4s);
  feed(log, Stream::Err, "later\n", t0 + 5s);
  REQUIRE(lines.got.size() == 2 * kChildLinesPerSecond + 3);
  CHECK(lines.got[2 * kChildLinesPerSecond + 1].text == "dropped 3 lines");
  CHECK(lines.got.back().text == "later");
  // a child that goes reports what it dropped at once
  for (int i = 0; i < 202; ++i)
    feed(log, Stream::Out, "burst\n", t0 + 6s);
  log.close(t0 + 6s);
  CHECK(lines.got.back().text == "dropped 2 lines");
}

TEST_CASE("off, every line is read and dropped uncounted; on again, they go out") {
  Lines lines;
  ChildLog log("soloist", lines.target());
  lines.forward->set(false);
  for (int i = 0; i < 300; ++i)
    feed(log, Stream::Out, "quiet\n", t0);
  feed(log, Stream::Out, "half", t0);
  log.tick(t0 + 2s);
  CHECK(lines.got.empty());
  lines.forward->set(true);
  feed(log, Stream::Out, "heard\n", t0 + 3s);
  REQUIRE(lines.got.size() == 1);
  CHECK(lines.got[0].text == "heard");
}
