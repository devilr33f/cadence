#include "app/routes/home/alarms-strip.h"

#include "app/routes/core/view.h"
#include "app/routes/system/alarms-view.h"
#include "ui/core/rml.h"

namespace cadence::screen::routes::home_alarms {

namespace {

using ui::rml::esc;

// one chip: the kind's glyph, the kind and where on top, the time or the countdown under it
std::string chip(std::size_t i, const alarms::Entry& e, const std::string& when, i18n::Translator& tr) {
  const auto& d = *e.device;
  std::string label = alarms_view::kindWord(tr, e.item->kind);
  // this screen's own goes without its name; another device's says which
  if (!d.here) {
    const auto where = alarms_view::whereText(d.name, d.room);
    if (!where.empty()) label += " \xc2\xb7 " + where;
  }
  return "<div class=\"hnp nbi alc f\" id=\"home-alarm-" + std::to_string(i) + "\" data-device=\"" + esc(d.id) +
         "\" data-action=\"alarms:open:" + esc(d.id) + "\">" + view::focusDecor() + "<div class=\"hnp-art\">" +
         ui::rml::icon(alarms_view::kindIcon(e.item->kind)) + "</div><div class=\"hnp-copy\"><div class=\"hnp-label\">" + label +
         "</div><div class=\"hnp-title\">" + when + "</div></div></div>";
}

}  // namespace

std::string strip(const alarms::Summary& summary, std::int64_t nowMs, i18n::Translator& tr) {
  std::string items;
  std::size_t n = 0;
  if (const auto next = alarms::nextAlarm(summary)) {
    std::string when = esc(next->item->time);
    if (next->item->snoozed) when += " \xc2\xb7 [[alarms.snoozed]]";
    items += chip(n++, *next, when, tr);
  }
  for (const auto& t : alarms::runningTimers(summary, nowMs)) {
    if (n >= kMaxItems) break;
    items += chip(n++, t, alarms_view::countdownSpan(t.item->nextMs, nowMs), tr);
  }
  if (items.empty()) return {};
  return "<div class=\"nearby alarms-strip\" id=\"home-alarms-strip\"><div class=\"nearby-label\">[[alarms.strip]]</div>"
         "<div class=\"nearby-track\">" + items + "</div></div>";
}

}  // namespace cadence::screen::routes::home_alarms
