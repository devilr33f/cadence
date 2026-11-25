#pragma once

#include "audiod/media-source.h"

#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <vector>

// shairport's metadata pipe (M4d-D5), read as its documentation describes it: a stream of items, each
// <item><type>T</type><code>C</code><length>N</length>, then for N above 0 <data encoding="base64">base64 of the N
// bytes</data>, then </item>. T and C are four-character codes, each written as 8 hex digits. what it says is memory
// only: never logged (rule 8)
namespace cadence::audiod::airplay {

// an item whose data is longer is skipped whole: a cover (ssnc PICT) is the one item that comes near it
inline constexpr std::size_t kMaxMetadataItem = std::size_t{1} << 20;

constexpr std::uint32_t fourcc(std::string_view code) {
  std::uint32_t value = 0;
  for (const char c : code.substr(0, 4))
    value = (value << 8) | static_cast<std::uint8_t>(c);
  return value;
}

inline constexpr std::uint32_t kTypeCore = fourcc("core");
inline constexpr std::uint32_t kTypeShairport = fourcc("ssnc");

struct MetadataItem {
  std::uint32_t type = 0;
  std::uint32_t code = 0;
  // decoded
  std::string data;

  bool operator==(const MetadataItem&) const = default;
};

// cuts the pipe's bytes into items, strictly: what is not a whole well-formed item (hex codes, a decimal length, base64
// that decodes to exactly that length) is skipped up to the next <item>, and an item longer than kMaxMetadataItem is
// skipped whole. what it holds stays bounded whatever the writer sends
class MetadataReader {
 public:
  // appends the items these bytes completed, in order
  void feed(std::span<const std::uint8_t> bytes, std::vector<MetadataItem>& items);
  // the writer closed the pipe: what it left unfinished is dropped
  void reset();
  // items skipped as malformed or oversize, since the start
  [[nodiscard]] std::uint64_t skipped() const { return skipped_; }

 private:
  enum class Parsed : std::uint8_t { More, Item, Malformed, Oversize };

  Parsed parse(std::size_t& used, MetadataItem& item) const;
  // drops what comes before the next <item>, keeping what may start one
  void seek();

  std::string buffer_;
  std::uint64_t skipped_ = 0;
  // what is dropped up to the next item belongs to a skip already counted
  bool discarding_ = false;
};

// where the track stands, from ssnc prgr
struct Progress {
  std::uint64_t positionMs = 0;
  // 0 = unknown
  std::uint64_t durationMs = 0;

  bool operator==(const Progress&) const = default;
};

// ssnc prgr's "start/current/end", rtp frames at 44.1 kHz (modulo 2^32), as a position within a duration; nullopt for
// another item or a malformed one
std::optional<Progress> progress(const MetadataItem& item);

// a cover's bytes when they are a jpeg or a png, the only images passed on
bool knownImage(std::string_view bytes);

// folds items into what MediaState shows: the title (core minm), artist (core asar) and album (core asal), each
// bounded and valid utf-8 (U+FFFD for a byte that starts no valid sequence), and the cover (ssnc PICT, a jpeg or png:
// its bytes kept here, its id the hex of their 64-bit fnv-1a). an mdst..mden sequence merges empty fields into the
// current track; a non-empty title, artist or album that differs starts another track, whose omitted fields and cover
// stay empty until its own arrive. an item outside a sequence replaces its own; a play stream's begin (ssnc pbeg) and
// end (ssnc pend) reset it
class MetadataTracker {
 public:
  // the whole metadata, when this item changed it
  std::optional<MediaMetadata> item(const MetadataItem& item);
  // a sequence under way is dropped and the fields cleared (a play stream's edge, a writer that went): the cleared
  // metadata when there was any
  std::optional<MediaMetadata> reset();
  [[nodiscard]] const MediaMetadata& current() const { return current_; }
  // the bytes of the latest cover, empty for none
  [[nodiscard]] const std::string& artwork() const { return artwork_; }

 private:
  std::optional<MediaMetadata> set(MediaMetadata next);
  std::optional<MediaMetadata> cover(const std::string& data);

  MediaMetadata current_;
  std::string artwork_;
  // the sequence under way, whether it held a field of ours, and whether a cover came within it
  std::optional<MediaMetadata> sequence_;
  bool sequenceHeld_ = false;
  bool sequenceCover_ = false;
};

}  // namespace cadence::audiod::airplay
