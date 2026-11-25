#include "app/routes/system/device.h"

#include "app/routes/system/env.h"

#include <nlohmann/json.hpp>

#include <cstring>

namespace cadence::screen::routes {

bool DeviceState::apply(const cadence_screen_v1_Envelope& m) {
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_ready_tag: {
      const auto& r = m.body.ready;
      deviceId = r.device_id;
      deviceName = sv(r.device_name);
      roomName = sv(r.room_name);
      daemonVersion = r.daemon_version;
      language = r.language;
      return true;
    }
    case cadence_screen_v1_Envelope_link_status_tag: {
      const auto& l = m.body.link_status;
      const bool changed = link != l.state || linkCode != l.code;
      link = l.state;
      linkCode = l.code;
      if (l.server_url != nullptr && *l.server_url != '\0') serverUrl = l.server_url;
      if (l.server_version[0] != '\0') serverVersion = l.server_version;
      return changed;
    }
    case cadence_screen_v1_Envelope_capability_config_tag: {
      const auto& c = m.body.capability_config;
      if (std::strcmp(c.capability, "display.screen") != 0 || c.json == nullptr) return false;
      const auto j = nlohmann::json::parse(c.json, nullptr, false);
      if (j.is_discarded() || !j.is_object()) return false;
      if (auto it = j.find("overscan"); it != j.end() && it->is_number()) overscan = it->get<double>();
      if (auto it = j.find("followUpSeconds"); it != j.end() && it->is_number_integer()) followUpS = it->get<int>();
      return true;
    }
    default:
      return false;
  }
}

}  // namespace cadence::screen::routes
