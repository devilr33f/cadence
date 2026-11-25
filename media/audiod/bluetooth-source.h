#pragma once

#include "audiod/bluetooth-receiver.h"
#include "audiod/media-source.h"
#include "backend/pcm-input.h"

#include <cstdint>
#include <memory>
#include <string_view>

namespace cadence::audiod {

// the bluetooth receiver as a media source, attached to the sessions once and kept (like airplay's): it waits paused
// while another source plays or the gate is shut, and idles while no phone streams. every call goes to the receiver,
// which it shares with the part, so it outlives the part on the way down. its commands go to the phone as
// BluetoothControl and are answered ok at once: the phone's next BluetoothPlayer says what happened
class BluetoothSource final : public MediaSource {
 public:
  explicit BluetoothSource(std::shared_ptr<BluetoothReceiver> receiver) : receiver_(std::move(receiver)) {}

  [[nodiscard]] MediaKind kind() const override { return MediaKind::Bluetooth; }
  [[nodiscard]] MediaActions actions() const override { return receiver_->actions(); }
  [[nodiscard]] bool keepsPaused() const override { return true; }

  void openOutput(PcmInput& music) override { receiver_->openOutput(music); }
  void closeOutput() override { receiver_->closeOutput(); }

  std::string_view pause() override { return receiver_->pause(); }
  std::string_view resume() override { return receiver_->resume(); }
  std::string_view stop() override { return receiver_->stop(); }
  std::string_view seek(std::uint64_t /*ms*/) override { return kMediaUnsupported; }
  std::string_view next() override { return receiver_->next(); }
  std::string_view previous() override { return receiver_->previous(); }

 private:
  std::shared_ptr<BluetoothReceiver> receiver_;
};

}  // namespace cadence::audiod
