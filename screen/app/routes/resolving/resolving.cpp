#include "app/routes/resolving/resolving.h"

#include "app/routes/core/failure-card.h"
#include "app/routes/core/backdrop.h"

#include "app/routes/core/methods.h"
#include "app/routes/core/view.h"
#include "app/routes/player/cache-info.h"
#include "i18n/locale.h"
#include "ui/tokens.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>

namespace cadence::screen::routes {

using namespace ui::rml;

namespace {

// the elapsed counter appears after 5 s
constexpr std::int64_t kElapsedAfterMs = 5000;
// a play nobody started within this long failed
constexpr std::int64_t kStartTimeoutMs = 20000;
// a session whose picture never comes (a stream the player shows no frame of while paused) is handed over anyway:
// the player's page has its own buffering and gated states
constexpr std::int64_t kPictureWaitMs = 15000;
// the server's own resolve: neither its play nor its failure within this long failed. past the server's play budget
// (agent/tools playBudget, 90 s: a list's songs, yt-dlp, youtube's available_at wait, a skip or two)
constexpr std::int64_t kExternalTimeoutMs = 100000;

// a collection's songs, read before its first song resolves (the server's album, playlist and artist stages)
bool collectionStage(const std::string& stage) { return stage == "album" || stage == "playlist" || stage == "artist"; }

std::string stageWords(const std::string& stage, bool torrent) {
  if (stage == "connecting_peers") return "[[resolving.connecting]]";
  if (stage == "reading_stream") return torrent ? "[[resolving.getting_files]]" : "[[resolving.reading_page]]";
  if (stage == "matching") return "[[resolving.finding_episode]]";
  if (stage == "starting") return "[[resolving.getting_video]]";
  if (stage == "play") return "[[resolving.starting]]";
  if (stage == "cookies") return "[[resolving.cookies]]";
  if (stage == "extract") return "[[resolving.extract]]";
  if (stage == "challenge") return "[[resolving.challenge]]";
  if (stage == "formats") return "[[resolving.formats]]";
  if (stage == "relay") return "[[resolving.relay]]";
  if (stage == "pick") return "[[resolving.pick]]";
  if (stage == "wait_available") return "[[resolving.waiting]]";
  if (stage == "album") return "[[resolving.collection_album]]";
  if (stage == "artist") return "[[resolving.collection_artist]]";
  if (stage == "playlist") return "[[resolving.collection_playlist]]";
  return "[[resolving.getting_video]]";
}

}  // namespace

ResolvingRoute::ResolvingRoute(Services& s, const RouteArgs& args)
    : Route(s), title_(play::titleOf(args)), choices_(args.data.value("choices", Json::object())),
      label_(args.data.value("label", "")), from_(args.data.value("from", "")), external_(args.data.value("external", false)) {}

void ResolvingRoute::start() {
  if (!external_) {
    resolve();
    return;
  }
  // the server resolves it already: what plays now is not it, the next session is
  state_ = State::Resolving;
  startedMs_ = s_.ui.nowMs();
  before_ = s_.router.player() != nullptr ? s_.router.player()->state().sessionId : std::string();
  steps_ = {Step::named("reading_stream")};
  redraw();
  changed();
}

void ResolvingRoute::prepared(const std::string& title, const std::string& label, const std::string& art) {
  if (!external_ || state_ == State::Done) return;
  const bool named = title_.title != title || label_ != label || title_.art != art;
  if (!title.empty()) title_.title = title;
  if (!label.empty()) label_ = label;
  if (!art.empty()) title_.art = art;
  if (state_ == State::Failed) {
    // the server tries again (another link): the step starts over
    start();
  }
  if (named || state_ == State::Resolving) {
    if (!steps_.empty() && !title.empty()) {
      for (auto& st : steps_) st.done = true;
      if (steps_.back().stage != "starting") steps_.push_back(Step::named("starting"));
    }
    s_.router.refresh();
  }
}

void ResolvingRoute::prepareFailed(const std::string& code) {
  if (!external_ || state_ == State::Done) return;
  fail(own(rpc::kMediaPlay, code, code == "sign_in_required" ? "sign_in" : "", startedMs_));
}

failure::Call ResolvingRoute::own(const char* method, const std::string& code, const std::string& failure,
                                  std::int64_t sinceMs) const {
  failure::Call c;
  c.method = method;
  c.code = failure::token(code);
  c.failure = failure::token(failure);
  c.waitedMs = sinceMs >= 0 ? s_.ui.nowMs() - sinceMs : -1;
  return c;
}

void ResolvingRoute::resolve() {
  cancel(callId_);
  state_ = State::Resolving;
  loadingSinceMs_ = -1;
  loadingText_.clear();
  loadingPercent_ = -2;
  if (auto* r = view::byId(page(), "r-root")) r->SetClass("leaving", false);
  steps_.clear();
  session_.clear();
  startedMs_ = s_.ui.nowMs();
  shownSeconds_ = -1;
  // what plays now: the new session is any other the player reports
  before_ = s_.router.player() != nullptr ? s_.router.player()->state().sessionId : std::string();
  Json args{{"ref", title_.ref}};
  if (!title_.episode.empty()) args["episode"] = title_.episode;
  if (choices_.is_object() && !choices_.empty()) args["choices"] = choices_;
  if (from_ == "start" || from_ == "resume") args["from"] = from_;
  // the server resolves and plays it (media.play): cadenced owns the session and sends the screen its MediaPlay,
  // which the player starts; the answer names the session
  callId_ = call(rpc::kMediaPlay, args,
                 {[this](const std::string& status, const Json& json) {
                    callId_.clear();
                    if (status != rpc::kStatusOK) {
                      fail(failure::of(rpc::kMediaPlay, status, json, s_.ui.nowMs() - startedMs_));
                      return;
                    }
                    started(json.value("sessionId", ""));
                  },
                  [this](const Json& json, bool last) {
                    event(json);
                    if (last) callId_.clear();
                  }}, true);
  redraw();
  changed();
}

void ResolvingRoute::started(const std::string& sessionId) {
  if (state_ != State::Resolving) return;
  retrying_ = false;
  session_ = sessionId;
  for (auto& s : steps_) s.done = true;
  steps_.push_back(Step::named("play"));
  state_ = State::Starting;
  playSentMs_ = s_.ui.nowMs();
  redraw();
  // its MediaPlay can come before the answer: the player may report it already
  if (auto* p = s_.router.player()) playerState(p->state());
}

void ResolvingRoute::event(const Json& ev) {
  if (!ev.is_object() || state_ != State::Resolving) return;
  if (ev.contains("stage") && ev["stage"].is_string()) {
    // a retry's first stage: its steps take the failure's place
    retrying_ = false;
    const auto stage = ev["stage"].get<std::string>();
    // a list's step takes the place of the opening one: nothing was read before it
    if (collectionStage(stage) && steps_.size() == 1 && !steps_.back().done) steps_.clear();
    if (steps_.empty() || steps_.back().stage != stage) {
      for (auto& s : steps_) s.done = true;
      steps_.push_back(Step::named(stage));
    }
    auto& current = steps_.back();
    if (stage == "wait_available" && ev.contains("remainingMs") && ev["remainingMs"].is_number()) {
      current.remainingMs = std::max<std::int64_t>(0, ev.value("remainingMs", 0));
      current.remainingAtMs = s_.ui.nowMs();
    }
    if (stage == "pick") {
      current.height = ev.value("height", 0);
      current.vcodec = ev.value("vcodec", "");
      current.acodec = ev.value("acodec", "");
      current.audioTracks = ev.value("audioTracks", 0);
    }
    redraw();
    return;
  }
  if (ev.contains("refused")) fail(failure::of(rpc::kMediaPlay, "", ev, s_.ui.nowMs() - startedMs_));
}

void ResolvingRoute::fail(failure::Call why) {
  state_ = State::Failed;
  retrying_ = false;
  failure_ = std::move(why);
  redraw();
  // focus where it was when it is still in the card (a retry that failed again keeps «Повторить»), else the first
  // way out
  auto* f = s_.ui.focus().current();
  auto* body = view::byId(page(), "r-body");
  const bool inCard = f != nullptr && body != nullptr && body->Contains(f);
  if (!inCard) {
    if (auto* other = view::byId(page(), external_ ? "r-back" : "r-other")) s_.ui.focus().focus(other);
  }
  changed();
}

void ResolvingRoute::playerState(const player::PlayerState& st) {
  if (state_ != State::Starting && state_ != State::Resolving && state_ != State::Loading) return;
  // before the answer: any session but the one that played when the call went; after it, the one it named
  const bool ours = session_.empty() ? !st.sessionId.empty() && st.sessionId != before_ : st.sessionId == session_;
  if (!ours) return;
  // the server's own play is content: a clip home or ambient plays meanwhile is not it
  if (external_ && st.purpose != cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT) return;
  if (st.ended && !st.errorCode.empty()) {
    // the player's own error code, as it said it
    fail(own("player", "unplayable", st.errorCode, playSentMs_));
    return;
  }
  if (state_ == State::Starting || state_ == State::Resolving) {
    if (st.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING || st.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED ||
        st.state == cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING || st.gated) {
      // the player has it: its own step (the cache filling) until its first frame
      if (session_.empty()) session_ = st.sessionId;
      for (auto& s : steps_) s.done = true;
      steps_.push_back(Step::named("buffering"));
      state_ = State::Loading;
      loadingSinceMs_ = s_.ui.nowMs();
      redraw();
      loadingStep();
    }
  }
}

void ResolvingRoute::loadingStep() {
  auto* p = s_.router.player();
  if (p == nullptr) return;
  const auto percent = p->bufferingPercent();
  const auto speed = p->cacheSpeed();
  std::string text = percent ? "[[resolving.buffering_percent|percent=" + std::to_string(*percent) + "]]" : "[[resolving.buffering]]";
  if (speed && *speed > 0) text += " \xc2\xb7 " + speedText(s_.ui.translator().language(), *speed);
  if (text != loadingText_) {
    loadingText_ = text;
    if (auto* e = view::byId(page(), "r-live")) view::fill(e, text);
  }
  // the bar fills with the cache once the player says how far it is; until then it keeps its sweep
  const int pc = percent ? *percent : -1;
  if (pc != loadingPercent_) {
    loadingPercent_ = pc;
    if (auto* bar = view::byId(page(), "r-bar")) {
      auto* line = bar->QuerySelector(".progress");
      auto* fill = bar->QuerySelector(".fill");
      if (pc >= 0 && line != nullptr && fill != nullptr && !line->IsClassSet("indeterminate")) {
        // in place, so the fill eases from where it was
        fill->SetProperty("transform", "scaleX(" + std::to_string(pc / 100.0) + ")");
      } else {
        view::fill(bar, progress(pc >= 0 ? std::optional<double>(pc / 100.0) : std::nullopt));
      }
    }
  }
}

void ResolvingRoute::handOver() {
  state_ = State::Handing;
  for (auto& s : steps_) s.done = true;
  if (auto* bar = view::byId(page(), "r-bar")) {
    auto* line = bar->QuerySelector(".progress");
    auto* fill = bar->QuerySelector(".fill");
    if (line != nullptr && fill != nullptr && !line->IsClassSet("indeterminate")) {
      fill->SetProperty("transform", "scaleX(1)");
    } else {
      view::fill(bar, progress(1.0));
    }
  }
  // the backdrop grows and fades, the card lifts away: the film, drawing under the page since it started, shows
  // through, and the player's page comes once nothing of this one is left to see
  if (auto* r = view::byId(page(), "r-root")) r->SetClass("leaving", true);
  s_.ui.markDirty();
  s_.ui.after(ui::tokens::kMotionHandoverMs, [this] {
    if (state_ == State::Handing) finish();
  });
}

void ResolvingRoute::finish() {
  // the film starts: the player's own screen takes over, else this step gives the screen back
  state_ = State::Done;
  if (s_.router.has(cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER)) {
    RouteArgs args;
    args.ref = title_.ref;
    args.episode = title_.episode;
    s_.router.replace(cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER, args);
  } else {
    s_.router.pop();
  }
}

std::string ResolvingRoute::stepsRml() const {
  std::string s = "<div class=\"rsteps\" id=\"r-steps\">";
  const bool torrent = choices_.is_object() && choices_.contains("torrent");
  for (const auto& st : steps_) {
    s += "<div class=\"rstep" + std::string(st.done ? " done" : "") + "\">";
    s += st.done ? icon("check") : "<div class=\"spinner\"/>";
    if (st.stage == "buffering") {
      s += "<span id=\"r-live\">" + (loadingText_.empty() ? std::string("[[resolving.buffering]]") : loadingText_) + "</span></div>";
    } else {
      auto words = stageWords(st.stage, torrent);
      if (st.stage == "wait_available" && st.remainingMs >= 0) {
        const auto left = std::max<std::int64_t>(0, st.remainingMs - (s_.ui.nowMs() - st.remainingAtMs));
        words = "[[resolving.wait_available|seconds=" + std::to_string((left + 999) / 1000) + "]]";
        s += "<span id=\"r-wait\">" + words + "</span></div>";
        continue;
      } else if (st.stage == "pick" && st.height > 0) {
        words = "[[resolving.pick_at|height=" + std::to_string(st.height) + "|codec=" + st.vcodec + "]]";
        if (st.audioTracks > 0) words += " · [[resolving.with_audio]]";
      }
      s += "<span>" + words + "</span></div>";
    }
  }
  if (steps_.empty()) {
    s += "<div class=\"rstep\"><div class=\"spinner\"/><span>" + stageWords("", torrent) + "</span></div>";
  }
  const bool full = state_ == State::Handing || state_ == State::Done;
  s += "</div><div id=\"r-bar\">" +
       progress(full ? std::optional<double>(1.0) : loadingPercent_ >= 0 ? std::optional<double>(loadingPercent_ / 100.0) : std::nullopt) +
       "</div>";
  s += "<div class=\"relapsed\" id=\"r-elapsed\"></div>";
  return s;
}

std::string ResolvingRoute::failureRml() const {
  // what failed, as it came: the source, the codes, the call; then what can be done
  ErrorCard e;
  e.title = "[[resolving.play_failed]]";
  failure::describe(e, failure_, title_.provider);
  if (external_) {
    // the server's own play: nothing here to try again or another source to pick
    e.actions = {Button{"[[common.back]]", "back", "back", true, false, false, "r-back", "default-focus"}};
  } else {
    e.actions = {Button{"[[picker.other_source]]", "audio", "other", true, false, false, "r-other", "default-focus"},
                 Button{"[[common.retry]]", "refresh", "retry", false, retrying_, false, "r-retry", {}},
                 Button{"[[common.back]]", "back", "back", false, false, false, "r-back", {}}};
  }
  return "<div class=\"rfail\">" + errorCard(e) + "</div>";
}

std::string ResolvingRoute::body() {
  std::string s = "<div class=\"resolving\" id=\"r-root\"><div class=\"rbase\"/>";
  if (const auto art = title_.art; ui::images::validId(art)) {
    // home's blurred backdrop, the same cache entry (core/backdrop.h)
    s += backdrop::rml(art, "rback");
  }
  s += "<div class=\"rshade\"/><div class=\"safe\"><div class=\"rbox\"><div class=\"rposter\"><div class=\"clip\">";
  if (ui::images::validId(title_.art)) s += "<div class=\"img netimg\" data-img=\"" + esc(title_.art) + "\"/>";
  s += "</div><div class=\"hair\"/></div><div class=\"rinfo\"><div class=\"rtitle\">" + esc(title_.title) + "</div><div class=\"rsub\">";
  std::string sub;
  if (!title_.episode.empty()) sub = "[[episodes.episode|n=" + catalog::argOf(title_.episode) + "]]";
  if (!label_.empty()) sub += (sub.empty() ? "" : " · ") + label_;
  s += sub + "</div><div id=\"r-body\">" + (state_ == State::Failed || retrying_ ? failureRml() : stepsRml()) +
       "</div></div></div></div>";
  return s + "</div>";
}

void ResolvingRoute::redraw() {
  auto* host = view::byId(page(), "r-body");
  if (host == nullptr) return;
  // a retry keeps the failure until its first stage: the focused «Повторить» stays where it is
  std::string focused;
  if (auto* f = s_.ui.focus().current(); f != nullptr && host->Contains(f)) focused = f->GetId();
  view::fill(host, state_ == State::Failed || retrying_ ? failureRml() : stepsRml());
  if (!focused.empty()) {
    if (auto* e = view::byId(page(), focused)) s_.ui.focus().focus(e);
  }
  shownSeconds_ = -1;
  // the newest step eases in (rstep.fresh's start state), laid out first
  Rml::ElementList steps;
  host->QuerySelectorAll(steps, ".rstep");
  if (!steps.empty() && !steps.back()->IsClassSet("done")) {
    steps.back()->SetClass("fresh", true);
    s_.ui.after(0, [e = steps.back()->GetObserverPtr()] {
      if (auto* el = e.get()) el->SetClass("fresh", false);
    });
  }
}

void ResolvingRoute::loaded(Rml::ElementDocument& doc, bool) {
  if (state_ == State::Failed) {
    if (auto* other = doc.GetElementById(external_ ? "r-back" : "r-other")) s_.ui.focus().focus(other);
  }
}

void ResolvingRoute::update(std::int64_t nowMs) {
  if (external_ && state_ == State::Resolving && nowMs - startedMs_ >= kExternalTimeoutMs) {
    fail(own(rpc::kMediaPlay, "unplayable", "timeout", startedMs_));
    return;
  }
  if (state_ == State::Starting && playSentMs_ >= 0 && nowMs - playSentMs_ >= kStartTimeoutMs) {
    // the play went out but no picture came: the player said nothing in time
    fail(own("player", "unplayable", "timeout", playSentMs_));
    return;
  }
  if (state_ == State::Loading) {
    auto* p = s_.router.player();
    if (p == nullptr || p->pictureReady() || nowMs - loadingSinceMs_ >= kPictureWaitMs) {
      handOver();
      return;
    }
    loadingStep();
  }
  if (state_ == State::Resolving && !steps_.empty() && steps_.back().stage == "wait_available" && steps_.back().remainingMs >= 0) {
    const auto& wait = steps_.back();
    const auto left = std::max<std::int64_t>(0, wait.remainingMs - (nowMs - wait.remainingAtMs));
    const auto seconds = (left + 999) / 1000;
    if (seconds != shownWaitSeconds_) {
      shownWaitSeconds_ = seconds;
      if (auto* e = view::byId(page(), "r-wait")) {
        view::fill(e, "[[resolving.wait_available|seconds=" + std::to_string(seconds) + "]]");
      }
    }
  }
  if (state_ != State::Resolving && state_ != State::Starting && state_ != State::Loading) return;
  const auto elapsed = nowMs - startedMs_;
  if (elapsed < kElapsedAfterMs) return;
  const auto seconds = elapsed / 1000;
  if (seconds == shownSeconds_) return;
  shownSeconds_ = seconds;
  if (auto* e = view::byId(page(), "r-elapsed")) view::fill(e, i18n::elapsed(seconds, 0));
}

bool ResolvingRoute::activate(const ui::Activation& a) {
  const auto& act = a.action;
  if (act == "retry" && !external_) {
    if (retrying_) return true;
    retrying_ = true;
    if (auto* b = view::byId(page(), "r-retry")) b->SetClass("loading", true);
    resolve();
    return true;
  }
  if (act == "back") {
    s_.router.pop();
    return true;
  }
  if (act == "other" && !external_) {
    // another voiceover or source: the picker this came from, else a picker over the title's options
    if (s_.router.find(cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER) != nullptr) {
      s_.router.popTo(cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER);
    } else {
      RouteArgs args;
      args.ref = title_.ref;
      args.episode = title_.episode;
      args.data = play::titleJson(title_);
      s_.router.replace(cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER, args);
    }
    return true;
  }
  return false;
}

bool ResolvingRoute::back() {
  // BACK cancels the wait (a torrent's peers among it) and returns to the page that started it; a session the player
  // already has (its cache filling) stops with it, so nothing plays behind the page BACK returns to
  cancel(callId_);
  if (state_ == State::Loading || state_ == State::Handing) {
    if (auto* p = s_.router.player(); p != nullptr && !session_.empty() && p->state().sessionId == session_) {
      player::Command stop;
      stop.action = player::CommandAction::Stop;
      p->command(stop);
    }
  }
  state_ = State::Done;
  return false;
}

void ResolvingRoute::context(ScreenContextData& c) const {
  c.pageRef = title_.ref;
  c.episode = title_.episode;
  if (choices_.is_object()) {
    c.voiceover = choices_.value("voiceover", "");
    c.quality = choices_.value("quality", "");
    if (choices_.contains("source")) c.source = choices_.value("source", "");
  }
}

}  // namespace cadence::screen::routes
