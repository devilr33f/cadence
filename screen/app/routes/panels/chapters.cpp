#include "app/routes/panels/chapters.h"

#include "ui/core/rml.h"

#include <cstdlib>

namespace cadence::screen::routes {

void ChaptersContent::open(SideSheet& sheet, Rml::Element* body) {
  sheet_ = &sheet;
  auto& c = sheet.context();
  if (!c.session) return;
  const auto& chapters = c.session->chapters;
  const int now = chapterAt(chapters, c.position());
  std::string s;
  for (std::size_t i = 0; i < chapters.size(); ++i) {
    ui::rml::ListItem it;
    it.leadIcon = static_cast<int>(i) == now ? "play" : "playlist";
    it.title = ui::rml::esc(chapters[i].title);
    it.trail = "<span class=\"figures\">" + clockTime(chapters[i].startMs) + "</span>";
    it.action = "chapter:" + std::to_string(i);
    it.id = "ch-" + std::to_string(i);
    if (static_cast<int>(i) == now) it.meta = "<div style=\"width: 200dp;\">" + ui::rml::progress(std::nullopt) + "</div>";
    s += ui::rml::listItem(it);
  }
  body->SetInnerRML(s);
  if (auto* e = body->GetElementById("ch-" + std::to_string(std::max(now, 0)))) {
    c.ui().focus().focus(e);
    sheet.scrollTo(e);
  }
}

bool ChaptersContent::activate(const ui::Activation& a) {
  if (!a.action.starts_with("chapter:")) return false;
  auto& c = sheet_->context();
  const auto i = static_cast<std::size_t>(std::atoi(a.action.c_str() + 8));
  if (c.session && i < c.session->chapters.size()) c.command(player::CommandAction::Seek, c.session->chapters[i].startMs);
  sheet_->close();
  return true;
}

}  // namespace cadence::screen::routes
