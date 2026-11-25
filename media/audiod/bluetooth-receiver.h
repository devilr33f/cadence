#pragma once

#include "audiod/log.h"
#include "audiod/media-source.h"
#include "audiod/sys.h"
#include "backend/pcm-input.h"

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::audiod {

// no pcm for this long reads as the phone pausing (a2dp suspends its transport rather than send silence)
inline constexpr std::chrono::milliseconds kBluetoothSilence{1000};

// a phone's pcm as BluetoothStream announced it; sender is memory-only, never logged (D11)
struct BluetoothStreamInfo {
  std::string id;
  std::uint32_t rate = 0;
  std::uint32_t channels = 0;
  std::string sender;
};

// the phone's avrcp player as BluetoothPlayer mirrors it; metadata memory-only
struct BluetoothPlayerInfo {
  std::string streamId;
  bool playing = false;
  MediaMetadata metadata;
  std::uint64_t durationMs = 0;
  std::uint64_t positionMs = 0;
};

// what the receiver drives, all on the loop thread
struct BluetoothReceiverHooks {
  // BluetoothControl{stream_id, action} to cadenced
  std::function<void(std::string_view streamId, MediaAction action)> control;
  // readable is called whenever fd has data or hung up; false when it cannot be watched
  std::function<bool(int fd, std::function<void()> readable)> watch;
  std::function<void()> unwatch;
  std::function<std::chrono::steady_clock::time_point()> now = [] { return std::chrono::steady_clock::now(); };
};

// bluetooth receive (the bluetooth capability's audio, spec "audiod"), on the loop thread alone: cadenced hands over a
// phone's pcm fd (bluealsa's fifo, s16le at the stream's rate and channels), which is drained without blocking for as
// long as it is open, whatever the session does, so bluealsa never stalls on it. its audio goes to music while the
// source holds the output, the gate is open and nothing paused it, else it is dropped. the loop thread is the music
// input's one producer while the output is open, so a close or pause returns with no push under way.
//
// it reports playing while pcm flows and the player (if the phone has one) says playing, paused when either stops (no
// pcm for kBluetoothSilence), ended when the pcm reads eof, its stream ends or receive is disabled. a pause (the
// sessions' own, a displacement, a shut gate, cadenced's command) sends pause to the phone and holds the audio until a
// resume, a new phone's stream, or the phone's player turning playing again. the newest stream wins; one with the id
// already open only replaces the fd (a codec change). logs carry the stream id, codes and counts: never the sender or
// metadata
class BluetoothReceiver {
 public:
  BluetoothReceiver(const Log& log, BluetoothReceiverHooks hooks);
  ~BluetoothReceiver();

  BluetoothReceiver(const BluetoothReceiver&) = delete;
  BluetoothReceiver& operator=(const BluetoothReceiver&) = delete;

  // the sessions' handle (the attached source's)
  void events(std::shared_ptr<MediaSourceEvents> events) { events_ = std::move(events); }

  // BluetoothConfig: disabled drops the stream, refuses new ones and forgets the player
  void configure(bool enabled);
  // takes the fd whatever it decides: a refused one is closed
  void stream(const BluetoothStreamInfo& info, UniqueFd fd);
  void streamEnd(std::string_view id);
  void player(const BluetoothPlayerInfo& info);
  // the playback gate (rule 7): no audio goes to music while it is shut, whatever the session does
  void gate(bool open);
  // cadenced is gone: its streams with it, and disabled until it configures again
  void reset();
  // drops everything without a report: the part is going
  void shutdown();

  // the fd's watch
  void readable();
  // the silence check, on a timer while a stream is open
  void tick();
  [[nodiscard]] bool hasStream() const { return stream_.has_value(); }

  // the source's calls (see MediaSource)
  [[nodiscard]] MediaActions actions() const;
  void openOutput(PcmInput& music);
  void closeOutput();
  std::string_view pause();
  std::string_view resume();
  std::string_view stop();
  std::string_view next();
  std::string_view previous();

 private:
  struct Stream {
    BluetoothStreamInfo info;
    UniqueFd fd;
    // pcm came within kBluetoothSilence
    bool flowing = false;
    std::chrono::steady_clock::time_point lastData{};
    std::uint64_t bytes = 0;
    std::uint64_t pushedFrames = 0;
    std::uint64_t droppedFrames = 0;
    // the start of a frame whose rest has not come yet
    std::vector<std::uint8_t> partial;
  };
  enum class Reported : std::uint8_t { None, Playing, Paused };

  void endStream(std::string_view reason, bool report);
  void deliver(std::span<const std::int16_t> samples);
  void hold();
  void control(MediaAction action);
  // what it reports follows what it hears and is told
  void update();
  void publishMetadata();
  [[nodiscard]] bool playerPlaying() const;
  [[nodiscard]] const BluetoothPlayerInfo* matchingPlayer() const;

  const Log& log_;
  BluetoothReceiverHooks hooks_;
  std::shared_ptr<MediaSourceEvents> events_;
  bool enabled_ = false;
  bool gate_ = false;
  // a pause the phone has not been seen to lift
  bool held_ = false;
  PcmInput* output_ = nullptr;
  std::optional<Stream> stream_;
  // the latest player, whichever stream it names: it may come before its stream
  std::optional<BluetoothPlayerInfo> player_;
  Reported reported_ = Reported::None;
  std::optional<MediaMetadata> sentMetadata_;
  std::uint64_t sentDuration_ = 0;
  std::optional<std::uint64_t> sentPosition_;
  std::vector<std::uint8_t> chunk_;
  std::vector<std::int16_t> samples_;
};

}  // namespace cadence::audiod
