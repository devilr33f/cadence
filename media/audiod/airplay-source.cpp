#include "audiod/airplay-source.h"

#include <string>
#include <utility>

namespace cadence::audiod {

AirplaySource::AirplaySource(AirplaySourceLink link, std::shared_ptr<AirplayControl> control, Post post)
    : link_(std::move(link)), control_(std::move(control)), post_(std::move(post)) {}

MediaActions AirplaySource::actions() const {
  return {MediaAction::Pause, MediaAction::Resume, MediaAction::Next, MediaAction::Previous};
}

void AirplaySource::openOutput(PcmInput& music) { link_.openOutput(music); }

void AirplaySource::closeOutput() { link_.closeOutput(); }

// silent before it returns, whatever the receiver or a release on its way does
std::string_view AirplaySource::pause() {
  send(MediaAction::Pause, nullptr, link_.pauseOutput());
  return kMediaOk;
}

std::string_view AirplaySource::resume() {
  send(MediaAction::Resume, nullptr);
  return kMediaOk;
}

std::string_view AirplaySource::next() {
  send(MediaAction::Next, nullptr);
  return kMediaOk;
}

std::string_view AirplaySource::previous() {
  send(MediaAction::Previous, nullptr);
  return kMediaOk;
}

std::string_view AirplaySource::command(MediaAction action, std::uint64_t positionMs, CommandDone done) {
  switch (action) {
    case MediaAction::Pause:
      send(action, std::move(done), link_.pauseOutput());
      return kMediaPending;
    case MediaAction::Resume:
    case MediaAction::Next:
    case MediaAction::Previous:
      send(action, std::move(done));
      return kMediaPending;
    case MediaAction::Stop:
    case MediaAction::Seek:
      break;
  }
  return MediaSource::command(action, positionMs, std::move(done));
}

void AirplaySource::send(MediaAction action, CommandDone done, std::uint64_t pauseNumber) {
  CommandDone answer;
  if (done) {
    // the control answers on the supervisor's thread
    answer = [post = post_, alive = std::weak_ptr<char>(alive_), done = std::move(done)](std::string_view code) {
      post([alive, done, code = std::string(code)] {
        if (alive.lock())
          done(code);
      });
    };
  }
  link_.run([control = control_, action, answer = std::move(answer), pauseNumber]() mutable {
    control->control(action, std::move(answer), pauseNumber);
  });
}

}  // namespace cadence::audiod
