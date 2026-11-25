#pragma once

#include "playback/volume-control.h"

#include <optional>
#include <span>
#include <string>

namespace cadence {

// amixer's mapping from percent to a control's raw range, rounded to nearest
long mixerValueForPercent(int percent, long min, long max);

// the first playback channel whose raw read-back is more than one step from the value written, if any; one step
// is a quantizing driver, anything wider a clamp or another writer. held is indexed by channel, -1 marks a
// channel the control lacks
std::optional<ReadBackMismatch> mixerReadBackMismatch(long wanted, std::span<const long> held);

// a playback volume control through the alsa simple mixer, opened for each call so a control another process
// creates after startup (pickle's hardvold creates `Master`) is still found
class AlsaMixer final : public VolumeControl {
 public:
  AlsaMixer(std::string device, std::string control);

  std::optional<VolumeError> setPercent(int percent) override;
  std::optional<VolumeError> verifyPercent(int percent) override;

 private:
  // writes the control, or reads it back and compares when verify is set
  std::optional<VolumeError> apply(int percent, bool verify);

  const std::string device_;
  const std::string control_;
};

}  // namespace cadence
