#include "app/routes/playback/server-choices.h"

#include "ui/core/rml.h"

#include <nlohmann/json.hpp>

namespace cadence::screen::routes::choices {

namespace {

const char* viewName(View v) {
  switch (v) {
    case View::Audio: return "audio";
    case View::Visualizer: return "visualizer";
    default: return "video";
  }
}

void send(PlaybackContext& c, nlohmann::json args) {
  if (!c.session) return;
  args["sessionId"] = c.session->sessionId;
  c.ui().closeLevel();
  // the server answers with a new play (a new session), which the routes show as any other
  c.rpc.call("media.video", args.dump(), [&c](const RpcAnswer& a) {
    if (!a.ok()) c.toast("[[resolving.play_failed]]");
  }, c.nowMs, 15000);
}

}  // namespace

std::string audioLabel(const AudioChoice& a) {
  std::string name = !a.label.empty() ? ui::rml::esc(a.label) : a.language == "und" ? std::string("[[player.sheet.audio]]") : ui::rml::esc(a.language);
  if (a.original) return "[[player.audio.original|name=" + name + "]]";
  if (a.dubbed) return "[[player.audio.dub|name=" + name + "]]";
  return name;
}

std::string viewLabel(View v) { return std::string("[[player.view.") + viewName(v) + "]]"; }

std::string currentAudio(const Session& s) {
  if (s.audioChoices.size() < 2) return {};
  for (const auto& a : s.audioChoices)
    if (a.selected) return audioLabel(a);
  return "[[player.sheet.audio]]";
}

std::string currentView(const Session& s) {
  if (s.views.size() < 2) return {};
  return viewLabel(s.view == View::Unspecified ? View::Video : s.view);
}

void openAudio(PlaybackContext& c) {
  if (!c.session) return;
  std::string body = "<div class=\"list\">";
  for (const auto& a : c.session->audioChoices)
    body += ui::rml::chip(audioLabel(a), "audio", a.selected, "schoice:audio:" + ui::rml::esc(a.id));
  c.ui().sheet("[[player.sheet.audio]]", body + "</div>");
}

void openViews(PlaybackContext& c) {
  if (!c.session) return;
  std::string body = "<div class=\"list\">";
  const auto current = c.session->view == View::Unspecified ? View::Video : c.session->view;
  for (const auto v : c.session->views)
    body += ui::rml::chip(viewLabel(v), v == View::Video ? "film" : v == View::Audio ? "audio" : "sparkle", v == current, std::string("schoice:view:") + viewName(v));
  c.ui().sheet("[[player.sheet.view]]", body + "</div>");
}

bool activate(PlaybackContext& c, const std::string& action) {
  if (action.starts_with("schoice:audio:")) {
    send(c, {{"audio", action.substr(14)}});
    return true;
  }
  if (action.starts_with("schoice:view:")) {
    send(c, {{"view", action.substr(13)}});
    return true;
  }
  return false;
}

void switchQuality(PlaybackContext& c, const std::string& name) { send(c, {{"quality", name}}); }

}  // namespace cadence::screen::routes::choices
