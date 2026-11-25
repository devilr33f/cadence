#include "audiod/airplay-metadata.h"
#include "ws/base64.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdint>
#include <cstdio>
#include <span>
#include <string>
#include <string_view>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::audiod::airplay;

namespace {

std::string hex(std::string_view code) {
  char text[9]{};
  std::snprintf(text, sizeof text, "%08x", fourcc(code));
  return text;
}

// one item as the pipe carries it
std::string item(std::string_view type, std::string_view code, std::string_view data) {
  auto text = "<item><type>" + hex(type) + "</type><code>" + hex(code) + "</code><length>" +
              std::to_string(data.size()) + "</length>";
  if (data.empty())
    return text + "</item>\n";
  const std::span bytes(reinterpret_cast<const std::uint8_t*>(data.data()), data.size());
  return text + "\n<data encoding=\"base64\">\n" + ws::base64(bytes) + "</data></item>\n";
}

std::vector<MetadataItem> readAll(MetadataReader& reader, std::string_view text, std::size_t chunk = 0) {
  std::vector<MetadataItem> items;
  const std::span bytes(reinterpret_cast<const std::uint8_t*>(text.data()), text.size());
  const auto step = chunk == 0 ? bytes.size() : chunk;
  for (std::size_t at = 0; at < bytes.size(); at += step)
    reader.feed(bytes.subspan(at, std::min(step, bytes.size() - at)), items);
  return items;
}

MetadataItem expected(std::string_view type, std::string_view code, std::string data = {}) {
  return {fourcc(type), fourcc(code), std::move(data)};
}

// a track's sequence as shairport sends it
std::string track(std::string_view title, std::string_view artist, std::string_view album) {
  return item("ssnc", "mdst", "1146374851") + item("core", "asal", album) + item("core", "asar", artist) +
         item("core", "caps", "\x01") + item("core", "minm", title) + item("ssnc", "mden", "1146374851");
}

}  // namespace

TEST_CASE("items are read as shairport writes them, however the pipe splits them") {
  // the documented shape, written out
  const std::string golden =
      "<item><type>73736e63</type><code>70626567</code><length>0</length></item>\n"
      "<item><type>636f7265</type><code>6d696e6d</code><length>5</length>\n"
      "<data encoding=\"base64\">\nVGl0bGU=</data></item>\n";
  const std::vector<MetadataItem> want{expected("ssnc", "pbeg"), expected("core", "minm", "Title")};
  for (const std::size_t chunk : {std::size_t{0}, std::size_t{1}, std::size_t{7}}) {
    INFO(chunk);
    MetadataReader reader;
    CHECK(readAll(reader, golden, chunk) == want);
    CHECK(reader.skipped() == 0);
  }

  MetadataReader reader;
  const auto items = readAll(reader, track("Song", "Band", "Record"), 3);
  REQUIRE(items.size() == 6);
  CHECK(items[1] == expected("core", "asal", "Record"));
  CHECK(items[2] == expected("core", "asar", "Band"));
  CHECK(items[4] == expected("core", "minm", "Song"));
  CHECK(items[5] == expected("ssnc", "mden", "1146374851"));
}

TEST_CASE("what is not a well-formed item is skipped up to the next one") {
  const auto good = item("core", "minm", "Kept");
  const std::vector<std::string> broken{
      // not hex
      "<item><type>73736e6g</type><code>70626567</code><length>0</length></item>",
      // a code too short
      "<item><type>73736e63</type><code>706265</code><length>0</length></item>",
      // no length
      "<item><type>73736e63</type><code>70626567</code><length></length></item>",
      // data that decodes to another length than it says
      "<item><type>636f7265</type><code>6d696e6d</code><length>4</length>\n<data encoding=\"base64\">\nVGl0bGU="
      "</data></item>",
      // a character base64 does not have
      "<item><type>636f7265</type><code>6d696e6d</code><length>5</length>\n<data encoding=\"base64\">\nVGl0b*U="
      "</data></item>",
      // data on an empty item
      "<item><type>636f7265</type><code>6d696e6d</code><length>0</length>\n<data encoding=\"base64\">\nVGl0bGU="
      "</data></item>",
      // another encoding
      "<item><type>636f7265</type><code>6d696e6d</code><length>5</length>\n<data encoding=\"hex\">\n5469746c65"
      "</data></item>",
      // never closed before the next item
      "<item><type>636f7265</type><code>6d696e6d</code><length>5</length>\n<data encoding=\"base64\">\nVGl0bGU=",
      // junk between items
      "garbage that is no item at all\n",
  };
  for (const auto& bad : broken) {
    INFO(bad);
    MetadataReader reader;
    const auto items = readAll(reader, bad + good, 5);
    REQUIRE(items.size() == 1);
    CHECK(items.front() == expected("core", "minm", "Kept"));
    CHECK(reader.skipped() == 1);
  }
}

TEST_CASE("an item longer than the bound is skipped whole, and what it holds stays bounded") {
  MetadataReader reader;
  const std::string big(kMaxMetadataItem + 1, 'x');
  const auto items = readAll(reader, item("ssnc", "PICT", big) + item("core", "asar", "After"), 4096);
  REQUIRE(items.size() == 1);
  CHECK(items.front() == expected("core", "asar", "After"));
  CHECK(reader.skipped() == 1);

  // at the bound it is read
  const std::string edge(kMaxMetadataItem, 'y');
  CHECK(readAll(reader, item("core", "asal", edge)) == std::vector{expected("core", "asal", edge)});

  // data longer than its length says is refused as it comes
  MetadataReader longer;
  std::string text = "<item><type>636f7265</type><code>6d696e6d</code><length>5</length>\n<data encoding=\"base64\">\n";
  text += std::string(kMaxMetadataItem * 2, 'A');
  const auto after = readAll(longer, text + item("core", "minm", "Next"), 8192);
  REQUIRE(after.size() == 1);
  CHECK(after.front() == expected("core", "minm", "Next"));
  CHECK(longer.skipped() == 1);
}

TEST_CASE("an item that never ends is given up on once it outgrows every item's text, and the next one is read") {
  const std::string head = "<item><type>636f7265</type><code>6d696e6d</code>";
  // whitespace inside the data, which takes no room in the base64, and zeros that never end a length
  for (const auto& endless :
       {head + "<length>5</length>\n<data encoding=\"base64\">\n" + std::string(kMaxMetadataItem * 2, ' '),
        head + "<length>" + std::string(kMaxMetadataItem * 2, '0')}) {
    INFO(endless.substr(head.size(), 12));
    MetadataReader reader;
    const auto items = readAll(reader, endless + item("core", "minm", "Next"), 8192);
    REQUIRE(items.size() == 1);
    CHECK(items.front() == expected("core", "minm", "Next"));
    CHECK(reader.skipped() == 1);
  }
}

TEST_CASE("a writer that closes leaves nothing behind for the next one") {
  MetadataReader reader;
  const auto whole = item("core", "minm", "Title");
  std::vector<MetadataItem> items;
  const std::span bytes(reinterpret_cast<const std::uint8_t*>(whole.data()), whole.size() / 2);
  reader.feed(bytes, items);
  reader.reset();
  CHECK(readAll(reader, item("core", "asar", "Fresh")) == std::vector{expected("core", "asar", "Fresh")});
  CHECK(items.empty());
}

TEST_CASE("a sequence sets the title, artist and album at its end, and a play stream's edges clear them") {
  MetadataTracker tracker;
  MetadataReader reader;
  std::vector<MediaMetadata> seen;
  const auto feed = [&](const std::string& text) {
    for (const auto& next : readAll(reader, text))
      if (const auto changed = tracker.item(next))
        seen.push_back(*changed);
  };

  feed(track("Song", "Band", "Record"));
  REQUIRE(seen.size() == 1);
  CHECK(seen.back() == MediaMetadata{.title = "Song", .artist = "Band", .album = "Record", .artworkUrl = {}, .artworkId = {}, .sender = {}});

  // the next track without an album leaves none
  feed(item("ssnc", "mdst", "") + item("core", "minm", "Other") + item("core", "asar", "Band") +
       item("ssnc", "mden", ""));
  REQUIRE(seen.size() == 2);
  CHECK(seen.back() == MediaMetadata{.title = "Other", .artist = "Band", .album = {}, .artworkUrl = {}, .artworkId = {}, .sender = {}});

  // a sequence of nothing of ours changes nothing, nor does the same again
  feed(item("ssnc", "mdst", "") + item("core", "caps", "\x02") + item("ssnc", "mden", ""));
  feed(item("ssnc", "mdst", "") + item("core", "minm", "Other") + item("core", "asar", "Band") +
       item("ssnc", "mden", ""));
  CHECK(seen.size() == 2);

  // outside a sequence an item replaces its own field
  feed(item("core", "asal", "Live"));
  REQUIRE(seen.size() == 3);
  CHECK(seen.back() == MediaMetadata{.title = "Other", .artist = "Band", .album = "Live", .artworkUrl = {}, .artworkId = {}, .sender = {}});

  feed(item("ssnc", "pend", ""));
  REQUIRE(seen.size() == 4);
  CHECK(seen.back() == MediaMetadata{});
  feed(track("Song", "Band", "Record") + item("ssnc", "pbeg", ""));
  REQUIRE(seen.size() == 6);
  CHECK(seen.back() == MediaMetadata{});
  // a picture that is no jpeg or png, and every other item, are no field of ours
  feed(item("ssnc", "PICT", "\x89PNG") + item("ssnc", "snam", "someone's phone") + item("ssnc", "clip", "10.0.0.2"));
  CHECK(seen.size() == 6);
}

TEST_CASE("a cover is kept with an id of its bytes, kept for the same track and dropped for another") {
  MetadataTracker tracker;
  MetadataReader reader;
  std::vector<MediaMetadata> seen;
  const auto feed = [&](const std::string& text) {
    for (const auto& next : readAll(reader, text))
      if (const auto changed = tracker.item(next))
        seen.push_back(*changed);
  };
  const std::string jpeg = std::string("\xFF\xD8\xFF\xE0", 4) + std::string(70000, '\x01');
  const std::string png = std::string("\x89PNG\r\n\x1A\n", 8) + "pixels";

  feed(track("Song", "Band", "Record") + item("ssnc", "pcst", "1") + item("ssnc", "PICT", jpeg) +
       item("ssnc", "pcen", "1"));
  REQUIRE(seen.size() == 2);
  const auto first = seen.back().artworkId;
  CHECK(first.size() == 16);
  CHECK(first.find_first_not_of("0123456789abcdef") == std::string::npos);
  CHECK(seen.back().title == "Song");
  CHECK(tracker.artwork() == jpeg);

  // the same track said again keeps it
  feed(track("Song", "Band", "Record"));
  CHECK(seen.size() == 2);
  // the same picture again changes nothing; another one gets another id
  feed(item("ssnc", "PICT", jpeg));
  CHECK(seen.size() == 2);
  feed(item("ssnc", "PICT", png));
  REQUIRE(seen.size() == 3);
  CHECK(seen.back().artworkId != first);
  CHECK(tracker.artwork() == png);

  // another track drops it until its own comes, and a cover within its sequence is its own
  feed(track("Next", "Band", "Record"));
  REQUIRE(seen.size() == 4);
  CHECK(seen.back().artworkId.empty());
  CHECK(tracker.artwork().empty());
  feed(item("ssnc", "mdst", "") + item("core", "minm", "Third") + item("ssnc", "PICT", jpeg) + item("ssnc", "mden", ""));
  REQUIRE(seen.size() == 5);
  CHECK(seen.back().artworkId == first);

  // an empty picture is none, and a stream's edge clears it
  feed(item("ssnc", "PICT", ""));
  REQUIRE(seen.size() == 6);
  CHECK(seen.back().artworkId.empty());
  feed(item("ssnc", "PICT", png) + item("ssnc", "pend", ""));
  CHECK(seen.back() == MediaMetadata{});
  CHECK(tracker.artwork().empty());
}

TEST_CASE("progress is where the track stands in its length, across rtp's wrap") {
  CHECK(progress(expected("ssnc", "prgr", "1000/45100/8821000")) == Progress{.positionMs = 1000, .durationMs = 200000});
  // the clock wrapped between the start and now
  CHECK(progress(expected("ssnc", "prgr", "4294923196/44100/4294923196")) ==
        Progress{.positionMs = 2000, .durationMs = 0});
  // a position past the end is the end
  CHECK(progress(expected("ssnc", "prgr", "0/88200/44100")) == Progress{.positionMs = 1000, .durationMs = 1000});
  for (const auto* bad : {"", "1/2", "1/2/x", "1//3", "-1/2/3", "99999999999/1/2", "1/2/3/4"})
    CHECK_FALSE(progress(expected("ssnc", "prgr", bad)));
  CHECK_FALSE(progress(expected("core", "prgr", "1/2/3")));
}

TEST_CASE("text that is not utf-8 gets U+FFFD, and long text is cut to the bound") {
  MetadataTracker tracker;
  const auto changed = tracker.item(expected("core", "minm", "Bad \xff byte"));
  REQUIRE(changed);
  CHECK(changed->title == "Bad \xef\xbf\xbd byte");

  std::string longest;
  for (int i = 0; i < 200; ++i)
    longest += "\xc3\xa9";
  const auto cut = tracker.item(expected("core", "asar", longest));
  REQUIRE(cut);
  CHECK(cut->artist.size() == kMetadataTextBytes - 1);
  CHECK(cut->artist == longest.substr(0, kMetadataTextBytes - 1));
}

TEST_CASE("a reset drops a sequence under way and clears what was set") {
  MetadataTracker tracker;
  REQUIRE(tracker.item(expected("core", "asar", "Band")));
  CHECK_FALSE(tracker.item(expected("ssnc", "mdst")));
  CHECK_FALSE(tracker.item(expected("core", "minm", "Half")));

  const auto cleared = tracker.reset();
  REQUIRE(cleared);
  CHECK(*cleared == MediaMetadata{});
  // the sequence that was under way never ends now
  CHECK_FALSE(tracker.item(expected("ssnc", "mden")));
  CHECK(tracker.current() == MediaMetadata{});
  // nothing was set: nothing to clear
  CHECK_FALSE(tracker.reset());
}
