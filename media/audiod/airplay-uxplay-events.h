#pragma once

#include <cstdint>
#include <span>
#include <string_view>

// what libairplay (uxplay's lib/, media/third_party/uxplay.cmake) reports, in our own types: the receiver
// (airplay-uxplay-receiver) turns each raop callback into one of these calls, on whichever of libairplay's threads made
// it (the httpd thread for rtsp, the rtp thread for audio, the mirror thread for video). the session implements it; the
// tests drive it directly as a fake sender would, without libairplay's encryption and fairplay. nothing a sender says
// is logged (rule 8): names, ids and keys are memory-only
namespace cadence::audiod::uxplay {

// raop's compression types (the ct of SETUP's audioFormat)
enum class AudioCodec : std::uint8_t { Unknown, Pcm, Alac, AacLc, AacEld };

AudioCodec codecOf(std::uint8_t ct);
std::string_view codecToken(AudioCodec codec);

// one decrypted packet of the sender's audio, in rtp order with the resends filled in
struct AudioPacket {
  std::span<const std::uint8_t> data;
  AudioCodec codec = AudioCodec::Unknown;
  // CLOCK_REALTIME ns when it is to be heard, from the sender's clock through ntp; 0 before the first sync
  std::int64_t localNs = 0;
  // the sender's own clock, ns: the media timeline mirror frames share
  std::int64_t remoteNs = 0;
  std::uint32_t rtpTime = 0;
  std::uint16_t seq = 0;
};

// one access unit of the mirror stream: annex b nals (start codes in place of airplay's lengths), the codec config
// prepended to the first frame after a change
struct VideoPacket {
  std::span<const std::uint8_t> data;
  bool h265 = false;
  int nalCount = 0;
  // CLOCK_REALTIME ns when it is to be shown, from the sender's clock through ntp
  std::int64_t localNs = 0;
  // the sender's clock, ns: the pts
  std::int64_t remoteNs = 0;
};

// what an hls cast (an apple app sending a url) reports of its player, for the sender's /playback-info: the screen's
// player as cadenced last said it (HlsPlaybackInfo)
struct HlsInfo {
  double durationS = 0;
  double positionS = 0;
  float rate = 0;
  bool ready = false;
  // buffered ahead of the position (loadedTimeRanges' end), s
  double bufferedS = 0;
  bool bufferEmpty = true;
  bool likelyToKeepUp = false;
};

class Events {
 public:
  virtual ~Events() = default;

  // a sender's rtsp connection opened or closed (raop's conn_init / conn_destroy)
  virtual void connectionOpened() = 0;
  virtual void connectionClosed() = 0;
  // the stream reset under the session (raop's conn_reset: a timeout, a lost connection)
  virtual void connectionReset(int reason) = 0;
  // pair-setup's client; admit false refuses it. memory-only
  virtual void client(std::string_view deviceId, std::string_view model, std::string_view name, bool& admit) = 0;
  // SETUP of the audio stream: its codec and frames per packet; usingScreen is a mirror session's audio
  virtual void audioFormat(AudioCodec codec, std::uint16_t framesPerPacket, bool usingScreen, bool isMedia) = 0;
  virtual void audio(const AudioPacket& packet) = 0;
  // the sender flushed (a pause, a seek, a track change): what is queued is stale
  virtual void audioFlush() = 0;
  // the sender's slider, dB: -144 mute, else -30..0
  virtual void volume(float db) = 0;
  // the volume the receiver reports to a sender (/info's initialVolume, GET_PARAMETER volume), dB
  virtual double clientVolume() = 0;
  // dmap (application/x-dmap-tagged): mlit with minm, asar, asal, ...
  virtual void metadata(std::span<const std::uint8_t> dmap) = 0;
  virtual void cover(std::span<const std::uint8_t> image) = 0;
  // rtp frames at the stream's rate (44.1 kHz), modulo 2^32
  virtual void progress(std::uint32_t start, std::uint32_t current, std::uint32_t end) = 0;

  // screen mirroring began or ended
  virtual void mirrorRunning(bool running) = 0;
  // the stream's codec: false refuses it (h265 the receiver did not offer)
  virtual bool videoCodec(bool h265) = 0;
  virtual void videoSize(float width, float height) = 0;
  virtual void video(const VideoPacket& packet) = 0;
  virtual void videoFlush() = 0;

  // pair-pin-start: the sender waits for the user to type this pin, 4 digits; never logged
  virtual void pin(std::string_view pin) = 0;
  // a sender that paired with the pin, by its ed25519 public key (base64); memory and the paired file only
  virtual void registered(std::string_view deviceId, std::string_view publicKey) = 0;
  // pair-verify: whether this key paired before (with a pin required, an unknown one must pair again)
  virtual bool known(std::string_view publicKey) = 0;

  // an hls cast: location is the receiver's own url of the playlist, start in s. never logged
  virtual void hlsPlay(std::string_view location, float startS) = 0;
  virtual void hlsScrub(float positionS) = 0;
  virtual void hlsRate(float rate) = 0;
  virtual void hlsStop() = 0;
  virtual HlsInfo hlsInfo() = 0;
};

}  // namespace cadence::audiod::uxplay
