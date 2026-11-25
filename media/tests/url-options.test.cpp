#include "url/ffmpeg-options.h"

#include <catch2/catch_test_macros.hpp>

#include <optional>
#include <string>
#include <vector>

using namespace cadence::url;

// M4b-D9: tls always verified, only http's protocols, and bounds on time, reconnects and redirects
TEST_CASE("OptionsVerifyTls") {
  const auto options = ffmpegOptions("/etc/cadence/ca.pem");
  CHECK(options == std::vector<FfmpegOption>{{"protocol_whitelist", "http,https,tcp,tls,hls,crypto"},
                                             {"tls_verify", "1"},
                                             {"ca_file", "/etc/cadence/ca.pem"},
                                             {"rw_timeout", "10000000"},
                                             {"reconnect", "1"},
                                             {"reconnect_streamed", "1"},
                                             {"reconnect_delay_max", "5"},
                                             {"icy", "1"},
                                             {"user_agent", "cadence"},
                                             {"max_redirects", "5"},
                                             {"probesize", "1048576"},
                                             {"analyzeduration", "3000000"}});

  FfmpegDictionary dictionary(options);
  CHECK(dictionary.size() == options.size());
  CHECK(dictionary.find("tls_verify") == "1");
  CHECK(dictionary.find("ca_file") == "/etc/cadence/ca.pem");
  CHECK(dictionary.find("protocol_whitelist") == "http,https,tcp,tls,hls,crypto");

  // no bundle: openssl's default paths, verified all the same
  FfmpegDictionary fallback(ffmpegOptions(""));
  CHECK(fallback.find("tls_verify") == "1");
  CHECK(fallback.find("ca_file") == std::nullopt);
}

TEST_CASE("only the format context's own options stay out of a nested open") {
  CHECK(formatOption("protocol_whitelist"));
  CHECK(formatOption("probesize"));
  CHECK(formatOption("analyzeduration"));
  CHECK_FALSE(formatOption("tls_verify"));
  CHECK_FALSE(formatOption("ca_file"));
  CHECK_FALSE(formatOption("max_redirects"));
}

TEST_CASE("only http and https urls are accepted, their scheme lowercased") {
  CHECK(acceptedUrl("http://radio.example/stream") == "http://radio.example/stream");
  CHECK(acceptedUrl("https://radio.example/a.m3u8?x=1") == "https://radio.example/a.m3u8?x=1");
  CHECK(acceptedUrl("HTTPS://Radio.Example/Path") == "https://Radio.Example/Path");
  CHECK(acceptedUrl("hTtP://a/b") == "http://a/b");

  const std::vector<std::string> refused = {"",
                                            "http",
                                            "http:",
                                            "http://",
                                            "http:/a",
                                            "https:a",
                                            "file:///etc/passwd",
                                            "FILE:///etc/passwd",
                                            "ftp://a/b",
                                            "data:audio/wav;base64,AA",
                                            "concat:http://a|http://b",
                                            "hls+http://a/b.m3u8",
                                            "hls+file:///a.m3u8",
                                            "crypto+http://a/b",
                                            "tcp://a:80",
                                            "httpx://a/b",
                                            " http://a/b",
                                            "http://a/b c",
                                            "http://a/\r\nX: y",
                                            "http://a/\tb",
                                            std::string("http://a/\x7f", 10)};
  for (const auto& url : refused) {
    INFO(url);
    CHECK(acceptedUrl(url) == std::nullopt);
  }
}
