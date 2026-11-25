#pragma once

#include <cstddef>
#include <cstdint>

// the sender's audio placed on the mixer's clock: each packet carries the time it is to be heard (the sender's clock
// through ntp, as CLOCK_MONOTONIC here), and the receiver knows when what it pushes now would be heard (the music
// input's queue and the output's latency). the placer says how much silence goes before a packet, or how much of it is
// dropped, so it is heard on time, and how fast the resampler plays the stream (steering): the two clocks' drift is
// taken up by a rate a fraction of a percent off, never by a cut. the presentation clock is then anchored where the
// audio really is, which the screen shows mirror frames by (one clock for every source)
namespace cadence::audiod::uxplay {

// what a stream's start tolerates before it delays or gives back: a period of the output and the estimate's jitter
inline constexpr std::int64_t kDeadBandNs = 20'000'000;
// a running stream's smoothed error past this is cut (silence, or frames dropped), spliced; under it the stream is
// steered. a steering that keeps up never gets here: a lost burst of packets, a latency step does
inline constexpr std::int64_t kHardNs = 60'000'000;
// the steering's gain, ppm faster per ms the stream is late (and slower per ms early), and its bound: 2000 ppm is 3.5
// cents, under what an ear tells apart, and takes up 2 ms a second, ten times two cheap crystals' drift
inline constexpr std::int32_t kSteerPpmPerMs = 100;
inline constexpr std::int32_t kMaxSteerPpm = 2000;
// a packet later than this is not caught up by dropping audio: the stream is delayed by it instead (an output whose
// latency is longer than the sender's lead, aac-eld's 170 ms against a tv's hdmi path), and the clock follows
inline constexpr std::int64_t kShiftNs = 100'000'000;
// a packet is pushed once it is due within this much, ahead of what is already queued
inline constexpr std::int64_t kLeadNs = 150'000'000;
// the smoothing of the drift estimate, per packet
inline constexpr double kErrorSmoothing = 0.1;
// packets in a row that late before the stream is delayed
inline constexpr int kShiftAfter = 3;
// the most the stream is ever delayed: lateness past it is dropped, so the stream catches up instead. libairplay holds
// its packets while a lost one waits for a resend (up to its 256-packet buffer, 2.8 s of aac-eld) and then hands them
// over in one late burst; delaying for each burst only ever grew the delay (6.5 s after a 2 min mirror on the stick)
inline constexpr std::int64_t kMaxDelayNs = 1'000'000'000;
// how long packets must keep arriving early before the delay gives back what they did not need
inline constexpr std::int64_t kRecoverWindowNs = 2'000'000'000;
// the margin a packet keeps before it must be in the output once the delay gave back
inline constexpr std::int64_t kSlackNs = 40'000'000;

struct Placement {
  // frames of silence to push before the packet
  std::size_t silence = 0;
  // frames of the packet's start not to push: a correction larger than one packet is spread over the next ones
  std::size_t drop = 0;
  // the stream's delay grew (a shift): from now on everything is heard this much later than the sender asked
  bool shifted = false;
  // the stream's delay shrank, as packets kept arriving early enough: the audio it held is dropped (drop)
  bool recovered = false;
  // lateness past kMaxDelayNs, dropped rather than delayed
  std::int64_t caughtUpNs = 0;
  // the rate the stream is to play at from here, ppm faster (positive) or slower than its own
  std::int32_t steerPpm = 0;
  // a running stream cut by the smoothed error (past kHardNs): silence or drop says which
  bool hard = false;
};

class Placer {
 public:
  // targetNs: when the packet's first frame is to be heard (before the stream's delay); heardNs: when a frame pushed
  // now would be heard; frames at rate. the first packet after a reset starts the stream: its gap is filled with
  // silence, or the delay made up when it is late
  Placement place(std::int64_t targetNs, std::int64_t heardNs, std::size_t frames, std::uint32_t rate);
  // a packet as it arrives: readyNs is when it would be heard at the earliest (now plus the output's latency). the
  // least margin packets kept over kRecoverWindowNs is what the delay can give back
  void arrived(std::int64_t targetNs, std::int64_t readyNs);
  // a new stream, a flush
  void reset();

  // what is added to every target
  [[nodiscard]] std::int64_t delayNs() const { return delayNs_; }
  [[nodiscard]] bool started() const { return started_; }
  // corrections since the start (cuts: shifts, recoveries, hard ones), for the logs
  [[nodiscard]] std::uint64_t corrections() const { return corrections_; }
  // the steering in force, and the smoothed error it steers by
  [[nodiscard]] std::int32_t steerPpm() const { return steerPpm_; }
  [[nodiscard]] std::int64_t errorNs() const { return static_cast<std::int64_t>(errorNs_); }

 private:
  // the delay grew to delayNs (capped); what it could not take is owed, dropped from the next frames
  void delayTo(std::int64_t delayNs, std::int64_t targetNs, std::int64_t heardNs, std::uint32_t rate, Placement& out);
  void restartWindow();

  bool started_ = false;
  std::int64_t delayNs_ = 0;
  // the arrival window: when it began (0: none yet), the least margin in it, and the last whole window's
  std::int64_t windowStartNs_ = 0;
  std::int64_t windowMinNs_ = 0;
  std::int64_t recoverableNs_ = 0;
  bool recoverable_ = false;
  double errorNs_ = 0;
  int lateRun_ = 0;
  // frames still to drop
  std::size_t owed_ = 0;
  std::uint64_t corrections_ = 0;
  std::int32_t steerPpm_ = 0;
};

// frames of ns at rate, rounded down; 0 for a negative span
std::size_t framesIn(std::int64_t ns, std::uint32_t rate);

// the mixer frame (the presentation clock's count) heard at monoNs, from a period the playhead knows: frames heard at
// heardNs, at 48 kHz
std::uint64_t frameAt(std::int64_t monoNs, std::uint64_t frames, std::int64_t heardNs);

}  // namespace cadence::audiod::uxplay
