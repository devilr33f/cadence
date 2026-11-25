#pragma once

#include "ui/core/rml.h"

#include <string>

// the full-screen blurred backdrop home and resolving share: decoded at a quarter of the screen's width at most and
// blurred once by the decode worker (ui/images/blur.h), the gpu's linear filter scaling it up. one element shape, one
// size and one radius, so both ask the image cache for the same key: a title focused on home is never blurred again
// when resolving shows it, and recently focused ones stay in the cache's lru (never a per-frame gpu filter)
namespace cadence::screen::routes::backdrop {

inline constexpr int kMaxWidth = 480;
inline constexpr int kBlur = 12;

// a full-screen element of class cls (it must fill the screen: the cache key is its size) for an image id
inline std::string rml(const std::string& imageId, const std::string& cls) {
  return "<div class=\"" + cls + " netimg\" data-img=\"" + ui::rml::esc(imageId) + "\" data-img-max=\"" +
         std::to_string(kMaxWidth) + "\" data-img-blur=\"" + std::to_string(kBlur) + "\"/>";
}

}  // namespace cadence::screen::routes::backdrop
