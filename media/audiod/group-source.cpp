#include "audiod/group-source.h"

#include <algorithm>
#include <cstdlib>
#include <string>

namespace cadence::audiod {

namespace {
// a heard position this far from where the session runs on is told again
constexpr std::int64_t kHeardDriftMs = 1000;
}  // namespace

GroupSource::GroupSource(std::shared_ptr<MediaSourceEvents> events, Control control, Activate activate,
                         std::function<void()> destroyed)
    : events_(std::move(events)),
      control_(std::move(control)),
      activate_(std::move(activate)),
      destroyed_(std::move(destroyed)) {}

GroupSource::~GroupSource() {
  if (open_ && activate_)
    activate_(false);
  if (destroyed_)
    destroyed_();
}

MediaActions GroupSource::actions() const {
  if (seekable_)
    return {MediaAction::Pause, MediaAction::Resume, MediaAction::Stop, MediaAction::Seek, MediaAction::Next,
            MediaAction::Previous};
  return {MediaAction::Pause, MediaAction::Resume, MediaAction::Stop, MediaAction::Next, MediaAction::Previous};
}

void GroupSource::openOutput(PcmInput& /*music*/) {
  open_ = true;
  local_ = false;
  if (activate_)
    activate_(true);
}

void GroupSource::closeOutput() {
  open_ = false;
  if (activate_)
    activate_(false);
}

void GroupSource::setAsideBy(std::string_view /*reason*/) { local_ = true; }

std::string_view GroupSource::send(cadence_sync_v1_ControlAction action, std::uint64_t positionMs) {
  return control_ && control_(action, positionMs) ? kMediaOk : kMediaFailed;
}

std::string_view GroupSource::pause() {
  // set aside on this device alone: the group plays on for the others
  if (local_) {
    local_ = false;
    return kMediaOk;
  }
  return send(cadence_sync_v1_ControlAction_CONTROL_ACTION_PAUSE);
}

std::string_view GroupSource::resume() { return send(cadence_sync_v1_ControlAction_CONTROL_ACTION_RESUME); }

std::string_view GroupSource::stop() {
  if (local_) {
    local_ = false;
    return kMediaOk;
  }
  return send(cadence_sync_v1_ControlAction_CONTROL_ACTION_STOP);
}

std::string_view GroupSource::seek(std::uint64_t ms) { return send(cadence_sync_v1_ControlAction_CONTROL_ACTION_SEEK, ms); }

std::string_view GroupSource::next() { return send(cadence_sync_v1_ControlAction_CONTROL_ACTION_NEXT); }

std::string_view GroupSource::previous() { return send(cadence_sync_v1_ControlAction_CONTROL_ACTION_PREVIOUS); }

void GroupSource::leaderState(const cadence_sync_v1_Metadata& m) {
  MediaMetadata metadata;
  metadata.title = m.title;
  metadata.artist = m.artist;
  metadata.album = m.album;
  metadata.artworkUrl = m.artwork_url;
  events_->metadata(metadata);
  events_->duration(m.duration_ms);
  seekable_ = m.duration_ms > 0;
  if (m.playing && !playing_) {
    playing_ = true;
    events_->playing();
  } else if (!m.playing && playing_) {
    playing_ = false;
    heardEpoch_.reset();
    // a leader with no session at all ended it; one that holds a paused one paused
    if (metadata.title.empty() && m.duration_ms == 0)
      events_->ended();
    else
      events_->paused();
  }
}

void GroupSource::heard(std::uint32_t epoch, std::uint64_t mediaMs, std::int64_t monoNs) {
  if (!playing_)
    return;
  if (heardEpoch_ == epoch) {
    const auto ranMs = (monoNs - heardMonoNs_) / 1'000'000;
    const auto expected = static_cast<std::int64_t>(heardMs_) + std::max<std::int64_t>(ranMs, 0);
    if (std::llabs(expected - static_cast<std::int64_t>(mediaMs)) < kHeardDriftMs)
      return;
  }
  heardEpoch_ = epoch;
  heardMs_ = mediaMs;
  heardMonoNs_ = monoNs;
  events_->position(mediaMs);
}

void GroupSource::gone() {
  playing_ = false;
  heardEpoch_.reset();
  events_->ended();
}

}  // namespace cadence::audiod
