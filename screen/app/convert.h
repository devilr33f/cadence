#pragma once

#include "app/ui-layer.h"
#include "ipc/codec.h"
#include "ipc/log.h"
#include "platform/keys.h"
#include "platform/platform.h"
#include "player/player.h"

#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::app {

// screen/v1 messages to and from the player's own types

player::PlayRequest playRequestOf(const cadence_screen_v1_MediaPlay& play);
// nullopt for actions the screen does not do itself (next and previous are the server's music or video service's)
std::optional<player::Command> commandOf(const cadence_screen_v1_MediaCommand& command);
player::EqRequest eqOf(const cadence_screen_v1_SetEq& eq);

ipc::OutEnvelope mediaStateEnvelope(const player::PlayerState& state);
ipc::OutEnvelope playerStatsEnvelope(const player::PlayerStats& stats);
// media.mpv's state back to cadenced (screen/v1 CapabilityState)
ipc::OutEnvelope capabilityStateEnvelope(std::string_view capability, std::string_view json);
ipc::OutEnvelope commandResultEnvelope(std::string_view requestId, std::string_view code);
ipc::OutEnvelope logLineEnvelope(const ipc::LogRecord& record);
ipc::OutEnvelope inputEventEnvelope(const platform::KeyEvent& event, std::int64_t atMs);

struct HelloInfo {
  std::string version;
  cadence_screen_v1_Platform platform = cadence_screen_v1_Platform_PLATFORM_LINUX;
  platform::DisplayInfo display;
  std::vector<std::string> hwdec;
  std::vector<std::string> videoCodecs;
  std::uint32_t maxVideoHeight = 0;
  std::vector<std::uint32_t> cardVersions;
  std::vector<std::string> cardFeatures;
  // it decodes airplay mirror frames (a mirror presenter is there)
  bool mirroring = false;
};
ipc::OutEnvelope helloEnvelope(const HelloInfo& info);

ipc::OutEnvelope screenStatusEnvelope(cadence_screen_v1_ScreenPower power, const UiStatus& ui,
                                      const platform::DisplayInfo& display);

// the screen/v1 protocol version this build speaks
inline constexpr std::uint32_t kProtocol = 1;

}  // namespace cadence::screen::app
