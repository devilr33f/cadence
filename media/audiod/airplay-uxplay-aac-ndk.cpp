// aac-lc and aac-eld through the ndk's AMediaCodec (android, api 30): the platform's own decoder, as
// android-airplay-server decodes them, synchronously on the stream's thread with short waits
#include "audiod/airplay-uxplay-decoder.h"

#include <media/NdkMediaCodec.h>
#include <media/NdkMediaFormat.h>

#include <cstring>

namespace cadence::audiod::uxplay {

namespace {

constexpr const char* kMime = "audio/mp4a-latm";
// iso 14496-3 object types
constexpr std::int32_t kProfileLc = 2;
constexpr std::int32_t kProfileEld = 39;
// a packet's input waits at most this long for a buffer, its output for the frame it makes
constexpr std::int64_t kInputWaitUs = 10'000;
constexpr std::int64_t kOutputWaitUs = 5'000;

class NdkAacDecoder final : public AudioDecoder {
 public:
  ~NdkAacDecoder() override {
    if (codec_ != nullptr) {
      AMediaCodec_stop(codec_);
      AMediaCodec_delete(codec_);
    }
  }

  bool open(AudioCodec codec) {
    codec_ = AMediaCodec_createDecoderByType(kMime);
    if (codec_ == nullptr)
      return false;
    AMediaFormat* format = AMediaFormat_new();
    if (format == nullptr)
      return false;
    const auto config = aacConfig(codec);
    AMediaFormat_setString(format, AMEDIAFORMAT_KEY_MIME, kMime);
    AMediaFormat_setInt32(format, AMEDIAFORMAT_KEY_SAMPLE_RATE, static_cast<std::int32_t>(kStreamRate));
    AMediaFormat_setInt32(format, AMEDIAFORMAT_KEY_CHANNEL_COUNT, static_cast<std::int32_t>(kStreamChannels));
    AMediaFormat_setInt32(format, AMEDIAFORMAT_KEY_AAC_PROFILE, codec == AudioCodec::AacEld ? kProfileEld : kProfileLc);
    AMediaFormat_setInt32(format, AMEDIAFORMAT_KEY_IS_ADTS, 0);
    AMediaFormat_setBuffer(format, "csd-0", config.data(), config.size());
    const bool ok = AMediaCodec_configure(codec_, format, nullptr, nullptr, 0) == AMEDIA_OK &&
                    AMediaCodec_start(codec_) == AMEDIA_OK;
    AMediaFormat_delete(format);
    return ok;
  }

  bool decode(std::span<const std::uint8_t> packet, std::vector<float>& out) override {
    const ssize_t input = AMediaCodec_dequeueInputBuffer(codec_, kInputWaitUs);
    if (input < 0)
      return false;
    std::size_t capacity = 0;
    std::uint8_t* buffer = AMediaCodec_getInputBuffer(codec_, static_cast<std::size_t>(input), &capacity);
    if (buffer == nullptr || capacity < packet.size())
      return false;
    std::memcpy(buffer, packet.data(), packet.size());
    if (AMediaCodec_queueInputBuffer(codec_, static_cast<std::size_t>(input), 0, packet.size(), pts_++, 0) != AMEDIA_OK)
      return false;
    bool any = false;
    for (;;) {
      AMediaCodecBufferInfo info{};
      const ssize_t output = AMediaCodec_dequeueOutputBuffer(codec_, &info, any ? 0 : kOutputWaitUs);
      if (output == AMEDIACODEC_INFO_OUTPUT_FORMAT_CHANGED) {
        readFormat();
        continue;
      }
      if (output == AMEDIACODEC_INFO_OUTPUT_BUFFERS_CHANGED)
        continue;
      if (output < 0)
        return any;
      std::size_t size = 0;
      const std::uint8_t* data = AMediaCodec_getOutputBuffer(codec_, static_cast<std::size_t>(output), &size);
      if (data != nullptr && info.size > 0)
        any = append(data + info.offset, static_cast<std::size_t>(info.size), out) || any;
      AMediaCodec_releaseOutputBuffer(codec_, static_cast<std::size_t>(output), false);
    }
  }

  void reset() override { AMediaCodec_flush(codec_); }

 private:
  void readFormat() {
    AMediaFormat* format = AMediaCodec_getOutputFormat(codec_);
    if (format == nullptr)
      return;
    std::int32_t channels = 0;
    std::int32_t rate = 0;
    if (AMediaFormat_getInt32(format, AMEDIAFORMAT_KEY_CHANNEL_COUNT, &channels))
      channels_ = channels;
    if (AMediaFormat_getInt32(format, AMEDIAFORMAT_KEY_SAMPLE_RATE, &rate))
      rate_ = rate;
    AMediaFormat_delete(format);
  }

  // s16 interleaved, the decoder's default pcm encoding; mono on both channels
  bool append(const std::uint8_t* data, std::size_t bytes, std::vector<float>& out) const {
    if (rate_ != static_cast<std::int32_t>(kStreamRate) || channels_ < 1)
      return false;
    const auto step = static_cast<std::size_t>(channels_);
    const auto frames = bytes / (2 * step);
    const auto right = channels_ > 1 ? 1U : 0U;
    for (std::size_t i = 0; i < frames; ++i) {
      std::int16_t l = 0;
      std::int16_t r = 0;
      std::memcpy(&l, data + 2 * (i * step), 2);
      std::memcpy(&r, data + 2 * (i * step + right), 2);
      out.push_back(static_cast<float>(l) / 32768.0f);
      out.push_back(static_cast<float>(r) / 32768.0f);
    }
    return frames > 0;
  }

  AMediaCodec* codec_ = nullptr;
  std::uint64_t pts_ = 0;
  std::int32_t channels_ = static_cast<std::int32_t>(kStreamChannels);
  std::int32_t rate_ = static_cast<std::int32_t>(kStreamRate);
};

}  // namespace

std::unique_ptr<AudioDecoder> makeAacDecoder(AudioCodec codec) {
  if (codec != AudioCodec::AacLc && codec != AudioCodec::AacEld)
    return nullptr;
  auto decoder = std::make_unique<NdkAacDecoder>();
  if (!decoder->open(codec))
    return nullptr;
  return decoder;
}

}  // namespace cadence::audiod::uxplay
