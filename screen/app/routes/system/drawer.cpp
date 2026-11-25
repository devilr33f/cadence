#include "app/routes/system/drawer.h"
#include "app/routes/system/drawer-failure.h"

#include "i18n/gen/keys.h"
#include "ui/cards/content.h"
#include "ui/core/rml.h"
#include "ui/rml/sprites.h"
#include "ui/tokens.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cstring>

namespace cadence::screen::routes {

namespace {

using ui::rml::esc;
using ui::rml::icon;

constexpr int kZ = 30;
// design r2 section 16's timings
constexpr std::int64_t kLineGrowMs = 180;
constexpr std::int64_t kAttackMs = 40;
constexpr std::int64_t kReleaseMs = 200;
constexpr std::int64_t kRestingMs = 20000;
// once media the turn started plays here, the drawer leaves this long after the reply so the media shows
constexpr std::int64_t kMediaRestingMs = 2500;
// the showcase's fade when media starts
constexpr std::int64_t kShowcaseFadeMs = 400;
constexpr std::int64_t kNotHeardMs = 4000;
constexpr std::int64_t kStepEnterGapMs = 120;
constexpr std::int64_t kConversationResetMs = 180;
constexpr double kWordsPerSecond = 3.0;
// the follow-up window's default when the server names none
constexpr std::uint32_t kFollowUpDefaultMs = 6000;

const char* stateWord(cadence_v2_AssistantState s) {
  switch (s) {
    case cadence_v2_AssistantState_ASSISTANT_STATE_INVOKED:
      return "invoked";
    case cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING:
      return "listening";
    case cadence_v2_AssistantState_ASSISTANT_STATE_THINKING:
      return "thinking";
    case cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING:
      return "speaking";
    case cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP:
      return "follow_up";
    case cadence_v2_AssistantState_ASSISTANT_STATE_RESTING:
      return "resting";
    default:
      return "other";
  }
}

bool listening(cadence_v2_AssistantState s) {
  return s == cadence_v2_AssistantState_ASSISTANT_STATE_INVOKED || s == cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING;
}

// the byte offset of each code point, and the end
std::vector<std::size_t> runeOffsets(std::string_view s) {
  std::vector<std::size_t> out;
  for (std::size_t i = 0; i < s.size(); ++i) {
    if ((static_cast<unsigned char>(s[i]) & 0xC0) != 0x80) out.push_back(i);
  }
  out.push_back(s.size());
  return out;
}

std::string fmt(const char* f, double v) {
  char buf[64];
  std::snprintf(buf, sizeof buf, f, v);
  return buf;
}

double cubicOut(double t) {
  t = std::clamp(t, 0.0, 1.0);
  const double u = 1 - t;
  return 1 - u * u * u;
}

}  // namespace

std::size_t runeCount(std::string_view s) { return runeOffsets(s).size() - 1; }

std::string replyMarkup(std::string_view text, std::size_t upTo, bool focusable, std::size_t freshFrom, std::int64_t bornMs) {
  const auto offs = runeOffsets(text);
  const std::size_t cut = offs[std::min(upTo, offs.size() - 1)];
  // the fresh words: from freshFrom to the cut
  const std::size_t from = freshFrom < upTo ? offs[std::min(freshFrom, offs.size() - 1)] : cut;
  std::string seen, fresh, unseen;
  const auto bucket = [&](std::size_t at) -> std::string& { return at < from ? seen : at < cut ? fresh : unseen; };
  std::size_t i = 0;
  std::string run;
  std::size_t runAt = 0;
  const auto flush = [&] {
    // a run straddling a cut splits there
    std::size_t at = runAt;
    std::string_view rest = run;
    while (!rest.empty()) {
      const std::size_t edge = at < from ? from : at < cut ? cut : SIZE_MAX;
      const std::size_t n = edge == SIZE_MAX ? rest.size() : std::min(rest.size(), edge - at);
      bucket(at) += esc(rest.substr(0, n));
      rest.remove_prefix(n);
      at += n;
    }
    run.clear();
  };
  while (i < text.size()) {
    // [n] with n 1-99: a citation marker
    if (text[i] == '[') {
      std::size_t j = i + 1;
      while (j < text.size() && j - i <= 3 && text[j] >= '0' && text[j] <= '9') ++j;
      if (j > i + 1 && j < text.size() && text[j] == ']' && j - i <= 3) {
        flush();
        const std::string n(text.substr(i + 1, j - i - 1));
        const bool shown = i < cut;
        const std::string cls = std::string("cmark") + (shown && focusable ? " f" : "");
        bucket(i) += "<span class=\"" + cls + "\" data-cite=\"" + n + "\"" + (shown && focusable ? " data-action=\"cite:" + n + "\"" : "") +
                     ">" + n + "</span>";
        i = j + 1;
        continue;
      }
    }
    if (run.empty()) runAt = i;
    run += text[i];
    ++i;
  }
  flush();
  std::string out = seen;
  if (!fresh.empty()) out += "<span class=\"fresh\" data-born=\"" + std::to_string(bornMs) + "\">" + fresh + "</span>";
  if (!unseen.empty()) out += "<span class=\"unseen\">" + unseen + "</span>";
  return out;
}

Drawer::Drawer(Env env, PageRpc& rpc, const DeviceState& device)
    : env_(std::move(env)), rpc_(rpc), device_(device), chat_(env_.ui) {
  cards_ = std::make_unique<cards::CardHost>(env_.ui, [this](const cards::ActionCall& call) {
    // card.action through the routes' rpc, its answer back into this host
    cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
    e.which_body = cadence_screen_v1_Envelope_rpc_request_tag;
    copyTo(e.body.rpc_request.id, "drw:" + call.rpcId);
    copyTo(e.body.rpc_request.method, "card.action");
    std::string json = call.json;
    e.body.rpc_request.json = json.data();
    return env_.out.send(e);
  });
  cards_->setPlacer([this](std::string_view id, bool replacing) { return placeCard(id, replacing); });
  show_ = std::make_unique<Showcase>(env_.ui, [this](const cards::ActionCall& call) {
    // the showcase's card actions, answered under its own prefix
    cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
    e.which_body = cadence_screen_v1_Envelope_rpc_request_tag;
    copyTo(e.body.rpc_request.id, "shw:" + call.rpcId);
    copyTo(e.body.rpc_request.method, "card.action");
    std::string json = call.json;
    e.body.rpc_request.json = json.data();
    return env_.out.send(e);
  });
  hooks_.push_back(env_.ui.addKeyFilter([this](const platform::KeyEvent& e) { return key(e); }));
  hooks_.push_back(env_.ui.addActivateFilter([this](const ui::Activation& a) { return activate(a); }));
  hooks_.push_back(env_.ui.addTicker([this](std::int64_t now) { tick(now); }));
  hooks_.push_back(env_.ui.addFocusListener([this](Rml::Element* e) { focusChanged(e); }));
  env_.ui.addDocument("drawer", kZ, [this] { return build(); }, [this](Rml::ElementDocument& d) { loaded(d); });
}

Drawer::~Drawer() {
  if (uiAbandoned()) return;
  for (const int h : hooks_) env_.ui.remove(h);
  // its focus scope goes with it, so the page under it takes focus again
  if (scoped_) env_.ui.focus().popScope();
  env_.ui.removeDocument("drawer");
}

Rml::Element* Drawer::el(const char* id) const { return doc_ != nullptr ? doc_->GetElementById(id) : nullptr; }

std::string Drawer::build() {
  headCache_.clear();
  footCache_.clear();
  // the chat's list sits on the viewport's bottom and grows upward; its top edge fades into the sheet
  return "<div id=\"dl\" class=\"dlayer\"><div class=\"scrim-duck dduck\"/>"
         "<div id=\"showcase\" class=\"showcase\"><div class=\"sback\"/><div id=\"sdeck\" class=\"sdeck\"/></div>"
         "<div id=\"drawer\" class=\"drawer\">"
         "<div class=\"dhead\"><span class=\"dname\">[[assistant.title]]</span><span id=\"dstatus\" class=\"dstatus\"/></div>"
         "<div id=\"dscroll\" class=\"dscroll\"><div id=\"dmsgs\" class=\"chat\"/></div><div class=\"dfade\"/>"
         "<div class=\"dfoot\"><div class=\"dtrack\"><div id=\"dline\" class=\"dline\"><div class=\"dseg\"/></div></div>"
         "<div id=\"dfootb\" class=\"dfootb\"/><div id=\"dhint\" class=\"dhint hidden\">[[assistant.press_down]]</div></div>"
         "</div></div>"
         "<div id=\"backhint\" class=\"backhint\">" +
         icon("mic") + "</div>";
}

void Drawer::loaded(Rml::ElementDocument& doc) {
  doc_ = &doc;
  chat_.attach(el("dscroll"), el("dmsgs"));
  if (auto* l = el("dl")) l->SetClass("open", open_);
  // a reload (the language) built it again: the focus scope with it
  if (scoped_) {
    if (auto* d = el("drawer")) {
      env_.ui.focus().pushScope(d);
    }
  }
  // the rounds first, so each card finds its place again
  render();
  cards_->attach(el("dmsgs"), cards::kMaxDrawerCards, false);
  inShowcase_ = false;
  show_->attach(el("showcase"), el("sdeck"));
}

void Drawer::openDrawer(bool keyed) {
  const bool was = open_;
  if (!open_) {
    open_ = true;
    openedMs_ = env_.ui.nowMs();
    lastKeyMs_ = env_.ui.nowMs();
    if (auto* l = el("dl")) {
      l->SetClass("leaving", false);
      l->SetClass("open", true);
      l->SetClass("entering", true);
      auto ptr = l->GetObserverPtr();
      env_.ui.after(0, [ptr] {
        if (auto* x = ptr.get()) x->SetClass("entering", false);
      });
    }
  }
  keyed_ = keyed || keyed_;
  if (keyed && !scoped_) enter();
  if (!was && onOpenChanged) onOpenChanged(true);
  env_.ui.markDirty();
}

void Drawer::enter() {
  auto* d = el("drawer");
  if (d == nullptr || scoped_) return;
  // a dialog or a toast over the page keeps its focus: the drawer takes it when it closes
  scoped_ = true;
  keyed_ = true;
  env_.ui.focus().pushScope(d);
  render();
  // the foot's button, else the first actionable card element (once laid out): a card takes focus only by ↑ while
  // the assistant listens or talks, so no ring lights on it unasked
  if (focusFoot()) return;
  // no foot button yet (a press before its turn's first state): by the time this runs the turn may have brought one,
  // and a card it brought too must not take the focus from it; a card is the fallback only while the foot has none
  afterLayout(env_.ui, alive_.guard([this] {
    if (!scoped_) return;
    if (focusFoot()) return;
    focusFirstCard();
  }));
}

void Drawer::close() {
  if (!open_) return;
  open_ = false;
  // a confirm over a card goes with it
  while (env_.ui.levelOpen()) env_.ui.closeLevel();
  if (inShowcase_) leaveShowcase();
  if (scoped_) {
    scoped_ = false;
    env_.ui.focus().popScope();
  }
  keyed_ = false;
  if (auto* l = el("dl")) {
    l->SetClass("leaving", true);
    auto ptr = l->GetObserverPtr();
    env_.ui.after(tokens::kMotionSheetExitMs, alive_.guard([ptr, this] {
      auto* x = ptr.get();
      if (x != nullptr && !open_) {
        x->SetClass("open", false);
        x->SetClass("leaving", false);
      }
    }));
  }
  if (!invokeId_.empty()) rpc_.cancel(invokeId_);
  invokeId_.clear();
  pendingOnline_ = false;
  releaseLease();
  // privacy: the conversation leaves the screen with the drawer, unreachable by BACK
  clearingConversation_ = false;
  reset("");
  cards_->clear();
  show_->clear();
  log_.clear();
  chat_.clear();
  view_ = View::Turn;
  render();
  if (onOpenChanged) onOpenChanged(false);
  env_.ui.markDirty();
}

void Drawer::mediaStarted(const std::string& sessionId) {
  if (sessionId.empty() || sessionId == lastMedia_) return;
  lastMedia_ = sessionId;
  if (!open_) return;
  mediaPlaying_ = true;
  // the left cards fade out; the deck empties once they are gone
  if (auto* b = show_->box(); b != nullptr && show_->reel().current() != std::string()) {
    b->SetClass("away", true);
    env_.ui.after(kShowcaseFadeMs, alive_.guard([this] {
      show_->clear();
      if (auto* x = show_->box()) x->SetClass("away", false);
      // the steps that pointed left point nowhere now
      renderChat();
    }));
  }
  if (state_ == cadence_v2_AssistantState_ASSISTANT_STATE_RESTING) stateMs_ = std::max(stateMs_, env_.ui.nowMs());
  env_.ui.markDirty();
}

void Drawer::reset(const std::string& turnId) {
  // the live round moves up as history (when it holds anything) and a new one starts at the foot
  if (doc_ != nullptr) renderChat();
  log_.next(turnId);
  // a new turn's cards alone on the left: an earlier turn's found things never linger behind them
  if (!turnId.empty()) show_->clear();
  mediaPlaying_ = false;
  fade_.clear();
  stepBorn_.clear();
  replyFrom_ = 0;
  replyBorn_ = 0;
  turnId_ = turnId;
  revision_ = 0;
  state_ = cadence_v2_AssistantState_ASSISTANT_STATE_UNSPECIFIED;
  stable_.clear();
  unstable_.clear();
  steps_.clear();
  reply_.clear();
  words_.clear();
  citations_.clear();
  error_.clear();
  failure_.reset();
  followUpMs_ = 0;
  speechStopped_ = false;
  speakingMs_ = -1;
  revealed_ = 0;
  replyRunes_ = 0;
  micTarget_ = outTarget_ = level_ = 0;
}

void Drawer::beginConversationReset(const std::string& turnId) {
  clearingConversation_ = true;
  clearConversationAt_ = env_.ui.nowMs() + kConversationResetMs;
  chat_.fadeOut();
  // the data and card ownership go now; the old chat nodes stay only to fade
  log_.clear();
  cards_->clear();
  show_->clear();
  citedFront_.clear();
  reset(turnId);
}

void Drawer::invoke() {
  const bool wasScoped = scoped_;
  openDrawer(true);
  if (wasScoped || !scoped_) enter();
  lastKeyMs_ = env_.ui.nowMs();
  pendingOnline_ = false;
  // a new turn: what was said before moves up
  reset("");
  // the server is not there: offline at once, no line that promises listening (section 16). the press is kept: a
  // link still coming up (a cold start's press may outrun it) asks again as it arrives
  if (!device_.online()) {
    pendingOnline_ = true;
    setView(View::Offline);
    return;
  }
  if (device_.mic == Mic::None) {
    setView(View::NoMic);
    return;
  }
  view_ = View::Turn;
  mic_ = device_.mic;
  setState(cadence_v2_AssistantState_ASSISTANT_STATE_INVOKED);
  if (!invokeId_.empty()) rpc_.cancel(invokeId_);
  invokeId_ = rpc_.call("assistant.invoke", "{}", [this](const cadence_v2_RpcResponse& r) {
    invokeId_.clear();
    const std::string json = std::string(sv(r.json));
    if (std::strcmp(r.status, "ok") == 0) {
      const auto turn = jsonField(json, "turnId");
      if (!turn.empty() && turnId_.empty()) turnId_ = turn;
      if (jsonField(json, "mic") == "local") mic_ = Mic::Local;
      else if (!jsonField(json, "micDeviceId").empty()) mic_ = Mic::Speaker;
      render();
      return;
    }
    const auto code = jsonField(json, "code");
    if (code == "no_microphone") setView(View::NoMic);
    else if (code == "mic_offline") setView(View::MicOffline);
    else setView(View::Failed);
  });
  if (invokeId_.empty()) setView(View::Offline);
  render();
}

void Drawer::setView(View v) {
  view_ = v;
  viewMs_ = env_.ui.nowMs();
  if (v != View::Turn) {
    // the turn's line and button go: the state block's one action takes the foot
    state_ = cadence_v2_AssistantState_ASSISTANT_STATE_UNSPECIFIED;
  }
  render();
  if (scoped_) focusFoot();
}

void Drawer::setState(cadence_v2_AssistantState s) {
  if (s == state_) return;
  state_ = s;
  stateMs_ = env_.ui.nowMs();
  if (s == cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING && speakingMs_ < 0) speakingMs_ = env_.ui.nowMs();
  if (s == cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP || s == cadence_v2_AssistantState_ASSISTANT_STATE_RESTING) {
    // the whole reply is shown once speech ends
    if (revealed_ < replyRunes_) revealTo(replyRunes_);
  }
  if (s == cadence_v2_AssistantState_ASSISTANT_STATE_RESTING) lastKeyMs_ = env_.ui.nowMs();
  render();
}

bool Drawer::message(const cadence_screen_v1_Envelope& m) {
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_media_play_tag:
      // a play on this screen (the server's: a catalog or video play, a song on its own player): the turn's media
      // shows, so the showcase goes and the drawer leaves after its reply; the play itself is the player's
      if (m.body.media_play.purpose == cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT) mediaStarted(m.body.media_play.session_id);
      return false;
    case cadence_screen_v1_Envelope_now_playing_tag:
      // a cast or a song audiod plays here started
      if (m.body.now_playing.session_id[0] != '\0' &&
          m.body.now_playing.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING) {
        mediaStarted(m.body.now_playing.session_id);
      }
      return false;
    case cadence_screen_v1_Envelope_rpc_response_tag: {
      // the answers to this host's card actions carry its prefix
      const auto& r = m.body.rpc_response;
      const bool shown = std::strncmp(r.id, "shw:", 4) == 0;
      if (std::strncmp(r.id, "drw:", 4) != 0 && !shown) return false;
      cadence_v2_RpcResponse copy = r;
      copyTo(copy.id, std::string_view(r.id + 4));
      if (shown) {
        show_->host().response(copy);
      } else {
        cards_->response(copy);
      }
      chat_.touch();
      render();
      return true;
    }
    case cadence_screen_v1_Envelope_link_status_tag:
      // a press that found no server asks again once the link is up; a turn or a close took it over already. the
      // envelope goes on: the enroll page hears the link too
      if (pendingOnline_ && m.body.link_status.state == cadence_screen_v1_LinkState_LINK_STATE_ONLINE) {
        pendingOnline_ = false;
        invoke();
      }
      return false;
    case cadence_screen_v1_Envelope_assistant_turn_tag:
      apply(m.body.assistant_turn);
      return true;
    case cadence_screen_v1_Envelope_voice_level_tag: {
      const auto& v = m.body.voice_level;
      if (!open_ || (v.turn_id[0] != '\0' && !turnId_.empty() && turnId_ != v.turn_id)) return true;
      micTarget_ = v.mic / 255.0;
      outTarget_ = v.output / 255.0;
      levelMs_ = env_.ui.nowMs();
      return true;
    }
    case cadence_screen_v1_Envelope_voice_state_tag: {
      // this screen's own microphone: its levels, the state as the server's turn says it
      const auto& v = m.body.voice_state;
      micTarget_ = v.mic / 255.0;
      outTarget_ = v.output / 255.0;
      levelMs_ = env_.ui.nowMs();
      return false;
    }
    case cadence_screen_v1_Envelope_cards_tag: {
      const auto& c = m.body.cards;
      if (open_ && std::strcmp(c.context_id, "live") == 0) {
        // a leased card's next state: it replaces the card where it is, in the chat or the showcase, never a new one
        for (pb_size_t i = 0; i < c.cards_count; ++i) {
          const auto id = cards::str(c.cards[i].id);
          if (cards_->has(id)) cards_->show(c.cards[i]);
          if (show_->has(id)) show_->show(c.cards[i]);
        }
        chat_.touch();
        render();
        return true;
      }
      if (!open_ || turnId_.empty() || turnId_ != c.context_id) return false;
      turnCards_ = true;
      placing_ = state_;
      for (pb_size_t i = 0; i < c.cards_count; ++i) showCard(c.cards[i]);
      turnCards_ = false;
      chat_.touch();
      render();
      return true;
    }
    default:
      return false;
  }
}

void Drawer::apply(const cadence_v2_AssistantTurn& t) {
  const std::string id = t.turn_id;
  // a turn takes the drawer: no press of its own is waiting on the link any more
  pendingOnline_ = false;
  if (t.state == cadence_v2_AssistantState_ASSISTANT_STATE_DISMISSED) {
    if (open_ && (id == turnId_ || turnId_.empty())) close();
    return;
  }
  if (t.conversation_reset) {
    // the server may repeat a state after a reconnect; one marker starts one fade
    if (clearingConversation_) return;
    beginConversationReset(id);
  }
  if (!open_) {
    // a wake word said to the room's speaker opens it without focus; a press with it
    trigger_ = t.trigger;
    reset(id);
    openDrawer(t.trigger != cadence_v2_Trigger_TRIGGER_WAKE);
  } else if (!id.empty() && id != turnId_ && !turnId_.empty()) {
    // a new turn in an open drawer: the old turn moves up as history, its cards with it (at most 6)
    reset(id);
    // focus left in that history goes to the new turn (its foot button, else its newest card)
    afterLayout(env_.ui, alive_.guard([this] {
      if (!scoped_ || !focusedRound().has_value() || *focusedRound() + 1 == log_.rounds().size()) return;
      if (!focusFoot()) focusFirstCard();
    }));
  } else if (!id.empty() && id == turnId_ && t.revision != 0 && t.revision <= revision_) {
    return;
  }
  if (turnId_.empty()) turnId_ = id;
  revision_ = t.revision;
  trigger_ = t.trigger;
  view_ = View::Turn;
  if (t.has_mic_source) {
    switch (t.mic_source.kind) {
      case cadence_v2_MicSourceKind_MIC_SOURCE_KIND_NONE:
        mic_ = Mic::None;
        break;
      case cadence_v2_MicSourceKind_MIC_SOURCE_KIND_SPEAKER:
        mic_ = Mic::Speaker;
        micName_ = t.mic_source.device_name != nullptr ? std::string(t.mic_source.device_name) : std::string();
        break;
      case cadence_v2_MicSourceKind_MIC_SOURCE_KIND_LOCAL:
        mic_ = Mic::Local;
        break;
      default:
        break;
    }
  }
  const auto now = env_.ui.nowMs();
  if (t.has_transcript) {
    // a partial transcript grows the bubble in place: the words it keeps stay, the ones it adds fade in
    stable_ = sv(t.transcript.stable);
    unstable_ = sv(t.transcript.unstable);
    fade_.update(stable_.empty() || unstable_.empty() ? stable_ + unstable_ : stable_ + " " + unstable_, now);
  }
  steps_.clear();
  int fresh = 0;
  for (pb_size_t i = 0; t.steps != nullptr && i < t.steps_count; ++i) {
    chat::Step st;
    st.id = t.steps[i].id;
    st.label = std::string(sv(t.steps[i].label));
    st.status = t.steps[i].status;
    st.cardId = t.steps[i].card_id;
    steps_.push_back(std::move(st));
    // each new step enters over 280 ms, 120 ms after the one before
    if (stepBorn_.emplace(t.steps[i].id, now + fresh * kStepEnterGapMs).second) ++fresh;
  }
  if (t.has_reply) {
    const std::string text(sv(t.reply.text));
    if (text != reply_) {
      reply_ = text;
      replyRunes_ = runeCount(reply_);
    }
    words_.clear();
    for (pb_size_t i = 0; t.reply.words != nullptr && i < t.reply.words_count; ++i) {
      const auto& w = t.reply.words[i];
      words_.push_back(Word{w.text_start, w.text_end, w.start_ms});
    }
    citations_.clear();
    for (pb_size_t i = 0; t.reply.citations != nullptr && i < t.reply.citations_count; ++i) {
      citations_.emplace_back(t.reply.citations[i].n, t.reply.citations[i].card_id);
    }
  }
  followUpMs_ = t.follow_up_window_ms;
  showTurnCards(t);
  // once the reply sounds, the card it cites first comes to the deck's front and stays while it is talked about
  if (!citations_.empty() && replyShown(t.state) && citedFront_ != turnId_) {
    if (show_->has(citations_.front().second)) {
      show_->cite(citations_.front().second, 0);
      citedFront_ = turnId_;
    }
  }
  error_ = t.error_code;
  if (t.has_failure) failure_ = t.failure;
  else failure_.reset();
  if (error_ == "not_heard") {
    setView(View::NotHeard);
    return;
  }
  if (error_ == "offline") {
    setView(View::Offline);
    return;
  }
  if (mic_ == Mic::None && listening(t.state)) {
    setView(View::NoMic);
    return;
  }
  if (!error_.empty()) {
    // a server error mid-turn: the steps keep their state, an error block explains with «Повторить»
    state_ = t.state;
    setView(View::Failed);
    return;
  }
  setState(t.state);
  render();
}

void Drawer::showTurnCards(const cadence_v2_AssistantTurn& t) {
  // a card lands where its tool ran: above the reply, or under it once the reply shows (a source the reply cites
  // always under it); a turn's newer revision of an older round's card moves down to this round
  turnCards_ = true;
  placing_ = t.state;
  for (pb_size_t i = 0; t.cards != nullptr && i < t.cards_count; ++i) showCard(t.cards[i]);
  turnCards_ = false;
  chat_.touch();
}

void Drawer::showCard(const cadence_cards_v1_Card& card) {
  // control and state cards stay in the chat; found things go to the showcase, the chat keeps a line of them
  if (!cards::isContent(card) || cards_->has(cards::str(card.id))) {
    cards_->show(card);
    return;
  }
  if (!show_->show(card)) return;
  const auto sum = cards::summarize(card);
  // a title is the server's text: no character of the message syntax may reach the translator's markup
  std::string title = sum.title;
  std::replace(title.begin(), title.end(), '|', '/');
  std::replace(title.begin(), title.end(), ']', ')');
  std::replace(title.begin(), title.end(), '[', '(');
  const std::string glyph = ui::rml::hasIcon(sum.icon) ? sum.icon : std::string("search");
  log_.ref(cards::str(card.id), "<div class=\"sref\">" + icon("chevron_left", "sarrow") + icon(glyph, "sicon") +
                                   "<span>[[showcase.ref|title=" + esc(title) + "|count=" + std::to_string(sum.count) +
                                   "]]</span></div>");
  renderChat();
}

bool Drawer::replyShown(cadence_v2_AssistantState s) const {
  return !reply_.empty() && (s == cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING ||
                             s == cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP ||
                             s == cadence_v2_AssistantState_ASSISTANT_STATE_RESTING);
}

Rml::Element* Drawer::placeCard(std::string_view cardId, bool replacing) {
  const auto in = log_.roundOf(cardId);
  const bool live = in.has_value() && *in + 1 == log_.rounds().size();
  if (!in.has_value() || (replacing && turnCards_ && !live)) {
    const bool cited = std::any_of(citations_.begin(), citations_.end(), [&](const auto& c) { return c.second == cardId; });
    log_.place(cardId, cited || replyShown(turnCards_ ? placing_ : state_));
    // the host holds it only once placed: the prune must not take it meanwhile
    placingCard_ = std::string(cardId);
    renderChat();
    placingCard_.clear();
  }
  const auto r = log_.roundOf(cardId);
  auto* e = r.has_value() ? chat_.element(chat::Log::cardKey(log_.rounds()[*r], cardId)) : nullptr;
  return e != nullptr ? e : el("dmsgs");
}

void Drawer::sendStop() {
  if (turnId_.empty()) return;
  rpc_.call("assistant.stop", "{\"turnId\":" + jsonString(turnId_) + "}");
}

void Drawer::stop() {
  // «Стоп»: stop listening now (the turn runs on what was heard), or stop speaking
  sendStop();
  if (state_ == cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING) {
    speechStopped_ = true;
    if (revealed_ < replyRunes_) revealTo(replyRunes_);
  }
  render();
}

void Drawer::back() {
  switch (state_) {
    case cadence_v2_AssistantState_ASSISTANT_STATE_INVOKED:
    case cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING:
    case cadence_v2_AssistantState_ASSISTANT_STATE_THINKING:
      // cancels the turn and closes
      sendStop();
      close();
      return;
    case cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING:
      // the first press stops speech, the second closes
      if (!speechStopped_) {
        stop();
        return;
      }
      close();
      return;
    default:
      close();
  }
}

bool Drawer::key(const platform::KeyEvent& e) {
  const auto k = e.key;
  const auto a = e.action;
  const bool press = a == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN || a == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS;
  // long BACK: the mic glyph fades in past 250 ms (the key tracker's hint), the drawer opens at 500
  if (k == cadence_screen_v1_Key_KEY_BACK && a == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN) {
    backHint_ = true;
    if (auto* h = el("backhint")) h->SetClass("on", true);
    env_.ui.markDirty();
    return false;
  }
  if (backHint_ && (k == cadence_screen_v1_Key_KEY_BACK || k == cadence_screen_v1_Key_KEY_ASSISTANT)) {
    backHint_ = false;
    if (auto* h = el("backhint")) h->SetClass("on", false);
  }
  const bool assistant = k == cadence_screen_v1_Key_KEY_ASSISTANT &&
                         (press || a == cadence_screen_v1_KeyAction_KEY_ACTION_LONG);
  if (!open_) {
    // G or long BACK from anywhere: the key still goes to cadenced, which acts on it for a screen's own microphone
    if (assistant) invoke();
    return false;
  }
  if (env_.ui.levelOpen() || env_.ui.toastFocused()) return false;
  lastKeyMs_ = env_.ui.nowMs();
  if (assistant) {
    // wake-word mode: G takes focus in; else a new turn without closing
    if (!scoped_) {
      enter();
    } else {
      invoke();
    }
    return false;
  }
  if (k == cadence_screen_v1_Key_KEY_ASSISTANT) return false;
  if (k == cadence_screen_v1_Key_KEY_BACK && inShowcase_) {
    // BACK in the showcase goes back to the drawer
    if (a == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS || a == cadence_screen_v1_KeyAction_KEY_ACTION_UP) leaveShowcase();
    return true;
  }
  if (k == cadence_screen_v1_Key_KEY_BACK) {
    // BACK always closes it in either mode, and never also acts on the screen behind
    if (a == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS || a == cadence_screen_v1_KeyAction_KEY_ACTION_UP) back();
    return true;
  }
  if (!scoped_) {
    // without focus the d-pad and OK keep doing what they did; the first ↓ moves focus in
    if (k == cadence_screen_v1_Key_KEY_DOWN) {
      if (press || a == cadence_screen_v1_KeyAction_KEY_ACTION_REPEAT) enter();
      return true;
    }
    return false;
  }
  // the showcase: ← past the drawer's left edge takes focus into its front card, → past the card's right edge or BACK
  // brings it back. arrows act on their down, press and repeat, as the focus does
  const bool arrow = a != cadence_screen_v1_KeyAction_KEY_ACTION_UP && a != cadence_screen_v1_KeyAction_KEY_ACTION_LONG;
  if (inShowcase_) {
    if (k == cadence_screen_v1_Key_KEY_RIGHT && arrow) {
      auto* before = env_.ui.focus().current();
      env_.ui.focus().move(ui::Dir::Right);
      if (env_.ui.focus().current() == before) leaveShowcase();
      return true;
    }
    return false;
  }
  if (k == cadence_screen_v1_Key_KEY_LEFT && arrow && state_ != cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING &&
      show_->currentCard() != nullptr) {
    auto* before = env_.ui.focus().current();
    env_.ui.focus().move(ui::Dir::Left);
    if (env_.ui.focus().current() == before) enterShowcase();
    return true;
  }
  // focus never moves into cards while speaking
  if (state_ == cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING && view_ == View::Turn &&
      (k == cadence_screen_v1_Key_KEY_UP || k == cadence_screen_v1_Key_KEY_DOWN)) {
    return true;
  }
  return false;
}

bool Drawer::activate(const ui::Activation& a) {
  auto* d = el("drawer");
  if (d == nullptr || a.element == nullptr || !d->Contains(a.element)) return false;
  lastKeyMs_ = env_.ui.nowMs();
  if (a.action == "drawer:retry") {
    invoke();
    return true;
  }
  if (a.action == "drawer:setup") {
    setView(View::SetupQr);
    return true;
  }
  if (a.action == "drawer:room") {
    close();
    if (onRoomSettings) onRoomSettings();
    return true;
  }
  if (a.action.starts_with("cite:")) {
    // OK on a marker focuses its numbered source: in the showcase's card it cites, else in the live round's cards
    const auto n = a.action.substr(5);
    for (const auto& c : citations_) {
      if (std::to_string(c.first) != n || !show_->has(c.second)) continue;
      show_->cite(c.second, c.first);
      show_->tick(env_.ui.nowMs());
      if (enterShowcase()) {
        if (auto* card = show_->currentCard()) {
          Rml::ElementList links;
          card->QuerySelectorAll(links, ".clink");
          for (auto* l : links) {
            if (l->GetAttribute<Rml::String>("data-cite", "") == n) env_.ui.focus().focus(l);
          }
        }
      }
      return true;
    }
    for (auto* w : liveCards()) {
      Rml::ElementList links;
      w->QuerySelectorAll(links, ".clink");
      for (auto* l : links) {
        if (l->GetAttribute<Rml::String>("data-cite", "") == a.action.substr(5)) {
          env_.ui.focus().focus(l);
          return true;
        }
      }
    }
    return true;
  }
  if (cards_->activate(a)) {
    chat_.touch();
    render();
    return true;
  }
  if (show_->host().activate(a)) return true;
  return false;
}

void Drawer::focusChanged(Rml::Element* e) {
  auto* d = el("drawer");
  if (d == nullptr) return;
  // a focused marker lights its source card in the live round
  const std::string n = e != nullptr && e->IsClassSet("cmark") ? e->GetAttribute<Rml::String>("data-cite", "") : "";
  for (auto* w : liveCards()) {
    Rml::ElementList cites;
    w->QuerySelectorAll(cites, ".clink .cite");
    for (auto* c : cites) c->SetClass("on", !n.empty() && c->GetParentNode()->GetAttribute<Rml::String>("data-cite", "") == n);
  }
  // and brings its card to the showcase's front
  std::string citedCard;
  for (const auto& c : citations_) {
    if (!n.empty() && std::to_string(c.first) == n) citedCard = c.second;
  }
  if (!citedCard.empty() && show_->has(citedCard)) {
    show_->cite(citedCard, static_cast<std::uint32_t>(std::stoul(n)));
  } else {
    show_->cite({}, 0);
  }
  if (e != nullptr && d->Contains(e)) follow();
  // focus in an older round wakes it whole, so what it holds reads at full size
  if (focusedRound() != awake_) renderChat();
}

void Drawer::follow() {
  auto* list = el("dmsgs");
  auto* f = env_.ui.focus().current();
  chat_.reveal(list != nullptr && f != nullptr && list->Contains(f) ? f : nullptr);
}

std::optional<std::size_t> Drawer::focusedRound() const {
  auto* item = chat_.itemOf(env_.ui.focus().current());
  if (item == nullptr) return std::nullopt;
  const auto key = item->GetAttribute<Rml::String>("data-key", "");
  for (std::size_t i = 0; i < log_.rounds().size(); ++i) {
    if (key.starts_with("r" + std::to_string(log_.rounds()[i].key) + "-")) return i;
  }
  return std::nullopt;
}

std::vector<Rml::Element*> Drawer::liveCards() const {
  std::vector<Rml::Element*> out;
  const auto& r = log_.live();
  for (const auto& c : r.cards) {
    if (auto* e = chat_.element(chat::Log::cardKey(r, c.id))) out.push_back(e);
  }
  return out;
}

bool Drawer::enterShowcase() {
  auto* card = show_->currentCard();
  auto* box = show_->box();
  if (card == nullptr || box == nullptr || !scoped_) return false;
  Rml::ElementList fs;
  card->QuerySelectorAll(fs, ".f");
  Rml::Element* first = nullptr;
  for (auto* f : fs) {
    if (f->IsVisible(true)) {
      first = f;
      break;
    }
  }
  if (first == nullptr) return false;
  env_.ui.focus().pushScope(box);
  env_.ui.focus().focus(first);
  inShowcase_ = true;
  env_.ui.markDirty();
  return true;
}

void Drawer::leaveShowcase() {
  if (!inShowcase_) return;
  inShowcase_ = false;
  // the drawer's scope is under it: popping brings back what was focused there
  env_.ui.focus().popScope();
  show_->cite({}, 0);
  env_.ui.markDirty();
}

bool Drawer::focusFoot() {
  for (const char* id : {"dbtn"}) {
    if (auto* b = el(id)) {
      env_.ui.focus().focus(b);
      return true;
    }
  }
  return false;
}

void Drawer::focusFirstCard() {
  auto* list = el("dmsgs");
  if (list == nullptr) return;
  // the newest card (nearest the foot) with something to act on
  for (int i = list->GetNumChildren() - 1; i >= 0; --i) {
    auto* item = list->GetChild(i);
    if (!item->IsClassSet("card")) continue;
    Rml::ElementList fs;
    item->QuerySelectorAll(fs, ".f");
    for (auto* f : fs) {
      if (f->IsVisible(true)) {
        env_.ui.focus().focus(f);
        return;
      }
    }
  }
}

void Drawer::place(Rml::Element* e, const std::string& rml, std::string& cache) {
  if (e == nullptr || rml == cache) return;
  cache = rml;
  e->SetInnerRML(rml);
  env_.ui.markDirty();
}

void Drawer::render() {
  if (doc_ == nullptr) return;
  if (auto* d = el("drawer")) {
    d->SetClass("wake", open_ && !scoped_);
    d->SetClass("resting", state_ == cadence_v2_AssistantState_ASSISTANT_STATE_RESTING);
    // a turn has no buttons at the foot: the chat reaches down to it
    d->SetClass("nofoot", view_ == View::Turn);
  }
  renderHeader();
  renderChat();
  renderFoot();
  if (auto* h = el("dhint")) h->SetClass("hidden", !(open_ && !scoped_ && view_ != View::Turn));
  env_.ui.markDirty();
}

void Drawer::renderHeader() {
  std::string word;
  if (view_ == View::Turn && state_ != cadence_v2_AssistantState_ASSISTANT_STATE_UNSPECIFIED) {
    word = env_.tr.tr(i18n::keys::AssistantStatus{stateWord(state_)});
  }
  // whose microphone listens sits by the status: the speaker's name (its room's when it gave none), or the tv's own
  std::string mic;
  if (mic_ == Mic::Local) {
    mic = env_.tr.tr(i18n::keys::AssistantMicName{"tv", ""});
  } else if (mic_ == Mic::Speaker) {
    const std::string name = micName_.empty() ? device_.roomName : micName_;
    if (!name.empty()) mic = env_.tr.tr(i18n::keys::AssistantMicName{"speaker", name});
  }
  std::string rml;
  if (!mic.empty()) rml = "<span class=\"dmic\">" + icon("mic") + "<span>" + esc(mic) + "</span></span>";
  if (!word.empty()) rml += "<span class=\"dword\">" + esc(word) + "</span>";
  place(el("dstatus"), rml, headCache_);
}

std::string Drawer::userRml(const chat::Round& r, bool live, bool listeningNow) {
  // whose microphone listens is the header's (renderHeader), not a line over the words
  (void)listeningNow;
  const std::string caption;
  const std::int64_t fadeMs = env_.ui.reducedMotion() ? 0 : tokens::kMotionEnterMs;
  std::string words;
  if (!live) {
    words = chat::wordsMarkup(r.words(), "", chat::Fade{}, 0, 0);
  } else if (listeningNow) {
    // the live partial: stable words paper, the ones that may still change paper-2
    words = chat::wordsMarkup(stable_, unstable_, fade_, env_.ui.nowMs(), fadeMs);
  } else {
    // the server heard the end of speech: all of it stands
    words = chat::wordsMarkup(r.words(), "", fade_, env_.ui.nowMs(), fadeMs);
  }
  if (words.empty()) return caption;
  return caption + "<div class=\"ububble\">" + words + "</div>";
}

std::string Drawer::stepsRml(const std::vector<chat::Step>& steps, bool live) {
  const auto now = env_.ui.nowMs();
  const bool reduced = env_.ui.reducedMotion();
  std::string s;
  for (const auto& st : steps) {
    if (st.inCard) continue;
    std::string mark;
    switch (st.status) {
      case cadence_v2_StepStatus_STEP_STATUS_DONE:
        mark = icon("check", "tone-success");
        break;
      case cadence_v2_StepStatus_STEP_STATUS_FAILED:
        mark = icon("triangle", "tone-warning");
        break;
      default:
        mark = "<div class=\"spinner small\"/>";
    }
    std::string cls = "dstep";
    std::string born;
    if (const auto it = stepBorn_.find(st.id); live && !reduced && it != stepBorn_.end() && now - it->second < tokens::kMotionEnterMs) {
      cls += " fresh rise";
      born = " data-born=\"" + std::to_string(it->second) + "\"";
    }
    s += "<div class=\"" + cls + "\"" + born + " data-step=\"" + esc(st.id) + "\">" + mark + (st.left ? icon("chevron_left", "sarrow") : std::string()) + "<span>" + esc(st.label) +
         "</span></div>";
  }
  return s;
}

std::string Drawer::stateRml() {
  switch (view_) {
    case View::NoMic:
      return "<div class=\"dstate\">" + icon("mic_off") + "<div class=\"stitle\">[[assistant.no_mic_room]]</div>"
             "<div class=\"sbody\">[[assistant.no_mic_why]]</div></div>";
    case View::MicOffline:
      return "<div class=\"dstate\">" + icon("mic_off") + "<div class=\"stitle\">" +
             esc(env_.tr.tr(i18n::keys::AssistantMicOffline{device_.roomName})) +
             "</div><div class=\"sbody\">[[assistant.mic_offline_why]]</div></div>";
    case View::SetupQr: {
      const std::string url = device_.serverUrl.empty() ? std::string("https://cadence.local") : device_.serverUrl;
      return "<div class=\"dstate\"><div class=\"qr dqr\" style=\"decorator: image(&quot;gen:qr:" + ui::hexOf(url) +
             "&quot;);\"/><div class=\"sbody\">[[assistant.setup_qr]]</div></div>";
    }
    case View::Offline:
      return "<div class=\"dstate\">" + icon("offline") + "<div class=\"stitle\">[[offline.no_voice]]</div></div>";
    case View::NotHeard:
      return "<div class=\"dstate\">" + icon("mic") + "<div class=\"stitle\">[[assistant.didnt_hear]]</div>"
             "<div class=\"sbody\">[[assistant.didnt_hear_why]]</div></div>";
    case View::Failed: {
      const auto failure = assistantfailure::of(error_, failure_.has_value() ? &*failure_ : nullptr);
      std::string s = "<div class=\"dstate danger\">" + icon("octagon", "tone-danger") +
                      "<div class=\"stitle\">[[assistant.failed]]</div><div class=\"sbody\">" + failure.reason + "</div>";
      if (!failure.source.empty()) s += "<div class=\"scode mono\">" + esc(failure.source) + "</div>";
      if (!failure.code.empty()) s += "<div class=\"scode mono\">" + esc(failure.code) + "</div>";
      return s + "</div>";
    }
    case View::Turn:
      break;
  }
  return {};
}

void Drawer::renderChat() {
  if (doc_ == nullptr || clearingConversation_) return;
  // the live round as the drawer holds it now
  auto& live = log_.live();
  live.turnId = turnId_;
  live.stable = stable_;
  live.unstable = unstable_;
  live.steps = steps_;
  live.reply = reply_;
  live.cited = !citations_.empty();
  // cards the host let go (a ttl, the limit of 6) leave their rounds
  log_.prune([this](std::string_view id) { return id == placingCard_ || cards_->has(id); });
  // a step whose card stands in the chat is drawn as that card; one whose card went left points to it
  log_.placeSteps([this](std::string_view id) { return cards_->has(id); }, [this](std::string_view id) { return show_->has(id); });

  chat::LiveView lv;
  lv.turn = view_ == View::Turn || view_ == View::Failed;
  lv.listening = view_ == View::Turn && listening(state_);
  lv.reply = replyShown(state_) || (view_ == View::Failed && !reply_.empty());
  lv.state = view_ != View::Turn;
  lv.thinking = view_ == View::Turn && state_ == cadence_v2_AssistantState_ASSISTANT_STATE_THINKING && !lv.reply &&
                std::none_of(steps_.begin(), steps_.end(), [](const chat::Step& st) {
                  return st.status == cadence_v2_StepStatus_STEP_STATUS_RUNNING;
                });
  awake_ = focusedRound();
  const auto items = log_.items(lv);
  const std::size_t liveIndex = log_.rounds().size() - 1;
  std::vector<ChatView::Spec> specs;
  specs.reserve(items.size());
  for (const auto& it : items) {
    const auto& r = log_.rounds()[it.round];
    const bool isLive = it.round == liveIndex;
    ChatView::Spec sp;
    sp.key = it.key;
    sp.look = chat::look(awake_ == it.round ? 0 : log_.age(it.round), it.kind, isLive && lv.reply);
    const std::string side = isLive ? " live" : " past";
    switch (it.kind) {
      case chat::Kind::User:
        sp.cls = "ci user" + side;
        sp.rml = userRml(r, isLive, lv.listening);
        break;
      case chat::Kind::Steps:
        sp.cls = "ci steps" + side;
        sp.rml = stepsRml(r.steps, isLive);
        break;
      case chat::Kind::Card:
        sp.cls = "ci card" + side;
        sp.host = true;
        break;
      case chat::Kind::Ref:
        sp.cls = "ci ref" + side;
        for (const auto& l : r.refs) {
          if (l.cardId == it.cardId) sp.rml = l.rml;
        }
        break;
      case chat::Kind::Thinking:
        // the pending reply: three dots in a wave (opacity and transform alone), alive only while it shows
        sp.cls = "ci thinking" + side;
        sp.rml = "<div class=\"tbubble\"><div class=\"tdot\"/><div class=\"tdot t2\"/><div class=\"tdot t3\"/></div>";
        break;
      case chat::Kind::Reply:
        sp.cls = "ci reply" + side;
        if (isLive) {
          sp.rml = replyMarkup(reply_, revealed_, true, env_.ui.reducedMotion() ? SIZE_MAX : replyFrom_, replyBorn_);
          // the markers walk with ←/→ like a row's items
          if (sp.rml.find("cmark f") != std::string::npos) sp.cls += " row";
        } else {
          sp.rml = replyMarkup(r.reply, SIZE_MAX, false);
        }
        break;
      case chat::Kind::Sources:
        sp.cls = "ci src" + side;
        sp.rml = "[[cards.sources]]";
        break;
      case chat::Kind::State:
        sp.cls = "ci state" + side;
        sp.rml = stateRml();
        break;
    }
    specs.push_back(std::move(sp));
  }
  chat_.sync(specs);
}

void Drawer::renderFoot() {
  std::string b;
  const auto button = [&](const char* label, const char* action) {
    return ui::rml::button(ui::rml::Button{label, {}, action, false, false, false, "dbtn", "default-focus"});
  };
  switch (view_) {
    case View::NoMic:
      b = button("[[enroll.how]]", "drawer:setup");
      break;
    case View::MicOffline:
      b = button("[[room.title]]", "drawer:room");
      break;
    case View::SetupQr:
      b = button("[[room.title]]", "drawer:room");
      break;
    case View::Offline:
    case View::Failed:
      b = button("[[common.retry]]", "drawer:retry");
      break;
    case View::NotHeard:
      b = button("[[assistant.again]]", "drawer:retry");
      break;
    case View::Turn:
      // no buttons during a turn (the owner's call): back stops speech or cancels, and the foot holds the focus
      // quietly, a ringless anchor, so ↑ still reaches the cards
      if (state_ == cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP)
        // «можно продолжить» is the header's status now: the foot stays empty, so the chat does not move
        b = "<div id=\"dbtn\" class=\"dhold dfollow f default-focus\"/>";
      else
        b = "<div id=\"dbtn\" class=\"dhold f default-focus\"/>";
      break;
  }
  auto* foot = el("dfootb");
  if (foot == nullptr || b == footCache_) return;
  // the button the focus was on comes back after the swap
  auto* cur = env_.ui.focus().current();
  const bool had = cur != nullptr && foot->Contains(cur);
  place(foot, b, footCache_);
  if (had && !focusFoot()) focusFirstCard();
}

void Drawer::revealTo(std::size_t upTo) {
  // the words just revealed fade in from now
  replyFrom_ = revealed_;
  replyBorn_ = env_.ui.nowMs();
  revealed_ = upTo;
  renderChat();
}

std::size_t Drawer::revealTarget(std::int64_t nowMs) const {
  if (speakingMs_ < 0) return 0;
  const auto elapsed = nowMs - speakingMs_;
  if (!words_.empty()) {
    // the server's word timings
    std::size_t upTo = 0;
    for (const auto& w : words_) {
      if (static_cast<std::int64_t>(w.atMs) <= elapsed) upTo = std::max<std::size_t>(upTo, w.end);
    }
    return std::min(upTo, replyRunes_);
  }
  // else 3 words a second
  const auto words = static_cast<std::size_t>(static_cast<double>(elapsed) * kWordsPerSecond / 1000.0) + 1;
  const auto offs = runeOffsets(reply_);
  std::size_t seen = 0;
  bool inWord = false;
  for (std::size_t r = 0; r + 1 < offs.size(); ++r) {
    const char c = reply_[offs[r]];
    const bool space = c == ' ' || c == '\n' || c == '\t';
    if (!space && !inWord) {
      if (seen == words) return r;
      ++seen;
    }
    inWord = !space;
  }
  return replyRunes_;
}

void Drawer::heartbeat(std::int64_t nowMs) {
  if (leased_ && nowMs - leaseMs_ < heartbeatMs_) return;
  const auto ids = cards_->ids();
  if (ids.empty()) return;
  if (leaseId_.empty()) leaseId_ = "drawer-" + std::to_string(++leases_) + "-" + std::to_string(nowMs);
  std::string list;
  for (const auto& id : ids) {
    if (!list.empty()) list += ",";
    list += jsonString(id);
  }
  // acquire and renew are one call; a link that dropped meanwhile gets the lease again by this one
  leaseMs_ = nowMs;
  leased_ = true;
  rpc_.call("cards.lease", "{\"leaseId\":" + jsonString(leaseId_) + ",\"cardIds\":[" + list + "]}",
            [this](const cadence_v2_RpcResponse& r) {
              // the rpc dies with the drawer, so its answer never outlives this
              const char* hb = r.json != nullptr ? std::strstr(r.json, "\"heartbeatMs\":") : nullptr;
              if (hb != nullptr) {
                const auto ms = std::strtoll(hb + 14, nullptr, 10);
                if (ms >= 500 && ms <= 30000) heartbeatMs_ = ms;
              }
            });
}

void Drawer::releaseLease() {
  if (leased_ && !leaseId_.empty()) rpc_.call("cards.release", "{\"leaseId\":" + jsonString(leaseId_) + "}");
  leased_ = false;
  leaseId_.clear();
}

void Drawer::tick(std::int64_t nowMs) {
  const auto dt = static_cast<double>(std::max<std::int64_t>(0, nowMs - now_));
  now_ = nowMs;
  if (!open_) return;
  heartbeat(nowMs);
  // the reply, word by word with speech
  if (state_ == cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING && view_ == View::Turn && !speechStopped_) {
    const auto target = revealTarget(nowMs);
    if (target > revealed_) revealTo(target);
  }
  // the chat's tweens, after anything above changed it
  chat_.tick(nowMs);
  if (clearingConversation_) {
    if (nowMs >= clearConversationAt_) {
      clearingConversation_ = false;
      chat_.clear();
      render();
    } else {
      // even an empty chat, or reduced motion's shorter fade, needs the swap's frame
      env_.ui.markDirty();
    }
  }
  // the showcase's reel: hurried while the reply is spoken, still while focus is in it
  show_->hurry(state_ == cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING);
  show_->hold(inShowcase_);
  show_->tick(nowMs);
  // the line: the listening microphone's level (attack 40 ms, release 200 ms) or the reply's
  const bool speaking = state_ == cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING;
  const double target = listening(state_) ? micTarget_ : speaking ? outTarget_ : 0.0;
  const double tau = target > level_ ? static_cast<double>(kAttackMs) : static_cast<double>(kReleaseMs);
  level_ += (target - level_) * (1 - std::exp(-dt / tau));
  auto* line = el("dline");
  if (line == nullptr) return;
  std::string mode = "hidden";
  std::string transform;
  if (view_ == View::Turn) {
    if (listening(state_)) {
      mode = "level";
      lineDp_ = 4 + 20 * level_;
      const double grow = cubicOut(static_cast<double>(nowMs - openedMs_) / static_cast<double>(kLineGrowMs));
      transform = fmt("scaleX(%.4f) ", grow) + fmt("scaleY(%.4f)", lineDp_ / 4);
    } else if (state_ == cadence_v2_AssistantState_ASSISTANT_STATE_THINKING) {
      mode = "sweep";
      lineDp_ = 4;
      transform = "scaleX(1)";
    } else if (speaking) {
      mode = "level";
      lineDp_ = 4 + 8 * level_;
      transform = fmt("scaleY(%.4f)", lineDp_ / 4);
    } else if (state_ == cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP) {
      mode = "count";
      lineDp_ = 4;
      const double window = followUpMs_ > 0 ? followUpMs_ : kFollowUpDefaultMs;
      const double left = std::clamp(1 - static_cast<double>(nowMs - stateMs_) / window, 0.0, 1.0);
      transform = fmt("scaleX(%.4f)", left);
    }
  }
  if (mode == "hidden") lineDp_ = 0;
  for (const char* m : {"hidden", "level", "sweep", "count"}) line->SetClass(m, mode == m);
  const auto* p = line->GetProperty("transform");
  if (!transform.empty() && (p == nullptr || p->ToString() != transform)) {
    line->SetProperty("transform", transform);
    env_.ui.markDirty();
  }
  // follow-up ends with its window when the server says nothing: resting with cards, else closed
  if (view_ == View::Turn && state_ == cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP) {
    const auto window = static_cast<std::int64_t>(followUpMs_ > 0 ? followUpMs_ : kFollowUpDefaultMs);
    if (nowMs - stateMs_ > window + 1000) {
      if (cards_->count() > 0) {
        setState(cadence_v2_AssistantState_ASSISTANT_STATE_RESTING);
      } else {
        close();
        return;
      }
    }
  }
  // the turn's media plays here: the drawer gets out of its way soon after the reply
  if (mediaPlaying_ && view_ == View::Turn && state_ == cadence_v2_AssistantState_ASSISTANT_STATE_RESTING &&
      nowMs - std::max(stateMs_, lastKeyMs_) >= kMediaRestingMs) {
    close();
    return;
  }
  // resting: auto-dismiss after 20 s without a key
  if (view_ == View::Turn && state_ == cadence_v2_AssistantState_ASSISTANT_STATE_RESTING && nowMs - lastKeyMs_ >= kRestingMs) {
    close();
    return;
  }
  // nothing heard: closes after 4 s
  if (view_ == View::NotHeard && nowMs - viewMs_ >= kNotHeardMs && nowMs - lastKeyMs_ >= kNotHeardMs) close();
}

}  // namespace cadence::screen::routes
