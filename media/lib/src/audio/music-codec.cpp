#include "audio/music-codec.h"

#include <opus.h>

#include <algorithm>
#include <cmath>

namespace cadence {

namespace {

constexpr std::size_t kFrameValues = kGroupFrameSamples * kGroupChannels;
// music at the group's bitrate on a speaker's cpu: 7 of 10 keeps most of the quality at a fraction of the cost
constexpr int kComplexity = 7;

std::int16_t toS16(float v) {
  const float scaled = std::clamp(v, -1.0f, 1.0f) * 32767.0f;
  return static_cast<std::int16_t>(std::lrint(scaled));
}

}  // namespace

struct MusicEncoder::State {
  OpusEncoder* opus = nullptr;
  ~State() {
    if (opus != nullptr)
      opus_encoder_destroy(opus);
  }
};

struct MusicDecoder::State {
  OpusDecoder* opus = nullptr;
  ~State() {
    if (opus != nullptr)
      opus_decoder_destroy(opus);
  }
};

MusicEncoder::MusicEncoder(MusicCodec codec, std::uint32_t kbps) : codec_(codec), state_(std::make_unique<State>()) {
  if (codec_ != MusicCodec::Opus)
    return;
  int error = OPUS_OK;
  state_->opus = opus_encoder_create(static_cast<opus_int32>(kGroupRate), static_cast<int>(kGroupChannels),
                                     OPUS_APPLICATION_AUDIO, &error);
  if (error != OPUS_OK) {
    state_->opus = nullptr;
    return;
  }
  setBitrate(kbps);
  opus_encoder_ctl(state_->opus, OPUS_SET_COMPLEXITY(kComplexity));
  opus_encoder_ctl(state_->opus, OPUS_SET_SIGNAL(OPUS_SIGNAL_MUSIC));
}

MusicEncoder::~MusicEncoder() = default;

void MusicEncoder::setBitrate(std::uint32_t kbps) {
  if (state_->opus == nullptr)
    return;
  const auto rate = static_cast<opus_int32>(std::clamp<std::uint32_t>(kbps == 0 ? kGroupDefaultKbps : kbps, 64, 510));
  opus_encoder_ctl(state_->opus, OPUS_SET_BITRATE(rate * 1000));
}

bool MusicEncoder::ok() const { return codec_ == MusicCodec::PcmS16 || state_->opus != nullptr; }

bool MusicEncoder::encode(std::span<const float> stereo, std::vector<std::uint8_t>& out) {
  if (stereo.size() != kFrameValues || !ok())
    return false;
  if (codec_ == MusicCodec::PcmS16) {
    out.resize(kFrameValues * 2);
    for (std::size_t i = 0; i < kFrameValues; ++i) {
      const auto s = static_cast<std::uint16_t>(toS16(stereo[i]));
      out[2 * i] = static_cast<std::uint8_t>(s & 0xff);
      out[2 * i + 1] = static_cast<std::uint8_t>(s >> 8);
    }
    return true;
  }
  out.resize(kGroupFrameBytes);
  const auto n = opus_encode_float(state_->opus, stereo.data(), static_cast<int>(kGroupFrameSamples), out.data(),
                                   static_cast<opus_int32>(out.size()));
  if (n < 0) {
    out.clear();
    return false;
  }
  out.resize(static_cast<std::size_t>(n));
  return true;
}

MusicDecoder::MusicDecoder(MusicCodec codec) : codec_(codec), state_(std::make_unique<State>()) {
  if (codec_ != MusicCodec::Opus)
    return;
  int error = OPUS_OK;
  state_->opus = opus_decoder_create(static_cast<opus_int32>(kGroupRate), static_cast<int>(kGroupChannels), &error);
  if (error != OPUS_OK)
    state_->opus = nullptr;
}

MusicDecoder::~MusicDecoder() = default;

bool MusicDecoder::ok() const { return codec_ == MusicCodec::PcmS16 || state_->opus != nullptr; }

bool MusicDecoder::decode(std::span<const std::uint8_t> data, std::span<float> out) {
  if (out.size() < kFrameValues || !ok() || data.empty())
    return false;
  if (codec_ == MusicCodec::PcmS16) {
    if (data.size() != kFrameValues * 2)
      return false;
    for (std::size_t i = 0; i < kFrameValues; ++i) {
      const auto s = static_cast<std::int16_t>(static_cast<std::uint16_t>(data[2 * i] | data[2 * i + 1] << 8));
      out[i] = static_cast<float>(s) / 32768.0f;
    }
    return true;
  }
  if (opus_packet_get_nb_samples(data.data(), static_cast<opus_int32>(data.size()),
                                 static_cast<opus_int32>(kGroupRate)) != static_cast<int>(kGroupFrameSamples))
    return false;
  const auto n = opus_decode_float(state_->opus, data.data(), static_cast<opus_int32>(data.size()), out.data(),
                                   static_cast<int>(kGroupFrameSamples), 0);
  return n == static_cast<int>(kGroupFrameSamples);
}

void MusicDecoder::conceal(std::span<float> out) {
  std::fill(out.begin(), out.end(), 0.0f);
  if (codec_ != MusicCodec::Opus || state_->opus == nullptr || out.size() < kFrameValues)
    return;
  // a failed concealment leaves the silence above
  if (opus_decode_float(state_->opus, nullptr, 0, out.data(), static_cast<int>(kGroupFrameSamples), 0) < 0)
    std::fill(out.begin(), out.end(), 0.0f);
}

void MusicDecoder::reset() {
  if (state_->opus != nullptr)
    opus_decoder_ctl(state_->opus, OPUS_RESET_STATE);
}

}  // namespace cadence
