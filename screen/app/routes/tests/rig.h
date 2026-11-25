#pragma once

#include "app/routes/core/view.h"
#include "app/routes/preview/pages.h"
#include "ui/preview/harness.h"

#include <catch2/catch_test_macros.hpp>

#include <string>

// a screen offscreen with i1's routes on the fake catalog: keys in, frames and the fake server's record out
namespace cadence::screen::routes::test {

struct Rig {
  explicit Rig(fake::Script script = {}) : h({960, 540, i18n::Lang::Ru}) {
    REQUIRE(h.ok());
    h.ui().setPage([] { return std::string("<div/>"); });
    h.step(32);
    s = &preview::start(h.ui(), std::move(script));
  }

  ui::preview::Harness h;
  preview::Session* s = nullptr;

  Router& router() { return *s->router; }
  fake::FakeCatalog& server() { return *s->server; }
  ui::Ui& ui() { return h.ui(); }
  Rml::Element* focused() { return ui().focus().current(); }
  std::string focusedId() {
    auto* f = focused();
    return f != nullptr ? f->GetId() : std::string();
  }
  std::string focusedAction() {
    auto* f = focused();
    return f != nullptr ? f->GetAttribute<Rml::String>("data-action", "") : std::string();
  }
  Rml::Element* byId(const std::string& id) { return view::byId(ui().page(), id); }
  void press(cadence_screen_v1_Key k, int times = 1) {
    for (int i = 0; i < times; ++i) {
      h.press(k);
      h.step(60);
    }
  }
  void ok() { press(cadence_screen_v1_Key_KEY_OK); }
  void back() { press(cadence_screen_v1_Key_KEY_BACK); }
  [[nodiscard]] cadence_v2_ScreenRoute route() const { return s->router->top() != nullptr ? s->router->top()->id() : cadence_v2_ScreenRoute_SCREEN_ROUTE_UNSPECIFIED; }
};

}  // namespace cadence::screen::routes::test
