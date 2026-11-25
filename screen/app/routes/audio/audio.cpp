#include "app/routes/audio/audio.h"

#include "app/routes/player/cache-info.h"
#include "app/routes/playback/server-choices.h"

#include "app/routes/ambient/drift.h"
#include "app/routes/core/backdrop.h"
#include "ui/core/rml.h"
#include "ui/core/ui.h"
#include "ui/images/image-cache.h"
#include "host/scene_source.h"

#include <RmlUi/Core/ElementDocument.h>
#include <nlohmann/json.hpp>

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <ctime>
#include <functional>
#include <string>
#include <vector>

namespace cadence::screen::routes {

namespace {

using ui::rml::esc;

constexpr std::int64_t kControlsMs = 8000;
constexpr std::int64_t kAmbientAfterMs = 120000;

std::string hex(std::uint32_t rgb) {
  char buf[16];
  std::snprintf(buf, sizeof buf, "#%06x", rgb & 0xffffff);
  return buf;
}

// the first letter of a title, whole (a utf-8 sequence), upper case for ascii
std::string firstLetter(const std::string& s) {
  if (s.empty()) return "♪";
  const auto c = static_cast<unsigned char>(s[0]);
  const std::size_t n = c < 0x80 ? 1 : c < 0xe0 ? 2 : c < 0xf0 ? 3 : 4;
  std::string out = s.substr(0, n);
  if (n == 1) out[0] = static_cast<char>(std::toupper(c));
  return out;
}

std::string iconFor(ActionKind k) {
  switch (k) {
    case ActionKind::Like:
      return "like";
    case ActionKind::Dislike:
      return "dislike";
    case ActionKind::Save:
      return "heart";
    case ActionKind::Subscribe:
      return "subscribe";
    case ActionKind::Queue:
      return "queue_add";
    default:
      return "more";
  }
}

// a sender's name as a message argument: the translator's own marks never pass
std::string argText(const std::string& s) {
  std::string out;
  for (const char c : s)
    if (c != '[' && c != ']' && c != '|' && c != '=') out += c;
  return esc(out);
}

const char* vizWord(VizFamily f) {
  switch (f) {
    case VizFamily::Off: return "[[audio.visualizer.off]]";
    case VizFamily::Classic: return "[[audio.visualizer.classic]]";
    case VizFamily::Scenes: return "[[audio.visualizer.scenes]]";
    case VizFamily::Drive: return "[[audio.visualizer.drive]]";
    case VizFamily::Milkdrop: return "[[audio.visualizer.milkdrop]]";
  }
  return "[[audio.visualizer.off]]";
}

std::vector<std::string> familyPrefixes(VizFamily f) {
  switch (f) {
    case VizFamily::Drive: return {"drive:"};
    case VizFamily::Scenes: return {"scene:"};
    case VizFamily::Milkdrop: return {"milkdrop:"};
    default: return {};
  }
}

// the drive family's shapes, as the setting names them, and DriveSource's codes
constexpr std::pair<const char*, int> kShapes[] = {{"triangle", 3}, {"square", 4}, {"pentagon", 5}, {"hexagon", 6},
                                                   {"circle", 0},   {"star", -1},  {"diamond", -2}};
constexpr const char* kScenes[] = {"cover", "warp", "tunnel"};
// the picture's lead the settings start at (context.h's vizOffsetMs), and the steps the sheet offers around it
constexpr int kDefaultLeadMs = 60;
constexpr int kSyncSteps[] = {-100, -50, 0, 50, 100, 150};

int shapeCode(const std::string& name) {
  for (const auto& [n, c] : kShapes)
    if (name == n) return c;
  return cadence::render::viz::SceneLook::kAutoShape;
}

std::string withId(std::string html, const std::string& id) {
  const auto at = html.find(' ');
  if (at != std::string::npos) html.insert(at, " id=\"" + id + "\"");
  return html;
}

}  // namespace

std::pair<std::uint32_t, std::uint32_t> placeholderColors(const std::string& album) {
  static constexpr std::pair<std::uint32_t, std::uint32_t> kPairs[8] = {
      {0x4a3a5c, 0x1f2a44}, {0x5c3a3a, 0x2d1f33}, {0x3a5c50, 0x1f3344}, {0x5c4f3a, 0x33261f},
      {0x3a465c, 0x2a1f44}, {0x4f5c3a, 0x1f3328}, {0x5c3a52, 0x331f2a}, {0x3a5a5c, 0x1f2633},
  };
  // fnv-1a: stable per album on every device
  std::uint32_t h = 2166136261u;
  for (const char c : album) h = (h ^ static_cast<std::uint8_t>(c)) * 16777619u;
  return kPairs[h % 8];
}

std::string darkened(std::uint32_t rgb, double maxLuma) {
  double r = ((rgb >> 16) & 0xff) / 255.0, g = ((rgb >> 8) & 0xff) / 255.0, b = (rgb & 0xff) / 255.0;
  const double luma = 0.2126 * r + 0.7152 * g + 0.0722 * b;
  if (luma > maxLuma && luma > 0) {
    const double k = maxLuma / luma;
    r *= k;
    g *= k;
    b *= k;
  }
  const auto ch = [](double v) { return static_cast<std::uint32_t>(std::lround(std::clamp(v, 0.0, 1.0) * 255)); };
  return hex((ch(r) << 16) | (ch(g) << 8) | ch(b));
}

AudioPage::AudioPage(PlaybackContext& c, SideSheet& sheet, std::unique_ptr<VizView> viz)
    : Page(c), sheet_(sheet), viz_(std::move(viz)), queue_(c) {}

AudioPage::~AudioPage() { releaseGraphics(); }

void AudioPage::releaseGraphics() {
  if (viz_) viz_->release();
  barRenderer_.release();
}

void AudioPage::abandonGraphics() {
  if (viz_) viz_->abandon();
  barRenderer_.abandon();
}

std::string AudioPage::controls() const {
  const auto& s = *c_.session;
  std::string b;
  // a cast's transport is what its sender allows (airplay: pause and resume alone; spotify connect: the skips too)
  if (s.has("previous")) b += withId(ui::rml::iconButton("previous", "[[player.osd.previous]]", "audio:previous"), "a-prev");
  const bool paused = c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
  b += withId(ui::rml::iconButton(paused ? "play" : "pause", paused ? "[[player.osd.play]]" : "[[player.osd.pause]]", "audio:play", true),
              "a-play");
  if (s.has("next") || (!s.device && !s.follow && s.upNext)) b += withId(ui::rml::iconButton("next", "[[player.osd.next]]", "audio:next"), "a-next");
  for (std::size_t i = 0; i < s.itemActions.size(); ++i) {
    const auto& a = s.itemActions[i];
    if (a.kind != ActionKind::Like && a.kind != ActionKind::Save) continue;
    b += withId(ui::rml::chip(esc(a.label), iconFor(a.kind), a.toggled, "audio:act:" + std::to_string(i)), "a-act-" + std::to_string(i));
  }
  for (const auto& p : s.panels) {
    if (p.kind == "lyrics") b += withId(ui::rml::chip("[[audio.lyrics]]", {}, std::nullopt, "audio:panel:" + p.id), "a-lyrics");
  }
  // the queue is the server's (media.queue): a cast has none here
  if (!s.device) b += withId(ui::rml::chip("[[audio.queue]]", "playlist", std::nullopt, "audio:queue"), "a-queue");
  for (const auto& p : s.panels) {
    if (p.kind == "channel") b += withId(ui::rml::chip("[[audio.artist]]", "user", std::nullopt, "audio:panel:" + p.id), "a-artist");
  }
  // a server video's own views and audio languages (rpc media.video): back to the picture, another dubbing
  if (const auto v = choices::currentView(s); !v.empty()) b += withId(ui::rml::chip(v, "film", std::nullopt, "audio:view"), "a-view");
  if (const auto a = choices::currentAudio(s); !a.empty()) b += withId(ui::rml::chip(a, "audio", std::nullopt, "audio:saudio"), "a-saudio");
  // the visualizer is hidden for a handoff: no audio here to analyse (§19)
  if (s.playable != cadence_v2_PlayableKind_PLAYABLE_KIND_HANDOFF) {
    const char* word = vizWord(c_.settings.visualizer);
    b += withId(ui::rml::chip(word, "sparkle", std::nullopt, "audio:viz"), "a-viz");
  }
  return b;
}

std::string AudioPage::page() const {
  const auto& s = *c_.session;
  // the background: the art's two colours (the server's), darkened, into ink. one decorator per track
  std::uint32_t a = s.colorA, b = s.colorB;
  const bool placeholder = s.art.empty();
  if (placeholder) std::tie(a, b) = placeholderColors(s.album.empty() ? s.title : s.album);
  barColorA_ = a;
  barColorB_ = b;
  background_ = "linear-gradient(180deg, " + darkened(a) + " 0%, " + darkened(b) + " 55%, #0c0e13 100%)";
  const auto strip = controls();
  std::string classes = "audio lean";
  if (c_.settings.showClock) classes += " clocked";
  if (queue_.visible()) classes += " queued";
  std::string p = "<div class=\"" + classes + "\" id=\"audio\" style=\"decorator: " + background_ + ";\">";
  // the cover itself, blurred, tints the sleeve (the backdrop home and resolving share: one decode, one blur)
  if (!placeholder && s.art.starts_with("img:")) p += "<div class=\"ablur\">" + backdrop::rml(s.art.substr(4), "ablurimg") + "</div>";
  // the drive scenes dim under the controls (never over them)
  p += "<div class=\"adrivedim\"/>";
  p += "<div class=\"awordmark\">Cadence</div><div class=\"group\" id=\"a-group\">";
  const std::string title = s.title.empty() && s.device ? s.providerName : s.title;
  // the art, or its placeholder: the title's first letter on the album's duotone
  if (placeholder) {
    auto [pa, pb] = placeholderColors(s.album.empty() ? s.title : s.album);
    p += "<div class=\"aart\" id=\"a-art\" style=\"decorator: linear-gradient(135deg, " + hex(pa) + ", " + hex(pb) +
         ");\"><div class=\"letter\">" + esc(firstLetter(title)) + "</div></div>";
  } else {
    // a cover comes rounded from its decode: the box needs no clip mask for it
    p += std::string(s.art.starts_with("img:") ? "<div class=\"aart pre\" id=\"a-art\">" : "<div class=\"aart\" id=\"a-art\">") +
         ui::rml::artImage(s.art, "aimg") + "</div>";
  }
  p += "<div class=\"acol\"><div class=\"asource\">";
  if (s.playable == cadence_v2_PlayableKind_PLAYABLE_KIND_HANDOFF) {
    p += ui::rml::badge(ui::rml::BadgeKind::Provider, esc(s.providerName), "SP") +
         "<div class=\"anote\">[[audio.handoff|name=" + esc(s.handoffTarget) + "]]</div>";
  } else if (!s.providerName.empty()) {
    p += ui::rml::badge(ui::rml::BadgeKind::Provider, esc(s.providerName), esc(monogram(s.providerId)));
    // a cast: where it plays, and from which device when the sender named itself
    if (s.device)
      p += "<div class=\"acontext\">" +
           (s.sender.empty() ? std::string("[[audio.cast.tv]]") : "[[audio.cast|name=" + argText(s.sender) + "]]") + "</div>";
    if (s.playable == cadence_v2_PlayableKind_PLAYABLE_KIND_MATCH)
      p += "<div class=\"anote\">[[audio.match|provider=" + esc(s.matchedFrom.empty() ? "YouTube Music" : s.matchedFrom) + "]]</div>";
  } else if (s.live) {
    p += ui::rml::badge(ui::rml::BadgeKind::Provider, "[[audio.radio]]", "FM");
  }
  p += "</div><div class=\"atitle\">" + esc(title) + (s.explicitContent ? " " + ui::rml::badge(ui::rml::BadgeKind::Explicit, {}) : "") +
       "</div><div class=\"aartist\">" + esc(s.artist) + "</div><div class=\"aalbum\">" + esc(s.album) + "</div>";
  if (s.upNext)
    p += "<div class=\"badge anext\" id=\"a-upnext\">" + ui::rml::icon("next") +
         "<span>[[audio.up_next|title=" + esc(s.upNext->title) + "]]</span></div>";
  p += "</div><div class=\"aclock\" id=\"a-clock\"/></div>";
  // the drive scenes' title, inside the shape they draw in the middle (lean-back only)
  p += "<div class=\"adrive\" id=\"a-drive\"><div class=\"dtitle\">" + esc(title) + "</div><div class=\"dartist\">" +
       esc(s.artist) + "</div>" + (s.album.empty() || s.album == s.title ? std::string() : "<div class=\"dalbum\">" + esc(s.album) + "</div>") +
       "</div>";
  p += "<div class=\"aprogress\" id=\"a-progress\">";
  if (!s.live) p += "<div id=\"a-line\">" + ui::rml::progress(0.0) + "</div>";
  p += "<div class=\"atimes\"><span id=\"a-pos\"/><span id=\"a-dur\">" +
       std::string(s.live ? ui::rml::badge(ui::rml::BadgeKind::Live, {}) : "") + "</span></div>" +
       "<div class=\"abuffer hidden\" id=\"a-buffer\"/></div>";
  p += "<div class=\"acontrols\" id=\"a-controls\">" + strip + "</div><div class=\"adim\"/><div id=\"a-queue-host\">" +
       queue_.body() + "</div></div><div id=\"side-host\"/>";
  return p;
}

void AudioPage::enter() {
  // a new page draws its bars from the floor up, whatever the last page's were
  bars_ = BarSmoother{};
  state_ = State::Lean;
  lastKeyMs_ = c_.nowMs;
  leanSinceMs_ = c_.nowMs;
  okDown_ = false;
  queue_.enter();
  if (viz_ && c_.session) {
    viz_->onTrack(std::hash<std::string>{}(c_.session->title));
    viz_->setTint(c_.session->colorA, 0.35f);
  }
  applyFamily();
  requestArtwork();
  updateBarsActivity();
}

std::string AudioPage::body() { return c_.session ? page() : std::string("<div/>"); }

void AudioPage::loaded(bool restoring) {
  (void)restoring;
  // a reload draws its bars from the floor
  bars_ = BarSmoother{};
  queue_.load();
  setState(state_);
  refresh();
}

void AudioPage::leave() {
  sheet_.close();
  barsActive_.store(false, std::memory_order_relaxed);
  if (viz_) viz_->setRunning(false);
}

void AudioPage::sessionChanged() {
  if (!c_.session) return;
  // a cover that came after the session (a group member's, fetched from the server): the drive scenes take it too
  if (c_.session->art != artFor_) requestArtwork();
  if (auto* ctl = c_.el("a-controls")) {
    const auto focused = c_.ui().focus().current() != nullptr ? c_.ui().focus().current()->GetId() : std::string();
    ctl->SetInnerRML(controls());
    if (auto* f = c_.el(focused)) c_.ui().focus().focus(f);
  }
}

bool AudioPage::classicBars() const {
  return c_.settings.visualizer == VizFamily::Classic && state_ != State::Ambient && c_.session &&
         c_.session->playable != cadence_v2_PlayableKind_PLAYABLE_KIND_HANDOFF;
}

bool AudioPage::hostShown() const {
  return hostFamily(c_.settings.visualizer) && c_.session && c_.session->playable != cadence_v2_PlayableKind_PLAYABLE_KIND_HANDOFF &&
         state_ != State::Ambient;
}

void AudioPage::applyFamily() {
  if (!viz_) return;
  viz_->setFamily(familyPrefixes(c_.settings.visualizer));
  const auto& scene = c_.settings.vizScene;
  viz_->setPin(c_.settings.visualizer == VizFamily::Drive && scene != "auto" && !scene.empty() ? "drive:" + scene : std::string());
  viz_->setShape(shapeCode(c_.settings.vizShape));
  viz_->setQuality(c_.settings.vizQuality);
  viz_->setLead(c_.settings.vizOffsetMs);
}

void AudioPage::settingsChanged() {
  applyFamily();
  if (c_.session && c_.el("audio") != nullptr) setState(state_);
}

void AudioPage::saveVisualizer() {
  // kept as this device's own display.screen setting; a server that does not know the keys yet refuses, and the
  // pick still holds until the screen restarts
  const char* family = c_.settings.visualizer == VizFamily::Off        ? "off"
                       : c_.settings.visualizer == VizFamily::Classic  ? "classic"
                       : c_.settings.visualizer == VizFamily::Scenes   ? "scenes"
                       : c_.settings.visualizer == VizFamily::Milkdrop ? "milkdrop"
                                                                        : "drive";
  nlohmann::json args = {{"screen", {{"visualizer", family}, {"vizScene", c_.settings.vizScene}, {"vizShape", c_.settings.vizShape}, {"vizQuality", c_.settings.vizQuality}, {"vizOffsetMs", c_.settings.vizOffsetMs}}}};
  (void)c_.rpc.call("settings.set", args.dump(), [](const RpcAnswer&) {}, c_.nowMs);
}

void AudioPage::requestArtwork() {
  auto* cache = c_.ui().images();
  if (cache != nullptr && !artKey_.empty()) cache->unpin(artKey_);
  artKey_.clear();
  artPending_ = false;
  if (!viz_ || !c_.session) return;
  const auto& art = c_.session->art;
  artFor_ = art;
  if (cache == nullptr) return;
  if (!art.starts_with("img:")) {
    // no cover yet (a group member's comes from the server once the source's is known): the nebula, never another
    // picture standing in for it
    viz_->setArtwork({}, 0, 0);
    return;
  }
  requestArt(art.substr(4));
}

void AudioPage::requestArt(const std::string& id) {
  auto* cache = c_.ui().images();
  if (cache == nullptr || id.empty()) return;
  // its own copy (another key than the page's cover): its pixels go to the viz thread, never the ui's gpu
  cache->request(id, 1024, 1024, &artKey_);
  cache->pin(artKey_);
  artPending_ = true;
}


void AudioPage::takeArtwork() {
  auto* cache = c_.ui().images();
  if (!artPending_ || cache == nullptr) return;
  const auto st = cache->state(artKey_);
  if (st == ui::images::State::Failed) {
    artPending_ = false;
    return;
  }
  ui::Pixels px;
  if (st != ui::images::State::Ready || !cache->take(artKey_, px)) return;
  artPending_ = false;
  if (viz_) viz_->setArtwork(std::move(px.rgba), px.width, px.height);
}

void AudioPage::updateBarsActivity() {
  barsActive_.store(classicBars() && c_.playing(), std::memory_order_relaxed);
}

void AudioPage::setState(State s) {
  state_ = s;
  auto* root = c_.el("audio");
  if (root == nullptr) return;
  root->SetClass("lean", s != State::Controls);
  root->SetClass("ambient", s == State::Ambient);
  // lean-back: the queue shows but takes no focus, else the focus manager parks the remote on its first row
  if (auto* q = c_.el("a-queue-host")) q->SetClass("inert", s != State::Controls);
  const bool scenes = hostShown();
  root->SetClass("viz", scenes);
  // the drive scenes put the title in their shape: the sleeve's own art and text step aside while lean
  root->SetClass("drive", scenes && c_.settings.visualizer == VizFamily::Drive);
  // the scenes and the classic background are drawn under the layer; an opaque decorator here would hide them
  root->SetProperty("decorator", scenes || classicBars() ? std::string("none") : background_);
  updateBarsActivity();
  if (viz_) viz_->setRunning(scenes);
  if (s == State::Controls) {
    if (auto* play = c_.el("a-play")) c_.ui().focus().focus(play);
  } else if (auto* f = c_.ui().focus().current()) {
    f->Blur();
  }
  if (s == State::Lean) leanSinceMs_ = c_.nowMs;
  if (s == State::Ambient) ambientSinceMs_ = c_.nowMs;
  refresh();
  if (s != State::Ambient) {
    if (auto* g = c_.el("a-group")) g->SetProperty("transform", "translate(0dp, 0dp)");
  }
  c_.ui().markDirty();
}

void AudioPage::trackChanged() {
  bars_ = BarSmoother{};
  if (viz_ && c_.session) {
    viz_->onTrack(std::hash<std::string>{}(c_.session->title));
    viz_->setTint(c_.session->colorA, 0.35f);
  }
  requestArtwork();
}

void AudioPage::setVisualizer(VizFamily f) {
  c_.settings.visualizer = f;
  applyFamily();
  saveVisualizer();
  sessionChanged();
  setState(state_);
}

void AudioPage::driftTo(double tS) {
  const auto d = drift(tS, 0.5, 160, 90);
  if (auto* g = c_.el("a-group")) g->SetProperty("transform", "translate(" + dp(d.x - 80) + ", " + dp(d.y - 45) + ")");
  c_.ui().markDirty();
}

bool AudioPage::key(const platform::KeyEvent& e) {
  if (!c_.session) return false;
  if (sheet_.isOpen()) return sheet_.key(e);
  if (c_.ui().levelOpen()) {
    lastKeyMs_ = c_.nowMs;
    return c_.ui().key(e);
  }
  const auto k = e.key;
  // volume and the assistant are cadenced's, whatever the state
  if (k == cadence_screen_v1_Key_KEY_VOLUME_UP || k == cadence_screen_v1_Key_KEY_VOLUME_DOWN ||
      k == cadence_screen_v1_Key_KEY_VOLUME_MUTE || k == cadence_screen_v1_Key_KEY_ASSISTANT || k == cadence_screen_v1_Key_KEY_POWER)
    return false;
  lastKeyMs_ = c_.nowMs;
  if (state_ != State::Controls) {
    // ← → change the scene while the controls are hidden
    if ((k == cadence_screen_v1_Key_KEY_LEFT || k == cadence_screen_v1_Key_KEY_RIGHT) && state_ == State::Lean &&
        hostFamily(c_.settings.visualizer) && viz_) {
      if (e.action == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN) k == cadence_screen_v1_Key_KEY_RIGHT ? viz_->next() : viz_->prev();
      return true;
    }
    if (k == cadence_screen_v1_Key_KEY_BACK && state_ == State::Lean) {
      if (isPress(e)) c_.go(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
      return true;
    }
    // the first key only reveals the controls (from ambient, it wakes in 280 ms): consumed, never acted on
    if (e.action == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN || e.action == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS)
      setState(State::Controls);
    okDown_ = false;
    return true;
  }
  if (k == cadence_screen_v1_Key_KEY_BACK) {
    if (isPress(e)) setState(State::Lean);
    return true;
  }
  // a key whose down came before the controls showed does not act on them
  if (k == cadence_screen_v1_Key_KEY_OK) {
    if (e.action == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN) okDown_ = true;
    if (!okDown_ && e.action != cadence_screen_v1_KeyAction_KEY_ACTION_PRESS) return true;
    if (isPress(e)) okDown_ = false;
  }
  const bool used = c_.ui().key(e);
  queue_.keepVisible();
  return used;
}

bool AudioPage::activate(const ui::Activation& a) {
  if (!c_.session) return false;
  if (sheet_.isOpen() && sheet_.activate(a)) return true;
  if (queue_.activate(a)) return true;
  const auto& act = a.action;
  if (act == "audio:play") {
    const bool paused = c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
    c_.command(paused ? player::CommandAction::Resume : player::CommandAction::Pause);
    return true;
  }
  if (act == "audio:previous" && c_.session->device) {
    // a cast's own skip, else this track's start where it seeks
    if (c_.session->has("previous")) {
      c_.deviceCommand(cadence_screen_v1_MediaAction_MEDIA_ACTION_PREVIOUS);
    } else if (c_.session->has("seek")) {
      c_.command(player::CommandAction::Seek, 0);
    }
    return true;
  }
  if (act == "audio:next" && c_.session->device) {
    if (c_.session->has("next")) c_.deviceCommand(cadence_screen_v1_MediaAction_MEDIA_ACTION_NEXT);
    return true;
  }
  if (act == "audio:previous") {
    // the item before within the first seconds (the server's previous), else this one's start
    if (c_.session->has("previous") && c_.transport && c_.position() < 3000) {
      c_.transport("previous", {}, nullptr);
    } else {
      c_.command(player::CommandAction::Seek, 0);
    }
    return true;
  }
  if (act == "audio:next") {
    // the server's next; else up next's item by its ref
    if (c_.session->has("next") && c_.transport) {
      c_.transport("next", {}, nullptr);
    } else if (c_.session->upNext && c_.session->upNext->server && c_.transport) {
      c_.transport("jump", c_.session->upNext->ref, nullptr);
    } else {
      c_.toast("[[common.unavailable_now]]");
    }
    return true;
  }
  if (act == "audio:queue") {
    if (auto* r = c_.el("audio")) r->SetClass("sheeted", true);
    if (openQueue) openQueue(a.element);
    return true;
  }
  if (act.starts_with("audio:panel:")) {
    if (auto* r = c_.el("audio")) r->SetClass("sheeted", true);
    if (openPanel) openPanel(act.substr(12), a.element);
    return true;
  }
  if (act.starts_with("audio:act:")) {
    const auto i = static_cast<std::size_t>(std::atoi(act.c_str() + 10));
    if (i < c_.session->itemActions.size()) c_.itemAction(c_.session->itemActions[i].id, [this] { sessionChanged(); });
    return true;
  }
  if (choices::activate(c_, act)) return true;
  if (act == "audio:view") {
    choices::openViews(c_);
    return true;
  }
  if (act == "audio:saudio") {
    choices::openAudio(c_);
    return true;
  }
  if (act == "audio:viz") {
    // each section a row of chips that wraps, so the sheet fits the screen however many rows it holds
    std::string body = "<div class=\"list vizsheet\"><div class=\"vizrow\">";
    body += ui::rml::chip("[[audio.visualizer.off]]", {}, c_.settings.visualizer == VizFamily::Off, "viz:off");
    body += ui::rml::chip("[[audio.visualizer.classic]]", {}, c_.settings.visualizer == VizFamily::Classic, "viz:classic");
    body += ui::rml::chip("[[audio.visualizer.drive]]", {}, c_.settings.visualizer == VizFamily::Drive, "viz:drive");
    body += ui::rml::chip("[[audio.visualizer.scenes]]", {}, c_.settings.visualizer == VizFamily::Scenes, "viz:scenes");
    body += ui::rml::chip("[[audio.visualizer.milkdrop]]", {}, c_.settings.visualizer == VizFamily::Milkdrop, "viz:milkdrop");
    body += "</div>";
    if (c_.settings.visualizer == VizFamily::Drive) {
      body += "<div class=\"sheet-label\">[[audio.visualizer.scene]]</div><div class=\"vizrow\">";
      body += ui::rml::chip("[[audio.visualizer.auto]]", {}, c_.settings.vizScene == "auto", "vizscene:auto");
      for (const char* sc : kScenes)
        body += ui::rml::chip("[[audio.visualizer.scene." + std::string(sc) + "]]", {}, c_.settings.vizScene == sc, std::string("vizscene:") + sc);
      body += "</div><div class=\"sheet-label\">[[audio.visualizer.shape]]</div><div class=\"vizrow\">";
      body += ui::rml::chip("[[audio.visualizer.auto]]", {}, c_.settings.vizShape == "auto", "vizshape:auto");
      for (const auto& [n, code] : kShapes) {
        (void)code;
        body += ui::rml::chip("[[audio.visualizer.shape." + std::string(n) + "]]", {}, c_.settings.vizShape == n, std::string("vizshape:") + n);
      }
      body += "</div>";
    }
    if (hostFamily(c_.settings.visualizer)) {
      body += "<div class=\"sheet-label\">[[audio.visualizer.quality]]</div><div class=\"vizrow\">";
      for (const char* q : {"low", "medium", "high", "max", "native"})
        body += ui::rml::chip("[[audio.visualizer.quality." + std::string(q) + "]]", {}, c_.settings.vizQuality == q, std::string("vizquality:") + q);
      body += "</div>";
      // the picture's lead over the sound, as a step from the default: the tv's own way to the panel is not known
      // here, so the owner sets it by eye (a kick and its flash together)
      body += "<div class=\"sheet-label\">[[audio.visualizer.sync]]</div><div class=\"vizrow\">";
      for (const int step : kSyncSteps) {
        const std::string label = step == 0 ? "0" : (step > 0 ? "+" : "\u2212") + std::to_string(std::abs(step));
        body += ui::rml::chip(label, {}, c_.settings.vizOffsetMs == kDefaultLeadMs + step, "vizsync:" + std::to_string(step));
      }
      body += "</div>";
    }
    c_.ui().sheet("[[audio.visualizer]]", body + "</div>");
    return true;
  }
  if (act.starts_with("vizsync:")) {
    c_.ui().closeLevel();
    c_.settings.vizOffsetMs = kDefaultLeadMs + std::atoi(act.c_str() + 8);
    applyFamily();
    saveVisualizer();
    return true;
  }
  if (act.starts_with("vizquality:")) {
    c_.ui().closeLevel();
    c_.settings.vizQuality = act.substr(11);
    applyFamily();
    saveVisualizer();
    return true;
  }
  if (act.starts_with("vizscene:") || act.starts_with("vizshape:")) {
    c_.ui().closeLevel();
    (act.starts_with("vizscene:") ? c_.settings.vizScene : c_.settings.vizShape) = act.substr(9);
    applyFamily();
    saveVisualizer();
    return true;
  }
  if (act.starts_with("viz:")) {
    c_.ui().closeLevel();
    setVisualizer(act == "viz:off"        ? VizFamily::Off
                  : act == "viz:classic"  ? VizFamily::Classic
                  : act == "viz:scenes"   ? VizFamily::Scenes
                  : act == "viz:milkdrop" ? VizFamily::Milkdrop
                                          : VizFamily::Drive);
    return true;
  }
  return false;
}

void AudioPage::bands(const std::array<std::uint8_t, 16>& b) {
  for (std::size_t i = 0; i < 16; ++i) bands_[i].store(b[i], std::memory_order_relaxed);
  bandsNew_.store(true, std::memory_order_release);
  if (barsActive_.load(std::memory_order_relaxed) && wake_) wake_();
}

void AudioPage::applyBars() {
  if (bandsNew_.exchange(false, std::memory_order_acquire)) {
    std::array<std::uint8_t, 16> b{};
    for (std::size_t i = 0; i < 16; ++i) b[i] = bands_[i].load(std::memory_order_relaxed);
    bars_.bands(b, c_.nowMs);
  }
  bars_.setPaused(!c_.playing());
  bars_.setReducedMotion(c_.ui().reducedMotion());
  if (bars_.step(c_.nowMs)) c_.ui().markDirty();
}

void AudioPage::refresh() {
  if (!c_.session) return;
  const auto pos = c_.position();
  const auto dur = c_.state.durationMs;
  if (auto* line = c_.el("a-line")) {
    if (auto* fill = line->QuerySelector(".fill"))
      fill->SetProperty("transform", "scaleX(" + std::to_string(dur > 0 ? std::min(1.0, static_cast<double>(pos) / static_cast<double>(dur)) : 0.0) + ")");
  }
  setText(c_.el("a-pos"), clockTime(pos));
  // lean-back names the length, the controls what is left of it
  if (!c_.session->live) {
    std::string right;
    if (dur > 0) right = state_ == State::Controls ? "\u2212" + clockTime(dur - std::min(pos, dur)) : clockTime(dur);
    setText(c_.el("a-dur"), right);
  }
  setText(c_.el("a-clock"), esc(c_.clock));
  // while the song loads: how full the player's cache is, how far ahead it reaches and how fast it reads
  if (auto* buf = c_.el("a-buffer")) {
    const auto* pc = c_.player();
    const bool loading = c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING || (pc != nullptr && pc->waiting());
    std::string text;
    if (loading) {
      const auto percent = pc != nullptr ? pc->bufferingPercent() : std::nullopt;
      text = percent ? "[[player.osd.buffering_percent|percent=" + std::to_string(*percent) + "]]" : "[[player.osd.buffering]]";
      const auto line = cacheLine(c_.ui().translator().language(), c_.state.bufferedMs, pc != nullptr ? pc->cacheSpeed() : std::nullopt);
      if (!line.empty()) text += " \xc2\xb7 " + line;
    }
    if (buf->IsClassSet("hidden") != text.empty()) buf->SetClass("hidden", text.empty());
    setText(buf, text);
  }
  if (auto* play = c_.el("a-play")) {
    const bool paused = c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
    setText(play->QuerySelector(".icon"), ui::rml::iconChar(paused ? "play" : "pause"));
  }
}

void AudioPage::playerChanged() {
  if (viz_) viz_->setPaused(!c_.playing());
  updateBarsActivity();
  refresh();
  // a paused band decays to its floor, then active() lets the loop sleep again
  c_.ui().markDirty();
}

void AudioPage::tick(std::int64_t nowMs) {
  if (!c_.session) return;
  sheet_.tick(nowMs);
  if (!sheet_.isOpen()) {
    if (auto* r = c_.el("audio"); r != nullptr && r->IsClassSet("sheeted")) r->SetClass("sheeted", false);
  }
  if (state_ == State::Controls && !sheet_.isOpen() && !c_.ui().levelOpen() && nowMs - lastKeyMs_ >= kControlsMs) setState(State::Lean);
  // the host's families are the show: no ambient dimming over them (they keep moving, nothing burns in)
  if (state_ == State::Lean && c_.playing() && !hostFamily(c_.settings.visualizer) && nowMs - leanSinceMs_ >= kAmbientAfterMs)
    setState(State::Ambient);
  takeArtwork();
  // the title in the drive shape punches with it
  if (state_ != State::Controls && c_.settings.visualizer == VizFamily::Drive && viz_) {
    const float p = c_.ui().reducedMotion() ? 1.0f : viz_->punch();
    if (std::abs(p - punchShown_) > 0.004f) {
      punchShown_ = p;
      if (auto* d = c_.el("a-drive")) d->SetProperty("transform", "scale(" + std::to_string(p) + ")");
    }
  }
  if (state_ == State::Lean && !c_.playing()) leanSinceMs_ = nowMs;
  if (state_ == State::Ambient) {
    // 1 dp every 2 s inside 160 x 90: a redraw only when it moved a whole dp
    const auto d = drift(static_cast<double>(nowMs - ambientSinceMs_) / 1000.0, 0.5, 160, 90);
    if (!(d == lastDrift_)) {
      lastDrift_ = d;
      driftTo(static_cast<double>(nowMs - ambientSinceMs_) / 1000.0);
    }
  } else if (c_.settings.visualizer == VizFamily::Classic) {
    applyBars();
  }
  if (nowMs / 500 != lastRefresh_) {
    lastRefresh_ = nowMs / 500;
    refresh();
  }
}

bool AudioPage::dirty() const {
  return (viz_ && viz_->frameReady()) || (classicBars() && bars_.active(c_.nowMs));
}

void AudioPage::renderUnder(render::RenderBackend& backend, render::Rect screen) {
  if (viz_ && hostShown()) {
    viz_->draw(backend, screen);
  } else if (classicBars()) {
    barRenderer_.draw(backend, screen, bars_.heights(), barColorA_, barColorB_);
  }
}

}  // namespace cadence::screen::routes
