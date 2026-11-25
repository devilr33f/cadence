#pragma once

#include <optional>
#include <string>

namespace cadence {

// bounded single-line text naming the device or control; audiod logs codes only, never this
// a read-back that differs from what was written, in the control's raw units
struct ReadBackMismatch {
  long wanted = 0;
  long held = 0;
  int channel = 0;
};

struct VolumeError {
  std::string detail;
  std::optional<ReadBackMismatch> mismatch;
};

// the board's hardware volume control, in percent of its range
class VolumeControl {
 public:
  virtual ~VolumeControl() = default;
  [[nodiscard]] virtual std::optional<VolumeError> setPercent(int percent) = 0;
  // reads the control back: an error unless it now holds what setPercent(percent) writes
  [[nodiscard]] virtual std::optional<VolumeError> verifyPercent(int percent) = 0;
};

}  // namespace cadence
