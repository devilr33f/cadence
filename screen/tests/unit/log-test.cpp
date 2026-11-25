#include "ipc/backoff.h"
#include "ipc/log.h"
#include "ipc/scrub.h"

#include <catch2/catch_test_macros.hpp>

#include <vector>

using namespace cadence::screen::ipc;

TEST_CASE("log lines carry numbers and tokens only") {
  CHECK(formatLine("player_load", {{"session", Token("sess-v1")}, {"start_ms", 60000}, {"gated", true}}) ==
        "player_load session=sess-v1 start_ms=60000 gated=true");
  CHECK(formatLine("frame_stats", {{"fps", 59.94}}) == "frame_stats fps=59.940");
}

TEST_CASE("a token that is not one is never logged as it is (rule 8)") {
  CHECK(Token("http://host/x").value() == "invalid");
  CHECK(Token("Episode Title").value() == "invalid");
  CHECK(Token("").value() == "invalid");
  CHECK(Token(std::string(65, 'a')).value() == "invalid");
  CHECK(Token("econnrefused").value() == "econnrefused");
}

TEST_CASE("third-party lines lose urls, paths and quoted text") {
  CHECK(scrubThirdParty("ffmpeg/demuxer: hls: Failed to open https://cdn.example/seg-1.ts?token=abc\n") ==
        "ffmpeg/demuxer: hls: Failed to open <redacted>");
  CHECK(scrubThirdParty("cplayer: Playing: /media/Anime/Episode 13.mkv") == "cplayer: Playing: <redacted>");
  CHECK(scrubThirdParty("file: Cannot open file '/srv/My Show.mkv': No such file") ==
        "file: Cannot open file <redacted>: No such file");
  CHECK(scrubThirdParty("lavf: title=\"A Series\" x") == "lavf: title=<redacted> x");
  CHECK(scrubThirdParty("ao: rtmp+tls://h/x and ok") == "ao: <redacted> and ok");
  CHECK(scrubThirdParty("demux: /srv/My Show.mkv: No such file") == "demux: <redacted>: No such file");
  CHECK(scrubThirdParty("vd: hwdec=auto-safe 1920x1080") == "vd: hwdec=auto-safe 1920x1080");
  CHECK(scrubThirdParty("a\tb\x01" "c") == "a b c");
}

TEST_CASE("long lines are cut on utf-8 boundaries and marked") {
  std::string text(10, 'x');
  text += "\xd0\xb6";  // a two-byte letter across the cut
  text += std::string(5, 'y');
  const auto parts = splitRecord(LogRecord{"mpv", Level::Warn, text, false, false}, 11);
  REQUIRE(parts.size() == 2);
  CHECK(parts[0].text == std::string(10, 'x'));
  CHECK(parts[0].truncated);
  CHECK_FALSE(parts[0].continued);
  CHECK(parts[1].text == "\xd0\xb6" + std::string(5, 'y'));
  CHECK(parts[1].continued);
  CHECK_FALSE(parts[1].truncated);
  CHECK(splitRecord(LogRecord{"s", Level::Info, "short", false, false}).size() == 1);
}

TEST_CASE("the log ships to its sink, scrubbed, above its level") {
  std::vector<LogRecord> got;
  Log::get().setSink([&](const LogRecord& r) { got.push_back(r); });
  Log::get().event(Level::Info, "link_connected", {{"attempts", 1}});
  Log::get().event(Level::Debug, "noise");
  Log::get().thirdParty("mpv", Level::Warn, "stream: https://x/y failed");
  Log::get().setSink(nullptr);
  REQUIRE(got.size() == 2);
  CHECK(got[0].source == "cadence-screen");
  CHECK(got[0].text == "link_connected attempts=1");
  CHECK(got[1].source == "mpv");
  CHECK(got[1].text == "stream: <redacted> failed");
}

TEST_CASE("reconnect backoff doubles to its cap and resets after a stable link") {
  Backoff b(100, 1000, 5000);
  CHECK(b.next() == 100);
  CHECK(b.next() == 200);
  CHECK(b.next() == 400);
  CHECK(b.next() == 800);
  CHECK(b.next() == 1000);
  CHECK(b.next() == 1000);
  b.connectedFor(100);
  CHECK(b.next() == 1000);
  b.connectedFor(5000);
  CHECK(b.next() == 100);
}
