#pragma once

#include <optional>
#include <string>
#include <string_view>

namespace cadence::screen::app {

// a number under a top-level key of a json object (display.screen's "overscan"), which cadenced already validated
// against its schema; nullopt when absent or not a number. no json library for one number
std::optional<double> topLevelNumber(std::string_view json, std::string_view key);
// a string under a top-level key (cadenced's config.json "serverUrl"); nullopt when absent, not a string, or holding
// a \u or control escape (no url has one)
std::optional<std::string> topLevelString(std::string_view json, std::string_view key);

}  // namespace cadence::screen::app
