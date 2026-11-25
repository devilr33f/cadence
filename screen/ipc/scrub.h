#pragma once

#include <string>
#include <string_view>

namespace cadence::screen::ipc {

// a third-party line (mpv, ffmpeg) made fit to ship (rule 8): urls, file paths and quoted strings become <redacted>,
// as they carry what is played (titles in file names, stream urls with their tokens); control characters become
// spaces. the device and the server still scrub known secrets afterwards
std::string scrubThirdParty(std::string_view line);

// true when the token is safe as a log value or code: 1-64 of [a-z0-9_.:-]
bool isLogToken(std::string_view token);

}  // namespace cadence::screen::ipc
