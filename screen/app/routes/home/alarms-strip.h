#pragma once

#include "app/routes/system/alarms-model.h"
#include "i18n/translator.h"

#include <cstdint>
#include <string>

namespace cadence::screen::routes::home_alarms {

// at most this many entries: the next alarm and the timers that run, this screen's and its room's first
inline constexpr std::size_t kMaxItems = 4;

// home's strip under «Играет рядом»: the next alarm (its time and where it rings) and each running timer's live
// countdown (alarms_view::tickCountdowns), in the nearby strip's chip shape; each opens the alarms page on its
// device. "" with nothing to show
std::string strip(const alarms::Summary& summary, std::int64_t nowMs, i18n::Translator& tr);

}  // namespace cadence::screen::routes::home_alarms
