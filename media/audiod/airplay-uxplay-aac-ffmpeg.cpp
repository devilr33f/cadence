// aac-lc and aac-eld through ffmpeg's aac decoder (linux: the system's libavcodec, lgpl)
#include "audiod/airplay-uxplay-decoder.h"

extern "C" {
#include <libavcodec/avcodec.h>
#include <libavutil/channel_layout.h>
#include <libavutil/mem.h>
}

#include <cstring>

namespace cadence::audiod::uxplay {

namespace {

class FfmpegAacDecoder final : public AudioDecoder {
 public:
  ~FfmpegAacDecoder() override {
    av_frame_free(&frame_);
    av_packet_free(&packet_);
    avcodec_free_context(&context_);
  }

  bool open(AudioCodec codec) {
    const AVCodec* aac = avcodec_find_decoder(AV_CODEC_ID_AAC);
    if (aac == nullptr)
      return false;
    context_ = avcodec_alloc_context3(aac);
    packet_ = av_packet_alloc();
    frame_ = av_frame_alloc();
    if (context_ == nullptr || packet_ == nullptr || frame_ == nullptr)
      return false;
    const auto config = aacConfig(codec);
    context_->extradata = static_cast<std::uint8_t*>(av_mallocz(config.size() + AV_INPUT_BUFFER_PADDING_SIZE));
    if (context_->extradata == nullptr)
      return false;
    std::memcpy(context_->extradata, config.data(), config.size());
    context_->extradata_size = static_cast<int>(config.size());
    context_->sample_rate = static_cast<int>(kStreamRate);
    av_channel_layout_default(&context_->ch_layout, static_cast<int>(kStreamChannels));
    return avcodec_open2(context_, aac, nullptr) == 0;
  }

  bool decode(std::span<const std::uint8_t> data, std::vector<float>& out) override {
    if (av_new_packet(packet_, static_cast<int>(data.size())) != 0)
      return false;
    std::memcpy(packet_->data, data.data(), data.size());
    const int sent = avcodec_send_packet(context_, packet_);
    av_packet_unref(packet_);
    if (sent != 0)
      return false;
    bool any = false;
    while (avcodec_receive_frame(context_, frame_) == 0) {
      any = append(out) || any;
      av_frame_unref(frame_);
    }
    return any;
  }

  void reset() override { avcodec_flush_buffers(context_); }

 private:
  // the frame as interleaved stereo; a mono frame on both channels
  bool append(std::vector<float>& out) const {
    const int channels = frame_->ch_layout.nb_channels;
    if (frame_->sample_rate != static_cast<int>(kStreamRate) || channels < 1 || frame_->nb_samples <= 0)
      return false;
    const auto frames = static_cast<std::size_t>(frame_->nb_samples);
    const auto right = channels > 1 ? 1 : 0;
    out.reserve(out.size() + frames * kStreamChannels);
    switch (frame_->format) {
      case AV_SAMPLE_FMT_FLTP: {
        const auto* l = reinterpret_cast<const float*>(frame_->extended_data[0]);
        const auto* r = reinterpret_cast<const float*>(frame_->extended_data[right]);
        for (std::size_t i = 0; i < frames; ++i) {
          out.push_back(l[i]);
          out.push_back(r[i]);
        }
        return true;
      }
      case AV_SAMPLE_FMT_FLT: {
        const auto* samples = reinterpret_cast<const float*>(frame_->extended_data[0]);
        const auto step = static_cast<std::size_t>(channels);
        for (std::size_t i = 0; i < frames; ++i) {
          out.push_back(samples[i * step]);
          out.push_back(samples[i * step + static_cast<std::size_t>(right)]);
        }
        return true;
      }
      case AV_SAMPLE_FMT_S16P: {
        const auto* l = reinterpret_cast<const std::int16_t*>(frame_->extended_data[0]);
        const auto* r = reinterpret_cast<const std::int16_t*>(frame_->extended_data[right]);
        for (std::size_t i = 0; i < frames; ++i) {
          out.push_back(static_cast<float>(l[i]) / 32768.0f);
          out.push_back(static_cast<float>(r[i]) / 32768.0f);
        }
        return true;
      }
      default:
        return false;
    }
  }

  AVCodecContext* context_ = nullptr;
  AVPacket* packet_ = nullptr;
  AVFrame* frame_ = nullptr;
};

}  // namespace

std::unique_ptr<AudioDecoder> makeAacDecoder(AudioCodec codec) {
  if (codec != AudioCodec::AacLc && codec != AudioCodec::AacEld)
    return nullptr;
  auto decoder = std::make_unique<FfmpegAacDecoder>();
  if (!decoder->open(codec))
    return nullptr;
  return decoder;
}

}  // namespace cadence::audiod::uxplay
