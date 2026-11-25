#pragma once

#include "audiod/daemon.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/media-handlers.h"
#include "audiod/playback.h"
#include "audiod/soloist-supervisor.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <cstdint>
#include <deque>
#include <memory>
#include <optional>
#include <string>

namespace cadence::audiod {

// soloist over the socket (M4c-D2). SoloistConfig configures the supervisor, SoloistSwitch starts the installed build
// once nothing plays, SoloistProbe runs a build's --version in the jail and is answered by SoloistProbeResult with its
// id. the playback gate (rule 7) reaches the supervisor as it opens and shuts: soloist starts only while it is open,
// and keeps running when it shuts. soloist's source is attached to the media sessions; SenderVolume{soloist, level}
// goes out for each deliberate slider move, and SenderVolumeSet{soloist, level} is the speaker's level for soloist.
//
// SoloistStatus goes out, the latest one, at each SoloistConfig (cadenced sends one after every Ready, so a restarted
// cadenced learns it), and on change after the connection's first; a cadenced that sends none gets none, and no probe
// result either (soloist_probe_dropped). a status the client's full socket refused goes out again, the latest one,
// once it has room, and so do the probe results it refused, in order. soloist outlives cadenced: the config the next
// one sends changes nothing when it is the same
class SoloistHandlers final : public Part {
 public:
  SoloistHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback, MediaHandlers& media,
                  SoloistOptions options = {});
  // stops soloist before the part goes
  ~SoloistHandlers() override;

  SoloistHandlers(const SoloistHandlers&) = delete;
  SoloistHandlers& operator=(const SoloistHandlers&) = delete;

  void configure(const Configure& configuration) override;
  void reset() override;

 private:
  void config(const cadence_audiod_v1_SoloistConfig& request);
  void probe(const cadence_audiod_v1_SoloistProbe& request);
  void senderVolume(std::uint8_t level);
  // the supervisor's reports, on the loop thread
  void status(const SoloistStatus& status);
  void probed(const std::string& requestId, const SoloistProbe& probe);
  // the status when it changed, then the probe results, as far as the socket takes them
  void flush();

  Loop& loop_;
  IpcServer& ipc_;
  const Log& log_;
  std::optional<SoloistStatus> status_;
  // this connection sent its config
  bool announced_ = false;
  // a status this connection has not got yet
  bool statusPending_ = false;
  // probe results the socket refused, oldest first
  std::deque<Envelope> results_;
  std::optional<Loop::TimerId> resend_;
  // last: its thread posts into the members above until it is stopped. shared with the source the sessions keep
  std::shared_ptr<SoloistSupervisor> supervisor_;
};

}  // namespace cadence::audiod
