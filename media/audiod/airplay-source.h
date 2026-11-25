#pragma once

#include "audiod/airplay-control.h"
#include "audiod/media-source.h"
#include "backend/pcm-input.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <string_view>

namespace cadence::audiod {

// what the source drives (the supervisor's, see AirplaySupervisor): the output, from the loop thread, and the
// supervisor's thread, where the control lives
struct AirplaySourceLink {
  // the session's target: the music input, or a group leader's feed
  std::function<void(PcmInput& music)> openOutput;
  std::function<void()> closeOutput;
  // holds the output at once and counts the pause: its number (AirplayOutput::pause)
  std::function<std::uint64_t()> pauseOutput;
  // runs a task on the supervisor's thread
  std::function<void(std::function<void()>)> run;
};

// the airplay receiver as a media source (M4d-D5), attached to the sessions once and kept: it waits paused while
// another source plays or the gate is shut, and the receiver keeps running meanwhile. its output is shairport's pipe,
// which flows into music while it is open and the receiver does not hold it, and is drained unheard otherwise.
//
// a pause holds the output before it returns (the sessions' own, displacement and a shut gate included) and counts it,
// so the session cannot release the output again before it took that pause; the control then takes it on the
// supervisor's thread: paused at once, the receiver asked to Pause and dropped when it plays on (see AirplaySession).
// cadenced's pause is answered once the control took it, resume, next and previous once the receiver answered
// (control_unsupported when it would not, or there is no session). what it reports comes from the control through the
// events handle
class AirplaySource final : public MediaSource {
 public:
  using Post = std::function<void(std::function<void()>)>;

  // post hands an answer to the loop thread
  AirplaySource(AirplaySourceLink link, std::shared_ptr<AirplayControl> control, Post post);

  [[nodiscard]] MediaKind kind() const override { return MediaKind::Airplay; }
  [[nodiscard]] MediaActions actions() const override;
  [[nodiscard]] bool keepsPaused() const override { return true; }

  void openOutput(PcmInput& music) override;
  void closeOutput() override;

  // the sessions' own: taken, never answered
  std::string_view pause() override;
  std::string_view resume() override;
  std::string_view stop() override { return kMediaUnsupported; }
  std::string_view seek(std::uint64_t /*ms*/) override { return kMediaUnsupported; }
  std::string_view next() override;
  std::string_view previous() override;

  std::string_view command(MediaAction action, std::uint64_t positionMs, CommandDone done) override;

 private:
  // the control takes it on the supervisor's thread, a pause with its number; done, when set, is answered on the loop
  // thread
  void send(MediaAction action, CommandDone done, std::uint64_t pauseNumber = 0);

  AirplaySourceLink link_;
  std::shared_ptr<AirplayControl> control_;
  Post post_;
  // an answer that comes back once the source is gone is dropped
  std::shared_ptr<char> alive_ = std::make_shared<char>();
};

}  // namespace cadence::audiod
