#include "audio/ogg-opus.h"
#include "support/ogg-file.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cmath>
#include <cstdlib>
#include <cstddef>
#include <cstdint>
#include <numbers>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;


TEST_CASE("a mono ogg opus file decodes to 48 kHz samples without its pre-skip, trimmed by the last granule") {
  const auto decoded = decodeOggOpus(oggOpusFile(25, 1, 100));
  REQUIRE(decoded.ok());
  CHECK(decoded.value().size() == 25 * 960 - 100 - kPreSkip);
  int peak = 0;
  for (const auto s : decoded.value())
    peak = std::max(peak, std::abs(static_cast<int>(s)));
  CHECK(peak > 1000);
  CHECK(peak <= kMaxPromptPeak);
}

TEST_CASE("a truncated file is refused") {
  auto f = oggOpusFile(10);
  f.resize(f.size() - 7);
  const auto decoded = decodeOggOpus(f);
  REQUIRE_FALSE(decoded.ok());
  CHECK(decoded.error() == PromptErrorCode::BadPage);
}

TEST_CASE("a page whose checksum does not hold is refused") {
  auto f = oggOpusFile(10);
  f[f.size() - 3] ^= 0x55;
  const auto decoded = decodeOggOpus(f);
  REQUIRE_FALSE(decoded.ok());
  CHECK(decoded.error() == PromptErrorCode::BadCrc);
}

TEST_CASE("a stereo file is refused") {
  const auto decoded = decodeOggOpus(oggOpusFile(10, 2));
  REQUIRE_FALSE(decoded.ok());
  CHECK(decoded.error() == PromptErrorCode::Channels);
}

TEST_CASE("what is no ogg, or ogg without opus, is refused") {
  const OggBytes riff = {'R', 'I', 'F', 'F', 0, 0, 0, 0};
  auto decoded = decodeOggOpus(riff);
  REQUIRE_FALSE(decoded.ok());
  CHECK(decoded.error() == PromptErrorCode::NotOgg);

  OggBytes vorbis = page({OggBytes{1, 'v', 'o', 'r', 'b', 'i', 's'}}, 0x02, 0, 0);
  const auto second = page({opusTags()}, 0, 0, 1);
  vorbis.insert(vorbis.end(), second.begin(), second.end());
  decoded = decodeOggOpus(vorbis);
  REQUIRE_FALSE(decoded.ok());
  CHECK(decoded.error() == PromptErrorCode::NotOpus);
}

TEST_CASE("a file past the size cap or the length cap is refused") {
  const OggBytes big(kMaxPromptFileBytes + 1, 0);
  auto decoded = decodeOggOpus(big);
  REQUIRE_FALSE(decoded.ok());
  CHECK(decoded.error() == PromptErrorCode::TooLarge);

  decoded = decodeOggOpus(oggOpusFile(static_cast<int>(kMaxPromptSamples / 960) + 20));
  REQUIRE_FALSE(decoded.ok());
  CHECK(decoded.error() == PromptErrorCode::TooLong);
}

TEST_CASE("headers alone are no audio") {
  OggBytes f = page({opusHead(1, kPreSkip)}, 0x02, 0, 0);
  const auto tags = page({opusTags()}, 0x04, 0, 1);
  f.insert(f.end(), tags.begin(), tags.end());
  const auto decoded = decodeOggOpus(f);
  REQUIRE_FALSE(decoded.ok());
  CHECK(decoded.error() == PromptErrorCode::NoAudio);
}
