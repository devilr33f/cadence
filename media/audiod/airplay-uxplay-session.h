#pragma once

#include "audiod/airplay-metadata.h"
#include "audiod/airplay-uxplay-decoder.h"
#include "audiod/airplay-uxplay-events.h"
#include "audiod/airplay-uxplay-mirror.h"
#include "audiod/airplay-uxplay-pin.h"
#include "audiod/airplay-uxplay-splice.h"
#include "audiod/airplay-uxplay-sync.h"
#include "audiod/log.h"
#include "audiod/media-source.h"
#include "audiod/sender-volume.h"
#include "backend/pcm-input.h"
#include "backend/presentation-clock.h"
#include "backend/span-histogram.h"
#include "playback/thread-priority.h"

#include <chrono>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::audiod::uxplay {

// MirrorState's content
struct MirrorReport {
  std::string sessionId;
  bool active = false;
  bool h265 = false;
  std::uint32_t width = 0;
  std::uint32_t height = 0;
  // memory-only, never logged
  std::string clientName;
  // when it ended: sender, displaced, failed, disabled
  std::string reason;
};

// AirplayPin's content
struct PinReport {
  bool active = false;
  // never logged
  std::string pin;
  std::string clientName;
};

// an hls cast, which cadenced plays as a media.video session (HlsCast). the url is the receiver's own (it proxies the
// sender's playlist); value is the start or the position in s, or the rate. url and name are never logged
struct HlsRequest {
  enum class Op : std::uint8_t { Play, Scrub, Rate, Stop } op = Op::Play;
  std::string url;
  float value = 0;
  // Play: the sender's name, for the toast
  std::string clientName;
};

// what the session reports; every hook but disconnect runs on the loop thread (the session posts them)
struct SessionHooks {
  std::function<void(std::function<void()>)> post;
  // a deliberate slider move on the sender (rule 7: the speaker's level, never the stream's gain)
  std::function<void(std::uint8_t level)> senderVolume;
  std::function<void(const MirrorReport& report)> mirrorState;
  std::function<void(const PinReport& report)> pin;
  // a mirror access unit for the screen, pts on the sender's clock
  std::function<void(const std::string& sessionId, const AccessUnit& unit, std::int64_t ptsNs)> mirrorUnit;
  std::function<void(const HlsRequest& request)> hls;
  // drops the sender's connection (the receiver's disconnect); any thread, never with the session's lock held
  std::function<void()> disconnect;
};

struct SessionSettings {
  // the cover goes to <scratch>/run/artwork, the paired senders to <scratch>/paired; empty: neither written
  std::string scratchDir;
  bool requirePin = false;
  bool mirroring = true;
  bool h265 = false;
};

// the clocks the session reads, so the tests can drive them
struct SessionClocks {
  std::function<std::int64_t()> monoNs;
  std::function<std::int64_t()> realNs;
  std::function<std::chrono::steady_clock::time_point()> steady;
};
SessionClocks systemClocks();

// where the presentation clock is anchored: the sender's media time heard at a monotonic time
struct ClockAnchor {
  std::int64_t mediaNs = 0;
  std::int64_t monoNs = 0;
};

// the sender's audio over a window (takeStats), for the airplay_audio line: counts, ms and ppm, never audio
struct AudioStats {
  // packets decoded; lost: sequence numbers libairplay never handed over (it gave up waiting for their resend)
  std::uint64_t packets = 0;
  std::uint64_t lost = 0;
  // packets that came too late to be heard on time (after the moment they had to be in the output)
  std::uint64_t late = 0;
  // the longest time between two packets: libairplay holds them back while it waits for a lost one's resend
  std::int64_t stallMaxNs = 0;
  // how long before it had to be in the output each packet came (the jitter buffer's slack), least and mean
  std::int64_t leadMinNs = 0;
  std::int64_t leadAvgNs = 0;
  // the music input's fill after each push, least and mean
  std::int64_t queueMinNs = 0;
  std::int64_t queueAvgNs = 0;
  SpanSummary decode;
  // 44.1 kHz frames: lost packets' gaps filled with silence, frames sent twice dropped, silence and drops the
  // placer cut for sync
  std::uint64_t concealedFrames = 0;
  std::uint64_t overlapFrames = 0;
  std::uint64_t silenceFrames = 0;
  std::uint64_t droppedFrames = 0;
  // the placer's cuts: hard ones (the steering fell behind), shifts (the delay grew), recoveries (it shrank)
  std::uint64_t hardCuts = 0;
  std::uint64_t shifts = 0;
  std::uint64_t recoveries = 0;
  // the steering now and its range over the window, ppm, the smoothed error and the delay now
  std::int32_t steerPpm = 0;
  std::int32_t steerMinPpm = 0;
  std::int32_t steerMaxPpm = 0;
  std::int64_t errorNs = 0;
  std::int64_t delayNs = 0;
  // 48 kHz frames the music input had no room for, and its reads that came up short while the stream ran
  std::uint64_t overruns = 0;
  std::uint64_t underruns = 0;
  // the rtp thread's priority (it decodes)
  ThreadPriority rtpPriority;
  bool streaming = false;
};

// one receiver's sessions (libairplay's sender, one at a time: the newest takes over), behind uxplay::Events. audio is
// decoded on libairplay's rtp thread, held until it is due and pushed into the input the media sessions opened the
// output on, only while it is open and not held; otherwise the sender's audio is drained unheard. into the music
// input (the one it was made with, whose playhead the mixer reports) it is placed on the mixer's clock (Placer);
// into another (a group leader's feed, multiroom) it goes at the sender's pace as it falls due, unplaced and
// unsteered: the leader's timeline steers its feed, and the mixer's playhead says nothing of when the group hears it. a sender's flush pauses it, as does an audio-only stream with no packet for 2.5 s
// (past libairplay's resend wait), and the next packet resumes it; a closed connection ends it. mirror units go to the
// loop as they come, while mirroring and not displaced; the presentation clock's anchor is where the audio really is
// (the video's own when no audio flows). the pin, the sender's slider (SenderGuard, rule 7), the metadata and the
// cover (written as the speakers' are, its id in the metadata for cadenced's upload) are reported through the hooks.
// thread safe: libairplay's threads call the events, the loop the rest
class Session final : public Events {
 public:
  Session(const Log& log, PcmInput& music, SessionHooks hooks, SessionSettings settings = {},
          SessionClocks clocks = systemClocks());
  ~Session() override;

  Session(const Session&) = delete;
  Session& operator=(const Session&) = delete;

  // the loop's side
  void events(std::shared_ptr<MediaSourceEvents> events);
  // the receiver's settings, before it starts again with them: the paired senders are read again from the scratch
  void configure(SessionSettings settings);
  // music outlives the open output; an open on another input while one is open closes that one first
  void openOutput(PcmInput& music);
  void closeOutput();
  // a pause the sessions or cadenced asked for: nothing is heard until resume; the sender streams on
  void hold(bool held);
  // displaced by another source, or stopped: the sender's connection is dropped and mirroring ends with reason; the
  // same sender asking again within 10 s is refused, so it ends its cast rather than reconnect
  void release(std::string_view reason);
  // the speaker's level for the sender (SenderVolumeSet)
  void speakerLevel(std::uint8_t level);
  // the output's latency (the presentation clock's), ns; 0 = unknown. what a pushed frame is heard at comes from it
  // until the session has a playhead
  void outputLatency(std::int64_t ns);
  // the mixer's last period (PresentationClock::playhead): when a pushed frame is heard comes from its position in
  // the music input against the period's, with no jitter from when the period ran
  void playhead(const Playhead& playhead);
  // the window since the last take, and a new one begun
  AudioStats takeStats();
  // pushes what fell due, ends an expired pin, sends a coalesced slider move; the loop calls it every few ms
  void tick();
  // the clock's anchor while mirroring, else none
  [[nodiscard]] std::optional<ClockAnchor> anchor() const;
  [[nodiscard]] bool mirroring() const;
  [[nodiscard]] bool streaming() const;
  // the receiver's settings went away (disabled): what was on screen goes
  void shutdown(std::string_view reason);

  // uxplay::Events, on libairplay's threads
  void connectionOpened() override;
  void connectionClosed() override;
  void connectionReset(int reason) override;
  void client(std::string_view deviceId, std::string_view model, std::string_view name, bool& admit) override;
  void audioFormat(AudioCodec codec, std::uint16_t framesPerPacket, bool usingScreen, bool isMedia) override;
  void audio(const AudioPacket& packet) override;
  void audioFlush() override;
  void volume(float db) override;
  double clientVolume() override;
  void metadata(std::span<const std::uint8_t> dmap) override;
  void cover(std::span<const std::uint8_t> image) override;
  void progress(std::uint32_t start, std::uint32_t current, std::uint32_t end) override;
  void mirrorRunning(bool running) override;
  bool videoCodec(bool h265) override;
  void videoSize(float width, float height) override;
  void video(const VideoPacket& packet) override;
  void videoFlush() override;
  void pin(std::string_view pin) override;
  void registered(std::string_view deviceId, std::string_view publicKey) override;
  bool known(std::string_view publicKey) override;
  void hlsPlay(std::string_view location, float startS) override;
  void hlsScrub(float positionS) override;
  void hlsRate(float rate) override;
  void hlsStop() override;
  HlsInfo hlsInfo() override;
  // the screen's playback of the cast (cadenced's HlsPlaybackInfo), what /playback-info answers from; any thread
  void playbackInfo(const HlsInfo& info);

 private:
  struct Chunk {
    std::int64_t targetNs = 0;
    std::int64_t remoteNs = 0;
    // frames of silence before it, for packets lost before it
    std::size_t gap = 0;
    std::vector<float> samples;
  };
  enum class Report : std::uint8_t { Playing, Paused, Ended, Buffering };

  // with mutex_ held
  void closeLocked();
  // the input the output is open on, else the music input, whose state paces what is drained
  [[nodiscard]] PcmInput& outLocked() const { return target_ != nullptr ? *target_ : home_; }
  // whether a push is placed on the mixer's clock: the output is open on the music input, or closed
  [[nodiscard]] bool placedLocked() const { return target_ == nullptr || target_ == &home_; }
  // the input's stats start again from its counters when the output moves to another
  void movedLocked(const PcmInput& before);
  void pumpLocked(std::int64_t nowNs);
  // when a frame pushed now would be heard
  std::int64_t heardLocked(std::int64_t nowNs) const;
  // steers the music input's resampler, at most every so often and in steps, as speexdsp rebuilds its table each time
  void steerLocked(std::int32_t ppm, std::int64_t nowNs);
  // the splicer's output into the music input
  void flushSplicerLocked();
  void attachLocked(bool attached);
  void clearMusicLocked();
  void dropPendingLocked();
  void endStreamLocked();
  void endMirrorLocked(std::string_view reason);
  [[nodiscard]] MirrorReport activeReportLocked() const;
  std::int64_t toMonoLocked(std::int64_t realNs) const;
  void metadataItemLocked(const airplay::MetadataItem& item);
  void writeArtworkLocked(MediaMetadata& metadata);

  // posts to the loop, without the lock
  void report(Report report);
  // the sender's name rides on every metadata report (clientName_ at the time it is sent)
  void reportMetadata(MediaMetadata metadata);
  // the track's metadata so far, so the sender's name is known before (or without) any dmap
  void announceSender();
  void reportProgress(std::uint64_t positionMs, std::uint64_t durationMs);
  void reportMirror(MirrorReport report);
  void reportPin(PinReport report);

  const Log& log_;
  // the device's music input: the mixer's playhead (playhead below) is a position in it
  PcmInput& home_;
  SessionHooks hooks_;
  SessionSettings settings_;
  SessionClocks clocks_;
  // shared with what the session posts, so a report queued when it is gone is dropped
  std::shared_ptr<char> alive_ = std::make_shared<char>();

  // the cast's playback, apart from mutex_: libairplay's httpd thread reads it under its own locks
  mutable std::mutex hlsMutex_;
  HlsInfo hls_;

  mutable std::mutex mutex_;
  std::shared_ptr<MediaSourceEvents> events_;
  int connections_ = 0;
  std::string clientName_;
  // the sender's device id (memory-only, never logged), and the one release let go, refused until letGoUntil_
  std::string deviceId_;
  std::string letGo_;
  std::chrono::steady_clock::time_point letGoUntil_{};
  // audio
  AudioCodec codec_ = AudioCodec::Unknown;
  std::uint16_t framesPerPacket_ = 0;
  std::unique_ptr<AudioDecoder> decoder_;
  bool decoderFailed_ = false;
  std::vector<float> decoded_;
  // the samples of chunks pushed, for the next ones: no allocation per packet once a stream runs
  std::vector<std::vector<float>> spare_;
  std::deque<Chunk> pending_;
  std::size_t pendingFrames_ = 0;
  Placer placer_;
  Splicer splicer_{kStreamChannels};
  std::optional<Playhead> playhead_;
  // the music input's write position at the session's last clear
  std::uint64_t clearedAt_ = 0;
  // the rtp timeline: the next packet's rtp time and sequence number when it follows on
  bool haveRtp_ = false;
  std::uint32_t nextRtp_ = 0;
  std::uint16_t lastSeq_ = 0;
  std::int64_t lastArrivalNs_ = 0;
  std::int32_t steerApplied_ = 0;
  std::int64_t steerAtNs_ = 0;
  bool attached_ = false;
  // the window
  AudioStats stats_;
  std::int64_t leadSumNs_ = 0;
  std::uint64_t leadCount_ = 0;
  std::int64_t queueSumNs_ = 0;
  std::uint64_t queueCount_ = 0;
  SpanHistogram<400> decodeTimes_{25'000};
  PcmInputStatus musicSeen_;
  bool outputOpen_ = false;
  // where the audio goes while the output is open: home_ or a group leader's feed; null while closed
  PcmInput* target_ = nullptr;
  bool held_ = false;
  bool streaming_ = false;
  std::int64_t latencyNs_ = 0;
  std::optional<ClockAnchor> audioAnchor_;
  std::int64_t audioAnchorAtNs_ = 0;
  std::uint64_t pushedFrames_ = 0;
  std::uint64_t droppedFrames_ = 0;
  std::uint64_t decodeFailures_ = 0;
  // mirroring
  bool mirroring_ = false;
  bool mirrorForwarded_ = false;
  bool h265_ = false;
  std::uint32_t width_ = 0;
  std::uint32_t height_ = 0;
  std::string mirrorId_;
  std::uint64_t mirrorSessions_ = 0;
  std::optional<ClockAnchor> videoAnchor_;
  // the pin and the paired senders
  PinPrompt prompt_;
  PairedSenders paired_;
  // the sender's slider
  SenderGuard guard_;
  Coalescer coalescer_;
  // metadata
  airplay::MetadataTracker tracker_;
  std::string writtenArtwork_;
};

}  // namespace cadence::audiod::uxplay
