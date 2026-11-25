#pragma once

#include "app/routes/system/device.h"
#include "app/routes/system/env.h"

#include <cstdint>
#include <functional>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// airplay on the screen (design r2 section 23): the pin a sender asks for, full screen (4 digits in inter figures
// 180 dp, «Введите на устройстве Apple», this tv's name and a countdown line), and mirroring: a 4 s toast on
// connect («Показ экрана: iPhone Маши»), then a 40 dp corner badge at 70 % top-right in title-safe; BACK asks
// «Отключить показ экрана?». the phone's frames fill the video plane (stream j); the sender's name is memory-only
class Airplay {
 public:
  Airplay(Env env, const DeviceState& device);
  ~Airplay();
  Airplay(const Airplay&) = delete;
  Airplay& operator=(const Airplay&) = delete;

  // airplay_pin, mirror_start, mirror_stop, and the answer to its stop: true when it was airplay's
  bool message(const cadence_screen_v1_Envelope& m);
  [[nodiscard]] bool pinShown() const { return pin_; }
  [[nodiscard]] bool mirroring() const { return mirroring_; }
  [[nodiscard]] const std::string& pin() const { return digits_; }
  std::function<void()> onChanged;

 private:
  std::string build();
  void loaded(Rml::ElementDocument& doc);
  bool key(const platform::KeyEvent& e);
  // the stop key's confirm: asks cadenced to end the mirror session it shows
  void stop();
  void stopFailed();
  void tick(std::int64_t nowMs);
  void render();
  [[nodiscard]] Rml::Element* el(const char* id) const;

  Env env_;
  const DeviceState& device_;
  std::vector<int> hooks_;
  Rml::ElementDocument* doc_ = nullptr;
  bool pin_ = false;
  std::string digits_;
  std::int64_t expiresMs_ = 0;
  std::int64_t shownMs_ = 0;
  bool mirroring_ = false;
  std::string session_;
  // the request id of the stop cadenced has not answered yet; empty when none
  std::string stopRequest_;
  std::uint64_t stops_ = 0;
  std::int64_t lastSecond_ = -1;
};

}  // namespace cadence::screen::routes
