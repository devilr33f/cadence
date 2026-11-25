#include "audiod/airplay-metadata.h"

#include "text.h"

#include <algorithm>
#include <array>

namespace cadence::audiod::airplay {

namespace {

constexpr std::string_view kItem = "<item>";
constexpr std::string_view kItemEnd = "</item>";
constexpr std::string_view kDataStart = "<data encoding=\"base64\">";
constexpr std::string_view kDataEnd = "</data>";
// an item's text at most: its tags, the base64 of the longest data and room for the whitespace between them
constexpr std::size_t kMaxItemText = 256 + (kMaxMetadataItem + 2) / 3 * 4 + 1024;
constexpr std::uint64_t kMaxLength = 0xffffffff;

bool space(char c) { return c == ' ' || c == '\n' || c == '\r' || c == '\t'; }

int base64Value(char c) {
  if (c >= 'A' && c <= 'Z')
    return c - 'A';
  if (c >= 'a' && c <= 'z')
    return c - 'a' + 26;
  if (c >= '0' && c <= '9')
    return c - '0' + 52;
  if (c == '+')
    return 62;
  if (c == '/')
    return 63;
  return -1;
}

int hexValue(char c) {
  if (c >= '0' && c <= '9')
    return c - '0';
  if (c >= 'a' && c <= 'f')
    return c - 'a' + 10;
  if (c >= 'A' && c <= 'F')
    return c - 'A' + 10;
  return -1;
}

// padded rfc 4648 base64, '=' only at the end; nullopt for anything else
std::optional<std::string> decodeBase64(std::string_view text) {
  if (text.size() % 4 != 0)
    return std::nullopt;
  const std::size_t padding = text.ends_with("==") ? 2 : (text.ends_with('=') ? 1 : 0);
  std::string out;
  out.reserve(text.size() / 4 * 3);
  for (std::size_t at = 0; at < text.size(); at += 4) {
    const bool last = at + 4 == text.size();
    std::array<int, 4> values{};
    for (std::size_t i = 0; i < 4; ++i) {
      const bool pad = last && i >= 4 - padding;
      values[i] = pad ? 0 : base64Value(text[at + i]);
      if (values[i] < 0)
        return std::nullopt;
    }
    const auto group = (static_cast<std::uint32_t>(values[0]) << 18) | (static_cast<std::uint32_t>(values[1]) << 12) |
                       (static_cast<std::uint32_t>(values[2]) << 6) | static_cast<std::uint32_t>(values[3]);
    const std::size_t bytes = last ? 3 - padding : 3;
    for (std::size_t i = 0; i < bytes; ++i)
      out.push_back(static_cast<char>((group >> (16 - 8 * i)) & 0xffU));
  }
  return out;
}

}  // namespace

MetadataReader::Parsed MetadataReader::parse(std::size_t& used, MetadataItem& item) const {
  const std::string_view text = buffer_;
  std::size_t at = 0;
  // the text there is literal, ends inside it, or differs
  const auto literal = [&](std::string_view expected) {
    const auto have = text.substr(at, expected.size());
    if (expected.substr(0, have.size()) != have)
      return Parsed::Malformed;
    if (have.size() < expected.size())
      return Parsed::More;
    at += expected.size();
    return Parsed::Item;
  };
  const auto spaces = [&] {
    while (at < text.size() && space(text[at]))
      ++at;
  };
  const auto hex8 = [&](std::uint32_t& out) {
    for (std::size_t i = 0; i < 8; ++i, ++at) {
      if (at == text.size())
        return Parsed::More;
      const int digit = hexValue(text[at]);
      if (digit < 0)
        return Parsed::Malformed;
      out = (out << 4) | static_cast<std::uint32_t>(digit);
    }
    return Parsed::Item;
  };
  const auto decimal = [&](std::uint64_t& out) {
    std::size_t digits = 0;
    for (; at < text.size() && text[at] >= '0' && text[at] <= '9'; ++at, ++digits) {
      out = out * 10 + static_cast<std::uint64_t>(text[at] - '0');
      if (out > kMaxLength)
        return Parsed::Malformed;
    }
    if (at == text.size())
      return Parsed::More;
    return digits == 0 ? Parsed::Malformed : Parsed::Item;
  };
  // an unfinished item that outgrew every item's text never finishes
  const auto more = [&] { return text.size() > kMaxItemText ? Parsed::Malformed : Parsed::More; };
  Parsed failed = Parsed::Item;
  // false once a step did not find what it expects, and failed says why
  const auto expect = [&](Parsed got) {
    if (got != Parsed::Item)
      failed = got == Parsed::More ? more() : got;
    return got == Parsed::Item;
  };

  std::uint64_t length = 0;
  if (!(expect(literal(kItem)) && expect(literal("<type>")) && expect(hex8(item.type)) &&
        expect(literal("</type><code>")) && expect(hex8(item.code)) && expect(literal("</code><length>")) &&
        expect(decimal(length)) && expect(literal("</length>"))))
    return failed;
  if (length > kMaxMetadataItem) {
    used = at;
    return Parsed::Oversize;
  }
  spaces();
  if (length == 0) {
    if (!expect(literal(kItemEnd)))
      return failed;
    used = at;
    return Parsed::Item;
  }
  if (!expect(literal(kDataStart)))
    return failed;
  // the base64 of length bytes, whitespace allowed between its characters
  const std::size_t expected = (static_cast<std::size_t>(length) + 2) / 3 * 4;
  std::string encoded;
  for (; at < text.size() && text[at] != '<'; ++at) {
    if (space(text[at]))
      continue;
    if (encoded.size() == expected || (base64Value(text[at]) < 0 && text[at] != '='))
      return Parsed::Malformed;
    encoded.push_back(text[at]);
  }
  if (at == text.size())
    return more();
  if (!expect(literal(kDataEnd)))
    return failed;
  spaces();
  if (!expect(literal(kItemEnd)))
    return failed;
  auto data = decodeBase64(encoded);
  if (!data || data->size() != length)
    return Parsed::Malformed;
  item.data = std::move(*data);
  used = at;
  return Parsed::Item;
}

void MetadataReader::seek() {
  const auto at = buffer_.find(kItem);
  std::size_t drop = at;
  if (at == std::string::npos) {
    // the longest end that may still begin an item stays
    std::size_t keep = std::min(kItem.size() - 1, buffer_.size());
    while (keep > 0 && !buffer_.ends_with(kItem.substr(0, keep)))
      --keep;
    drop = buffer_.size() - keep;
  }
  const auto dropped = std::string_view(buffer_).substr(0, drop);
  if (!discarding_ && std::any_of(dropped.begin(), dropped.end(), [](char c) { return !space(c); })) {
    ++skipped_;
    discarding_ = true;
  }
  buffer_.erase(0, drop);
  if (at != std::string::npos)
    discarding_ = false;
}

void MetadataReader::feed(std::span<const std::uint8_t> bytes, std::vector<MetadataItem>& items) {
  buffer_.append(reinterpret_cast<const char*>(bytes.data()), bytes.size());
  for (;;) {
    seek();
    if (buffer_.empty())
      return;
    std::size_t used = 0;
    MetadataItem item;
    switch (parse(used, item)) {
      case Parsed::More:
        return;
      case Parsed::Item:
        items.push_back(std::move(item));
        buffer_.erase(0, used);
        break;
      case Parsed::Malformed:
        // whatever follows up to the next item goes with it
        ++skipped_;
        discarding_ = true;
        buffer_.erase(0, 1);
        break;
      case Parsed::Oversize:
        ++skipped_;
        discarding_ = true;
        buffer_.erase(0, used);
        break;
    }
  }
}

void MetadataReader::reset() {
  buffer_.clear();
  discarding_ = false;
}

std::optional<MediaMetadata> MetadataTracker::item(const MetadataItem& item) {
  if (item.type == kTypeShairport) {
    switch (item.code) {
      case fourcc("mdst"):
        sequence_ = MediaMetadata{};
        sequenceHeld_ = false;
        sequenceCover_ = false;
        return std::nullopt;
      case fourcc("mden"): {
        if (!sequence_ || (!sequenceHeld_ && !sequenceCover_))
          return std::nullopt;
        auto next = std::move(*sequence_);
        sequence_.reset();
        // a dmap update is often partial, and a phone may send an empty one just after the cover. empty fields merge
        // into the current track; a field that actually changes names another track, whose omitted fields stay empty
        const bool anotherTrack = (!current_.title.empty() && !next.title.empty() && next.title != current_.title) ||
                                  (!current_.artist.empty() && !next.artist.empty() && next.artist != current_.artist) ||
                                  (!current_.album.empty() && !next.album.empty() && next.album != current_.album);
        if (!anotherTrack) {
          if (next.title.empty())
            next.title = current_.title;
          if (next.artist.empty())
            next.artist = current_.artist;
          if (next.album.empty())
            next.album = current_.album;
          if (!sequenceCover_)
            next.artworkId = current_.artworkId;
        } else if (!sequenceCover_) {
          artwork_.clear();
        }
        return set(std::move(next));
      }
      case fourcc("PICT"):
        return cover(item.data);
      case fourcc("pbeg"):
      case fourcc("pend"):
        return reset();
      default:
        return std::nullopt;
    }
  }
  if (item.type != kTypeCore)
    return std::nullopt;
  std::string MediaMetadata::*field = nullptr;
  switch (item.code) {
    case fourcc("minm"):
      field = &MediaMetadata::title;
      break;
    case fourcc("asar"):
      field = &MediaMetadata::artist;
      break;
    case fourcc("asal"):
      field = &MediaMetadata::album;
      break;
    default:
      return std::nullopt;
  }
  auto value = text::boundedUtf8(item.data, kMetadataTextBytes);
  if (sequence_) {
    (*sequence_).*field = std::move(value);
    sequenceHeld_ = true;
    return std::nullopt;
  }
  auto next = current_;
  next.*field = std::move(value);
  return set(std::move(next));
}

std::optional<MediaMetadata> MetadataTracker::cover(const std::string& data) {
  std::string id;
  if (knownImage(data)) {
    std::uint64_t hash = 14695981039346656037ULL;
    for (const char c : data) {
      hash ^= static_cast<std::uint8_t>(c);
      hash *= 1099511628211ULL;
    }
    static constexpr std::string_view kHex = "0123456789abcdef";
    id.resize(16);
    for (std::size_t i = 0; i < 16; ++i)
      id[i] = kHex[(hash >> (60 - 4 * i)) & 0xF];
    artwork_ = data;
  } else {
    artwork_.clear();
  }
  if (sequence_) {
    sequence_->artworkId = std::move(id);
    sequenceCover_ = true;
    return std::nullopt;
  }
  auto next = current_;
  next.artworkId = std::move(id);
  return set(std::move(next));
}

std::optional<MediaMetadata> MetadataTracker::reset() {
  sequence_.reset();
  sequenceHeld_ = false;
  sequenceCover_ = false;
  artwork_.clear();
  return set({});
}

bool knownImage(std::string_view bytes) {
  static constexpr std::string_view kJpeg = "\xFF\xD8\xFF";
  static constexpr std::string_view kPng = "\x89PNG\r\n\x1A\n";
  return bytes.starts_with(kJpeg) || bytes.starts_with(kPng);
}

namespace {

std::optional<std::uint32_t> rtp(std::string_view text) {
  if (text.empty() || text.size() > 10)
    return std::nullopt;
  std::uint64_t value = 0;
  for (const char c : text) {
    if (c < '0' || c > '9')
      return std::nullopt;
    value = value * 10 + static_cast<std::uint64_t>(c - '0');
  }
  if (value > 0xFFFFFFFFULL)
    return std::nullopt;
  return static_cast<std::uint32_t>(value);
}

constexpr std::uint64_t kRtpRate = 44100;

}  // namespace

std::optional<Progress> progress(const MetadataItem& item) {
  if (item.type != kTypeShairport || item.code != fourcc("prgr"))
    return std::nullopt;
  const std::string_view text = item.data;
  const auto first = text.find('/');
  const auto second = first == std::string_view::npos ? first : text.find('/', first + 1);
  if (second == std::string_view::npos)
    return std::nullopt;
  const auto start = rtp(text.substr(0, first));
  const auto current = rtp(text.substr(first + 1, second - first - 1));
  const auto end = rtp(text.substr(second + 1));
  if (!start || !current || !end)
    return std::nullopt;
  // rtp time wraps: the differences are taken modulo 2^32
  const std::uint64_t length = static_cast<std::uint32_t>(*end - *start);
  const std::uint64_t played = static_cast<std::uint32_t>(*current - *start);
  Progress out{.positionMs = played * 1000 / kRtpRate, .durationMs = length * 1000 / kRtpRate};
  if (out.durationMs > 0)
    out.positionMs = std::min(out.positionMs, out.durationMs);
  return out;
}

std::optional<MediaMetadata> MetadataTracker::set(MediaMetadata next) {
  if (next == current_)
    return std::nullopt;
  current_ = std::move(next);
  return current_;
}

}  // namespace cadence::audiod::airplay
