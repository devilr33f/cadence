#pragma once

#include "audiod/airplay-uxplay-events.h"

#include <cstddef>
#include <cstdint>
#include <memory>
#include <span>
#include <string_view>
#include <vector>

// the sender's audio into pcm: alac through apple's alac decoder (apache-2.0, fetched pinned: media/CMakeLists.txt),
// aac-lc and aac-eld (mirroring's) through ffmpeg's aac decoder on linux and the ndk's AMediaCodec on android, and l16.
// one decoder per stream, on the thread that delivers its packets
namespace cadence::audiod::uxplay {

// the stream's rate: airplay's audio is 44.1 kHz stereo, whatever the codec
inline constexpr std::uint32_t kStreamRate = 44100;
inline constexpr std::size_t kStreamChannels = 2;
// alac's frames per packet when SETUP gives none
inline constexpr std::uint16_t kAlacFramesPerPacket = 352;

class AudioDecoder {
 public:
  virtual ~AudioDecoder() = default;
  // appends the packet's interleaved float stereo at kStreamRate; false for a packet it could not decode (dropped, the
  // stream goes on)
  virtual bool decode(std::span<const std::uint8_t> packet, std::vector<float>& out) = 0;
  // a flush: whatever the decoder holds of the packets before it is dropped
  virtual void reset() {}
};

// nullptr for a codec this build cannot decode
std::unique_ptr<AudioDecoder> makeDecoder(AudioCodec codec, std::uint16_t framesPerPacket);

std::unique_ptr<AudioDecoder> makeAlacDecoder(std::uint16_t framesPerPacket);
std::unique_ptr<AudioDecoder> makePcmDecoder();
// the platform's: ffmpeg (linux) or AMediaCodec (android)
std::unique_ptr<AudioDecoder> makeAacDecoder(AudioCodec codec);

// the audio specific config (iso 14496-3) airplay's aac streams have: 44.1 kHz stereo, lc with 1024 frames, eld with 480
std::span<const std::uint8_t> aacConfig(AudioCodec codec);
// alac's magic cookie (ALACSpecificConfig) for airplay's stream: 16 bits, stereo, 44.1 kHz, rice 40/10/14
std::vector<std::uint8_t> alacCookie(std::uint16_t framesPerPacket);

}  // namespace cadence::audiod::uxplay
