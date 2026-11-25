#include "app/routes/system/settings.h"

#include "app/routes/core/methods.h"
#include "ipc/codec.h"

#include "i18n/gen/keys.h"
#include "i18n/locale.h"
#include "ui/core/rml.h"
#include "ui/rml/sprites.h"

#include <RmlUi/Core/ElementDocument.h>

namespace cadence::screen::routes {

namespace {

using ui::rml::esc;
using ui::rml::icon;
using ui::rml::ListItem;

// a row of the list: its label, its value, focusable so the d-pad walks the whole list
std::string row(const std::string& id, const std::string& label, const std::string& value, const std::string& action,
                bool chevron = false) {
  ListItem i;
  i.id = id;
  i.title = label;
  i.trail = value;
  if (chevron) i.trailIcon = "chevron_right";
  i.action = action;
  return ui::rml::listItem(i);
}

std::string group(const std::string& title) { return "<div class=\"sgroup\">" + title + "</div>"; }

std::string protocolVersion() { return "screen/v1 · cards/v1"; }

// display.screen's overscan enum (0, 2.5, 5): whole values as integers
Json overscanJson(double v) {
  if (v == 0) return 0;
  if (v == 5) return 5;
  return v;
}

// the follow-up window's steps, seconds (settings.set's followUpWindowMs bounds: 2-15 s)
constexpr int kFollowUpSteps[] = {4, 6, 8, 10, 15};

}  // namespace

bool sendMicChoice(app::Outbox& out, const MicPick& pick) {
  ipc::OutEnvelope e(cadence_screen_v1_Envelope_mic_choice_tag);
  e->body.mic_choice.kind = pick.kind;
  ipc::copyField(e->body.mic_choice.device_id, pick.deviceId);
  return out.send(e.get());
}

bool applyScreenConfig(LocalSettings& l, const std::string& raw) {
  const auto j = Json::parse(raw, nullptr, false);
  if (!j.is_object()) return false;
  const LocalSettings before = l;
  if (const auto sub = j.find("subtitles"); sub != j.end() && sub->is_object()) {
    const auto size = sub->value("size", l.subtitleSize);
    if (size == "s" || size == "m" || size == "l") l.subtitleSize = size;
    const auto style = sub->value("style", l.subtitleStyle);
    if (style == "box" || style == "outline") l.subtitleStyle = style;
  }
  if (const auto o = j.find("overscan"); o != j.end() && o->is_number()) l.overscan = o->get<double>();
  if (const auto a = j.find("autoSkipOpenings"); a != j.end() && a->is_boolean()) l.autoSkip = a->get<bool>();
  if (const auto r = j.find("reducedMotion"); r != j.end() && r->is_boolean()) l.reducedMotion = r->get<bool>();
  return l.subtitleSize != before.subtitleSize || l.subtitleStyle != before.subtitleStyle || l.overscan != before.overscan ||
         l.autoSkip != before.autoSkip || l.reducedMotion != before.reducedMotion;
}

std::string micButton(i18n::Translator& tr, const DeviceState& device) {
  const std::string state = device.micState();
  const std::string room = device.roomName;
  std::string kind = "other";
  if (state == "none") kind = "none";
  else if (state == "offline") kind = "offline";
  else if (device.mic == Mic::Local) kind = "tv";
  else if (device.mic == Mic::Speaker) kind = room.empty() ? "other" : "speaker";
  const char* glyph = state == "none" ? "mic_off" : "mic";
  std::string s = "<div id=\"mic-btn\" class=\"micbtn f " + state + "\" data-action=\"mic:open\"><div class=\"glow\"/><div class=\"ring\"/>" +
                  icon(glyph);
  if (state == "offline") s += icon("offline", "micmark");
  // icon + word on focus
  return s + "<span class=\"micword\">" + esc(tr.tr(i18n::keys::AssistantVoice{kind, room})) + "</span></div>";
}

Settings::Settings(Services& s, std::shared_ptr<SystemState> state, const RouteArgs& args)
    : Route(s),
      state_(std::move(state)),
      env_{s.ui, *state_->out, s.ui.translator(), [st = state_.get()] { return st->epochMs(); }},
      device_(state_->device),
      local_(state_->local),
      room_(args.data.is_object() && args.data.value("section", std::string()) == "room") {}

void Settings::openRoom(Router& router) {
  RouteArgs a;
  a.data = Json{{"section", "room"}};
  router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS, a);
}

void Settings::rebuild() {
  if (current()) s_.router.refresh();
}

std::string Settings::listBody() {
  const auto unknown = env_.tr.tr("settings.unknown");
  const auto value = [&](const std::string& v) { return esc(v.empty() ? unknown : v); };
  const auto lang = env_.tr.language();
  std::string s = "<div class=\"safe\"><div class=\"stitle-l\">[[settings.title]]</div><div class=\"slist vscroll\">";
  s += group("[[settings.group.this_tv]]");
  s += row("set-name", "[[settings.name]]", value(device_.deviceName), {});
  s += row("set-room", "[[settings.room]]", value(device_.roomName), {});
  s += row("set-server", "[[settings.server]]", value(device_.serverUrl), {});
  s += row("set-disconnect", "[[settings.disconnect]]", {}, "settings:disconnect");
  s += group("[[settings.group.voice]]");
  s += row("set-mic", "[[settings.mic]]", esc(env_.tr.tr(i18n::keys::RoomMicState{device_.micState()})), "settings:room", true);
  s += row("set-follow", "[[settings.follow_up]]", esc(env_.tr.tr(i18n::keys::SettingsSeconds{device_.followUpS})),
           "settings:follow");
  s += group("[[settings.group.video]]");
  s += row("set-sub-size", "[[settings.subtitles.size]]", esc(env_.tr.tr(i18n::keys::SettingsSize{local_.subtitleSize})),
           "settings:subsize");
  s += row("set-sub-style", "[[settings.subtitles.style|style=other]]",
           esc(env_.tr.tr(i18n::keys::SettingsSubtitlesStyle{local_.subtitleStyle})), "settings:substyle");
  s += ui::rml::toggle(ui::rml::Toggle{"[[settings.auto_skip_openings]]", {}, local_.autoSkip, "settings:autoskip", false, "set-skip"});
  s += row("set-overscan", "[[settings.overscan]]", esc(env_.tr.tr(i18n::keys::SettingsPercent{local_.overscan})), "settings:overscan");
  s += group("[[settings.group.interface]]");
  s += row("set-lang", "[[settings.language]]", lang == i18n::Lang::En ? "[[settings.language.en]]" : "[[settings.language.ru]]",
           "settings:language");
  s += ui::rml::toggle(ui::rml::Toggle{"[[settings.reduced_motion]]", {}, local_.reducedMotion, "settings:reduced", false, "set-reduced"});
  s += group("[[settings.group.about]]");
  s += row("set-app", "[[settings.app_version]]", value(state_->appVersion), {});
  s += row("set-protocol", "[[settings.protocol_version]]", esc(protocolVersion()), {});
  s += row("set-server-version", "[[settings.server_version]]", value(device_.serverVersion), {});
  return s + "</div></div>";
}

bool Settings::activate(const ui::Activation& a) {
  const auto& act = a.action;
  if (room_) {
    if (act == "room:mic:speaker") {
      pickMic(cadence_v2_MicSourceKind_MIC_SOURCE_KIND_SPEAKER);
      return true;
    }
    if (act == "room:mic:local") {
      pickMic(cadence_v2_MicSourceKind_MIC_SOURCE_KIND_LOCAL);
      return true;
    }
    if (act != "room:how") return false;
    toggleQr();
    return true;
  }
  if (act == "settings:room") {
    openRoom(s_.router);
    return true;
  }
  if (act == "settings:disconnect") {
    ui::DialogSpec d;
    d.title = "[[settings.disconnect_question]]";
    d.body = "[[settings.disconnect_body]]";
    d.confirmLabel = "[[settings.disconnect]]";
    d.onConfirm = [this] {
      // the link has no way to ask for it yet (a stub until a settings write exists)
      env_.ui.toast(ui::ToastSpec{{}, ui::ToastTone::Info, "[[common.unavailable_now]]", "info", {}, {}, 0});
    };
    env_.ui.dialog(std::move(d));
    return true;
  }
  // each change shows at once and goes to the server (settings.set), which keeps it as this device's own setting;
  // a refusal puts it back
  if (act == "settings:subsize") {
    const auto was = local_.subtitleSize;
    local_.subtitleSize = was == "s" ? "m" : was == "m" ? "l" : "s";
    rebuild();
    write(Json{{"screen", {{"subtitles", {{"size", local_.subtitleSize}}}}}}, [st = state_, was] { st->local.subtitleSize = was; });
    return true;
  }
  if (act == "settings:substyle") {
    const auto was = local_.subtitleStyle;
    local_.subtitleStyle = was == "box" ? "outline" : "box";
    rebuild();
    write(Json{{"screen", {{"subtitles", {{"style", local_.subtitleStyle}}}}}}, [st = state_, was] { st->local.subtitleStyle = was; });
    return true;
  }
  if (act == "settings:overscan") {
    const auto was = local_.overscan;
    local_.overscan = was < 1 ? 2.5 : was < 3 ? 5 : 0;
    rebuild();
    write(Json{{"screen", {{"overscan", overscanJson(local_.overscan)}}}}, [st = state_, was] { st->local.overscan = was; });
    return true;
  }
  if (act == "settings:autoskip") {
    const auto was = local_.autoSkip;
    local_.autoSkip = a.on.value_or(!local_.autoSkip);
    if (a.element != nullptr) a.element->SetClass("pending", false);
    write(Json{{"screen", {{"autoSkipOpenings", local_.autoSkip}}}}, [st = state_, was] { st->local.autoSkip = was; });
    return true;
  }
  if (act == "settings:reduced") {
    const auto was = local_.reducedMotion;
    local_.reducedMotion = a.on.value_or(!local_.reducedMotion);
    if (a.element != nullptr) a.element->SetClass("pending", false);
    env_.ui.setReducedMotion(local_.reducedMotion);
    write(Json{{"screen", {{"reducedMotion", local_.reducedMotion}}}}, [st = state_, shell = &env_.ui, was] {
      st->local.reducedMotion = was;
      shell->setReducedMotion(was);
    });
    return true;
  }
  if (act == "settings:follow") {
    const int was = device_.followUpS;
    int next = kFollowUpSteps[0];
    for (const int step : kFollowUpSteps) {
      if (step > was) {
        next = step;
        break;
      }
    }
    device_.followUpS = next;
    rebuild();
    write(Json{{"followUpWindowMs", next * 1000}}, [st = state_, was] { st->device.followUpS = was; });
    return true;
  }
  if (act == "settings:language") {
    // live: every open document is built again in the new language (the translator's listener)
    const auto was = env_.tr.language();
    const auto lang = was == i18n::Lang::En ? i18n::Lang::Ru : i18n::Lang::En;
    env_.tr.set_language(lang);
    write(Json{{"language", lang == i18n::Lang::En ? "en" : "ru"}}, [tr = &env_.tr, was] { tr->set_language(was); });
    return true;
  }
  return false;
}

std::string Settings::roomBody() {
  std::string s = "<div class=\"safe\"><div class=\"stitle-l\">[[room.title]]</div><div class=\"slist vscroll\">";
  const std::string room = device_.roomName.empty() ? env_.tr.tr("room.no_room") : device_.roomName;
  ListItem r;
  r.id = "room-room";
  r.leadIcon = "home";
  r.title = "[[room.room]]";
  r.meta = "[[room.room_hint]]";
  r.trail = esc(room);
  s += ui::rml::listItem(r);
  s += "<div class=\"sgroup\">[[room.mic]]</div>";
  // the room's speaker and the tv's usb microphone, each with its state; the one in use is checked
  const bool speakerOn = device_.mic == Mic::Speaker;
  ListItem sp;
  sp.id = "room-speaker";
  sp.leadIcon = "share_to_speaker";
  sp.title = esc(env_.tr.tr(i18n::keys::RoomMicSpeaker{room}));
  sp.meta = esc(env_.tr.tr(i18n::keys::RoomMicState{speakerOn ? device_.micState() : (device_.mic == Mic::None ? "none" : "unknown")}));
  if (speakerOn) sp.trailIcon = "check";
  sp.action = "room:mic:speaker";
  s += ui::rml::listItem(sp);
  ListItem usb;
  usb.id = "room-usb";
  usb.leadIcon = device_.mic == Mic::Local ? "mic" : "mic_off";
  usb.title = "[[room.mic_usb]]";
  usb.meta = esc(env_.tr.tr(i18n::keys::RoomMicState{device_.mic == Mic::Local ? "ready" : "none"}));
  if (device_.mic == Mic::Local) usb.trailIcon = "check";
  usb.action = "room:mic:local";
  s += ui::rml::listItem(usb);
  ListItem how;
  how.id = "room-how";
  how.leadIcon = "qr";
  how.title = "[[enroll.how]]";
  how.trailIcon = "chevron_right";
  how.action = "room:how";
  s += ui::rml::listItem(how);
  if (qr_) {
    const std::string url = device_.serverUrl.empty() ? std::string("https://cadence.local") : device_.serverUrl;
    s += "<div id=\"room-qr\" class=\"cqr roomqr\"><div class=\"qr\" style=\"decorator: image(&quot;gen:qr:" + ui::hexOf(url) +
         "&quot;);\"/><div class=\"cmeta\">[[assistant.setup_qr]]</div></div>";
  }
  return s + "</div></div>";
}

void Settings::write(const Json& args, std::function<void()> revert) {
  // not a call the route owns: BACK right after a change must not cancel the write on the server. revert touches the
  // shared state alone, and the page only while it is there
  std::weak_ptr<bool> page = alive_;
  s_.rpc.call(rpc::kSettingsSet, args,
              {[this, page, shell = &env_.ui, revert = std::move(revert)](const std::string& status, const Json&) {
                 if (status == rpc::kStatusOK) return;
                 if (revert) revert();
                 if (!page.expired()) rebuild();
                 shell->toast(ui::ToastSpec{{}, ui::ToastTone::Warning, "[[common.unavailable_now]]", "triangle", {}, {}, 0});
               },
               nullptr});
}

void Settings::pickMic(cadence_v2_MicSourceKind kind) {
  // the room's default speaker (an empty id: the server picks the room's), or the tv's own usb microphone
  state_->mic.kind = kind;
  state_->mic.deviceId.clear();
  if (!sendMicChoice(*state_->out, state_->mic)) {
    env_.ui.toast(ui::ToastSpec{{}, ui::ToastTone::Warning, "[[common.unavailable_now]]", "triangle", {}, {}, 0});
    return;
  }
  device_.mic = kind == cadence_v2_MicSourceKind_MIC_SOURCE_KIND_LOCAL ? Mic::Local : Mic::Speaker;
  if (device_.mic == Mic::Local) device_.micOffline = false;
  rebuild();
}

void Settings::toggleQr() {
  {
    qr_ = !qr_;
    // the page again, focus kept on the row
    auto* doc = env_.ui.page();
    if (doc != nullptr) {
      if (auto* list = doc->QuerySelector(".slist")) {
        if (qr_) {
          const std::string url = device_.serverUrl.empty() ? std::string("https://cadence.local") : device_.serverUrl;
          auto e = doc->CreateElement("div");
          e->SetClassNames("cqr roomqr");
          e->SetId("room-qr");
          e->SetInnerRML("<div class=\"qr\" style=\"decorator: image(&quot;gen:qr:" + ui::hexOf(url) +
                         "&quot;);\"/><div class=\"cmeta\">[[assistant.setup_qr]]</div>");
          list->AppendChild(std::move(e));
        } else if (auto* q = doc->GetElementById("room-qr")) {
          q->GetParentNode()->RemoveChild(q);
        }
      }
    }
    env_.ui.markDirty();
  }
}


}  // namespace cadence::screen::routes
