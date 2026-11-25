#include "app/routes/home/now-playing.h"

#include "app/routes/core/view.h"
#include "ui/core/rml.h"

namespace cadence::screen::routes::home_now {

std::string chip(const std::optional<BackgroundPlay>& play) {
  if (!play || !play->audio) return {};
  std::string title = play->title.empty() ? std::string("[[home.now_playing]]") : play->title;
  if (!play->artist.empty()) title += " \xc2\xb7 " + play->artist;

  std::string art = ui::rml::icon("audio");
  if (!play->art.empty()) art = ui::rml::artImage(play->art, "hnp-img");
  // the state is visible without opening the page: pause while playing, continue while paused
  const std::string state = play->paused ? "play" : "pause";
  const std::string word = play->paused ? "[[home.continue]]" : "[[player.osd.pause]]";
  const std::string ref = play->itemRef.empty() ? std::string() : " data-ref=\"" + ui::rml::esc(play->itemRef) + "\"";
  return "<div class=\"hnp f" + std::string(play->itemRef.empty() ? "" : " has-ref") +
         std::string(play->paused ? " paused" : "") + "\" id=\"home-now-playing\" data-action=\"hero:resume\"" + ref + ">" +
         view::focusDecor() +
         "<div class=\"hnp-art\">" + art + "</div><div class=\"hnp-copy\"><div class=\"hnp-label\">[[home.now_playing]]</div>" +
         "<div class=\"hnp-title\">" + title + "</div></div><div class=\"hnp-state\">" + ui::rml::icon(state) +
         "<span>" + word + "</span></div></div>";
}

}  // namespace cadence::screen::routes::home_now
