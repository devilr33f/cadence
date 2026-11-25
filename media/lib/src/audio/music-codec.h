#pragma once

#include <cstddef>
#include <cstdint>
#include <memory>
#include <span>
#include <vector>

// a group's audio between devices (cadence.sync.v1 Frame.data): 20 ms of 48 kHz stereo per frame, as opus (music,
// OPUS_APPLICATION_AUDIO, the group's bitrate) or s16le interleaved (a wired pair). the voice codec (opus-codec.h) is
// mono and tuned for speech; this one is for the music a group plays
namespace cadence {

inline constexpr std::uint32_t kGroupRate = 48000;
inline constexpr std::size_t kGroupChannels = 2;
inline constexpr std::size_t kGroupFrameSamples = 960;
inline constexpr std::int64_t kGroupFrameNs = 20'000'000;
// Frame.data's bound (sync.options): pcm's 3840 bytes fit, and opus at 510 kbps is 1275
inline constexpr std::size_t kGroupFrameBytes = 4000;
inline constexpr std::uint32_t kGroupDefaultKbps = 256;

enum class MusicCodec : std::uint8_t { Opus, PcmS16 };

// one thread at a time
class MusicEncoder {
 public:
  // kbps 64..510 (0: 256), opus alone
  MusicEncoder(MusicCodec codec, std::uint32_t kbps);
  ~MusicEncoder();
  MusicEncoder(const MusicEncoder&) = delete;
  MusicEncoder& operator=(const MusicEncoder&) = delete;

  // false when libopus could not be set up
  [[nodiscard]] bool ok() const;
  [[nodiscard]] MusicCodec codec() const { return codec_; }
  // the opus bitrate from the next frame on (64..510, 0: 256); pcm has none
  void setBitrate(std::uint32_t kbps);
  // one frame: kGroupFrameSamples interleaved stereo floats in, its bytes in out (replaced); false when it failed
  bool encode(std::span<const float> stereo, std::vector<std::uint8_t>& out);

 private:
  struct State;
  MusicCodec codec_;
  std::unique_ptr<State> state_;
};

class MusicDecoder {
 public:
  explicit MusicDecoder(MusicCodec codec);
  ~MusicDecoder();
  MusicDecoder(const MusicDecoder&) = delete;
  MusicDecoder& operator=(const MusicDecoder&) = delete;

  [[nodiscard]] bool ok() const;
  [[nodiscard]] MusicCodec codec() const { return codec_; }
  // one frame into out (kGroupFrameSamples interleaved stereo floats); false for bytes that are not a 20 ms frame
  bool decode(std::span<const std::uint8_t> data, std::span<float> out);
  // a lost frame: opus conceals it from what came before, pcm is silence
  void conceal(std::span<float> out);
  // a new stream: nothing of the last one carries over
  void reset();

 private:
  struct State;
  MusicCodec codec_;
  std::unique_ptr<State> state_;
};

}  // namespace cadence
