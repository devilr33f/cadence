#pragma once

#include "audiod/airplay-control.h"
#include "audiod/airplay-supervisor.h"
#include "audiod/daemon.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/media-handlers.h"
#include "audiod/playback.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <cstdint>
#include <memory>
#include <optional>

namespace cadence::audiod {

// the airplay receiver over the socket (M4d-D2, D5, D7). AirplayConfig configures the supervisor; the playback gate
// (rule 7) reaches it as it opens and shuts: the receiver starts only while it is open, and keeps running when it
// shuts, its audio dropped. the receiver's source is attached to the media sessions; SenderVolume{airplay, level} goes
// out for each deliberate slider move, and SenderVolumeSet{airplay, level} is the speaker's level for it.
//
// AirplayStatus goes out, the latest one, at each AirplayConfig (cadenced sends one after every Ready, so a restarted
// cadenced learns it), and on change after the connection's first; a cadenced that sends none gets none. a status the
// client's full socket refused goes out again, the latest one, once it has room. the receiver outlives cadenced: the
// config the next one sends changes nothing when it is the same
class AirplayHandlers final : public Part {
 public:
  AirplayHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback, MediaHandlers& media,
                  AirplayOptions options = {}, AirplayControlTimings control = {});
  // stops the receiver before the part goes
  ~AirplayHandlers() override;

  AirplayHandlers(const AirplayHandlers&) = delete;
  AirplayHandlers& operator=(const AirplayHandlers&) = delete;

  void configure(const Configure& configuration) override;
  void reset() override;

 private:
  void config(const cadence_audiod_v1_AirplayConfig& request);
  // the control's and the supervisor's reports, on the loop thread
  void senderVolume(std::uint8_t level);
  void status(const AirplayStatus& status);
  void flush();

  Loop& loop_;
  IpcServer& ipc_;
  const Log& log_;
  std::optional<AirplayStatus> status_;
  // this connection sent its config
  bool announced_ = false;
  // a status this connection has not got yet
  bool statusPending_ = false;
  std::optional<Loop::TimerId> resend_;
  // last: its thread posts into the members above until it is stopped. both shared with the source the sessions keep
  std::shared_ptr<AirplaySupervisor> supervisor_;
  std::shared_ptr<AirplayControl> control_;
};

}  // namespace cadence::audiod
