#pragma once

#include "app/routes/system/device.h"
#include "app/ui-layer.h"

#include <cstdint>
#include <functional>
#include <string>

namespace cadence::screen::routes {

// the settings the tv's page switches (design r2 section 24), as display.screen's config last said them: a change
// shows at once and is written with rpc settings.set, and the config sync that follows says them again
struct LocalSettings {
  // s, m, l
  std::string subtitleSize = "m";
  // box, outline
  std::string subtitleStyle = "box";
  bool autoSkip = true;
  double overscan = 0;
  bool reducedMotion = false;
};

// the microphone the owner picked in room and microphone (MicChoice): kept for the process and sent after each
// Hello's Ready; UNSPECIFIED until a pick (cadenced then takes the local mic when there is one, else the speaker)
struct MicPick {
  cadence_v2_MicSourceKind kind = cadence_v2_MicSourceKind_MIC_SOURCE_KIND_UNSPECIFIED;
  std::string deviceId;
};

// what i3's layer and its routes share, made once by registerSystemRoutes: the device's state as the link says it,
// the local settings, the outbox (set at each attach) and the device clock
struct SystemState {
  DeviceState device;
  LocalSettings local;
  MicPick mic;
  app::Outbox* out = nullptr;
  std::function<std::int64_t()> epochMs;
  std::string appVersion = "dev";
};

}  // namespace cadence::screen::routes
