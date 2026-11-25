#pragma once

#include "app/routes/core/failure.h"
#include "ui/core/rml.h"

#include <string_view>

namespace cadence::screen::routes::failure {

// a card's lines for a failure: the source, the plain reason and its hint (wordsFor), the code line and the call
// (fallbackProvider: the route's own, when the answer named none)
inline void describe(ui::rml::ErrorCard& e, const Call& c, std::string_view fallbackProvider = {}) {
  const auto words = wordsFor(c, fallbackProvider);
  e.source = ui::rml::esc(sourceLine(c, fallbackProvider));
  e.plain = words.reason;
  e.hint = words.hint;
  e.reason = ui::rml::esc(reasonLine(c));
  e.detail = ui::rml::esc(detailLine(c));
}

}  // namespace cadence::screen::routes::failure
