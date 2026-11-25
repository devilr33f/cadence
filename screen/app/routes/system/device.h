#pragma once

#include "app/ui-layer.h"

#include <cstdint>
#include <string>

namespace cadence::screen::routes {

// which microphone serves this screen (design r2 section 21), as the screen last learned it: an assistant turn's
// mic_source, assistant.invoke's answer, the hello's usb microphone
enum class Mic { Unknown, None, Speaker, Local };

// what the system screens show of this device and its link: Ready, LinkStatus, display.screen's config. names are
// shown, never logged (rule 8)
struct DeviceState {
  std::string deviceId;
  std::string deviceName;
  std::string roomName;
  std::string daemonVersion;
  std::string language;
  cadence_screen_v1_LinkState link = cadence_screen_v1_LinkState_LINK_STATE_UNSPECIFIED;
  std::string linkCode;
  std::string serverUrl;
  std::string serverVersion;
  Mic mic = Mic::Unknown;
  bool micOffline = false;
  std::string micDeviceId;
  // display.screen's config (the server's): what settings show
  double overscan = 0;
  int followUpS = 6;

  [[nodiscard]] bool online() const { return link == cadence_screen_v1_LinkState_LINK_STATE_ONLINE; }
  [[nodiscard]] bool enrolled() const {
    return link != cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED && link != cadence_screen_v1_LinkState_LINK_STATE_REMOVED &&
           link != cadence_screen_v1_LinkState_LINK_STATE_UNSPECIFIED;
  }
  // the microphone's state word for the top bar and the room screen: ready, offline, none, unknown
  [[nodiscard]] const char* micState() const {
    switch (mic) {
      case Mic::None:
        return "none";
      case Mic::Speaker:
        return micOffline ? "offline" : "ready";
      case Mic::Local:
        return "ready";
      case Mic::Unknown:
        break;
    }
    return "unknown";
  }

  // takes Ready, LinkStatus and display.screen's config; true when something shown changed
  bool apply(const cadence_screen_v1_Envelope& m);
};

}  // namespace cadence::screen::routes
