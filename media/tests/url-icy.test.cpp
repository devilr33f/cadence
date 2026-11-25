#include "url/icy.h"

#include <catch2/catch_test_macros.hpp>

#include <optional>
#include <string>

using namespace cadence::url;

TEST_CASE("the StreamTitle of an icy packet") {
  CHECK(icyStreamTitle("StreamTitle='Artist - Title';") == "Artist - Title");
  CHECK(icyStreamTitle("StreamTitle='Artist - Title';StreamUrl='http://a/b';") == "Artist - Title");
  CHECK(icyStreamTitle("StreamUrl='http://a/b';StreamTitle='Song';") == "Song");
  // an apostrophe of its own runs on to the "';" that closes the value
  CHECK(icyStreamTitle("StreamTitle='Guns N' Roses - Don't Cry';StreamUrl='';") == "Guns N' Roses - Don't Cry");
  // a closing quote without the semicolon, as some servers send
  CHECK(icyStreamTitle("StreamTitle='Song'") == "Song");
  CHECK(icyStreamTitle("StreamTitle='';") == "");
  CHECK(icyStreamTitle("StreamUrl='http://a/b';") == std::nullopt);
  CHECK(icyStreamTitle("") == std::nullopt);
  CHECK(icyStreamTitle("StreamTitle='unterminated") == "unterminated");
}

TEST_CASE("an icy title splits on its first separator") {
  const auto both = splitIcyTitle("Artist - Title");
  CHECK(both.artist == "Artist");
  CHECK(both.title == "Title");
  const auto more = splitIcyTitle("A - B - C");
  CHECK(more.artist == "A");
  CHECK(more.title == "B - C");
  const auto none = splitIcyTitle("Jingle");
  CHECK(none.artist.empty());
  CHECK(none.title == "Jingle");
  const auto hyphen = splitIcyTitle("Jay-Z-Song");
  CHECK(hyphen.artist.empty());
  CHECK(hyphen.title == "Jay-Z-Song");
}
