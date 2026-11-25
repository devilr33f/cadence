#include "audio/opus-codec.h"

#include "text.h"

#include <opus.h>

#include <new>
#include <optional>
#include <string>
#include <utility>

namespace cadence {

namespace {

constexpr int kEncodeSampleRate = kOpusSampleRate;
constexpr int kBitrate = 24000;
// libopus' recommended output size; one 20 ms packet never comes close
constexpr std::size_t kMaxEncodedBytes = 4000;
static_assert(kMaxEncodedBytes <= kMaxAudioFrameBytes);

const char* summary(OpusErrorCode code) {
  switch (code) {
    case OpusErrorCode::CreateFailed:
      return "opus codec failed to start";
    case OpusErrorCode::ConfigureFailed:
      return "opus encoder setting refused";
    case OpusErrorCode::NotOpen:
      return "opus encoder not open";
    case OpusErrorCode::EmptyPacket:
      return "opus packet is empty";
    case OpusErrorCode::PacketTooLarge:
      return "opus packet too large";
    case OpusErrorCode::InvalidPacket:
      return "opus packet invalid";
    case OpusErrorCode::WrongDuration:
      return "opus packet is not 20 ms";
    case OpusErrorCode::WrongFrameSize:
      return "opus frame size wrong";
    case OpusErrorCode::EncodeFailed:
      return "opus encode failed";
    case OpusErrorCode::ResetFailed:
      return "opus decoder reset failed";
  }
  return "opus failed";
}

std::string opusText(const char* step, int error) { return std::string(step).append(": ").append(opus_strerror(error)); }

}  // namespace

std::string describe(const OpusError& error) {
  std::string message = summary(error.code);
  if (!error.detail.empty())
    message.append(": ").append(error.detail);
  return text::boundedMessage(message.c_str());
}

struct OpusDecoder::State {
  explicit State(::OpusDecoder* created) : decoder(created) {}
  ~State() { opus_decoder_destroy(decoder); }

  State(const State&) = delete;
  State& operator=(const State&) = delete;

  ::OpusDecoder* decoder;
};

OpusDecoder::OpusDecoder() {
  int error = OPUS_OK;
  auto* decoder = opus_decoder_create(kDecodeSampleRate, 1, &error);
  // the rate and channel count are constants libopus accepts, so only allocation can fail
  if (decoder == nullptr)
    throw std::bad_alloc();
  state_ = std::make_unique<State>(decoder);
}

OpusDecoder::~OpusDecoder() = default;

Result<DecodedFrame, OpusError> OpusDecoder::decode(std::span<const std::uint8_t> packet) {
  if (packet.empty())
    return OpusError{OpusErrorCode::EmptyPacket, {}};
  if (packet.size() > kMaxAudioFrameBytes)
    return OpusError{OpusErrorCode::PacketTooLarge,
                     std::to_string(packet.size()) + " bytes, limit " + std::to_string(kMaxAudioFrameBytes)};

  const auto length = static_cast<opus_int32>(packet.size());
  const int samples = opus_packet_get_nb_samples(packet.data(), length, kDecodeSampleRate);
  if (samples < 0)
    return OpusError{OpusErrorCode::InvalidPacket, opusText("table of contents", samples)};
  if (samples != static_cast<int>(kDecodedFrameSamples))
    return OpusError{OpusErrorCode::WrongDuration,
                     std::to_string(samples) + " samples at 48 kHz, need " + std::to_string(kDecodedFrameSamples)};

  DecodedFrame frame{};
  const int decoded =
      opus_decode(state_->decoder, packet.data(), length, frame.data(), static_cast<int>(frame.size()), 0);
  if (decoded < 0)
    return OpusError{OpusErrorCode::InvalidPacket, opusText("decode", decoded)};
  if (decoded != static_cast<int>(kDecodedFrameSamples))
    return OpusError{OpusErrorCode::WrongDuration,
                     "decoded " + std::to_string(decoded) + " samples, need " + std::to_string(kDecodedFrameSamples)};
  return frame;
}

std::optional<OpusError> OpusDecoder::reset() {
  if (const int error = opus_decoder_ctl(state_->decoder, OPUS_RESET_STATE); error != OPUS_OK)
    return OpusError{OpusErrorCode::ResetFailed, opusText("reset", error)};
  return std::nullopt;
}

struct OpusEncoder::State {
  explicit State(::OpusEncoder* created) : encoder(created) {}
  ~State() { opus_encoder_destroy(encoder); }

  State(const State&) = delete;
  State& operator=(const State&) = delete;

  ::OpusEncoder* encoder;
};

OpusEncoder::OpusEncoder() = default;

OpusEncoder::~OpusEncoder() = default;

Result<OpusEncoderSettings, OpusError> OpusEncoder::open(const OpusEncoderConfig& config) {
  state_.reset();
  int error = OPUS_OK;
  auto* created = opus_encoder_create(kEncodeSampleRate, 1, OPUS_APPLICATION_VOIP, &error);
  if (created == nullptr)
    return OpusError{OpusErrorCode::CreateFailed, opusText("create", error)};
  auto state = std::make_unique<State>(created);
  auto* encoder = state->encoder;

  const auto refused = [](const char* setting, int value, int result) {
    return OpusError{OpusErrorCode::ConfigureFailed, opusText((std::string(setting) + " " + std::to_string(value)).c_str(), result)};
  };
  if (const int result = opus_encoder_ctl(encoder, OPUS_SET_BITRATE(kBitrate)); result != OPUS_OK)
    return refused("bitrate", kBitrate, result);
  if (const int result = opus_encoder_ctl(encoder, OPUS_SET_COMPLEXITY(config.complexity)); result != OPUS_OK)
    return refused("complexity", config.complexity, result);
  if (const int result = opus_encoder_ctl(encoder, OPUS_SET_DTX(0)); result != OPUS_OK)
    return refused("dtx", 0, result);
  if (const int result = opus_encoder_ctl(encoder, OPUS_SET_INBAND_FEC(0)); result != OPUS_OK)
    return refused("inband fec", 0, result);

  OpusEncoderSettings settings;
  opus_int32 sampleRate = 0;
  opus_int32 application = 0;
  opus_int32 bitrate = 0;
  opus_int32 complexity = 0;
  opus_int32 dtx = 0;
  opus_int32 inbandFec = 0;
  if (opus_encoder_ctl(encoder, OPUS_GET_SAMPLE_RATE(&sampleRate)) != OPUS_OK ||
      opus_encoder_ctl(encoder, OPUS_GET_APPLICATION(&application)) != OPUS_OK ||
      opus_encoder_ctl(encoder, OPUS_GET_BITRATE(&bitrate)) != OPUS_OK ||
      opus_encoder_ctl(encoder, OPUS_GET_COMPLEXITY(&complexity)) != OPUS_OK ||
      opus_encoder_ctl(encoder, OPUS_GET_DTX(&dtx)) != OPUS_OK ||
      opus_encoder_ctl(encoder, OPUS_GET_INBAND_FEC(&inbandFec)) != OPUS_OK)
    return OpusError{OpusErrorCode::ConfigureFailed, "settings could not be read back"};
  settings.sampleRate = sampleRate;
  settings.voip = application == OPUS_APPLICATION_VOIP;
  settings.bitrate = bitrate;
  settings.complexity = complexity;
  settings.dtx = dtx != 0;
  settings.inbandFec = inbandFec != 0;

  state_ = std::move(state);
  return settings;
}

Result<std::vector<std::uint8_t>, OpusError> OpusEncoder::encode(std::span<const std::int16_t> frame) {
  if (!state_)
    return OpusError{OpusErrorCode::NotOpen, {}};
  if (frame.size() != kEncoderFrameSamples)
    return OpusError{OpusErrorCode::WrongFrameSize,
                     std::to_string(frame.size()) + " samples, need " + std::to_string(kEncoderFrameSamples)};

  std::vector<std::uint8_t> packet(kMaxEncodedBytes);
  const opus_int32 bytes = opus_encode(state_->encoder, frame.data(), static_cast<int>(frame.size()), packet.data(),
                                       static_cast<opus_int32>(packet.size()));
  if (bytes < 0)
    return OpusError{OpusErrorCode::EncodeFailed, opusText("encode", bytes)};
  packet.resize(static_cast<std::size_t>(bytes));
  return packet;
}

}  // namespace cadence
