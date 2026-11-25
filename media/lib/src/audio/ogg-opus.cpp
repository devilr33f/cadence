#include "audio/ogg-opus.h"

#include <opus.h>

#include <algorithm>
#include <array>
#include <cstdlib>
#include <cstring>
#include <memory>
#include <utility>

namespace cadence {

namespace {

constexpr int kRate = 48000;
// libopus' longest frame, 120 ms at 48 kHz
constexpr int kMaxFrameSamples = 5760;
constexpr std::size_t kPageHeaderBytes = 27;
constexpr std::size_t kCrcOffset = 22;
constexpr std::uint8_t kContinued = 0x01;
constexpr std::uint8_t kFirstPage = 0x02;
constexpr std::size_t kOpusHeadBytes = 19;

std::array<std::uint32_t, 256> crcTable() {
  std::array<std::uint32_t, 256> table{};
  for (std::uint32_t i = 0; i < 256; ++i) {
    std::uint32_t r = i << 24;
    for (int bit = 0; bit < 8; ++bit)
      r = (r & 0x80000000U) != 0 ? (r << 1) ^ 0x04c11db7U : r << 1;
    table[i] = r;
  }
  return table;
}

std::uint32_t le32(const std::uint8_t* p) {
  return static_cast<std::uint32_t>(p[0]) | static_cast<std::uint32_t>(p[1]) << 8 |
         static_cast<std::uint32_t>(p[2]) << 16 | static_cast<std::uint32_t>(p[3]) << 24;
}

std::int64_t le64(const std::uint8_t* p) {
  std::uint64_t v = 0;
  for (int i = 7; i >= 0; --i)
    v = v << 8 | p[i];
  return static_cast<std::int64_t>(v);
}

struct DecoderDeleter {
  void operator()(::OpusDecoder* decoder) const { opus_decoder_destroy(decoder); }
};

// the packets of a stream, in order, and the granule position of its last page
struct Packets {
  std::vector<std::vector<std::uint8_t>> list;
  std::int64_t lastGranule = -1;
};

Result<Packets, PromptErrorCode> readPages(std::span<const std::uint8_t> file) {
  Packets out;
  std::vector<std::uint8_t> partial;
  bool open = false;
  std::uint32_t serial = 0;
  std::uint32_t nextSeq = 0;
  std::size_t at = 0;
  if (file.size() < 4 || std::memcmp(file.data(), "OggS", 4) != 0)
    return PromptErrorCode::NotOgg;
  while (at < file.size()) {
    const auto rest = file.subspan(at);
    if (rest.size() < kPageHeaderBytes || std::memcmp(rest.data(), "OggS", 4) != 0 || rest[4] != 0)
      return PromptErrorCode::BadPage;
    const std::uint8_t flags = rest[5];
    const std::size_t segments = rest[26];
    if (rest.size() < kPageHeaderBytes + segments)
      return PromptErrorCode::BadPage;
    std::size_t body = 0;
    for (std::size_t i = 0; i < segments; ++i)
      body += rest[kPageHeaderBytes + i];
    const std::size_t pageBytes = kPageHeaderBytes + segments + body;
    if (rest.size() < pageBytes)
      return PromptErrorCode::BadPage;
    std::vector<std::uint8_t> page(rest.begin(), rest.begin() + static_cast<std::ptrdiff_t>(pageBytes));
    const std::uint32_t stored = le32(page.data() + kCrcOffset);
    std::fill_n(page.begin() + kCrcOffset, 4, std::uint8_t{0});
    if (oggCrc(page) != stored)
      return PromptErrorCode::BadCrc;
    const std::uint32_t pageSerial = le32(rest.data() + 14);
    const std::uint32_t seq = le32(rest.data() + 18);
    if (!open) {
      if ((flags & kFirstPage) == 0)
        return PromptErrorCode::BadPage;
      open = true;
      serial = pageSerial;
    } else if (pageSerial != serial || (flags & kFirstPage) != 0) {
      // one logical stream only
      return PromptErrorCode::BadPage;
    }
    // a lost page would splice two packets' bytes
    if (seq != nextSeq)
      return PromptErrorCode::BadPage;
    nextSeq = seq + 1;
    if (((flags & kContinued) != 0) != !partial.empty())
      return PromptErrorCode::BadPage;
    const std::uint8_t* data = rest.data() + kPageHeaderBytes + segments;
    for (std::size_t i = 0; i < segments; ++i) {
      const std::uint8_t lace = rest[kPageHeaderBytes + i];
      partial.insert(partial.end(), data, data + lace);
      data += lace;
      if (lace < 255)
        out.list.push_back(std::exchange(partial, {}));
    }
    const std::int64_t granule = le64(rest.data() + 6);
    if (granule != -1)
      out.lastGranule = granule;
    at += pageBytes;
  }
  // a packet cut off by the end of the file
  if (!partial.empty())
    return PromptErrorCode::BadPage;
  return out;
}

}  // namespace

std::uint32_t oggCrc(std::span<const std::uint8_t> bytes) {
  static const auto table = crcTable();
  std::uint32_t crc = 0;
  for (const auto b : bytes)
    crc = (crc << 8) ^ table[((crc >> 24) & 0xffU) ^ b];
  return crc;
}

std::string_view promptErrorName(PromptErrorCode code) {
  switch (code) {
    case PromptErrorCode::TooLarge:
      return "too_large";
    case PromptErrorCode::NotOgg:
      return "not_ogg";
    case PromptErrorCode::BadPage:
      return "bad_page";
    case PromptErrorCode::BadCrc:
      return "bad_crc";
    case PromptErrorCode::NotOpus:
      return "not_opus";
    case PromptErrorCode::Channels:
      return "channels";
    case PromptErrorCode::Mapping:
      return "mapping";
    case PromptErrorCode::NoAudio:
      return "no_audio";
    case PromptErrorCode::InvalidPacket:
      return "invalid_packet";
    case PromptErrorCode::TooLong:
      return "too_long";
    case PromptErrorCode::DecoderFailed:
      return "decoder_failed";
  }
  return "unknown";
}

Result<std::vector<std::int16_t>, PromptErrorCode> decodeOggOpus(std::span<const std::uint8_t> file) {
  if (file.size() > kMaxPromptFileBytes)
    return PromptErrorCode::TooLarge;
  auto pages = readPages(file);
  if (!pages.ok())
    return pages.error();
  const auto& packets = pages.value().list;
  if (packets.size() < 2)
    return PromptErrorCode::NotOpus;
  const auto& head = packets[0];
  if (head.size() < kOpusHeadBytes || std::memcmp(head.data(), "OpusHead", 8) != 0 || (head[8] & 0xf0) != 0)
    return PromptErrorCode::NotOpus;
  if (head[9] != 1)
    return PromptErrorCode::Channels;
  if (head[18] != 0)
    return PromptErrorCode::Mapping;
  const std::size_t preSkip = static_cast<std::size_t>(head[10]) | static_cast<std::size_t>(head[11]) << 8;
  const auto gain = static_cast<std::int16_t>(static_cast<std::uint16_t>(head[16]) | static_cast<std::uint16_t>(head[17]) << 8);
  if (packets[1].size() < 8 || std::memcmp(packets[1].data(), "OpusTags", 8) != 0)
    return PromptErrorCode::NotOpus;

  int error = OPUS_OK;
  std::unique_ptr<::OpusDecoder, DecoderDeleter> decoder(opus_decoder_create(kRate, 1, &error));
  if (error != OPUS_OK || !decoder)
    return PromptErrorCode::DecoderFailed;
  if (gain != 0 && opus_decoder_ctl(decoder.get(), OPUS_SET_GAIN(gain)) != OPUS_OK)
    return PromptErrorCode::DecoderFailed;

  std::vector<std::int16_t> pcm;
  std::array<opus_int16, kMaxFrameSamples> frame{};
  for (std::size_t i = 2; i < packets.size(); ++i) {
    const auto& packet = packets[i];
    if (packet.empty())
      return PromptErrorCode::InvalidPacket;
    const int n = opus_decode(decoder.get(), packet.data(), static_cast<opus_int32>(packet.size()), frame.data(),
                              kMaxFrameSamples, 0);
    if (n < 0)
      return PromptErrorCode::InvalidPacket;
    pcm.insert(pcm.end(), frame.begin(), frame.begin() + n);
    if (pcm.size() > kMaxPromptSamples + preSkip + kMaxFrameSamples)
      return PromptErrorCode::TooLong;
  }
  // the granule position counts the samples the stream holds, the pre-skip included
  if (const auto last = pages.value().lastGranule; last >= 0 && static_cast<std::uint64_t>(last) < pcm.size())
    pcm.resize(static_cast<std::size_t>(last));
  if (pcm.size() <= preSkip)
    return PromptErrorCode::NoAudio;
  pcm.erase(pcm.begin(), pcm.begin() + static_cast<std::ptrdiff_t>(preSkip));
  if (pcm.size() > kMaxPromptSamples)
    return PromptErrorCode::TooLong;

  int peak = 0;
  for (const auto s : pcm)
    peak = std::max(peak, std::abs(static_cast<int>(s)));
  if (peak > kMaxPromptPeak) {
    for (auto& s : pcm)
      s = static_cast<std::int16_t>(static_cast<int>(s) * kMaxPromptPeak / peak);
  }
  return pcm;
}

}  // namespace cadence
