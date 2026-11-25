#pragma once

#include "player/mpv-options.h"

#include <string>
#include <string_view>
#include <vector>

namespace cadence::screen::player {

// one mpv option a media.mpv config key maps to: an option name from our tables only, never from the config itself
struct MpvSettingOption {
  std::string option;
  std::string value;
  // next load: the option rides the loadfile command (mpv takes no runtime set of it, or a new file is the honest
  // moment); false: applied at once through options/<name>
  bool nextLoad = false;
};

// one config key, parsed and mapped
struct MpvSetting {
  // the config's key ("profile", "cacheSecs", ...), what the state reports back. "profile" is the preset, whose
  // options every explicit key of the same option wins over
  std::string key;
  std::vector<MpvSettingOption> options;
};

// media.mpv's config as the screen takes it: the keys it mapped and the keys it refused. the device checks the config
// again although cadenced already validated it against the schema (the server's check is not a guarantee): an unknown
// key, a wrong type or a value out of range is refused here, key by key, and lands in the refused list
struct MpvSettings {
  std::vector<MpvSetting> entries;
  std::vector<std::string> refused;
};

MpvSettings parseMpvSettings(std::string_view json, VideoPath path);

// the next-load options of a parsed config, option name sorted, an explicit key's value winning over the preset's
MpvOptions nextLoadOptions(const MpvSettings& settings);

// media.mpv's state as the screen reports it (screen/v1 CapabilityState): which config keys are applied, which wait
// for the next file, and which were refused (the device's own check, or mpv's answer). the names are the config's keys
struct MpvKeyState {
  std::vector<std::string> applied;
  std::vector<std::string> pending;
  std::vector<std::string> refused;
};

}  // namespace cadence::screen::player
