#pragma once

#include "audiod/daemon.h"
#include "audiod/external-sources.h"
#include "audiod/group-clock.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/playback.h"
#include "audiod/sys.h"
#include "backend/pcm-snapshots.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <chrono>
#include <cstdint>
#include <functional>
#include <optional>
#include <span>
#include <string>
#include <vector>

namespace cadence::audiod {

// how often the tap sends what changed: a pcm snapshot about 60 times a second, the clock 4 times
inline constexpr std::chrono::milliseconds kTapPcmEvery{16};
inline constexpr std::chrono::milliseconds kTapClockEvery{250};

// the screen's tap (TapConfig): a SOCK_SEQPACKET socket of its own at the path cadenced names (an abstract name when
// it starts with @, else a path made 0600), one client, the screen. it carries TapEnvelope frames only, never an
// Envelope: pcm snapshots of the mix after the limiter while pcm is on, the 16 bands of each viz frame while bands is
// on (the viz itself is MediaSettings'), the presentation clock while mirror is on and a source anchored it, and a
// group's media clock (GroupClock) while this device plays a multiroom group's stream (multiroom P4). the tap sends only: anything the client writes is read away, and a client that stops reading loses frames. a config
// with another path moves it, an empty one closes it, and cadenced going away closes it too. audio leaves audiod
// here alone, for the visualizer, and only to the client of the socket cadenced named (rule 8: nothing is stored).
// with useRings, the tap also offers the client every MIXER session's pcm ring (RingOffer, its memfd and eventfd as
// SCM_RIGHTS): each one as it is made, and all of them to a client that connects, so a screen's player in another
// process writes into the mixer
class TapServer final : public Part {
 public:
  TapServer(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback);
  ~TapServer() override;

  TapServer(const TapServer&) = delete;
  TapServer& operator=(const TapServer&) = delete;

  void configure(const Configure& configuration) override;
  void reset() override;
  // offers externals' rings to the tap's client; set before the loop runs
  void useRings(ExternalSources& externals);

  enum class Sent : std::uint8_t { Sent, Dropped, NoClient };
  // one encoded TapEnvelope (an airplay mirror frame, larger than any other) as one message, while mirror is on: Dropped
  // when the client's socket is full or the frame is larger than it takes (about twice the system's wmem_max), and
  // NoClient without a client or with mirror off. on the loop thread
  Sent sendMirror(std::span<const std::uint8_t> encoded);
  // each runs when a client connects (the newest screen), and when mirror is turned on for one; register before the
  // loop runs
  void onClient(std::function<void()> hook) { clientHooks_.push_back(std::move(hook)); }
  // the group's media clock at a CLOCK_MONOTONIC instant (GroupHandlers::heardClock), sent kTapClockEvery while it says
  // one and at once when its epoch or its playing changes; set before the loop runs
  using GroupClockSource = std::function<std::optional<GroupClockSample>(std::int64_t nowMono)>;
  void useGroupClock(GroupClockSource source) { groupClock_ = std::move(source); }

 private:
  void apply(const cadence_audiod_v1_TapConfig& config);
  bool listenAt(const std::string& path);
  void close();
  void accept();
  void drain();
  void dropClient();
  void arm();
  void tick();
  void sendGroupClock(Loop::Clock::time_point now);
  void sendBands(const dsp::VizFrame& frame);
  void offerRing(std::string_view id, const RingInput& ring);
  bool send(const cadence_audiod_v1_TapEnvelope& envelope);

  Loop& loop_;
  const Log& log_;
  Playback& playback_;
  ExternalSources* externals_ = nullptr;
  std::string path_;
  bool pcm_ = false;
  bool bands_ = false;
  bool mirror_ = false;
  UniqueFd listener_;
  std::optional<Loop::WatchId> listenerWatch_;
  UniqueFd client_;
  std::optional<Loop::WatchId> clientWatch_;
  std::optional<Loop::TimerId> timer_;
  std::uint64_t sentSeq_ = 0;
  Loop::Clock::time_point nextClock_{};
  bool stalled_ = false;
  bool mirrorStalled_ = false;
  PcmSnapshot snapshot_;
  std::vector<std::function<void()>> clientHooks_;
  GroupClockSource groupClock_;
  Loop::Clock::time_point nextGroupClock_{};
  // what the last GroupClock said, so a change goes out at once
  std::optional<GroupClockSample> sentGroupClock_;
};

}  // namespace cadence::audiod
