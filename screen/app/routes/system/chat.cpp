#include "app/routes/system/chat.h"

#include "ui/core/rml.h"

#include <algorithm>

namespace cadence::screen::routes::chat {

namespace {

bool space(char c) { return c == ' ' || c == '\n' || c == '\t' || c == '\r'; }

}  // namespace

bool Round::empty() const {
  return stable.empty() && unstable.empty() && steps.empty() && reply.empty() && cards.empty() && refs.empty();
}

bool Round::hasStepLines() const {
  return std::any_of(steps.begin(), steps.end(), [](const Step& s) { return !s.inCard; });
}

bool Round::stepCard(std::string_view cardId) const {
  return !cardId.empty() && std::any_of(steps.begin(), steps.end(), [&](const Step& s) { return s.cardId == cardId; });
}

std::string Round::words() const {
  if (stable.empty()) return unstable;
  if (unstable.empty()) return stable;
  return stable + " " + unstable;
}

Log::Log() { clear(); }

void Log::clear() {
  rounds_.clear();
  rounds_.push_back(Round{});
  rounds_.back().key = nextKey_++;
}

Round& Log::next(const std::string& turnId) {
  if (!live().empty()) {
    rounds_.push_back(Round{});
    rounds_.back().key = nextKey_++;
    // the oldest have long left the top
    while (rounds_.size() > kMaxRounds) rounds_.erase(rounds_.begin());
  } else {
    // nothing of the reused round may linger
    const auto key = live().key;
    live() = Round{};
    live().key = key;
  }
  live().turnId = turnId;
  return live();
}

std::optional<std::size_t> Log::roundOf(std::string_view cardId) const {
  for (std::size_t i = 0; i < rounds_.size(); ++i) {
    for (const auto& c : rounds_[i].cards) {
      if (c.id == cardId) return i;
    }
  }
  return std::nullopt;
}

void Log::place(std::string_view cardId, bool afterReply) {
  for (auto& r : rounds_) {
    std::erase_if(r.cards, [&](const CardRef& c) { return c.id == cardId; });
  }
  live().cards.push_back(CardRef{std::string(cardId), afterReply});
}

void Log::ref(std::string_view cardId, std::string rml) {
  for (auto& r : rounds_) {
    for (auto& l : r.refs) {
      if (l.cardId == cardId) {
        l.rml = std::move(rml);
        return;
      }
    }
  }
  live().refs.push_back(RefLine{std::string(cardId), std::move(rml)});
}

void Log::prune(const std::function<bool(std::string_view)>& keep) {
  for (auto& r : rounds_) {
    std::erase_if(r.cards, [&](const CardRef& c) { return !keep(c.id); });
  }
}

void Log::placeSteps(const std::function<bool(std::string_view)>& inChat,
                     const std::function<bool(std::string_view)>& inShowcase) {
  for (auto& r : rounds_) {
    for (auto& st : r.steps) {
      const bool placed = std::any_of(r.cards.begin(), r.cards.end(), [&](const CardRef& c) { return c.id == st.cardId; });
      st.inCard = !st.cardId.empty() && placed && inChat(st.cardId);
      st.left = !st.cardId.empty() && !st.inCard && inShowcase(st.cardId);
    }
    // a showcase card a step points to needs no line of its own, and one the showcase let go (a new turn, the
    // turn's media started) none at all
    std::erase_if(r.refs, [&](const RefLine& l) { return r.stepCard(l.cardId) || !inShowcase(l.cardId); });
  }
}

std::string Log::cardKey(const Round& r, std::string_view cardId) {
  return "r" + std::to_string(r.key) + "-c-" + std::string(cardId);
}

std::string Log::refKey(const Round& r, std::string_view cardId) {
  return "r" + std::to_string(r.key) + "-f-" + std::string(cardId);
}

std::vector<Item> Log::items(const LiveView& live) const {
  std::vector<Item> out;
  for (std::size_t i = 0; i < rounds_.size(); ++i) {
    const auto& r = rounds_[i];
    const bool isLive = i + 1 == rounds_.size();
    const std::string k = "r" + std::to_string(r.key);
    if (!isLive || live.turn) {
      if (!r.stable.empty() || !r.unstable.empty() || (isLive && live.listening)) out.push_back({Kind::User, i, k + "-u", {}});
      if (r.hasStepLines()) out.push_back({Kind::Steps, i, k + "-s", {}});
      for (const auto& l : r.refs) out.push_back({Kind::Ref, i, refKey(r, l.cardId), l.cardId});
      for (const auto& c : r.cards) {
        if (!c.afterReply) out.push_back({Kind::Card, i, cardKey(r, c.id), c.id});
      }
      const bool reply = !r.reply.empty() && (!isLive || live.reply);
      if (isLive && live.thinking && !reply) out.push_back({Kind::Thinking, i, k + "-t", {}});
      if (reply) out.push_back({Kind::Reply, i, k + "-r", {}});
      const bool after = std::any_of(r.cards.begin(), r.cards.end(), [](const CardRef& c) { return c.afterReply; });
      if (after && reply && r.cited) out.push_back({Kind::Sources, i, k + "-src", {}});
      for (const auto& c : r.cards) {
        if (c.afterReply) out.push_back({Kind::Card, i, cardKey(r, c.id), c.id});
      }
    }
    if (isLive && live.state) out.push_back({Kind::State, i, k + "-st", {}});
  }
  return out;
}

Look look(std::size_t age, Kind kind, bool settled) {
  // each older round a step smaller and dimmer, still read from the sofa: past the third they stay at the last step
  // until they leave the top
  static constexpr Look kAges[] = {{1.0, 1.0}, {0.9, 0.8}, {0.82, 0.66}, {0.76, 0.56}};
  if (age == 0) {
    if (settled && (kind == Kind::User || kind == Kind::Steps)) return {0.94, 0.82};
    return kAges[0];
  }
  return kAges[std::min<std::size_t>(age, 3)];
}

std::vector<double> bottoms(const std::vector<Box>& boxes, const std::vector<Look>& looks, double offset) {
  const std::size_t n = std::min(boxes.size(), looks.size());
  std::vector<double> out(n);
  // what the items below gave up, carried upward: an item shrinks toward its own bottom by h * (1 - s), and the gap
  // above an item shrinks with that item
  double down = 0;
  for (std::size_t k = n; k-- > 0;) {
    if (k + 1 < n) {
      const double gap = std::max(0.0, boxes[k + 1].y - (boxes[k].y + boxes[k].h));
      down += boxes[k + 1].h * (1 - looks[k + 1].scale) + gap * (1 - looks[k].scale);
    }
    out[k] = boxes[k].y + boxes[k].h + down + offset;
  }
  return out;
}

double cubicOut(double t) {
  t = std::clamp(t, 0.0, 1.0);
  const double u = 1 - t;
  return 1 - u * u * u;
}

double Tween::at(std::int64_t now) const {
  if (ms <= 0 || now - start >= ms) return to;
  if (now <= start) return from;
  return from + (to - from) * cubicOut(static_cast<double>(now - start) / static_cast<double>(ms));
}

void Tween::retarget(double target, std::int64_t now, std::int64_t durationMs) {
  if (target == to) return;
  from = at(now);
  to = target;
  start = now;
  ms = durationMs;
}

void Tween::snap(double v) {
  from = to = v;
  ms = 0;
}

std::vector<std::string_view> words(std::string_view text) {
  std::vector<std::string_view> out;
  std::size_t i = 0;
  while (i < text.size()) {
    while (i < text.size() && space(text[i])) ++i;
    const std::size_t from = i;
    while (i < text.size() && !space(text[i])) ++i;
    if (i > from) out.push_back(text.substr(from, i - from));
  }
  return out;
}

std::size_t sharedWords(std::string_view a, std::string_view b) {
  const auto wa = words(a);
  const auto wb = words(b);
  std::size_t n = 0;
  while (n < wa.size() && n < wb.size() && wa[n] == wb[n]) ++n;
  return n;
}

void Fade::update(std::string_view text, std::int64_t now) {
  const auto was = words(text_);
  const auto is = words(text);
  std::vector<std::int64_t> born;
  born.reserve(is.size());
  bool same = true;
  for (std::size_t i = 0; i < is.size(); ++i) {
    // a word kept, or one still being spoken (the recognizer grew it), keeps its birth; from the first that changed
    // on, every word is new
    same = same && i < was.size() && (is[i] == was[i] || is[i].starts_with(was[i]));
    born.push_back(same ? (i < born_.size() ? born_[i] : 0) : now);
  }
  born_ = std::move(born);
  text_ = std::string(text);
}

void Fade::clear() {
  text_.clear();
  born_.clear();
}

std::string wordsMarkup(std::string_view stable, std::string_view unstable, const Fade& fade, std::int64_t now,
                        std::int64_t fadeMs) {
  const auto ws = words(stable);
  const auto wu = words(unstable);
  const std::size_t n = ws.size() + wu.size();
  std::string out;
  std::string run;
  std::string runClass;
  std::int64_t runBorn = 0;
  const auto flush = [&] {
    if (run.empty()) return;
    out += "<span class=\"" + runClass + (runBorn != 0 ? " fresh\" data-born=\"" + std::to_string(runBorn) : std::string()) +
           "\">" + ui::rml::esc(run) + "</span>";
    run.clear();
  };
  for (std::size_t i = 0; i < n; ++i) {
    const bool isStable = i < ws.size();
    const std::string cls = isStable ? "stable" : "unstable";
    const std::int64_t b = fade.born(i);
    const std::int64_t fresh = fadeMs > 0 && b != 0 && now - b < fadeMs ? b : 0;
    if (!run.empty() && (cls != runClass || fresh != runBorn)) flush();
    runClass = cls;
    runBorn = fresh;
    run += isStable ? ws[i] : wu[i - ws.size()];
    if (i + 1 < n) run += ' ';
  }
  flush();
  return out;
}

}  // namespace cadence::screen::routes::chat
