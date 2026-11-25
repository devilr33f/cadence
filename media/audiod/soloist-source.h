#pragma once

#include "audiod/media-source.h"
#include "audiod/soloist-supervisor.h"
#include "backend/pcm-input.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <string_view>

namespace cadence::audiod {

// soloist as a media source (M4c-D8), attached to the sessions once and kept: it waits paused while another source
// plays or the gate is shut, and soloist keeps running meanwhile. pause, resume, next and previous go to the
// supervisor as pause, play, skip_next and skip_prev on soloist's websocket; cadenced's command is answered when
// soloist answers it (control_unsupported when it is not logged in, or answers with an error within 2 s). its output
// is soloist's pcm, which goes into music while it is open and is consumed unheard while it is closed. what it reports
// comes from the supervisor's thread through the events handle
class SoloistSource final : public MediaSource {
 public:
  using Post = std::function<void(std::function<void()>)>;

  // post hands an answer to the loop thread
  SoloistSource(std::shared_ptr<SoloistSupervisor> supervisor, Post post);

  [[nodiscard]] MediaKind kind() const override { return MediaKind::Soloist; }
  [[nodiscard]] MediaActions actions() const override;
  [[nodiscard]] bool keepsPaused() const override { return true; }

  void openOutput(PcmInput& music) override;
  void closeOutput() override;

  // the sessions' own pause (displaced, gate shut) and the rest: sent, never answered
  std::string_view pause() override;
  std::string_view resume() override;
  std::string_view stop() override { return kMediaUnsupported; }
  std::string_view seek(std::uint64_t /*ms*/) override { return kMediaUnsupported; }
  std::string_view next() override;
  std::string_view previous() override;

  std::string_view command(MediaAction action, std::uint64_t positionMs, CommandDone done) override;

 private:
  std::shared_ptr<SoloistSupervisor> supervisor_;
  Post post_;
  // an answer that comes back once the source is gone is dropped
  std::shared_ptr<char> alive_ = std::make_shared<char>();
};

}  // namespace cadence::audiod
