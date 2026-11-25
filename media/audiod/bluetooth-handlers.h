#pragma once

#include "audiod/bluetooth-receiver.h"
#include "audiod/daemon.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/media-handlers.h"
#include "audiod/playback.h"

#include <memory>
#include <optional>

namespace cadence::audiod {

// bluetooth receive over the socket (spec "audiod"): cadenced owns bluez and bluealsa and hands over each phone's pcm
// as BluetoothStream with its fd; BluetoothStreamEnd, BluetoothPlayer and BluetoothConfig follow, and the receiver's
// transport actions go back as BluetoothControl. the receiver's source is attached to the media sessions, and the
// playback gate (rule 7) reaches it as it opens and shuts. its fd is read on the loop, and a timer checks for silence
// while a stream is open. cadenced going away drops its stream; receive stays off until the next BluetoothConfig
class BluetoothHandlers final : public Part {
 public:
  BluetoothHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback, MediaHandlers& media);
  // drops the stream and its watch before the part goes; the source the sessions keep goes quiet
  ~BluetoothHandlers() override;

  BluetoothHandlers(const BluetoothHandlers&) = delete;
  BluetoothHandlers& operator=(const BluetoothHandlers&) = delete;

  void configure(const Configure& /*configuration*/) override {}
  void reset() override;

 private:
  void armTick();

  Loop& loop_;
  IpcServer& ipc_;
  const Log& log_;
  std::optional<Loop::WatchId> watch_;
  std::optional<Loop::TimerId> tick_;
  // shared with the source the sessions keep
  std::shared_ptr<BluetoothReceiver> receiver_;
};

}  // namespace cadence::audiod
