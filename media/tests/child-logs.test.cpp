#include "audiod/child-log-reader.h"
#include "audiod/child-logs.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/child-lines.h"
#include "support/eventually.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <unistd.h>

#include <array>
#include <chrono>
#include <string>
#include <string_view>
#include <vector>

using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

constexpr auto kInfo = cadence_audiod_v1_LogLevel_LOG_LEVEL_INFO;
constexpr auto kWarn = cadence_audiod_v1_LogLevel_LOG_LEVEL_WARN;
constexpr std::string_view kEllipsis = "\xe2\x80\xa6";

Envelope logsSet(bool on) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_logs_set_tag;
  envelope.body.logs_set.third_party = on;
  return envelope;
}

struct Pipe {
  UniqueFd read;
  UniqueFd write;
};

Pipe pipeOf() {
  std::array<int, 2> fds{};
  REQUIRE(::pipe2(fds.data(), O_CLOEXEC) == 0);
  REQUIRE(::fcntl(fds[0], F_SETFL, O_NONBLOCK) == 0);
  return {UniqueFd(fds[0]), UniqueFd(fds[1])};
}

void write(const UniqueFd& fd, std::string_view text) {
  REQUIRE(::write(fd.get(), text.data(), text.size()) == static_cast<ssize_t>(text.size()));
}

}  // namespace

TEST_CASE("a child's longest line reaches cadenced whole over the socket") {
  AudiodHarness harness;
  const auto client = connectTo(harness.socket());
  expectPong(client.get(), 1);
  const auto text = std::string(kChildLineBytes - kEllipsis.size(), 'x') + std::string(kEllipsis);
  harness.childLogs().sink(ChildLine{.source = "shairport-sync", .level = kWarn, .text = text, .truncated = true});
  harness.childLogs().sink(
      ChildLine{.source = "nqptp", .level = kInfo, .text = "short", .truncated = false, .continued = true});
  const auto first = expectFrame(client.get(), cadence_audiod_v1_Envelope_log_line_tag).body.log_line;
  CHECK(std::string(first.source) == "shairport-sync");
  CHECK(first.level == kWarn);
  CHECK(std::string(first.text).size() == kChildLineBytes);
  CHECK(std::string(first.text) == text);
  CHECK(first.truncated);
  const auto second = expectFrame(client.get(), cadence_audiod_v1_Envelope_log_line_tag).body.log_line;
  CHECK(std::string(second.text) == "short");
  CHECK_FALSE(second.truncated);
  CHECK(second.continued);
  CHECK_FALSE(first.continued);
}

TEST_CASE("LogsSet turns the children's lines off and on again") {
  AudiodHarness harness;
  const auto client = connectTo(harness.socket());
  const auto target = harness.childLogs();
  CHECK(target.forward->on());
  sendFrame(client.get(), logsSet(false));
  expectPong(client.get(), 1);
  CHECK_FALSE(target.forward->on());
  CHECK(harness.logs().count(R"("msg":"child_logs","on":0)") == 1);
  sendFrame(client.get(), logsSet(false));
  sendFrame(client.get(), logsSet(true));
  expectPong(client.get(), 2);
  CHECK(target.forward->on());
  CHECK(harness.logs().count(R"("msg":"child_logs")") == 2);
}

// with nobody connected, a line has nowhere to go: it is counted, and the count logged
TEST_CASE("lines the socket cannot take are counted") {
  AudiodHarness harness;
  for (int i = 0; i < 3; ++i)
    harness.childLogs().sink(ChildLine{.source = "dbus", .level = kInfo, .text = "lost", .truncated = false});
  REQUIRE(harness.logs().eventually(R"("msg":"child_log_dropped","count":3)"));
}

TEST_CASE("a child's pipes are drained on the reader's own thread, a partial line after a second, to their end") {
  ChildLines lines;
  auto out = pipeOf();
  auto err = pipeOf();
  {
    ChildLogReader reader(lines.target());
    reader.add("nqptp", std::move(out.read), std::move(err.read));
    const auto written = std::chrono::steady_clock::now();
    write(out.write, "hello\nhal");
    write(err.write, "oops\n");
    REQUIRE(lines.has(2));
    REQUIRE(lines.has(3));
    CHECK(std::chrono::steady_clock::now() - written >= kChildPartialWait);
    write(err.write, "end");
    err.write.reset();
    REQUIRE(lines.has(4));
    // what a child left in its pipe as it went is read before the reader stops
    write(out.write, "last words");
  }
  const auto got = lines.got();
  REQUIRE(got.size() == 5);
  for (const auto& line : got)
    CHECK(line.source == "nqptp");
  CHECK(got[0].text == "hello");
  CHECK(got[0].level == kInfo);
  CHECK(got[1].text == "oops");
  CHECK(got[1].level == kWarn);
  CHECK(got[2].text == std::string("hal") + std::string(kEllipsis));
  CHECK(got[2].truncated);
  // lines a stream ended, or the reader's stop, without their newline: cut, as their end may start a secret
  CHECK(got[3].text == std::string("end") + std::string(kEllipsis));
  CHECK(got[3].truncated);
  CHECK(got[4].text == std::string("last words") + std::string(kEllipsis));
  CHECK(got[4].truncated);
  // the rest of the line the reader sent partially as "hal…"
  CHECK(got[4].continued);
}
