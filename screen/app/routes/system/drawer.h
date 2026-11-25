#pragma once

#include "app/routes/system/chat-view.h"
#include "app/routes/system/chat.h"
#include "app/routes/system/device.h"
#include "app/routes/system/env.h"
#include "app/routes/system/page-rpc.h"
#include "app/routes/system/showcase.h"
#include "ui/cards/host.h"

#include <cstdint>
#include <functional>
#include <map>
#include <memory>
#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// the assistant drawer (design r2 section 16): a 760 dp sheet on the right over a 55 % duck of whatever is behind it,
// a layer of its own over any route. its body is a chat (chat.h): while it stays open each turn is a round, newest at
// the foot, older ones pushed up, smaller and dimmer; closing it forgets them all. a turn's seven states come from the server's AssistantTurn (the room's speaker
// listens, the server fans the turn out to this screen); the press paths (G, long BACK) open it at once and ask the
// server to open the room's microphone (rpc assistant.invoke). no microphone, offline, not heard and a failed turn
// are its own states. a wake-word turn opens it without focus: ↓ or G takes focus in, BACK always closes it
class Drawer {
 public:
  enum class View { Turn, NoMic, MicOffline, Offline, NotHeard, Failed, SetupQr };

  Drawer(Env env, PageRpc& rpc, const DeviceState& device);
  ~Drawer();
  Drawer(const Drawer&) = delete;
  Drawer& operator=(const Drawer&) = delete;

  // assistant_turn, voice_level, voice_state and cards for the shown turn: true when it was the drawer's
  bool message(const cadence_screen_v1_Envelope& m);
  // a press opened it (G, long BACK, the mic button, «Ассистент» in an actions sheet)
  void invoke();
  void close();
  // a lease's first heartbeat goes at once, the next at the server's pace (it answers heartbeatMs)
  static constexpr std::int64_t kLeaseHeartbeatMs = 3000;

  [[nodiscard]] bool isOpen() const { return open_; }
  // focus is in the drawer (key-opened, or after ↓ in wake-word mode)
  [[nodiscard]] bool focused() const { return scoped_; }
  [[nodiscard]] View view() const { return view_; }
  [[nodiscard]] cadence_v2_AssistantState state() const { return state_; }
  [[nodiscard]] const std::string& turnId() const { return turnId_; }
  [[nodiscard]] cards::CardHost& cards() { return *cards_; }
  // the content cards on the left (the tests' view of the reel)
  [[nodiscard]] Showcase& showcase() { return *show_; }
  // the listening line's thickness in dp (the tests' and the gallery's view of it)
  [[nodiscard]] double lineDp() const { return lineDp_; }
  // the reply's code points revealed so far
  [[nodiscard]] std::size_t revealed() const { return revealed_; }
  // the conversation and its elements (the tests' view of the order and the ages)
  [[nodiscard]] const chat::Log& log() const { return log_; }
  [[nodiscard]] const ChatView& chat() const { return chat_; }

  // the system hears of an open, a close and a new turn (screen context, the panels under it)
  std::function<void(bool open)> onOpenChanged;
  // «Как подключить» and the mic's settings: the room screen
  std::function<void()> onRoomSettings;

 private:
  struct Word {
    std::uint32_t start = 0, end = 0, atMs = 0;
  };

  std::string build();
  void loaded(Rml::ElementDocument& doc);
  bool key(const platform::KeyEvent& e);
  bool activate(const ui::Activation& a);
  void tick(std::int64_t nowMs);
  void focusChanged(Rml::Element* e);

  void openDrawer(bool keyed);
  void enter();
  void reset(const std::string& turnId);
  void beginConversationReset(const std::string& turnId);
  void apply(const cadence_v2_AssistantTurn& t);
  void setState(cadence_v2_AssistantState s);
  void setView(View v);
  void stop();
  void back();
  void sendStop();

  void render();
  void renderHeader();
  void renderChat();
  void renderFoot();
  std::string userRml(const chat::Round& r, bool live, bool listeningNow);
  std::string stepsRml(const std::vector<chat::Step>& steps, bool live);
  std::string stateRml();
  void focusFirstCard();
  // stop/cancel first, reset when it is the foot's only action
  bool focusFoot();
  // the history follows focus: back to a focused element in it, else to the newest
  void follow();
  // the card host's placer: a card's place in its round, a new one (or a turn's newer revision) in the live round
  Rml::Element* placeCard(std::string_view cardId, bool replacing);
  void showTurnCards(const cadence_v2_AssistantTurn& t);
  // a card from the server: a content card to the showcase (and its line in the chat), the rest inline
  void showCard(const cadence_cards_v1_Card& card);
  // focus into the showcase's front card (← past the drawer's left edge) and back (→ past its right edge, BACK)
  bool enterShowcase();
  void leaveShowcase();
  // the round focus is in, if any
  [[nodiscard]] std::optional<std::size_t> focusedRound() const;
  // the live round's card wrappers
  [[nodiscard]] std::vector<Rml::Element*> liveCards() const;
  [[nodiscard]] bool replyShown(cadence_v2_AssistantState s) const;
  void place(Rml::Element* e, const std::string& rml, std::string& cache);
  std::size_t revealTarget(std::int64_t nowMs) const;
  void revealTo(std::size_t upTo);
  [[nodiscard]] Rml::Element* el(const char* id) const;

  Env env_;
  Alive alive_;
  PageRpc& rpc_;
  const DeviceState& device_;
  std::unique_ptr<cards::CardHost> cards_;
  std::unique_ptr<Showcase> show_;
  // focus is in the showcase (its scope pushed over the drawer's)
  bool inShowcase_ = false;
  // the turn whose first cited card was brought to the showcase's front
  std::string citedFront_;
  std::vector<int> hooks_;
  Rml::ElementDocument* doc_ = nullptr;

  bool open_ = false;
  bool scoped_ = false;
  bool keyed_ = false;
  View view_ = View::Turn;
  std::string turnId_;
  std::uint32_t revision_ = 0;
  cadence_v2_AssistantState state_ = cadence_v2_AssistantState_ASSISTANT_STATE_UNSPECIFIED;
  cadence_v2_Trigger trigger_ = cadence_v2_Trigger_TRIGGER_UNSPECIFIED;
  Mic mic_ = Mic::Unknown;
  std::string stable_, unstable_;
  std::vector<chat::Step> steps_;
  // each step's birth, so a new one fades in (120 ms after the one before)
  std::map<std::string, std::int64_t> stepBorn_;
  std::string reply_;
  std::vector<Word> words_;
  std::vector<std::pair<std::uint32_t, std::string>> citations_;
  std::uint32_t followUpMs_ = 0;
  std::string error_;
  std::optional<cadence_v2_TurnFailure> failure_;
  bool speechStopped_ = false;
  std::string invokeId_;
  // the listening speaker's own name (MicSource.device_name), the header's
  std::string micName_;
  // media the turn started plays here (a play on this screen while the drawer was open): the showcase went, and the
  // drawer leaves soon after the reply
  bool mediaPlaying_ = false;
  std::string lastMedia_;
  void mediaStarted(const std::string& sessionId);
  // the lease on the chat's live cards (a Home Assistant write's entity, server-side pkg/rpc CardsLease): renewed by
  // heartbeat while the drawer is open and holds cards, released when it closes
  std::string leaseId_;
  std::uint64_t leases_ = 0;
  std::int64_t leaseMs_ = 0;
  std::int64_t heartbeatMs_ = kLeaseHeartbeatMs;
  bool leased_ = false;
  void heartbeat(std::int64_t nowMs);
  void releaseLease();
  // a press found no server (the link still coming up on a cold start): ask again as it arrives
  bool pendingOnline_ = false;

  std::int64_t now_ = 0;
  std::int64_t openedMs_ = 0;
  std::int64_t stateMs_ = 0;
  std::int64_t speakingMs_ = -1;
  std::int64_t lastKeyMs_ = 0;
  std::int64_t viewMs_ = 0;
  std::int64_t levelMs_ = 0;
  double micTarget_ = 0, outTarget_ = 0, level_ = 0;
  double lineDp_ = 4;
  std::size_t revealed_ = 0;
  std::size_t replyRunes_ = 0;
  bool backHint_ = false;
  // old chat nodes fade while the new reset round waits for their short exit
  bool clearingConversation_ = false;
  std::int64_t clearConversationAt_ = 0;

  std::string headCache_, footCache_;

  chat::Log log_;
  ChatView chat_;
  // the live words' births, and the reply's words revealed last (they fade in from replyBorn_)
  chat::Fade fade_;
  std::size_t replyFrom_ = 0;
  std::int64_t replyBorn_ = 0;
  // a turn's own cards are being shown: a newer revision of an older round's card moves to the live round
  bool turnCards_ = false;
  cadence_v2_AssistantState placing_ = cadence_v2_AssistantState_ASSISTANT_STATE_UNSPECIFIED;
  std::optional<std::size_t> awake_;
  std::string placingCard_;
};

// a reply's text as rml: escaped, its [n] markers as 30 dp squares on the baseline (reachable, data-cite), the code
// points from upTo on kept in place but unseen (so the layout never moves as words appear), those from freshFrom to
// upTo in a span that fades in (class fresh, data-born)
std::string replyMarkup(std::string_view text, std::size_t upTo, bool focusable, std::size_t freshFrom = SIZE_MAX,
                        std::int64_t bornMs = 0);
// code points of utf-8 text
std::size_t runeCount(std::string_view s);

}  // namespace cadence::screen::routes
