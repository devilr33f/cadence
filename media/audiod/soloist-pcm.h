#pragma once

#include "audiod/log.h"
#include "audiod/sys.h"
#include "backend/pcm-input.h"
#include "pcmframe/codec.h"

#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <span>
#include <string>
#include <thread>
#include <vector>

namespace cadence::audiod {

// the defaults are M4c's; tests shorten them
struct SoloistPcmOptions {
  // a slice of a pcm frame is pushed once the input holds less than this
  std::chrono::milliseconds queueTarget{150};
  // how long a slice waits for the input to drain below the target. past it nothing reads the input (no player): the
  // rest of the frame, and every frame after it until the input is read again or has room, is consumed at real-time
  // pace unheard, so soloist never stalls
  std::chrono::milliseconds stallBound{1000};
};

// soloist's pcm socket (M4c-D6): the libpulse shim in soloist's jail connects, one connection at a time, served on
// this server's own thread. its frames are decoded strictly: any error closes the connection
// (soloist_pcm_invalid reason=frame|no_format|ragged) and the next connection starts afresh.
//
// while the output is open (soloist owns the media session) each pcm frame is converted to float and pushed into the
// input the output was opened on (the music input, or a group leader's feed) in 20 ms slices, each paced by what the
// backend consumes: a slice waits until less than queueTarget is queued, so the input holds at most the target and a
// slice and the shim's buffer holds what is ahead. every pcm frame is answered with Played, the connection's total of
// consumed bytes, once its last slice is in. a cork or a flush clears the input, so it drops at most that much of what
// was answered. while the output is closed, and while nothing reads the input (see stallBound), the frames are
// consumed at real-time pace, dropped and still answered. an input has one producer at a time (see PcmInput):
// closeOutput clears it and returns once no push is under way, and only then lets go of it.
//
// logs carry event names and counts only, never audio
class SoloistPcm {
 public:
  // replaces a stale socket or a symlink at socketPath, never follows one, and refuses any other file; the socket is
  // 0600 from the moment it exists and, as root, owned by nobody. null, logged, when it cannot listen
  static std::unique_ptr<SoloistPcm> listen(const Log& log, std::string socketPath, SoloistPcmOptions options = {});
  // bounded: shuts the connection down, joins the thread and removes the socket
  ~SoloistPcm();

  SoloistPcm(const SoloistPcm&) = delete;
  SoloistPcm& operator=(const SoloistPcm&) = delete;

  // any thread: the frames go into music until closeOutput. music outlives the open output; an open on another input
  // while one is open closes that one first, so the input changes only while the output is closed
  void openOutput(PcmInput& music);
  void closeOutput();

 private:
  SoloistPcm(const Log& log, std::string path, UniqueFd listener, SoloistPcmOptions options);

  // one connection's episode
  struct Episode {
    pcmframe::Decoder decoder;
    std::optional<pcmframe::Format> format;
    // pcm bytes consumed on the connection: what Played reports, never reset
    std::uint64_t consumed = 0;
  };

  enum class Wait { Ready, Stopped, Failed };

  void serve();
  void accepted(UniqueFd connection);
  // false ends the connection
  bool handle(Episode& episode, int connection, const pcmframe::ToServer& message);
  bool pcm(Episode& episode, int connection, std::span<const std::uint8_t> bytes);
  // into the target slice by slice while the output is open and the input has room, else consumed at real-time pace
  void deliver(const pcmframe::Format& format, std::span<const std::uint8_t> bytes);
  // whether a slice may go in now: waits, up to the stall bound, for the input to drain below the target; false at
  // once while the stall that bound found lasts (nothing read since, no room), and when the output closes or the
  // server stops
  bool room(std::unique_lock<std::mutex>& lock);
  // runs work as the target's producer while the output is open, with mutex_ released meanwhile; false, doing
  // nothing, when it is closed or the server stops
  bool produce(std::unique_lock<std::mutex>& lock, const std::function<void(PcmInput& target)>& work);
  void push(PcmInput& target, const pcmframe::Format& format, std::span<const std::uint8_t> bytes);
  // with mutex_ held: the output closes, its target cleared and let go once no push is under way
  void closeLocked(std::unique_lock<std::mutex>& lock);
  // waits out the audio's duration from where the last frame consumed in real time ended, or until the server stops
  void consumeInRealTime(std::unique_lock<std::mutex>& lock, std::chrono::steady_clock::duration duration);
  // the producer's clear of the target (a cork, a flush), only while the output is open
  void clearOwn();
  // waits until fd is readable or the server stops; a failed poll leaves its errno in error
  Wait readable(int fd, int& error);
  [[nodiscard]] bool stopping();

  const Log& log_;
  const std::string path_;
  UniqueFd listener_;
  UniqueFd wake_;
  const SoloistPcmOptions options_;

  std::mutex mutex_;
  std::condition_variable changed_;
  // all of these under mutex_
  bool open_ = false;
  // where the frames go: set while the output is open, null while it is closed
  PcmInput* target_ = nullptr;
  bool stopping_ = false;
  bool pushing_ = false;
  // counts closeOutput's clears, so a push that began just before one is cleared again after it
  std::uint64_t clears_ = 0;
  // the connection being served, for stop to shut down
  int connection_ = -1;
  // where the last frame consumed in real time ends
  std::chrono::steady_clock::time_point paceUntil_{};
  // the input's read count when a slice last waited out the stall bound, while that stall lasts
  std::optional<std::uint64_t> stalledAt_;

  // the server thread's
  std::vector<std::int16_t> s16_;
  std::vector<float> f32_;

  std::thread thread_;
};

}  // namespace cadence::audiod
