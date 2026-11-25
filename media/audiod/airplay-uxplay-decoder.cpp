#include "audiod/airplay-uxplay-decoder.h"

#include "ALACBitUtilities.h"
#include "ALACDecoder.h"

#include <array>
#include <cstring>
#include <initializer_list>

namespace cadence::audiod::uxplay {

namespace {

constexpr std::uint8_t kCtPcm = 1;
constexpr std::uint8_t kCtAlac = 2;
constexpr std::uint8_t kCtAacLc = 4;
constexpr std::uint8_t kCtAacEld = 8;

constexpr float kS16Scale = 1.0f / 32768.0f;

// object type 2 (lc), 44.1 kHz (index 4), 2 channels
constexpr std::array<std::uint8_t, 2> kAacLcConfig{0x12, 0x10};
// object type 39 (eld, escaped as 31 + 7), 44.1 kHz, 2 channels, 480 frames, no sbr
constexpr std::array<std::uint8_t, 4> kAacEldConfig{0xf8, 0xe8, 0x50, 0x00};

void putBe32(std::vector<std::uint8_t>& out, std::uint32_t value) {
  for (int shift = 24; shift >= 0; shift -= 8)
    out.push_back(static_cast<std::uint8_t>(value >> shift));
}

class AlacDecoder final : public AudioDecoder {
 public:
  explicit AlacDecoder(std::uint16_t framesPerPacket)
      : frames_(framesPerPacket != 0 ? framesPerPacket : kAlacFramesPerPacket),
        samples_(static_cast<std::size_t>(frames_) * kStreamChannels) {}

  bool init() {
    auto cookie = alacCookie(static_cast<std::uint16_t>(frames_));
    return decoder_.Init(cookie.data(), static_cast<uint32_t>(cookie.size())) == 0;
  }

  bool decode(std::span<const std::uint8_t> packet, std::vector<float>& out) override {
    // the decoder reads its bits in place and never writes them; the copy keeps it off the sender's buffer, with
    // slack for its look-ahead reads at the end
    scratch_.assign(packet.begin(), packet.end());
    scratch_.resize(packet.size() + 8, 0);
    BitBuffer bits;
    BitBufferInit(&bits, scratch_.data(), static_cast<uint32_t>(packet.size()));
    uint32_t decoded = 0;
    if (decoder_.Decode(&bits, reinterpret_cast<uint8_t*>(samples_.data()), frames_, kStreamChannels, &decoded) != 0 ||
        decoded > frames_)
      return false;
    const auto count = static_cast<std::size_t>(decoded) * kStreamChannels;
    out.reserve(out.size() + count);
    for (std::size_t i = 0; i < count; ++i)
      out.push_back(static_cast<float>(samples_[i]) * kS16Scale);
    return true;
  }

 private:
  uint32_t frames_;
  ALACDecoder decoder_;
  std::vector<std::int16_t> samples_;
  std::vector<std::uint8_t> scratch_;
};

// l16: big-endian signed 16-bit stereo
class PcmDecoder final : public AudioDecoder {
 public:
  bool decode(std::span<const std::uint8_t> packet, std::vector<float>& out) override {
    const auto samples = packet.size() / 2;
    if (samples % kStreamChannels != 0)
      return false;
    for (std::size_t i = 0; i < samples; ++i) {
      const auto value = static_cast<std::int16_t>(static_cast<std::uint16_t>(packet[2 * i] << 8 | packet[2 * i + 1]));
      out.push_back(static_cast<float>(value) * kS16Scale);
    }
    return true;
  }
};

}  // namespace

AudioCodec codecOf(std::uint8_t ct) {
  switch (ct) {
    case kCtPcm:
      return AudioCodec::Pcm;
    case kCtAlac:
      return AudioCodec::Alac;
    case kCtAacLc:
      return AudioCodec::AacLc;
    case kCtAacEld:
      return AudioCodec::AacEld;
    default:
      return AudioCodec::Unknown;
  }
}

std::string_view codecToken(AudioCodec codec) {
  switch (codec) {
    case AudioCodec::Pcm:
      return "pcm";
    case AudioCodec::Alac:
      return "alac";
    case AudioCodec::AacLc:
      return "aac_lc";
    case AudioCodec::AacEld:
      return "aac_eld";
    case AudioCodec::Unknown:
      break;
  }
  return "unknown";
}

std::span<const std::uint8_t> aacConfig(AudioCodec codec) {
  if (codec == AudioCodec::AacEld)
    return kAacEldConfig;
  return kAacLcConfig;
}

std::vector<std::uint8_t> alacCookie(std::uint16_t framesPerPacket) {
  std::vector<std::uint8_t> cookie;
  putBe32(cookie, framesPerPacket != 0 ? framesPerPacket : kAlacFramesPerPacket);
  // compatible version, bit depth, pb, mb, kb, channels
  for (const std::uint8_t byte : std::initializer_list<std::uint8_t>{0, 16, 40, 10, 14, 2})
    cookie.push_back(byte);
  // max run
  cookie.push_back(0x00);
  cookie.push_back(0xff);
  // max frame bytes, average bit rate: unknown
  putBe32(cookie, 0);
  putBe32(cookie, 0);
  putBe32(cookie, kStreamRate);
  return cookie;
}

std::unique_ptr<AudioDecoder> makeAlacDecoder(std::uint16_t framesPerPacket) {
  auto decoder = std::make_unique<AlacDecoder>(framesPerPacket);
  if (!decoder->init())
    return nullptr;
  return decoder;
}

std::unique_ptr<AudioDecoder> makePcmDecoder() { return std::make_unique<PcmDecoder>(); }

std::unique_ptr<AudioDecoder> makeDecoder(AudioCodec codec, std::uint16_t framesPerPacket) {
  switch (codec) {
    case AudioCodec::Alac:
      return makeAlacDecoder(framesPerPacket);
    case AudioCodec::Pcm:
      return makePcmDecoder();
    case AudioCodec::AacLc:
    case AudioCodec::AacEld:
      return makeAacDecoder(codec);
    case AudioCodec::Unknown:
      break;
  }
  return nullptr;
}

}  // namespace cadence::audiod::uxplay
