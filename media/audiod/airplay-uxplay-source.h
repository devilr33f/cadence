#pragma once

#include "audiod/airplay-uxplay-session.h"
#include "audiod/media-source.h"

#include <memory>
#include <string>
#include <string_view>

namespace cadence::audiod::uxplay {

// libairplay's receiver as the media sessions' long-lived source: attached once, waits paused, and reports through the
// session (see Session). it is airplay while a sender streams audio and screencast while one mirrors, its audio on the
// mixer either way. there is no remote control of the sender (dacp needs an mdns browse cadence's dnssd lacks), so:
//   - pause (cadenced's, the gate's) holds it: nothing is heard, the sender streams on, resume lets it be heard again;
//   - displaced by another source, or stopped, it lets the sender go (its connection is dropped and mirroring ends with
//     reason displaced or stopped), so a phone never shows a cast nobody hears or sees;
//   - seek, next and previous are unsupported
class Source final : public MediaSource {
 public:
  explicit Source(std::shared_ptr<Session> session) : session_(std::move(session)) {}

  [[nodiscard]] MediaKind kind() const override {
    return session_->mirroring() ? MediaKind::Screencast : MediaKind::Airplay;
  }
  [[nodiscard]] MediaActions actions() const override {
    return {MediaAction::Pause, MediaAction::Resume, MediaAction::Stop};
  }
  [[nodiscard]] bool keepsPaused() const override { return true; }

  void openOutput(PcmInput& music) override { session_->openOutput(music); }
  void closeOutput() override { session_->closeOutput(); }

  std::string_view pause() override;
  std::string_view resume() override;
  std::string_view stop() override;
  std::string_view seek(std::uint64_t /*ms*/) override { return kMediaUnsupported; }
  std::string_view next() override { return kMediaUnsupported; }
  std::string_view previous() override { return kMediaUnsupported; }

  void setAsideBy(std::string_view reason) override { aside_ = reason; }

 private:
  std::shared_ptr<Session> session_;
  std::string aside_;
};

}  // namespace cadence::audiod::uxplay
