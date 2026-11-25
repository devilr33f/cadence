#pragma once

#include "app/routes/system/preview/fixture.h"
#include "ui/preview/harness.h"

#include <RmlUi/Core/ElementDocument.h>

#include <catch2/catch_test_macros.hpp>

#include <string>

namespace cadence::screen::routes::systest {

using syspreview::Fixture;
using syspreview::Turn;

// one harness for every test (rmlui's state is global): a page of home's stand-in under the routes
inline ui::preview::Harness& harness() {
  static ui::preview::Harness h({960, 540, i18n::Lang::Ru});
  REQUIRE(h.ok());
  return h;
}

inline std::string homeBody() {
  std::string s = "<div class=\"safe\"><div class=\"vscroll\" style=\"position: absolute; left: 0dp; top: 440dp;\"><div class=\"row\"><div class=\"track\">";
  for (int i = 0; i < 4; ++i) {
    s += "<div class=\"card poster f\" id=\"films-" + std::to_string(i) + "\" data-action=\"open\"><div class=\"art\"/><div class=\"title\">t" +
         std::to_string(i) + "</div></div>";
  }
  return s + "</div><div class=\"cline\"/></div></div></div>";
}

// a fresh page and a fresh system on the shared harness
inline Fixture& fresh(ui::preview::Harness& h) {
  ui::preview::Page p;
  p.name = "test";
  p.body = homeBody;
  h.load(p);
  auto& f = syspreview::fixtureFor(h.ui(), homeBody);
  h.step(48);
  return f;
}

inline std::string focusedId(ui::preview::Harness& h) {
  auto* e = h.ui().focus().current();
  return e != nullptr ? e->GetId() : "";
}

inline Rml::Element* focused(ui::preview::Harness& h) { return h.ui().focus().current(); }

inline bool inDoc(Rml::Element* e, const char* doc) {
  return e != nullptr && e->GetOwnerDocument() != nullptr && e->GetOwnerDocument()->GetSourceURL() == std::string(doc) + ".rml";
}

inline void press(ui::preview::Harness& h, cadence_screen_v1_Key k) { h.press(k); }

}  // namespace cadence::screen::routes::systest
