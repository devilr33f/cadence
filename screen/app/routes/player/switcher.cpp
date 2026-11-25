#include "app/routes/player/switcher.h"

#include "ui/core/rml.h"

#include <nlohmann/json.hpp>

#include <cstdlib>

namespace cadence::screen::routes {

using ui::rml::esc;

void SwitcherContent::open(SideSheet& sheet, Rml::Element* body) {
  sheet_ = &sheet;
  body_ = body->GetObserverPtr();
  auto& c = sheet.context();
  body->SetInnerRML(ui::rml::spinner("[[common.loading]]"));
  if (!c.session) return;
  nlohmann::json args = {{"ref", c.session->itemRef}};
  if (!c.session->episode.empty()) args["episode"] = c.session->episode;
  const auto asked = c.nowMs;
  c.rpc.call("catalog.options", args.dump(), [this, asked](const RpcAnswer& a) {
    if (sheet_ == nullptr || !sheet_->isOpen() || sheet_->content() != this) return;
    if (!a.ok()) {
      if (auto* b = body_.get()) b->SetInnerRML(failureCard("catalog.options", a, asked, sheet_->context().nowMs));
      return;
    }
    show(a.json);
  }, c.nowMs);
}

void SwitcherContent::show(const std::string& optionsJson) {
  options_ = optionsJson;
  choices_ = switchChoices(optionsJson);
  render();
}

void SwitcherContent::render() {
  auto* body = body_.get();
  if (body == nullptr) return;
  auto& c = sheet_->context();
  const std::string ep = c.session ? esc(c.session->episode) : std::string();
  std::string s;
  std::string group;
  for (std::size_t i = 0; i < choices_.size(); ++i) {
    const auto& ch = choices_[i];
    if (ch.group != group) {
      group = ch.group;
      s += "<div class=\"sgroup\">" + std::string(group == "voiceover" ? "[[picker.voiceover]]" : "[[player.switcher.sources]]") + "</div>";
    }
    ui::rml::ListItem it;
    it.leadIcon = ch.current ? "check" : (group == "voiceover" ? "audio" : "playlist");
    it.title = esc(ch.label);
    if (!ep.empty()) it.meta = ch.hasEpisode ? "[[player.switcher.has|label=" + ep + "]]" : "[[player.switcher.missing|label=" + ep + "]]";
    // the focused row's note sits at its end, so focus never changes a row's height
    it.trail = "<span class=\"choice-note\" id=\"sw-note-" + std::to_string(i) + "\"/>";
    it.action = "switch:" + std::to_string(i);
    it.id = "sw-" + std::to_string(i);
    s += ui::rml::listItem(it);
  }
  if (choices_.empty()) s = "<div class=\"muted\">[[ui.empty]]</div>";
  body->SetInnerRML(s);
  // focus on the current row, else the first
  Rml::Element* first = nullptr;
  for (std::size_t i = 0; i < choices_.size(); ++i) {
    if (choices_[i].current || first == nullptr) {
      if (auto* e = body->GetElementById("sw-" + std::to_string(i))) first = e;
      if (choices_[i].current) break;
    }
  }
  if (first != nullptr) {
    c.ui().focus().focus(first);
    sheet_->scrollTo(first);
    note(first);
  }
  c.ui().markDirty();
}

void SwitcherContent::focused(Rml::Element* e) { note(e); }

void SwitcherContent::failed() {
  resolving_ = false;
  render();
}

void SwitcherContent::note(Rml::Element* focused) {
  auto* body = body_.get();
  if (body == nullptr || sheet_ == nullptr) return;
  const auto& c = sheet_->context();
  for (std::size_t i = 0; i < choices_.size(); ++i) {
    auto* n = body->GetElementById("sw-note-" + std::to_string(i));
    const bool here = focused != nullptr && focused->GetId() == "sw-" + std::to_string(i);
    if (!here || choices_[i].current) {
      setText(n, "");
      continue;
    }
    setText(n, choices_[i].hasEpisode ? "[[player.osd.resume_from|time=" + clockTime(c.position()) + "]]"
                                      : "[[episodes.starts_over]]");
  }
}

bool SwitcherContent::activate(const ui::Activation& a) {
  if (!a.action.starts_with("switch:") || resolving_) return a.action.starts_with("switch:");
  const auto i = static_cast<std::size_t>(std::atoi(a.action.c_str() + 7));
  if (i >= choices_.size()) return true;
  const auto choice = choices_[i];
  if (choice.current) {
    sheet_->close();
    return true;
  }
  // a spinner in the sheet while the server resolves and starts it
  resolving_ = true;
  if (auto* b = body_.get()) b->SetInnerRML(ui::rml::spinner("[[resolving.getting_video]]"));
  if (chosen_) chosen_(choice, selectionJson(options_, choice.group, choice.id));
  return true;
}

}  // namespace cadence::screen::routes
