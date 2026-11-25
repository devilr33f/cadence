#pragma once

#include "audio/music-codec.h"
#include "backend/pcm-input.h"
#include "backend/presentation-clock.h"
#include "dsp/splicer.h"
#include "sync/calibration/probe.h"
#include "sync/clock-map.h"
#include "sync/group-placer.h"
#include "sync/sync-clock.h"

#include <cstdint>
#include <functional>
#include <map>
#include <memory>
#include <mutex>
#include <optional>
#include <string_view>
#include <utility>
#include <vector>

namespace cadence::sync {

enum class GroupChannelMap : std::uint8_t { Both, Left, Right, Mono };

enum class PlayoutState : std::uint8_t { Idle, Syncing, Buffering, Playing, Paused, Rejoining };

// why a playout asks to rejoin
enum class RejoinReason : std::uint8_t { None, Error, Underrun, ClockLost, ClockStep, Gap };

// the log token of a rejoin's reason: placement (an error past rejoinNs), underrun, clock_lost, clock_step, gap
std::string_view rejoinToken(RejoinReason reason);

enum class GroupTrimMode : std::uint8_t { Slew, Jump };

struct GroupPlayoutConfig {
  GroupChannelMap channel = GroupChannelMap::Both;
  // added to every due time: the member's trim within the group
  std::int64_t trimNs = 0;
  // how far ahead of real time, past the output's latency, what is pushed reaches
  std::int64_t leadNs = 60'000'000;
  // silence pushed first, so the input reads something and the playhead formula holds
  std::int64_t prerollNs = 30'000'000;
  // the fade of a pause, a cut, a flush
  std::size_t fadeFrames = 480;
  GroupPlacerConfig placer;
  // the clock its mapping's local side speaks (the sim's); due times are turned back into mono by it
  SyncClock local;
  // a probe of what the output did (the sim's group_heard): for every frame whose seq is a multiple of heardEvery,
  // once the output read its first sample, the leader time it was heard at by the playhead, mapped back. on the
  // playout's thread
  std::function<void(std::uint32_t epoch, std::uint64_t seq, std::int64_t leaderNs)> heard{};
  std::uint64_t heardEvery = 250;
};

struct PlayoutStats {
  PlayoutState state = PlayoutState::Idle;
  std::int32_t steerPpm = 0;
  std::int64_t errorNs = 0;
  std::uint64_t rejoins = 0;
  std::uint64_t underruns = 0;
  std::uint64_t cuts = 0;
  std::uint64_t concealed = 0;
  // the leader time the next frame starts at, and how much is queued after it
  std::int64_t queuedNs = 0;
  // the output's delay (the playhead's latency) at the last pump, in 48 kHz frames
  std::int64_t delayFrames = 0;
};

// what the playout saw as it last rejoined: why, how far off the audio was, and the output's delay then and over the
// run that ended (a delay that jumps tells an output's problem from a clock's)
struct PlayoutDrop {
  RejoinReason reason = RejoinReason::None;
  // heard minus due: the frame's own for a placement, the smoothed one otherwise
  std::int64_t errorNs = 0;
  std::int64_t delayFrames = 0;
  std::int64_t delayMinFrames = 0;
  std::int64_t delayMaxFrames = 0;
  std::int64_t queuedNs = 0;
  // how long it had played since its start
  std::int64_t playedNs = 0;
};

// where a frame's first sample was put: the music input's write position it lands at, for the tests and the leader's
// heard position
struct PlacedFrame {
  std::uint32_t epoch = 0;
  std::uint64_t seq = 0;
  std::int64_t tNs = 0;
  std::uint64_t mediaMs = 0;
  // when it is to be heard on mono
  std::int64_t dueMonoNs = 0;
  // the input position its first sample lands at (with what the resampler and the splicer hold)
  std::uint64_t position = 0;
};

// a group's playout on one device: the frames of the leader's timeline (leader sync time) are decoded, mapped to the
// device's channels and placed into the music input so each frame's first sample is heard at
// local.toMono(mapping.toLocal(t_ns)) + trim, by the playhead formula (presentation-clock.h). the playout is the
// input's producer: pump runs on one thread (a playout thread's), the rest is safe from any thread (the stream's).
// nothing is pushed before the first due frame, which fades in; a pause fades out over the 10 ms before its sample
class GroupPlayout {
 public:
  using PlayheadSource = std::function<std::optional<Playhead>()>;

  GroupPlayout(PcmInput& out, PlayheadSource playhead, GroupPlayoutConfig config);
  ~GroupPlayout();
  GroupPlayout(const GroupPlayout&) = delete;
  GroupPlayout& operator=(const GroupPlayout&) = delete;

  // any thread: the stream's messages
  void format(std::uint32_t epoch, MusicCodec codec);
  void frame(std::uint32_t epoch, std::uint64_t seq, std::int64_t tNs, std::uint64_t mediaMs,
             std::vector<std::uint8_t> data);
  void pause(std::uint32_t epoch, std::int64_t tNs);
  void flush(std::uint32_t epoch);
  // a rejoin from fromNs (leader time): what is queued goes, and the first frame due at or after it starts the audio
  void rejoinFrom(std::int64_t fromNs);
  // idle and changes past 200 ms jump; smaller ones slew, and the resampler carries their rate gaplessly
  GroupTrimMode setTrim(std::int64_t ns);
  [[nodiscard]] std::int64_t trimNs() const;
  void setChannel(GroupChannelMap channel);
  // this member's own ultrasonic slot, mixed locally after placement and before the output; never streamed
  void mixUltrasonic(std::uint32_t epoch, std::int64_t tNs, std::uint32_t code);
  // a voice session, a flush or a stopped cycle: nothing more is mixed; already placed music is not cut
  void clearUltrasonic();

  // the playout's thread: places what is due within the lead. mapping: the leader's clock on the local one (identity
  // on the leader), none when it is lost. returns a rejoin to ask for, None when all is well
  RejoinReason pump(std::int64_t nowMono, const std::optional<ClockMapping>& mapping);
  // the playout's thread: fade out and let go of the input (a stop, a gate that shut, the group leaving)
  void release();

  [[nodiscard]] PlayoutStats stats() const;
  // the last rejoin's, reason None before one
  [[nodiscard]] PlayoutDrop lastDrop() const;
  // the epoch whose Format came last, 0 for none
  [[nodiscard]] std::uint32_t epoch() const;
  // the playout's thread: the frames placed, oldest first, kept up to kPlacedKept
  [[nodiscard]] std::vector<PlacedFrame> placed() const;
  // the frame due last by nowMono (heard then, or the last heard before the playout stopped), none before any
  [[nodiscard]] std::optional<PlacedFrame> heardAt(std::int64_t nowMono) const;

  static constexpr std::size_t kPlacedKept = 1024;

 private:
  struct Queued {
    std::int64_t tNs = 0;
    std::uint64_t mediaMs = 0;
    std::vector<std::uint8_t> data;
  };
  using Key = std::pair<std::uint32_t, std::uint64_t>;
  struct UltrasonicSlot {
    std::uint32_t epoch = 0;
    std::int64_t tNs = 0;
    std::uint32_t code = 0;
    // built off the audio thread: probeAt itself is too slow per sample there
    std::shared_ptr<const calibration::ProbeTable> table;
  };

  // the playout's thread
  std::optional<std::int64_t> heardNext(const Playhead& playhead) const;
  [[nodiscard]] std::int64_t dueOf(std::int64_t tNs, const ClockMapping& mapping) const;
  void decode(const Queued* queued, std::vector<float>& pcm);
  void mapChannels(std::vector<float>& pcm) const;
  void push(std::span<const float> stereo);
  void flushSplicer();
  void silence(std::size_t frames);
  // what the splicer holds fades out, and all of it reaches the input
  void fadeOut();
  // primed: the input stays fed and its formula holds, so the next start needs no preroll (a pause, a run-out)
  void reset(PlayoutState state, bool primed = false);
  // the input's clear, and the placements it drops before they were heard
  void clearInput();
  RejoinReason rejoin(RejoinReason reason, std::optional<std::int64_t> errorNs = std::nullopt);
  // whether a period since the last playhead it placed by read short of a period: what it pushed since landed later
  // than placed. moves the baseline on
  bool readShort(const Playhead& playhead);
  // an underrun while it plays: a start nothing of which was heard yet starts again, anything else rejoins
  RejoinReason starved(const Playhead& playhead, std::uint64_t underruns);
  // moves the due-time offset toward its target and feeds the slew's rate to the resampler
  void advanceTrim(std::int64_t nowMono);
  void record(const PlacedFrame& frame);
  // tells config_.heard of the frames the output read since the last pump
  void probe(const Playhead& playhead, const ClockMapping& mapping);

  PcmInput& out_;
  PlayheadSource playhead_;

  mutable std::mutex mutex_;
  GroupPlayoutConfig config_;
  std::map<Key, Queued> queue_;
  std::uint32_t epoch_ = 0;
  MusicCodec codec_ = MusicCodec::Opus;
  bool codecChanged_ = false;
  std::optional<std::int64_t> pauseAtNs_;
  std::optional<std::int64_t> fromNs_;
  std::vector<UltrasonicSlot> ultrasonic_;
  bool flushed_ = false;
  PlayoutStats stats_;
  PlayoutDrop lastDrop_;
  std::vector<PlacedFrame> placed_;

  // the playout's thread
  std::unique_ptr<MusicDecoder> decoder_;
  dsp::Splicer splicer_;
  GroupPlacer placer_;
  std::vector<float> pcm_;
  std::vector<float> zeros_;
  bool primed_ = false;
  std::uint64_t primedAt_ = 0;
  bool started_ = false;
  bool attached_ = false;
  std::uint32_t playingEpoch_ = 0;
  std::uint64_t nextSeq_ = 0;
  std::uint64_t generation_ = 0;
  bool haveGeneration_ = false;
  // the offset dueOf uses now, its last movement, and the opposite of that rate for the resampler
  std::int64_t appliedTrimNs_ = 0;
  std::int64_t trimAtMonoNs_ = 0;
  std::int32_t trimSlewPpm_ = 0;
  std::uint64_t underrunsSeen_ = 0;
  // the playhead the last placement went by, while it plays
  std::optional<Playhead> seen_;
  // the input position the start's first frame lands at
  std::uint64_t startedAt_ = 0;
  // the output's delay over the run since the start, and when it started (mono)
  std::int64_t delayMinFrames_ = 0;
  std::int64_t delayMaxFrames_ = 0;
  std::int64_t startedMono_ = 0;
  std::int64_t lastPumpMono_ = 0;
  // the last frame the probe told of
  Key probed_{0, 0};
  bool probedAny_ = false;
  // the playhead the probe last went by, and the input position from which every period read a whole period since
  std::optional<Playhead> probeSeen_;
  std::uint64_t probeWholeFrom_ = 0;
};

}  // namespace cadence::sync
