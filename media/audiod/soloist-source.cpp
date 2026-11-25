#include "audiod/soloist-source.h"

#include <optional>
#include <string>
#include <utility>

namespace cadence::audiod {

namespace {

std::optional<soloist::Command> commandFor(MediaAction action) {
  switch (action) {
    case MediaAction::Pause:
      return soloist::Command::Pause;
    case MediaAction::Resume:
      return soloist::Command::Play;
    case MediaAction::Next:
      return soloist::Command::SkipNext;
    case MediaAction::Previous:
      return soloist::Command::SkipPrev;
    case MediaAction::Stop:
    case MediaAction::Seek:
      return std::nullopt;
  }
  return std::nullopt;
}

}  // namespace

SoloistSource::SoloistSource(std::shared_ptr<SoloistSupervisor> supervisor, Post post)
    : supervisor_(std::move(supervisor)), post_(std::move(post)) {}

MediaActions SoloistSource::actions() const {
  return {MediaAction::Pause, MediaAction::Resume, MediaAction::Next, MediaAction::Previous};
}

void SoloistSource::openOutput(PcmInput& music) { supervisor_->openOutput(music); }

void SoloistSource::closeOutput() { supervisor_->closeOutput(); }

std::string_view SoloistSource::pause() {
  supervisor_->control(soloist::Command::Pause, nullptr);
  return kMediaOk;
}

std::string_view SoloistSource::resume() {
  supervisor_->control(soloist::Command::Play, nullptr);
  return kMediaOk;
}

std::string_view SoloistSource::next() {
  supervisor_->control(soloist::Command::SkipNext, nullptr);
  return kMediaOk;
}

std::string_view SoloistSource::previous() {
  supervisor_->control(soloist::Command::SkipPrev, nullptr);
  return kMediaOk;
}

std::string_view SoloistSource::command(MediaAction action, std::uint64_t positionMs, CommandDone done) {
  const auto command = commandFor(action);
  if (!command)
    return MediaSource::command(action, positionMs, std::move(done));
  // the supervisor answers on its own thread
  supervisor_->control(*command, [post = post_, alive = std::weak_ptr<char>(alive_),
                                  done = std::move(done)](std::string_view code) {
    post([alive, done, code = std::string(code)] {
      if (alive.lock())
        done(code);
    });
  });
  return kMediaPending;
}

}  // namespace cadence::audiod
