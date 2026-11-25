#include "app/routes/system/alarms-page.h"

#include "app/routes/core/view.h"
#include "app/routes/system/alarms-view.h"
#include "app/routes/system/alarms.h"
#include "i18n/gen/keys.h"
#include "i18n/locale.h"
#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>

#include <cstdlib>
#include <ctime>

namespace cadence::screen::routes {

namespace {

using ui::rml::esc;

constexpr const char* kPrefix = "al:";

// a unix ms as the screen's clock reads it (an item of a kind the screen does not know has only its next)
std::string clockOf(i18n::Lang lang, std::int64_t ms) {
  const auto t = static_cast<std::time_t>(ms / 1000);
  std::tm local{};
  localtime_r(&t, &local);
  return i18n::clock(lang, local.tm_hour, local.tm_min);
}

std::string rowId(const alarms::Device& d, const alarms::Item& it) {
  return "al-" + alarms_view::idPart(d.id) + "-" + alarms_view::idPart(it.kind) + "-" + alarms_view::idPart(it.id);
}

std::string join(const std::string& a, const std::string& b) {
  if (a.empty()) return b;
  if (b.empty()) return a;
  return a + " \xc2\xb7 " + b;
}

}  // namespace

AlarmsPage::AlarmsPage(Services& s, const RouteArgs& args) : Route(s), focusDevice_(args.ref) {}

std::string AlarmsPage::action(const std::string& verb, Target t) {
  targets_.push_back(std::move(t));
  return kPrefix + verb + ":" + std::to_string(targets_.size() - 1);
}

std::string AlarmsPage::body() {
  auto& tr = s_.ui.translator();
  const auto* part = alarmsPart();
  targets_.clear();
  revision_ = part != nullptr ? part->revision() : 0;
  const std::int64_t now = part != nullptr ? part->epochMs() : 0;
  std::string s = "<div class=\"safe\"><div class=\"stitle-l\">[[alarms.title]]</div><div class=\"slist vscroll\" id=\"al-list\">";
  bool any = false;
  if (part != nullptr) {
    const auto& sum = part->summary();
    for (const auto& d : sum.devices) {
      if (d.items.empty()) continue;
      any = true;
      std::string where = alarms_view::whereText(d.name, d.room);
      if (d.here) where = join(where, "[[alarms.here]]");
      s += "<div class=\"sgroup\">" + where + "</div>";
      const auto snooze = d.snoozeMinutes > 0 ? d.snoozeMinutes : alarms::kDefaultSnoozeMinutes;
      for (const auto& it : d.items) {
        const bool ringing = alarms::listed(sum, d.id + "/" + it.kind + "/" + it.id);
        const Target target{d.id, it.kind, it.id, snooze};
        ui::rml::ListItem li;
        li.id = rowId(d, it);
        li.leadIcon = alarms_view::kindIcon(it.kind);
        const std::string word = alarms_view::kindWord(tr, it.kind);
        std::string meta;
        if (it.kind == "alarm") {
          li.title = esc(it.time.empty() && it.nextMs > 0 ? clockOf(tr.language(), it.nextMs) : it.time);
          meta = join(word, alarms_view::daysText(tr, it.days));
        } else if (it.kind == "timer") {
          li.title = it.nextMs > now ? alarms_view::countdownSpan(it.nextMs, now) : esc(i18n::countdown(it.seconds));
          meta = join(word, esc(i18n::duration(tr.language(), it.seconds)));
        } else if (it.kind == "reminder") {
          li.title = alarms_view::titleOf(tr, it.kind, it.label);
          meta = join(join(word, esc(it.time)), alarms_view::daysText(tr, it.days));
        } else {
          // a kind this screen does not know: its label and when it rings next
          li.title = alarms_view::titleOf(tr, it.kind, it.label);
          meta = esc(!it.time.empty() ? it.time : it.nextMs > 0 ? clockOf(tr.language(), it.nextMs) : std::string());
        }
        if (it.snoozed) meta = join(meta, "[[alarms.snoozed]]");
        if (ringing) {
          meta = join("[[alarms.ringing]]", meta);
          li.classes = "al-ringing";
        } else if (it.kind == "timer" || it.kind == "reminder") {
          // rule 7: a timer or a reminder may go from here, an alarm never
          li.action = action("cancel", target);
          li.trail = "[[alarms.cancel]]";
        }
        li.meta = meta;
        s += ui::rml::listItem(li);
        if (ringing) {
          s += "<div class=\"al-acts\">" +
               ui::rml::button(ui::rml::Button{"[[alarms.stop]]", "close", action("stop", target), true, false, false, li.id + "-stop", {}}) +
               ui::rml::button(ui::rml::Button{esc(tr.tr(i18n::keys::AlarmsSnooze{snooze})), "timer", action("snooze", target), false,
                                               false, false, li.id + "-snooze", {}}) +
               "</div>";
        }
      }
    }
  }
  if (!any) s += "<div class=\"smeta\">[[alarms.empty]]</div>";
  return s + "</div></div>";
}

void AlarmsPage::loaded(Rml::ElementDocument& doc, bool restoring) {
  second_ = -1;
  if (restoring || placed_) return;
  placed_ = true;
  // Route.ref's device: its first item, else the list's first
  Rml::ElementList items;
  doc.QuerySelectorAll(items, "#al-list .item.f");
  if (items.empty()) return;
  std::string id = items.front()->GetId();
  if (!focusDevice_.empty()) {
    const std::string prefix = "al-" + alarms_view::idPart(focusDevice_) + "-";
    for (auto* e : items) {
      if (e->GetId().starts_with(prefix)) {
        id = e->GetId();
        break;
      }
    }
  }
  focusLater(id);
}

bool AlarmsPage::activate(const ui::Activation& a) {
  if (!a.action.starts_with(kPrefix)) return false;
  const auto rest = a.action.substr(3);
  const auto colon = rest.find(':');
  if (colon == std::string::npos) return true;
  const auto verb = rest.substr(0, colon);
  const auto index = static_cast<std::size_t>(std::strtoul(rest.c_str() + colon + 1, nullptr, 10));
  if (index >= targets_.size()) return true;
  const Target t = targets_[index];
  if (verb == "cancel") {
    ui::DialogSpec d;
    d.title = esc(s_.ui.translator().tr(i18n::keys::AlarmsCancelQuestion{t.kind}));
    d.confirmLabel = "[[alarms.cancel]]";
    d.cancelLabel = "[[alarms.keep]]";
    d.onConfirm = [this, t, alive = std::weak_ptr<bool>(alive_)] {
      if (!alive.expired()) cancel(t);
    };
    s_.ui.dialog(std::move(d));
    return true;
  }
  if ((verb == "stop" || verb == "snooze") && alarmsPart() != nullptr) alarmsPart()->act(t.deviceId, t.kind, t.id, verb);
  return true;
}

void AlarmsPage::cancel(const Target& t) {
  if (auto* part = alarmsPart()) part->act(t.deviceId, t.kind, t.id, "cancel");
}

void AlarmsPage::update(std::int64_t) {
  const auto* part = alarmsPart();
  if (part == nullptr || page() == nullptr) return;
  if (part->revision() != revision_) {
    // what changed is drawn again, focus kept on the same item (by its id)
    s_.router.refresh();
    return;
  }
  const auto now = part->epochMs();
  if (now / 1000 == second_) return;
  second_ = now / 1000;
  alarms_view::tickCountdowns(view::byId(page(), "al-list"), now);
  s_.ui.markDirty();
}

void AlarmsPage::context(ScreenContextData& c) const { c.pageRef = focusDevice_; }

}  // namespace cadence::screen::routes
