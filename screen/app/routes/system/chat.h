#pragma once

#include "cadence/v2/assistant.pb.h"

#include <cstddef>
#include <cstdint>
#include <functional>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

// the assistant drawer as a chat (the owner's revision of design r2 section 16): while the drawer is open each turn
// is a round of the conversation, newest at the bottom, older ones pushed up, smaller and dimmer, until they leave at
// the top. the user's words sit in a bubble on the right, the assistant's steps, cards and reply on the left; a card
// sits in the round its tool ran in, a source card after the reply that cites it. nothing here touches rmlui: the
// order, the ages, the stack's geometry and the tweens, so the tests read them plainly
namespace cadence::screen::routes::chat {

enum class Kind { User, Steps, Card, Ref, Thinking, Reply, Sources, State };

struct Step {
  std::string id;
  // the call card's title in the call's current state
  std::string label;
  cadence_v2_StepStatus status = cadence_v2_StepStatus_STEP_STATUS_UNSPECIFIED;
  // the call's one card, "" while it shows none
  std::string cardId{};
  // its card stands in the chat in the step's place: the line is not drawn
  bool inCard = false;
  // its card is in the showcase: the line points left to it
  bool left = false;
};

struct CardRef {
  std::string id;
  // placed under the reply (it arrived once the reply showed, or the reply cites it), else above it
  bool afterReply = false;
};

// a content card shown in the showcase on the left: the chat keeps one line of it in the round its tool ran in
struct RefLine {
  std::string cardId;
  // the line's rml (its icon, the card's title and count)
  std::string rml;
};

struct Round {
  // the dom's key, never reused while the drawer is open
  std::uint32_t key = 0;
  std::string turnId;
  // the user's words: the recognizer's stable part and the part it may still change
  std::string stable, unstable;
  std::vector<Step> steps;
  std::string reply;
  bool cited = false;
  std::vector<CardRef> cards;
  std::vector<RefLine> refs;
  // nothing worth keeping above the next round
  [[nodiscard]] bool empty() const;
  // a step line to draw (one whose card stands in its place is not)
  [[nodiscard]] bool hasStepLines() const;
  // a step of the round names the card
  [[nodiscard]] bool stepCard(std::string_view cardId) const;
  [[nodiscard]] std::string words() const;
};

// what of the live round shows now (the drawer's view and state decide)
struct LiveView {
  // the user's bubble, the steps and the cards (a turn or a failed one)
  bool turn = true;
  // listening: the bubble shows with no words yet (the microphone's caption)
  bool listening = false;
  // the reply has started to sound
  bool reply = false;
  // the assistant is thinking with nothing of its own to show yet (no reply, no step running): a pending reply
  // bubble where the reply will come
  bool thinking = false;
  // a state block (no microphone, offline, not heard, failed) under the round
  bool state = false;
};

struct Item {
  Kind kind = Kind::User;
  // the round's index in rounds()
  std::size_t round = 0;
  // the item's dom key, stable while it lives
  std::string key;
  std::string cardId;
};

// at most this many rounds are kept; older ones have long left the top
inline constexpr std::size_t kMaxRounds = 8;

class Log {
 public:
  Log();
  [[nodiscard]] const std::vector<Round>& rounds() const { return rounds_; }
  Round& live() { return rounds_.back(); }
  [[nodiscard]] const Round& live() const { return rounds_.back(); }
  // a new turn: the live round stays as history when it holds anything, else it is reused
  Round& next(const std::string& turnId);
  // the drawer closed: nothing of the conversation is kept
  void clear();
  // the round a card is in (its index), if any
  [[nodiscard]] std::optional<std::size_t> roundOf(std::string_view cardId) const;
  // the card joins the live round (leaving the round it was in); afterReply as CardRef says
  void place(std::string_view cardId, bool afterReply);
  // a content card's line joins the live round, once (a newer revision of it keeps the line where it is)
  void ref(std::string_view cardId, std::string rml);
  // the cards keep says are gone (a ttl, the drawer's limit) leave their rounds
  void prune(const std::function<bool(std::string_view)>& keep);
  // each round's steps learn where their cards are: in the chat (the card stands for the line) or the showcase; a
  // showcase line whose card is no longer there goes
  void placeSteps(const std::function<bool(std::string_view)>& inChat, const std::function<bool(std::string_view)>& inShowcase);
  // oldest first: per round its user's words, steps, the showcase's lines, cards before the reply, the reply, then the sources label and
  // the cards after it; the live round as live says
  [[nodiscard]] std::vector<Item> items(const LiveView& live) const;
  // rounds after round i (0 for the live round)
  [[nodiscard]] std::size_t age(std::size_t round) const { return rounds_.size() - 1 - round; }
  [[nodiscard]] static std::string cardKey(const Round& r, std::string_view cardId);
  [[nodiscard]] static std::string refKey(const Round& r, std::string_view cardId);

 private:
  std::vector<Round> rounds_;
  std::uint32_t nextKey_ = 1;
};

// how an item looks at its age: a scale (toward its own bottom corner) and an opacity. the live round is whole; its
// user's words and steps settle a step back once the reply shows; each older round a step further
struct Look {
  double scale = 1;
  double opacity = 1;
};
Look look(std::size_t age, Kind kind, bool settled);

// an item's layout box in the viewport's coordinates (y down), untouched by transforms
struct Box {
  double y = 0;
  double h = 0;
};
// each item's visual bottom: the newest stays where layout put it; each older one shrinks toward its bottom and moves
// down to close what its newer neighbours gave up, its gap shrinking with it; offset moves them all down (the
// history scrolled back)
std::vector<double> bottoms(const std::vector<Box>& boxes, const std::vector<Look>& looks, double offset);

double cubicOut(double t);

// a value easing to its target, cubic-out over a duration, retargeted mid-way without a jump
struct Tween {
  double from = 0;
  double to = 0;
  std::int64_t start = 0;
  std::int64_t ms = 0;
  [[nodiscard]] double at(std::int64_t now) const;
  [[nodiscard]] bool done(std::int64_t now) const { return now - start >= ms || from == to; }
  void retarget(double target, std::int64_t now, std::int64_t durationMs);
  void snap(double v);
};

// the live words' birth times, so the words a partial transcript adds fade in and the ones it keeps stay
class Fade {
 public:
  // the text now: words past what both texts share are born now
  void update(std::string_view text, std::int64_t now);
  void clear();
  // word i's birth (ms), 0 for one known since before the fades
  [[nodiscard]] std::int64_t born(std::size_t i) const { return i < born_.size() ? born_[i] : 0; }
  [[nodiscard]] std::size_t size() const { return born_.size(); }

 private:
  std::string text_;
  std::vector<std::int64_t> born_;
};

// the words of text, split on white space
std::vector<std::string_view> words(std::string_view text);
// how many leading words a and b share
std::size_t sharedWords(std::string_view a, std::string_view b);

// the user's words as rml: stable words paper, the rest paper-2, words younger than fadeMs in spans of their birth
// (class fresh, data-born) that the drawer fades in; every space inside a span (rmlui drops one standing alone
// between two elements)
std::string wordsMarkup(std::string_view stable, std::string_view unstable, const Fade& fade, std::int64_t now,
                        std::int64_t fadeMs);

}  // namespace cadence::screen::routes::chat
