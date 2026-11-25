#pragma once

#include "audiod/airplay-metadata.h"
#include "audiod/log.h"
#include "audiod/media-source.h"
#include "audiod/sys.h"
#include "backend/pcm-input.h"

#include <condition_variable>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <span>
#include <string>
#include <thread>

namespace cadence::audiod {

// shairport's pipe carries 48 kHz s16 stereo (M4d-R2): its config pins it, so nothing is resampled
inline constexpr std::uint32_t kAirplayRate = 48000;
inline constexpr std::size_t kAirplayChannels = 2;

// where shairport's audio goes: into the input the output was opened on (the music input, or a group leader's feed)
// while airplay owns the output, the playback gate is open and the receiver does not hold it, else nowhere. any
// thread. an input has one producer at a time (see PcmInput): the pipe's reader pushes only while it flows, and close,
// a gate that shuts and a hold clear it and return once no push is under way; close then lets go of it. the input is
// attached only while it flows, so the underruns it counts are airplay's. as a flow stops, airplay_output_closed logs
// what it queued, dropped and ran short of, and how full the input was at the end: shairport's clock against the
// device's, whose drift nothing corrects yet (M4d-R6). counts only
class AirplayOutput {
 public:
  explicit AirplayOutput(const Log& log) : log_(log) {}

  AirplayOutput(const AirplayOutput&) = delete;
  AirplayOutput& operator=(const AirplayOutput&) = delete;

  // airplay took the session, or lost it but waits paused: its audio goes into music, which outlives the open
  // output. an open on another input while one is open closes that one first, so the input changes only while closed
  void open(PcmInput& music);
  void close();
  // the playback gate (rule 7), which the receiver follows whatever the session
  void gate(bool open);
  // the receiver's own: held from the start, released only while it genuinely plays (never optimistically)
  void hold(bool held);
  // a pause on its way to the receiver's session, from another thread: held at once, and counted, so that no release
  // from a session that has not taken it yet reopens the output; the number of this one
  std::uint64_t pause();
  // the session's release, which a pause it has not taken (pausesTaken below the count) refuses
  void release(std::uint64_t pausesTaken);
  // the pauses asked for so far
  [[nodiscard]] std::uint64_t pauses() const;

  // the reader's: interleaved stereo frames, pushed without waiting (what finds no room is dropped) or dropped
  void deliver(std::span<const std::int16_t> samples);

 private:
  // one flag to value: the input cleared as the flow opens and as it stops, the stop waiting out a push under way
  void set(bool AirplayOutput::*flag, bool value);
  void set(std::unique_lock<std::mutex>& lock, bool AirplayOutput::*flag, bool value);
  [[nodiscard]] bool flowing() const { return open_ && gate_ && !held_; }

  const Log& log_;
  mutable std::mutex mutex_;
  std::condition_variable changed_;
  // all under mutex_
  bool open_ = false;
  // where the audio goes: set while open, null while closed, so a flow always has one
  PcmInput* music_ = nullptr;
  bool gate_ = false;
  bool held_ = true;
  bool pushing_ = false;
  std::uint64_t pauses_ = 0;
  // counts the clears a stop made, so a push that began just before one is cleared again after it
  std::uint64_t clears_ = 0;
  // the input's counters as the flow opened
  PcmInputStatus opened_{};
};

// shairport's audio and metadata fifos, read on a thread of their own for as long as shairport runs, whatever the
// output does (M4d-R2): shairport waits up to 5 s per packet for a reader that stopped reading, so both are always
// drained. the audio goes to the output in whole frames; the metadata is read into its items (airplay::MetadataReader)
// and the title, artist, album and cover they set go to the sink whenever they change, a new cover written first to
// artworkPath (0600, through a rename, so a reader never sees half of one); where the track stands goes to progress. each fifo is opened for reading
// without blocking, polled beside a stop fd, and opened again once its writer closed it: shairport opens and closes the
// audio pipe per session. logs carry counts only, never audio or metadata
class AirplayPipe {
 public:
  // the whole metadata, on the pipe's thread; memory only
  using MetadataSink = std::function<void(const MediaMetadata& metadata)>;
  // where the track stands, on the pipe's thread
  using ProgressSink = std::function<void(const airplay::Progress& progress)>;

  // the fifos exist already (airplay::privateFifo); null, logged, when the thread cannot start. without an
  // artworkPath no cover is passed on
  static std::unique_ptr<AirplayPipe> start(const Log& log, std::string audio, std::string metadata,
                                            AirplayOutput& output, MetadataSink sink = {},
                                            ProgressSink progress = {}, std::string artworkPath = {});
  // bounded: stops the thread and joins it
  ~AirplayPipe();

  AirplayPipe(const AirplayPipe&) = delete;
  AirplayPipe& operator=(const AirplayPipe&) = delete;

 private:
  struct Fifo;

  AirplayPipe(const Log& log, std::string audio, std::string metadata, AirplayOutput& output, MetadataSink sink,
              ProgressSink progress, std::string artworkPath, UniqueFd stop);
  // the metadata the tracker changed, its cover written out first; on the pipe's thread
  void publish(MediaMetadata metadata);
  void run();
  // reads what is there; false once its writer closed it
  bool readAudio(Fifo& fifo);
  bool readMetadata(Fifo& fifo);

  const Log& log_;
  const std::string audioPath_;
  const std::string metadataPath_;
  AirplayOutput& output_;
  const MetadataSink sink_;
  const ProgressSink progress_;
  const std::string artworkPath_;
  // the thread's own
  airplay::MetadataReader reader_;
  airplay::MetadataTracker tracker_;
  // the id of the cover at artworkPath_
  std::string written_;
  UniqueFd stop_;
  std::thread thread_;
};

}  // namespace cadence::audiod
