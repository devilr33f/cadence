#include "audiod/external-source.h"

#include <utility>

namespace cadence::audiod {

ExternalSource::ExternalSource(std::string id, MediaKind kind, std::shared_ptr<MediaSourceEvents> events, Act act,
                               PcmReaderSlot& slot, std::unique_ptr<RingInput> ring, GroupFeed* const* groupFeed)
    : id_(std::move(id)),
      kind_(kind),
      events_(std::move(events)),
      act_(std::move(act)),
      slot_(slot),
      ring_(std::move(ring)),
      groupFeed_(groupFeed) {}

ExternalSource::~ExternalSource() { closeOutput(); }

void ExternalSource::update(PlayState state, AudioRoute route, AudioControls controls,
                            std::optional<std::uint64_t> positionMs) {
  route_ = route;
  controls_ = controls;
  // the state's position first, so a group leader's epoch that the state starts begins there
  if (positionMs && positionMs != positionMs_ && state != PlayState::Idle) {
    positionMs_ = positionMs;
    events_->position(*positionMs);
  }
  if (state == state_)
    return;
  state_ = state;
  switch (state) {
    case PlayState::Playing:
      events_->playing();
      return;
    case PlayState::Paused:
      events_->paused();
      return;
    case PlayState::Buffering:
      events_->buffering();
      return;
    case PlayState::Idle:
      events_->ended();
      return;
  }
}

MediaActions ExternalSource::actions() const {
  return {MediaAction::Pause, MediaAction::Resume, MediaAction::Stop, MediaAction::Seek, MediaAction::Next,
          MediaAction::Previous};
}

void ExternalSource::openOutput(PcmInput& music) {
  output_ = true;
  if (!ring_)
    return;
  ring_->setOpen(true);
  // the music target is a group leader's feed: the group hears the ring, the mixer reads it nowhere
  if (groupFeed_ != nullptr && *groupFeed_ != nullptr && &(*groupFeed_)->input() == &music) {
    pump_ = std::make_unique<RingPump>(*ring_, **groupFeed_);
    return;
  }
  slot_.set(ring_.get());
}

void ExternalSource::closeOutput() {
  if (!output_)
    return;
  output_ = false;
  if (!ring_)
    return;
  ring_->setOpen(false);
  if (pump_) {
    pump_.reset();
    return;
  }
  slot_.clear();
}

void ExternalSource::leaveGroupFeed() { pump_.reset(); }

std::string_view ExternalSource::ask(MediaAction action, std::uint64_t positionMs) {
  const auto reason = std::exchange(reason_, std::string_view{});
  act_(action, positionMs, reason);
  return kMediaOk;
}

std::string_view ExternalSource::pause() { return ask(MediaAction::Pause); }
std::string_view ExternalSource::resume() { return ask(MediaAction::Resume); }
std::string_view ExternalSource::stop() { return ask(MediaAction::Stop); }
std::string_view ExternalSource::seek(std::uint64_t ms) { return ask(MediaAction::Seek, ms); }
std::string_view ExternalSource::next() { return ask(MediaAction::Next); }
std::string_view ExternalSource::previous() { return ask(MediaAction::Previous); }

}  // namespace cadence::audiod
