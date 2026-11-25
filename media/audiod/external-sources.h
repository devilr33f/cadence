#pragma once

#include "audiod/external-source.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/media-sessions.h"
#include "audiod/playback.h"
#include "ring/ring-input.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <chrono>
#include <cstdint>
#include <functional>
#include <map>
#include <memory>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::audiod {

// makes the ring a MIXER external session's player writes; null when this build has none (until ao_cadence ships,
// stream E) or it could not be made. the session is heard through it alone
// burstFrames is what the output reads at a time (0 when none opened yet), which sizes the ring
// (RingInput::capacityFor)
using RingFactory = std::function<std::unique_ptr<RingInput>(std::size_t burstFrames)>;

// rings are off unless the build asks for them
std::unique_ptr<RingInput> noRings(std::size_t burstFrames);
// a ring sized for the burst, or none when it cannot be made
std::unique_ptr<RingInput> sizedRing(std::size_t burstFrames);

// the sessions cadenced registers (ExternalSource) and the controls it sends for any session (SessionControl), on the
// loop thread.
//
// ExternalSource UPDATE adds a session under cadenced's id or updates it: its state drives the sessions (playing takes
// the speaker, idle ends it), and what the sessions ask of it goes back as ExternalAction{id, action, reason}.
// UNREGISTER removes it, ending its session. cadenced going away removes them all: they were its.
//
// SessionControl is answered by one CommandResult. for the whole mix (an empty session id) the duck, the level, the
// eq and the mute apply in the mixer, and go back to the session's player as a RouteControl too when the session
// holds the speaker on the DIRECT route. for one session: a MIXER external one takes a duck and a mute on its own
// ring; a DIRECT one gets the control back as a RouteControl when it lists it; audiod's own session takes the duck,
// as the whole mix's. anything else is unsupported, and an unknown id is no_session. a duck never raises a level
// (rule 7): its level is clamped to 0..1 of what it ducks
class ExternalSources {
 public:
  // with a loop, each ring session's counters are logged every kRingStatsEvery while it holds the speaker, and once
  // when it ends (external_ring: counts, the ring's fill in ms, the writer's state, the gate and the ducks)
  ExternalSources(IpcServer& ipc, const Log& log, MediaSessions& sessions, Playback& playback, RingFactory rings,
                  Loop* loop = nullptr);

  static constexpr std::chrono::seconds kRingStatsEvery{10};
  ~ExternalSources();

  ExternalSources(const ExternalSources&) = delete;
  ExternalSources& operator=(const ExternalSources&) = delete;

  // cadenced is gone: every session it registered goes
  void reset();
  // the ring of a registered MIXER session, for a player in this process (android); null for none
  [[nodiscard]] RingInput* ring(std::string_view id) const;
  // every ring a registered session holds, for a player in another process (the tap's RingOffer)
  void eachRing(const std::function<void(std::string_view id, const RingInput& ring)>& visit) const;
  // the group leader's feed while this device leads (GroupHandlers'), or null: a MIXER session whose output opens on
  // it is read by a ring pump. null stops every pump, before the leader goes. on the loop thread
  void useGroupFeed(GroupFeed* feed);
  [[nodiscard]] GroupFeed* groupFeed() const { return groupFeed_; }
  // told of each ring as a new session makes it, on the loop thread
  void onRing(std::function<void(std::string_view id, const RingInput& ring)> listener) { onRing_ = std::move(listener); }

 private:
  struct Registered {
    std::uint64_t serial = 0;
    ExternalSource* source = nullptr;
  };

  void update(const cadence_audiod_v1_ExternalSource& request);
  void control(const cadence_audiod_v1_SessionControl& request);
  [[nodiscard]] std::string_view mix(const cadence_audiod_v1_SessionControl& request);
  [[nodiscard]] std::string_view session(ExternalSource& source, const cadence_audiod_v1_SessionControl& request);
  void routeBack(std::string_view id, const cadence_audiod_v1_SessionControl& request);
  void act(const std::string& id, MediaAction action, std::uint64_t positionMs, std::string_view reason);
  void answer(std::string_view requestId, std::string_view code);
  // the ring's counters as a line (rule 8: counts, ms and tokens only); phase is tick or end
  void logRing(std::string_view id, const RingInput& ring, std::string_view phase) const;
  void armStats();
  void statsTick();

  IpcServer& ipc_;
  const Log& log_;
  MediaSessions& sessions_;
  Playback& playback_;
  RingFactory rings_;
  Loop* loop_;
  std::optional<Loop::TimerId> stats_;
  std::function<void(std::string_view, const RingInput&)> onRing_;
  std::map<std::string, Registered, std::less<>> registered_;
  GroupFeed* groupFeed_ = nullptr;
};

}  // namespace cadence::audiod
