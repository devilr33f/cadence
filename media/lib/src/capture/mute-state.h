#pragma once

#include <cstdint>

namespace cadence {

// one coherent view of the two independent microphone-mute sources. generation increases for either source
// change, including one that leaves the effective OR unchanged; consumers ignore snapshots older than the last
// one they applied
struct MuteState {
  std::uint64_t generation = 0;
  bool physicalMuted = false;
  bool privacyMuted = false;
  bool effectiveMuted = false;

  bool operator==(const MuteState&) const = default;
};

// the capture-side privacy gate used by the daemon's privacy controls. setters return only after the gate is
// coherent, so a caller can publish the returned snapshot before acknowledging its request
class MuteControl {
 public:
  virtual ~MuteControl() = default;
  virtual MuteState setPhysicalMuted(bool muted) = 0;
  virtual MuteState setPrivacyMuted(bool muted) = 0;
  [[nodiscard]] virtual MuteState muteState() const = 0;
};

}  // namespace cadence
