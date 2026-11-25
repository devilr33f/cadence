#pragma once

#include <string>

namespace cadence::screen::test {

// a golden screen/v1 frame's bytes (proto/fixtures/screen/v1/<name>.binpb)
std::string fixture(const std::string& name);
// a test clip made by make-clips.sh, from CADENCE_SCREEN_CLIPS; the test fails when it is not there
std::string clip(const std::string& name);
// a fresh socket path in a temporary directory
std::string socketPath(const std::string& tag);

}  // namespace cadence::screen::test
