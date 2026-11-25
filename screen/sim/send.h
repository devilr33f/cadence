#pragma once

#include "ipc/codec.h"
#include "ui/cards/model.h"

#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::sim {

namespace cards = ::cadence::screen::ui::cards;

// one `send` built from its json: the envelope, and the deep copies of any cards it carries (the envelope points
// into them; both go together)
struct SendBuilt {
  ipc::OutEnvelope envelope{cadence_screen_v1_Envelope_hello_tag};
  std::vector<cards::CardPtr> cards;

  [[nodiscard]] const cadence_screen_v1_Envelope& get() const { return envelope.get(); }
};

// builds one cadenced->screen envelope from a script's `send` json: {"toast": {...}}, {"route": {...}},
// {"link_status": {...}}, {"claim_state": {...}}, {"voice_state": {...}}, {"assistant_turn": {...}},
// {"airplay_pin": {...}}, {"mirror_start": {...}}, {"mirror_stop": {...}}, {"display_power": {...}},
// {"capability_config": {...}}, {"duck": {...}}, {"media_session_state": {...}}, {"tasks_summary": {...}},
// {"alarms_summary": {...}} (nextInS: a time that many seconds after the send). cards come from
// the golden fixtures
// (.binpb files): every "cards" array names files, resolved against cardDir when not absolute. nullopt and the
// offending key in error
std::optional<SendBuilt> buildSend(const std::string& json, const std::string& cardDir, std::string* error);

}  // namespace cadence::screen::sim
