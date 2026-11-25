#pragma once

#include "app/routes/system/alarms-model.h"
#include "app/routes/system/env.h"

#include <cstdint>
#include <functional>
#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// a ring on the screen: a full-screen layer over everything but the volume pill and toasts, with the time now in
// big figures, the reminder's label or the kind's word, the ringing device's name and room, and «Стоп» (focused) and
// «Ещё N мин». only those buttons act (OK on one): BACK does nothing, so a stray press never silences an alarm. it
// holds focus in a scope of its own while shown; the drawer or a dialog may open over it and give focus back
class AlarmRing {
 public:
  static constexpr int kZ = 55;

  explicit AlarmRing(Env env);
  ~AlarmRing();
  AlarmRing(const AlarmRing&) = delete;
  AlarmRing& operator=(const AlarmRing&) = delete;

  // the ring to show (nullopt hides it); the same ring again changes only what it says
  void show(const std::optional<alarms::Ring>& ring);
  [[nodiscard]] bool shown() const { return ring_.has_value(); }
  [[nodiscard]] const std::optional<alarms::Ring>& ring() const { return ring_; }
  // a press waits for its answer: the buttons spin and take no second press
  void setPending(bool pending);

  // «Стоп» (stop) or «Ещё N мин» (snooze) on the ring shown
  std::function<void(const alarms::Ring& ring, const std::string& action)> onAct;

 private:
  std::string build();
  void loaded(Rml::ElementDocument& doc);
  void render(bool focus);
  bool key(const platform::KeyEvent& e);
  bool activate(const ui::Activation& a);
  void tick(std::int64_t nowMs);
  void release();
  [[nodiscard]] Rml::Element* el(const char* id) const;

  Env env_;
  Alive alive_;
  std::vector<int> hooks_;
  Rml::ElementDocument* doc_ = nullptr;
  std::optional<alarms::Ring> ring_;
  bool scoped_ = false;
  bool pending_ = false;
  std::int64_t minute_ = -1;
};

}  // namespace cadence::screen::routes
