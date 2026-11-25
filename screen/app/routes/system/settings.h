#pragma once

#include "app/routes/core/router.h"
#include "app/routes/system/env.h"
#include "app/routes/system/state.h"

#include <functional>
#include <memory>
#include <string>

namespace cadence::screen::routes {

// settings on the tv (design r2 section 24): one plain list grouped as «Этот телевизор» (name, room, the server's
// address read-only, «Отключить от Cadence» behind a confirm), «Голос» (the microphone: the room screen; the
// follow-up window), «Видео» (subtitle size and style, auto-skip openings, overscan), «Интерфейс» (the language,
// switched live; reduced motion) and «О приложении» (app, protocol and server versions). with args.data.section
// "room" it is settings › «Комната и микрофон» (design r2 section 21): the room (read-only, set in the dashboard),
// the microphone (the room's speaker or the tv's usb microphone, each with its state) and «Как подключить» as a qr
class Settings final : public Route {
 public:
  Settings(Services& s, std::shared_ptr<SystemState> state, const RouteArgs& args);

  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS; }
  std::string body() override { return room_ ? roomBody() : listBody(); }
  bool activate(const ui::Activation& a) override;
  [[nodiscard]] bool room() const { return room_; }

  // opens room and microphone over whatever is shown
  static void openRoom(Router& router);

 private:
  std::string listBody();
  std::string roomBody();
  void rebuild();
  void toggleQr();
  // rpc settings.set with what changed; a refusal runs revert and shows the page again
  void write(const Json& args, std::function<void()> revert);
  void pickMic(cadence_v2_MicSourceKind kind);

  std::shared_ptr<SystemState> state_;
  Env env_;
  DeviceState& device_;
  LocalSettings& local_;
  bool room_ = false;
  bool qr_ = false;
  // gone with the page: a write answered after it changes the state alone
  std::shared_ptr<bool> alive_ = std::make_shared<bool>(true);
};

// the top bar's microphone button (design r2 section 21): ready (mic; focused «Голос: колонка «Кухня»»), speaker
// offline (mic with an offline mark; «Колонка «Кухня» не в сети»), none (muted mic; «Нет микрофона»)
std::string micButton(i18n::Translator& tr, const DeviceState& device);

// screen -> cadenced: the microphone picked (screen/v1 MicChoice); false without a link
bool sendMicChoice(app::Outbox& out, const MicPick& pick);

// display.screen's config (the json cadenced validated) into the settings page's values; true when one changed
bool applyScreenConfig(LocalSettings& local, const std::string& json);

}  // namespace cadence::screen::routes
