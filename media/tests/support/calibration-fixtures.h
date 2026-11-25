#pragma once

namespace cadence::sync::calibration::test {

// writes the golden files when CADENCE_UPDATE_FIXTURES=1, otherwise compares them byte-for-byte in meaning
void checkCalibrationFixtures();

}  // namespace cadence::sync::calibration::test
