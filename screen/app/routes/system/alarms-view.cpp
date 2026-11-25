#include "app/routes/system/alarms-view.h"

#include "i18n/gen/keys.h"
#include "ui/core/rml.h"

#include <RmlUi/Core/Element.h>

#include <array>
#include <cstdlib>

namespace cadence::screen::routes::alarms_view {

using ui::rml::esc;

std::string kindWord(i18n::Translator& tr, std::string_view kind) { return esc(tr.tr(i18n::keys::AlarmsKind{kind})); }

std::string kindIcon(std::string_view kind) {
  if (kind == "timer") return "timer";
  if (kind == "reminder") return "calendar";
  return "alarm";
}

std::string titleOf(i18n::Translator& tr, std::string_view kind, std::string_view label) {
  return label.empty() ? kindWord(tr, kind) : esc(label);
}

std::string daysText(i18n::Translator& tr, const std::vector<std::string>& days) {
  if (days.empty()) return esc(tr.tr(i18n::keys::AlarmsOnce{}));
  if (days.size() >= 7) return esc(tr.tr(i18n::keys::AlarmsEveryDay{}));
  // the week's order, whatever order they came in
  static constexpr std::array<std::string_view, 7> kWeek{"mon", "tue", "wed", "thu", "fri", "sat", "sun"};
  std::string out;
  const auto add = [&](std::string_view d) {
    if (!out.empty()) out += ", ";
    out += esc(tr.tr(i18n::keys::AlarmsDay{d}));
  };
  for (const auto d : kWeek) {
    for (const auto& have : days) {
      if (have == d) add(d);
    }
  }
  // a day the week has no name for keeps its own
  for (const auto& have : days) {
    bool known = false;
    for (const auto d : kWeek) known = known || have == d;
    if (!known) add(have);
  }
  return out;
}

std::string whereText(std::string_view name, std::string_view room) {
  if (name.empty()) return esc(room);
  // a device named for its room says it once
  if (room.empty() || room == name) return esc(name);
  return esc(name) + " \xc2\xb7 " + esc(room);
}

std::string countdownSpan(std::int64_t endMs, std::int64_t nowMs) {
  return "<span class=\"al-cd\" data-end=\"" + std::to_string(endMs) + "\">" + alarms::countdownText(endMs, nowMs) + "</span>";
}

void tickCountdowns(Rml::Element* root, std::int64_t nowMs) {
  if (root == nullptr) return;
  Rml::ElementList spans;
  root->QuerySelectorAll(spans, ".al-cd");
  for (auto* s : spans) {
    const auto end = std::strtoll(s->GetAttribute<Rml::String>("data-end", "0").c_str(), nullptr, 10);
    const auto text = alarms::countdownText(end, nowMs);
    if (s->GetInnerRML() != text) s->SetInnerRML(text);
  }
}

std::string idPart(std::string_view s) {
  std::string out;
  out.reserve(s.size());
  for (const char c : s) {
    const bool ok = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-';
    out += ok ? c : '_';
  }
  return out;
}

}  // namespace cadence::screen::routes::alarms_view
