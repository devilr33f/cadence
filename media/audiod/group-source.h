#pragma once

#include "audiod/media-source.h"

#include "cadence/sync/v1/sync.pb.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <string_view>

namespace cadence::audiod {

// a group member's GROUP session: the leader's stream, which the member's playout places into the music input while
// this source holds the session (open output: the playout plays; closed: it lets go of the input before closeOutput
// returns). it takes the session when the leader says it plays, as any newer session does, and waits paused when
// displaced. a transport command goes to the leader as Control, which answers with its state in Metadata; on the loop
// thread
class GroupSource final : public MediaSource {
 public:
  using Control = std::function<bool(cadence_sync_v1_ControlAction action, std::uint64_t positionMs)>;
  using Activate = std::function<void(bool active)>;

  // destroyed runs as the sessions let it go
  GroupSource(std::shared_ptr<MediaSourceEvents> events, Control control, Activate activate,
              std::function<void()> destroyed = {});
  ~GroupSource() override;

  [[nodiscard]] MediaKind kind() const override { return MediaKind::Group; }
  [[nodiscard]] MediaActions actions() const override;
  [[nodiscard]] bool keepsPaused() const override { return true; }
  void openOutput(PcmInput& music) override;
  void closeOutput() override;
  std::string_view pause() override;
  std::string_view resume() override;
  std::string_view stop() override;
  std::string_view seek(std::uint64_t ms) override;
  std::string_view next() override;
  std::string_view previous() override;
  void setAsideBy(std::string_view reason) override;

  // the leader's Metadata: what plays and whether it does (memory only, D11)
  void leaderState(const cadence_sync_v1_Metadata& metadata);
  // the group is gone (disabled, another role): the session ends
  void gone();
  // where the group is as this member hears it (the playout's last frame: its epoch and the source's media position, at
  // a CLOCK_MONOTONIC instant): a new epoch (a start, a seek, the next track, a resume) or a drift past a second from
  // where the session runs on is the session's position, so the member's screen shows the source's
  void heard(std::uint32_t epoch, std::uint64_t mediaMs, std::int64_t monoNs);

 private:
  std::string_view send(cadence_sync_v1_ControlAction action, std::uint64_t positionMs = 0);

  std::shared_ptr<MediaSourceEvents> events_;
  Control control_;
  Activate activate_;
  std::function<void()> destroyed_;
  bool open_ = false;
  bool playing_ = false;
  // a pause or stop the sessions make because another source took the speaker (or the gate shut) stays here: it is
  // this device's, not the group's
  bool local_ = false;
  bool seekable_ = false;
  // the position last told and when: none since the session last played
  std::optional<std::uint32_t> heardEpoch_;
  std::uint64_t heardMs_ = 0;
  std::int64_t heardMonoNs_ = 0;
};

}  // namespace cadence::audiod
