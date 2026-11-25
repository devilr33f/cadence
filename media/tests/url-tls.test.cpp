#include "support/audiod-client.h"
#include "support/eventually.h"
#include "support/http-server.h"
#include "support/media-client.h"
#include "support/playback-client.h"
#include "support/test-certificates.h"
#include "support/url-rig.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <fstream>
#include <string>

using namespace cadence;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

// the test authority as Configure's ca bundle, in the rig's own temp dir
std::string bundle(UrlRig& rig, const TestCertificates& certificates) {
  const auto path = rig.audiod().file("ca.pem");
  std::ofstream(path) << certificates.authorityPem;
  return path;
}

// plays url as a session; the frames until its answer and, when it was ok, its playing state
Frames play(int fd, const std::string& url) {
  sendFrame(fd, urlPlay("play", url));
  return until(fd, [](const Frames& got) {
    if (!hasResult(got, "play"))
      return false;
    return !got.results.back().ok || hasState(got, kPlaying);
  });
}

}  // namespace

// tls is always verified, against Configure's ca bundle: never pruned (hard rule)
TEST_CASE("HttpsVerifiesAgainstTheConfiguredBundle") {
  const auto certificates = makeTestCertificates();
  HttpServer secure(certificates.server);
  secure.route("/tone.flac", {.contentType = "audio/flac", .body = fixture("tone-44k.flac")});
  UrlRig rig;

  SECTION("the bundle with the test authority plays") {
    auto client = rig.connect(bundle(rig, certificates));
    const auto frames = play(client.get(), secure.url("/tone.flac"));
    expectResult(frames, "play", "");
    CHECK(hasState(frames, kPlaying));
    CHECK(secure.requests("/tone.flac") >= 1);
    CHECK(secure.handshakeFailures() == 0);
  }
  SECTION("openssl's default paths do not know it") {
    auto client = rig.connect();
    const auto frames = play(client.get(), secure.url("/tone.flac"));
    expectResult(frames, "play", "open_failed");
    CHECK(frames.states.empty());
    CHECK(secure.requests("/tone.flac") == 0);
    CHECK(eventually([&] { return secure.handshakeFailures() >= 1; }));
  }
}

TEST_CASE("HttpsRefusesALeafForAnotherHost") {
  const auto certificates = makeTestCertificates();
  HttpServer other(certificates.otherHost);
  other.route("/tone.flac", {.contentType = "audio/flac", .body = fixture("tone-44k.flac")});
  UrlRig rig;
  auto client = rig.connect(bundle(rig, certificates));

  const auto frames = play(client.get(), other.url("/tone.flac"));
  expectResult(frames, "play", "open_failed");
  CHECK(frames.states.empty());
  CHECK(other.requests("/tone.flac") == 0);
  CHECK(eventually([&] { return other.handshakeFailures() >= 1; }));
}

// ffmpeg's hls demuxer hands a segment's open only some of the options: the bundle reaches it through UrlInput's own
// io_open, without which this https segment cannot be verified
TEST_CASE("PlaylistSegmentsVerifyAgainstTheBundleToo") {
  const auto certificates = makeTestCertificates();
  HttpServer secure(certificates.server);
  secure.route("/seg.flac", {.contentType = "audio/flac", .body = fixture("tone-44k.flac")});
  UrlRig rig;
  const std::string playlist = "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n"
                               "#EXTINF:2.0,\n" +
                               secure.url("/seg.flac") + "\n#EXT-X-ENDLIST\n";
  rig.server().route("/list.m3u8", {.contentType = "application/vnd.apple.mpegurl", .body = playlist});
  auto client = rig.connect(bundle(rig, certificates));

  const auto frames = play(client.get(), rig.server().url("/list.m3u8"));
  expectResult(frames, "play", "");
  CHECK(hasState(frames, kPlaying));
  CHECK(secure.requests("/seg.flac") >= 1);
  CHECK(secure.handshakeFailures() == 0);
}

TEST_CASE("PlaylistSegmentsRefuseALeafForAnotherHost") {
  const auto certificates = makeTestCertificates();
  HttpServer other(certificates.otherHost);
  other.route("/seg.flac", {.contentType = "audio/flac", .body = fixture("tone-44k.flac")});
  UrlRig rig;
  const std::string playlist = "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n"
                               "#EXTINF:2.0,\n" +
                               other.url("/seg.flac") + "\n#EXT-X-ENDLIST\n";
  rig.server().route("/list.m3u8", {.contentType = "application/vnd.apple.mpegurl", .body = playlist});
  auto client = rig.connect(bundle(rig, certificates));

  const auto frames = play(client.get(), rig.server().url("/list.m3u8"));
  REQUIRE(frames.results.size() == 1);
  CHECK_FALSE(frames.results[0].ok);
  CHECK_FALSE(hasState(frames, kPlaying));
  CHECK(other.requests("/seg.flac") == 0);
  // the server counts the refused handshake on its own thread, which can come after the client's answer
  CHECK(eventually([&] { return other.handshakeFailures() >= 1; }));
}
