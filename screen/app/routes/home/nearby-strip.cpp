#include "app/routes/home/nearby-strip.h"

#include "app/routes/core/view.h"
#include "ui/core/rml.h"

#include <algorithm>

namespace cadence::screen::routes::home_nearby {

std::string strip(const std::vector<nearby::Item>& sameRoom) {
  if (sameRoom.empty()) return {};
  using ui::rml::esc;
  std::string s = "<div class=\"nearby\" id=\"home-nearby-strip\"><div class=\"nearby-label\">[[home.nearby]]</div>"
                  "<div class=\"nearby-track\">";
  const auto n = std::min(sameRoom.size(), kMaxItems);
  for (std::size_t i = 0; i < n; ++i) {
    const auto& it = sameRoom[i];
    std::string title = it.title.empty() ? std::string("[[home.now_playing]]") : esc(it.title);
    if (!it.artist.empty()) title += " \xc2\xb7 " + esc(it.artist);
    std::string label = esc(it.name);
    if (it.here) label += " \xc2\xb7 [[home.nearby_here]]";
    const std::string art = it.art.empty() ? ui::rml::icon("music") : ui::rml::artImage(it.art, "hnp-img");
    // the state as a glyph: what plays shows play, what waits shows pause
    s += "<div class=\"hnp nbi f" + std::string(it.paused ? " paused" : "") + "\" id=\"nearby-" + std::to_string(i) +
         "\" data-device=\"" + esc(it.deviceId) + "\" data-action=\"devices:open:" + esc(it.deviceId) + "\">" +
         view::focusDecor() + "<div class=\"hnp-art\">" + art + "</div><div class=\"hnp-copy\"><div class=\"hnp-label\">" +
         label + "</div><div class=\"hnp-title\">" + title + "</div></div><div class=\"hnp-state\">" +
         ui::rml::icon(it.paused ? "pause" : "play") + "</div></div>";
  }
  return s + "</div></div>";
}

}  // namespace cadence::screen::routes::home_nearby
