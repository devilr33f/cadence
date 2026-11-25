#pragma once

#include "app/ui-layer.h"
#include "i18n/translator.h"
#include "ui/core/ui.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <string>
#include <string_view>

// stream i3's layers and routes over stream b's ui: the assistant drawer, the tasks panels, enrollment, room and
// microphone, airplay, settings. each part takes the ui, the link out, the translator and the device clock
namespace cadence::screen::ui::cards {}
namespace cadence::screen::ui::tokens {}

namespace cadence::screen::routes {

namespace cards = ui::cards;
namespace tokens = ui::tokens;

struct Env {
  ui::Ui& ui;
  app::Outbox& out;
  i18n::Translator& tr;
  // unix ms on the device clock (countdowns, expiry lines)
  std::function<std::int64_t()> epochMs;
};

// a timer's callback that does nothing once its owner is gone (the ui's timers outlive a route or a layer)
class Alive {
 public:
  template <class F>
  std::function<void()> guard(F f) const {
    return [w = std::weak_ptr<char>(token_), f = std::move(f)]() mutable {
      if (!w.expired()) f();
    };
  }

 private:
  std::shared_ptr<char> token_ = std::make_shared<char>();
};

// fn once rmlui laid out what changed now: a timer's next update runs before that update's layout, so it hops twice
inline void afterLayout(ui::Ui& ui, std::function<void()> fn) {
  ui.after(1, [&ui, f = std::move(fn)]() mutable { ui.after(1, std::move(f)); });
}

// the gallery's harness went before the routes on it (its ui is gone): the layers' destructors touch nothing of it
inline bool& uiAbandoned() {
  static bool gone = false;
  return gone;
}

// the text of a nanopb string field (null is "")
inline std::string_view sv(const char* s) { return s != nullptr ? std::string_view(s) : std::string_view(); }

// a json string literal of s
std::string jsonString(std::string_view s);

// copies s into a nanopb char array, cut to fit
template <std::size_t N>
void copyTo(char (&dst)[N], std::string_view s) {
  const auto n = s.size() < N - 1 ? s.size() : N - 1;
  for (std::size_t i = 0; i < n; ++i) dst[i] = s[i];
  dst[n] = '\0';
}

}  // namespace cadence::screen::routes
