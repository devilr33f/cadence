#pragma once

#include "app/routes/system/alarms-model.h"
#include "i18n/translator.h"

#include <cstdint>
#include <string>
#include <string_view>
#include <vector>

namespace Rml {
class Element;
}

// what the ring overlay, home's strip and the alarms page draw alike: a kind's word and glyph, a repeat's days, where
// a device is, and a timer's countdown as a span its holder ticks each second (data-end, unix ms). every text that
// comes from the owner (a label, a name, a room) is escaped here
namespace cadence::screen::routes::alarms_view {

// Будильник, Таймер, Напоминание, else Сигнал
std::string kindWord(i18n::Translator& tr, std::string_view kind);
// alarm, timer, calendar
std::string kindIcon(std::string_view kind);
// a reminder's label, else its kind's word (escaped)
std::string titleOf(i18n::Translator& tr, std::string_view kind, std::string_view label);
// «Каждый день», «Один раз», or «Пн, Ср, Пт»
std::string daysText(i18n::Translator& tr, const std::vector<std::string>& days);
// «Кухня · Гостиная», either alone, or "" (escaped)
std::string whereText(std::string_view name, std::string_view room);
// a span with the time left until endMs, ticked by tickCountdowns
std::string countdownSpan(std::int64_t endMs, std::int64_t nowMs);
// every countdown span under root set to nowMs's time left
void tickCountdowns(Rml::Element* root, std::int64_t nowMs);
// a string as an rml id's part ([A-Za-z0-9_-], the rest as _)
std::string idPart(std::string_view s);

}  // namespace cadence::screen::routes::alarms_view
