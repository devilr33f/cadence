#include "file/wav.h"
#include "support/samples.h"
#include "support/temp-dir.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdint>
#include <fstream>
#include <iterator>
#include <string>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;

namespace {

using Bytes = std::vector<std::uint8_t>;

Bytes slurp(const std::string& path) {
  std::ifstream in(path, std::ios::binary);
  return {std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
}

std::uint32_t le32(const Bytes& b, std::size_t at) {
  return static_cast<std::uint32_t>(b[at]) | (static_cast<std::uint32_t>(b[at + 1]) << 8) | (static_cast<std::uint32_t>(b[at + 2]) << 16) |
         (static_cast<std::uint32_t>(b[at + 3]) << 24);
}

void put(Bytes& b, const std::string& tag) {
  b.insert(b.end(), tag.begin(), tag.end());
}

void put32(Bytes& b, std::uint32_t v) {
  for (int i = 0; i < 4; ++i)
    b.push_back(static_cast<std::uint8_t>(v >> (8 * i)));
}

void put16(Bytes& b, std::uint16_t v) {
  b.push_back(static_cast<std::uint8_t>(v));
  b.push_back(static_cast<std::uint8_t>(v >> 8));
}

Bytes fmtChunk(std::uint16_t tag, std::uint16_t channels, std::uint32_t rate, std::uint16_t bits) {
  Bytes b;
  put(b, "fmt ");
  put32(b, 16);
  put16(b, tag);
  put16(b, channels);
  put32(b, rate);
  put32(b, rate * channels * bits / 8);
  put16(b, static_cast<std::uint16_t>(channels * bits / 8));
  put16(b, bits);
  return b;
}

Bytes riff(const std::vector<Bytes>& chunks) {
  Bytes body;
  put(body, "WAVE");
  for (const auto& c : chunks)
    body.insert(body.end(), c.begin(), c.end());
  Bytes b;
  put(b, "RIFF");
  put32(b, static_cast<std::uint32_t>(body.size()));
  b.insert(b.end(), body.begin(), body.end());
  return b;
}

Bytes dataChunk(std::size_t bytes, std::uint32_t declared) {
  Bytes b;
  put(b, "data");
  put32(b, declared);
  b.insert(b.end(), bytes, 0x01);
  return b;
}

}  // namespace

TEST_CASE("wav: samples survive a write and a read", "[wav]") {
  TempDir dir;
  for (const std::size_t channels : {std::size_t{1}, std::size_t{2}}) {
    const auto path = dir.file("a.wav");
    const auto samples = ramp(480 * channels, -200);
    {
      auto writer = WavWriter::create(path, {48000, channels});
      REQUIRE(writer.ok());
      REQUIRE_FALSE(writer.value().write(std::span(samples).first(100)));
      REQUIRE_FALSE(writer.value().write(std::span(samples).subspan(100)));
      CHECK(writer.value().frames() == 480);
      REQUIRE_FALSE(writer.value().close());
    }
    const auto reader = WavReader::open(path);
    REQUIRE(reader.ok());
    CHECK(reader.value().format().sampleRate == 48000);
    CHECK(reader.value().format().channels == channels);
    CHECK(reader.value().samples() == samples);
    CHECK(reader.value().frames() == 480);
  }
}

TEST_CASE("wav: the header sizes are finalized on close and on destruction", "[wav]") {
  TempDir dir;
  const auto samples = ramp(10);
  {
    auto writer = WavWriter::create(dir.file("a.wav"), {16000, 1});
    REQUIRE(writer.ok());
    REQUIRE_FALSE(writer.value().write(samples));
    REQUIRE_FALSE(writer.value().close());
    REQUIRE_FALSE(writer.value().close());
    CHECK(writer.value().write(samples)->code == WavErrorCode::WriteFailed);
  }
  auto bytes = slurp(dir.file("a.wav"));
  REQUIRE(bytes.size() == 44 + 20);
  CHECK(le32(bytes, 4) == 36 + 20);
  CHECK(le32(bytes, 40) == 20);
  CHECK(le32(bytes, 24) == 16000);

  {
    auto writer = WavWriter::create(dir.file("b.wav"), {16000, 2});
    REQUIRE(writer.ok());
    REQUIRE_FALSE(writer.value().write(samples));
  }
  bytes = slurp(dir.file("b.wav"));
  CHECK(le32(bytes, 4) == 36 + 20);
  CHECK(le32(bytes, 40) == 20);
}

TEST_CASE("wav: the writer refuses what the reader would and missing directories", "[wav]") {
  TempDir dir;
  CHECK(WavWriter::create(dir.file("a.wav"), {0, 1}).error().code == WavErrorCode::Unsupported);
  CHECK(WavWriter::create(dir.file("a.wav"), {16000, 6}).error().code == WavErrorCode::Unsupported);
  CHECK(WavWriter::create(dir.file("missing/a.wav"), {16000, 1}).error().code == WavErrorCode::OpenFailed);
}

TEST_CASE("wav: formats other than 16-bit pcm are refused with their code", "[wav]") {
  const auto code = [](const Bytes& bytes) {
    const auto parsed = WavReader::parse(bytes);
    REQUIRE_FALSE(parsed.ok());
    return parsed.error().code;
  };
  CHECK(code(riff({fmtChunk(3, 1, 16000, 32), dataChunk(8, 8)})) == WavErrorCode::Unsupported);
  CHECK(code(riff({fmtChunk(1, 1, 16000, 8), dataChunk(8, 8)})) == WavErrorCode::Unsupported);
  CHECK(code(riff({fmtChunk(1, 6, 16000, 16), dataChunk(8, 8)})) == WavErrorCode::Unsupported);
  CHECK(code({'n', 'o', 'p', 'e', 0, 0, 0, 0, 'W', 'A', 'V', 'E'}) == WavErrorCode::Malformed);
  CHECK(code(riff({dataChunk(8, 8)})) == WavErrorCode::Malformed);
  CHECK(code(riff({fmtChunk(1, 1, 16000, 16)})) == WavErrorCode::Malformed);
  CHECK(code({}) == WavErrorCode::Malformed);
  CHECK(WavReader::open("/nonexistent/x.wav").error().code == WavErrorCode::OpenFailed);
}

TEST_CASE("wav: unknown chunks are skipped and short or open-ended data is clamped", "[wav]") {
  Bytes junk;
  put(junk, "LIST");
  put32(junk, 3);
  junk.insert(junk.end(), {1, 2, 3, 0});

  auto parsed = WavReader::parse(riff({fmtChunk(1, 1, 16000, 16), junk, dataChunk(8, 8)}));
  REQUIRE(parsed.ok());
  CHECK(parsed.value().frames() == 4);

  // a streaming writer leaves the maximum size; an odd trailing byte is not a frame
  parsed = WavReader::parse(riff({fmtChunk(1, 1, 16000, 16), dataChunk(9, 0xffffffffu)}));
  REQUIRE(parsed.ok());
  CHECK(parsed.value().frames() == 4);

  parsed = WavReader::parse(riff({fmtChunk(1, 2, 16000, 16), dataChunk(10, 10)}));
  REQUIRE(parsed.ok());
  CHECK(parsed.value().frames() == 2);
}
