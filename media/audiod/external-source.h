#pragma once

#include "audiod/group-feed.h"
#include "audiod/media-sessions.h"
#include "audiod/media-source.h"
#include "audiod/ring-pump.h"
#include "backend/pcm-reader-slot.h"
#include "ring/ring-input.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::audiod {

// a session audiod does not decode, registered by cadenced (ExternalSource): a screen's player, airplay mirroring, a
// handoff. it stands in for that player in the sessions, so it takes the speaker when it reports playing and loses it
// like any other source: what the sessions ask of it (a pause when another source took the speaker, or the gate shut)
// goes back to cadenced as an ExternalAction, which the player carries out and reports in its next state.
//
// a MIXER session with a ring (mpv's ao_cadence) is heard through the backend's external slot while it holds the
// speaker and read nowhere else; its own gain (a duck or a mute of that session alone) ramps on the ring. on a group's
// leader (its output opened on the leader's feed, which groupFeed names) a ring pump reads the ring into the feed
// instead, and the session's own gain stays off (the device's mixer ducks what the group plays)
class ExternalSource final : public MediaSource {
 public:
  // what audiod asks of the player; positionMs is a seek's target (0 for any other action), reason a token
  // (displaced, gated) or empty for cadenced's own command
  using Act = std::function<void(MediaAction action, std::uint64_t positionMs, std::string_view reason)>;

  // groupFeed: where the leader's feed is found while this device leads a group (ExternalSources'), or null
  ExternalSource(std::string id, MediaKind kind, std::shared_ptr<MediaSourceEvents> events, Act act,
                 PcmReaderSlot& slot, std::unique_ptr<RingInput> ring, GroupFeed* const* groupFeed = nullptr);
  ~ExternalSource() override;

  ExternalSource(const ExternalSource&) = delete;
  ExternalSource& operator=(const ExternalSource&) = delete;

  // the player's state as cadenced reports it: playing takes the speaker, idle ends the session. positionMs is where the
  // player said it is (none when it said nothing): the session's position, which a group leader's epoch of its ring
  // starts at (multiroom P4)
  void update(PlayState state, AudioRoute route, AudioControls controls,
              std::optional<std::uint64_t> positionMs = std::nullopt);
  // null on any route but MIXER, or in a build without rings
  [[nodiscard]] RingInput* ring() const { return ring_.get(); }
  [[nodiscard]] PlayState state() const { return state_; }

  [[nodiscard]] MediaKind kind() const override { return kind_; }
  // a seek travels as an ExternalAction with its position, as every other action does
  [[nodiscard]] MediaActions actions() const override;
  [[nodiscard]] bool keepsPaused() const override { return true; }
  [[nodiscard]] std::string_view sessionId() const override { return id_; }
  [[nodiscard]] AudioRoute audioRoute() const override { return route_; }
  [[nodiscard]] AudioControls audioControls() const override { return controls_; }
  void setAsideBy(std::string_view reason) override { reason_ = reason; }

  void openOutput(PcmInput& music) override;
  void closeOutput() override;
  // the group's leader goes: a pump reading the ring stops (the output opens again on the music input next)
  void leaveGroupFeed();
  [[nodiscard]] bool pumping() const { return pump_ != nullptr; }

  std::string_view pause() override;
  std::string_view resume() override;
  std::string_view stop() override;
  std::string_view seek(std::uint64_t ms) override;
  std::string_view next() override;
  std::string_view previous() override;

 private:
  std::string_view ask(MediaAction action, std::uint64_t positionMs = 0);

  std::string id_;
  MediaKind kind_;
  std::shared_ptr<MediaSourceEvents> events_;
  Act act_;
  PcmReaderSlot& slot_;
  std::unique_ptr<RingInput> ring_;
  GroupFeed* const* groupFeed_;
  // while the output is open on a group leader's feed
  std::unique_ptr<RingPump> pump_;
  AudioRoute route_ = AudioRoute::None;
  AudioControls controls_;
  PlayState state_ = PlayState::Idle;
  // the position the session last heard of
  std::optional<std::uint64_t> positionMs_;
  bool output_ = false;
  // a fixed token, or empty
  std::string_view reason_;
};

}  // namespace cadence::audiod
