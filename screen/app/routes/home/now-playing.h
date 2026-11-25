#pragma once

#include "app/routes/core/router.h"

#include <optional>
#include <string>

namespace cadence::screen::routes::home_now {

// home's compact way back to background audio; video keeps the hero's «Продолжить» instead
std::string chip(const std::optional<BackgroundPlay>& play);

}  // namespace cadence::screen::routes::home_now
