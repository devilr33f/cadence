#pragma once

#include "audio/opus-frame.h"
#include "result.h"

#include <array>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <optional>
#include <span>
#include <string>
#include <vector>

namespace cadence {

// libopus resamples while decoding, so a 20 ms response packet comes out as 48 kHz mono for playback
inline constexpr int kDecodeSampleRate = 48000;
inline constexpr std::size_t kDecodedFrameSamples = static_cast<std::size_t>(kDecodeSampleRate * kOpusFrameMs / 1000);
using DecodedFrame = std::array<std::int16_t, kDecodedFrameSamples>;

// 20 ms of 16 kHz mono microphone audio per uplink packet
inline constexpr std::size_t kEncoderFrameSamples = static_cast<std::size_t>(kOpusSampleRate * kOpusFrameMs / 1000);

enum class OpusErrorCode {
  CreateFailed,
  ConfigureFailed,
  NotOpen,
  EmptyPacket,
  PacketTooLarge,
  InvalidPacket,
  WrongDuration,
  WrongFrameSize,
  EncodeFailed,
  ResetFailed,
};

// detail names the step with a size or libopus' error text; never audio or packet bytes
struct OpusError {
  OpusErrorCode code;
  std::string detail;
};

// bounded single-line text, safe to log
std::string describe(const OpusError& error);

// decodes the server's 20 ms mono packets; one thread at a time
class OpusDecoder {
 public:
  // like any allocation, throws std::bad_alloc if libopus has no memory
  OpusDecoder();
  ~OpusDecoder();

  OpusDecoder(const OpusDecoder&) = delete;
  OpusDecoder& operator=(const OpusDecoder&) = delete;

  // accepts only packets of exactly 20 ms
  Result<DecodedFrame, OpusError> decode(std::span<const std::uint8_t> packet);
  // forgets the previous stream, so a new response never continues an old one's audio
  [[nodiscard]] std::optional<OpusError> reset();

 private:
  struct State;
  std::unique_ptr<State> state_;
};

struct OpusEncoderConfig {
  // 0 to 10; tuned against pickle's cpu budget
  int complexity = 5;
};

// what libopus reports once configured
struct OpusEncoderSettings {
  int sampleRate = 0;
  bool voip = false;
  int bitrate = 0;
  int complexity = 0;
  bool dtx = false;
  bool inbandFec = false;
};

// encodes 16 kHz mono microphone frames for the uplink at 24 kbps, without dtx or in-band fec; one thread at a time
class OpusEncoder {
 public:
  OpusEncoder();
  ~OpusEncoder();

  OpusEncoder(const OpusEncoder&) = delete;
  OpusEncoder& operator=(const OpusEncoder&) = delete;

  // a failure leaves the encoder closed
  Result<OpusEncoderSettings, OpusError> open(const OpusEncoderConfig& config = {});
  // one packet of at most kMaxAudioFrameBytes from exactly kEncoderFrameSamples samples
  Result<std::vector<std::uint8_t>, OpusError> encode(std::span<const std::int16_t> frame);

 private:
  struct State;
  std::unique_ptr<State> state_;
};

}  // namespace cadence
