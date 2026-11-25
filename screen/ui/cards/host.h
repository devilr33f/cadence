#pragma once

#include "cadence/v2/rpc.pb.h"
#include "ui/cards/model.h"
#include "ui/cards/render.h"
#include "ui/core/ui.h"

#include <cstdint>
#include <functional>
#include <map>
#include <string>
#include <vector>

namespace cadence::screen::ui::cards {

// the request a card action becomes: screen/v1's rpc_request, method card.action, json {cardId | pageId, revision,
// actionId, value?} (pkg/rpc)
struct ActionCall {
  std::string rpcId;
  std::string json;
};

// cards on screen (design r2 sections 8, 9 and 17): a stack of at most 6 in the drawer or a panel page, replaced in
// place by a higher revision with only the changed fills and switches animating; actions with confirm dialogs, the
// optimistic pending state, the slider's one action 600 ms after its last key, choices, a page's next action,
// refusals (a toast and the rollback) and the 5 s timeout; countdowns and timeline sliders tick once a second (a
// timeline the remote moved holds its choice until the next revision, or a refusal sends it running again), ttl
// expires a card, and a long code block says how many lines are left
class CardHost {
 public:
  using Sender = std::function<bool(const ActionCall&)>;
  CardHost(Ui& ui, Sender send) : ui_(ui), send_(std::move(send)) {}

  // where the cards go (an element of the page); limit 6 for the drawer, 20 for a panel page
  void attach(Rml::Element* container, int limit, bool panel);
  // the element inside the container a card goes in (the drawer's chat: its turn's place), given its id and whether
  // it replaces a shown revision; null is the container. a revision whose place moved leaves its old one and enters
  // the new one as a new card
  using Placer = std::function<Rml::Element*(std::string_view cardId, bool replacing)>;
  void setPlacer(Placer placer) { placer_ = std::move(placer); }
  [[nodiscard]] bool has(std::string_view cardId) const;
  // the ids of the cards it holds, oldest first
  [[nodiscard]] std::vector<std::string> ids() const;
  // a card from the link; false when an equal or newer revision is already shown
  bool show(const cadence_cards_v1_Card& card);
  void showPage(const cadence_cards_v1_Page& page);
  void clear();
  [[nodiscard]] std::size_t count() const { return cards_.size(); }

  // an activation the ui could not place: true when it was a card's
  bool activate(const Activation& a);
  // the answer to a call this host sent: true when it was one
  bool response(const cadence_v2_RpcResponse& r);
  // a new page the server answered (a page's next action or an action that opens one)
  void onPage(std::function<void(const cadence_cards_v1_Page&)> f) { page_ = std::move(f); }

  // the device clock (epoch ms) and the ui's (monotonic) for countdowns, ttl and timeouts
  void tick(std::int64_t epochMs);

  // what a card's draw cost: counted refusals by reason (never the card's text)
  [[nodiscard]] const std::map<std::string, int>& fallbacks() const { return fallbacks_; }

 private:
  struct Shown {
    CardPtr card;
    Rml::ObserverPtr<Rml::Element> element;
    std::int64_t expiresMs = 0;
  };
  struct Pending {
    std::string cardId;
    std::string pageId;
    Rml::ObserverPtr<Rml::Element> element;
    std::int64_t sentMs = 0;
  };

  Rml::Element* place(const cadence_cards_v1_Card& card, Rml::Element* replacing);
  void send(Rml::Element* e, const std::string& actionId, const std::string& value);
  void rollback(const Pending& p);
  void refused(const Pending& p, std::string_view toast);
  void countdowns();
  // timeline sliders run from their anchors, each one the remote is not moving
  void timelines();
  void runTimeline(Rml::Element* e);
  // a title held to its lines (a track's beside its cover) cut at a word with «…»: rmlui has no line clamp
  void clampTitles();
  void codeBlocks();

  Ui& ui_;
  Sender send_;
  Rml::ObserverPtr<Rml::Element> container_;
  int limit_ = kMaxDrawerCards;
  bool panel_ = false;
  std::vector<Shown> cards_;
  std::map<std::string, Pending> pending_;
  std::uint64_t seq_ = 0;
  std::int64_t epochMs_ = 0;
  std::int64_t lastSecond_ = -1;
  std::map<std::string, int> fallbacks_;
  std::function<void(const cadence_cards_v1_Page&)> page_;
  Placer placer_;
};

// a card action's json (pkg/rpc's CardAction args); value is "" or a json number, string or array already
std::string actionJson(std::string_view cardId, std::string_view pageId, std::uint32_t revision, std::string_view actionId,
                       std::string_view value);

}  // namespace cadence::screen::ui::cards
