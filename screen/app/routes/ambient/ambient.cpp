#include "app/routes/ambient/ambient.h"

#include <ctime>

#include "ipc/log.h"
#include "screen_backend.h"
#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>
#include <nlohmann/json.hpp>

#include <algorithm>
#include <functional>

namespace cadence::screen::routes {

namespace {

using ui::rml::esc;

// 8 dp a minute (plan item 15), inside a 160 x 90 box
constexpr double kOverlayDpPerS = 8.0 / 60.0;
constexpr std::int64_t kFadeMs = 400;
constexpr std::int64_t kImageDefaultMs = 30000;
constexpr std::int64_t kRetryMs = 30000;

class NoImages final : public ImageSource {
 public:
  void request(const std::string&, std::function<void(std::optional<DecodedImage>)> done) override {
    done_.push_back(std::move(done));
  }
  void poll() override {
    auto pending = std::move(done_);
    done_.clear();
    for (auto& d : pending) d(std::nullopt);
  }

 private:
  std::vector<std::function<void(std::optional<DecodedImage>)>> done_;
};

}  // namespace

std::unique_ptr<ImageSource> makeNoImages() { return std::make_unique<NoImages>(); }

std::optional<AmbientPick> ambientPickFrom(const std::string& raw) {
  const auto j = nlohmann::json::parse(raw, nullptr, false);
  if (!j.is_object()) return std::nullopt;
  AmbientPick p;
  p.kind = j.value("kind", "");
  p.id = j.value("id", "");
  p.url = j.value("url", "");
  p.category = j.value("category", "");
  p.durationS = j.value("duration", 0);
  p.weather = j.value("weather", "");
  for (const auto& c : j.value("captions", nlohmann::json::array())) {
    if (c.is_object()) p.captions.push_back({c.value("atS", 0), c.value("text", "")});
  }
  std::sort(p.captions.begin(), p.captions.end(), [](const auto& a, const auto& b) { return a.atS < b.atS; });
  if (p.url.empty() || (p.kind != "video" && p.kind != "image")) return std::nullopt;
  return p;
}

std::string captionAt(const std::vector<AmbientPick::Caption>& captions, double s) {
  std::string out;
  for (const auto& c : captions)
    if (c.atS <= s) out = c.text;
  return out;
}

AmbientPage::AmbientPage(PlaybackContext& c, std::unique_ptr<ImageSource> images) : Page(c), images_(std::move(images)) {}

AmbientPage::~AmbientPage() {
  // gl objects go with the context's owner (the ui's detach); a texture left here is the context's to free
}

bool AmbientPage::clipPlaying() const {
  // the player's own state, not the last report: a content play that replaced the clip a moment ago is never stopped
  const auto* p = c_.player();
  return !clipSession_.empty() && p != nullptr && p->state().sessionId == clipSession_;
}

void AmbientPage::enter() {
  enteredMs_ = c_.nowMs;
  drift_ = {-1, -1};
  caption_.clear();
  asleep_ = false;
  // the aurora alone: nothing to ask the server for
  if (c_.settings.ambientSource == "aurora") {
    if (auto* f = c_.el("amb-fade")) f->SetClass("black", false);
    return;
  }
  if (!pick_) next();
}

bool AmbientPage::auroraShown() const {
  if (asleep_) return false;
  if (c_.settings.ambientSource == "aurora") return true;
  return !kbStarted_ && !pending_ && !clipPlaying();
}

std::string AmbientPage::body() {
  return "<div class=\"amb\" id=\"amb\"><div class=\"fade black\" id=\"amb-fade\"/><div class=\"shade\"/><div class=\"over\" "
         "id=\"amb-over\"><div class=\"aclockbig\" id=\"amb-clock\"/><div class=\"aweather\" id=\"amb-weather\"/><div "
         "class=\"aplaying\" id=\"amb-playing\"/><div class=\"acaption\" id=\"amb-caption\"/></div></div>";
}

void AmbientPage::loaded(bool restoring) {
  (void)restoring;
  drift_ = {-1, -1};
  refreshOverlay();
  driftTo(static_cast<double>(c_.nowMs - enteredMs_) / 1000.0);
}

void AmbientPage::leave() {
  // the clip goes with idle: nothing of ambient stays playing behind another page
  if (clipPlaying()) c_.command(player::CommandAction::Stop);
  clipSession_.clear();
  pick_.reset();
  pending_.reset();
  kbStarted_ = false;
  retryAtMs_ = -1;
}

bool AmbientPage::key(const platform::KeyEvent& event) {
  (void)event;
  // the routes wake from ambient on the first key; nothing here acts on one
  return true;
}

void AmbientPage::next() {
  if (asking_ || c_.host == nullptr) return;
  asking_ = true;
  nlohmann::json args = nlohmann::json::object();
  const bool sent = c_.rpc.call("ambient.next", args.dump(), [this](const RpcAnswer& a) {
    asking_ = false;
    std::optional<AmbientPick> p;
    if (a.ok()) p = ambientPickFrom(a.json);
    else CADENCE_LOG(Info, "ambient_next_failed", {{"status", ipc::Token(a.status)}});
    // each answer brings the weather as it is now (none: the line goes)
    if (p) c_.weather = p->weather;
    if (!p) {
      // nothing to show yet (no manifest, no image, offline): the clock alone, asking again later
      retryAtMs_ = c_.nowMs + kRetryMs;
      if (auto* f = c_.el("amb-fade")) f->SetClass("black", false);
      return;
    }
    show(*p);
  }, c_.nowMs);
  if (!sent) {
    asking_ = false;
    retryAtMs_ = c_.nowMs + kRetryMs;
    if (auto* f = c_.el("amb-fade")) f->SetClass("black", false);
  }
}

void AmbientPage::show(const AmbientPick& p) {
  // a change goes through black: the fade covers the swap
  if (auto* f = c_.el("amb-fade")) f->SetClass("black", true);
  fadeUntilMs_ = c_.nowMs + kFadeMs;
  pick_ = p;
  pickedMs_ = c_.nowMs;
  kbStarted_ = false;
  caption_.clear();
  const bool pausedFilm = c_.session && c_.state.sessionId == c_.session->sessionId &&
                          c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
  if (p.kind == "video" && pausedFilm) {
    // one player: a clip would replace the paused session idle covers, so the clock and its line alone (a stopped
    // or finished one keeps nothing to cover: the clip plays)
    pending_.reset();
    retryAtMs_ = c_.nowMs + kRetryMs * 10;
    if (auto* f = c_.el("amb-fade")) f->SetClass("black", false);
  } else if (p.kind == "video") {
    pending_.reset();
    clipSession_ = "ambient-" + std::to_string(++clips_);
    player::PlayRequest r;
    r.sessionId = clipSession_;
    r.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_AMBIENT;
    r.url = p.url;
    auto* pl = c_.player();
    if (pl != nullptr && !pl->play(r).empty()) {
      // the player refused it (the video surface is an airplay mirror's): the clock alone, asking again later
      clipSession_.clear();
      retryAtMs_ = c_.nowMs + kRetryMs;
      if (auto* f = c_.el("amb-fade")) f->SetClass("black", false);
    }
  } else {
    if (clipPlaying()) c_.command(player::CommandAction::Stop);
    clipSession_.clear();
    const auto id = p.id;
    images_->request(p.url, [this, id](std::optional<DecodedImage> img) {
      if (!pick_ || pick_->id != id) return;
      if (!img) {
        CADENCE_LOG(Info, "ambient_image_unavailable");
        retryAtMs_ = c_.nowMs + kRetryMs;
        if (auto* f = c_.el("amb-fade")) f->SetClass("black", false);
        return;
      }
      pending_ = std::move(img);
      // its time on screen (and the fade in) counts from now, not from the frame that uploads it
      pendingAtMs_ = c_.nowMs;
    });
  }
  refreshOverlay();
  c_.ui().markDirty();
}

void AmbientPage::setAsleep(bool asleep) {
  asleep_ = asleep;
  if (auto* f = c_.el("amb-fade")) f->SetClass("black", asleep);
  if (asleep && clipPlaying()) c_.command(player::CommandAction::Stop);
  c_.ui().markDirty();
}

void AmbientPage::driftTo(double tS) {
  const auto d = drift(tS, kOverlayDpPerS, 160, 90);
  if (d == drift_) return;
  drift_ = d;
  setStyle(c_.el("amb-over"), "transform", "translate(" + dp(d.x - 80) + ", " + dp(d.y - 45) + ")");
  c_.ui().markDirty();
}

void AmbientPage::refreshOverlay() {
  const auto& s = c_.settings;
  setText(c_.el("amb-clock"), s.showClock ? esc(c_.clock) : "");
  setText(c_.el("amb-weather"), s.showWeather ? esc(c_.weather) : "");
  std::string playing;
  if (c_.session) playing = esc(c_.session->title) + (c_.session->artist.empty() ? "" : " · " + esc(c_.session->artist));
  setText(c_.el("amb-playing"), playing);
  setText(c_.el("amb-caption"), s.showCaptions ? esc(caption_) : "");
}

void AmbientPage::playerChanged() {
  if (clipSession_.empty() || c_.state.sessionId != clipSession_) return;
  // the clip's first frame lifts the black; its end asks for the next
  if (c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING && c_.nowMs >= fadeUntilMs_) {
    if (auto* f = c_.el("amb-fade"); f != nullptr && f->IsClassSet("black") && !asleep_) f->SetClass("black", false);
  }
  if (c_.state.ended) {
    clipSession_.clear();
    next();
  }
}

void AmbientPage::tick(std::int64_t nowMs) {
  if (images_) images_->poll();
  if (asleep_) return;
  // an image is in: the black lifts now, the quad is drawn from the next frame (where gl uploads it)
  if (pending_) {
    if (auto* f = c_.el("amb-fade"); f != nullptr && f->IsClassSet("black")) f->SetClass("black", false);
  }
  driftTo(static_cast<double>(nowMs - enteredMs_) / 1000.0);
  if (pick_ && pick_->kind == "video" && c_.state.sessionId == clipSession_) {
    // captions at their times, by the clip's own position
    const auto text = captionAt(pick_->captions, static_cast<double>(c_.position()) / 1000.0);
    if (text != caption_) {
      caption_ = text;
      refreshOverlay();
    }
  }
  if (pick_ && pick_->kind == "image" && kbStarted_) {
    const std::int64_t shown = pick_->durationS > 0 ? pick_->durationS * 1000LL : kImageDefaultMs;
    if (nowMs - pickedMs_ >= shown) next();
  }
  if (retryAtMs_ >= 0 && nowMs >= retryAtMs_ && c_.settings.ambientSource != "aurora") {
    retryAtMs_ = -1;
    next();
  }
  if (nowMs / 1000 != lastClock_) {
    lastClock_ = nowMs / 1000;
    refreshOverlay();
  }
}

bool AmbientPage::dirty() const {
  if (kbStarted_ && !asleep_) return true;
  // the aurora moves slowly: 20 frames a second are plenty, and cheap for an idle screen
  return auroraShown() && (auroraDrawnMs_ < 0 || c_.nowMs - auroraDrawnMs_ >= 50);
}

void AmbientPage::renderUnder(render::RenderBackend& backend, render::Rect screen) {
  if (asleep_) return;
  backend_ = &backend;
  const double nowS = static_cast<double>(c_.nowMs) / 1000.0;
  if (pending_) {
    // uploaded here, where gl is current; the drift starts with it
    if (texture_.id != 0) backend.destroyTexture(texture_);
    texture_ = backend.createTexture(render::Size{pending_->width, pending_->height}, render::PixelFormat::Rgba8,
                                     pending_->rgba.data(), pending_->width * 4);
    cadence::render::KenBurnsConfig kc;
    kc.duration_s = pick_ && pick_->durationS > 0 ? pick_->durationS : kImageDefaultMs / 1000.0;
    kb_ = cadence::render::KenBurns(kc);
    kb_.set_reduced_motion(c_.ui().reducedMotion());
    kb_.start(texture_.id, pending_->width, pending_->height, std::hash<std::string>{}(pick_ ? pick_->id : ""),
              static_cast<double>(pendingAtMs_) / 1000.0);
    pending_.reset();
    kbStarted_ = true;
    pickedMs_ = pendingAtMs_;
    if (auto* f = c_.el("amb-fade")) f->SetClass("black", false);
  }
  if (auroraShown()) {
    std::time_t now = static_cast<std::time_t>(c_.host != nullptr ? c_.host->epochMs() / 1000 : 0);
    std::tm local{};
    localtime_r(&now, &local);
    aurora_.draw(backend, screen, nowS, local.tm_hour, c_.ui().reducedMotion());
    auroraDrawnMs_ = c_.nowMs;
    return;
  }
  if (!kbStarted_ || texture_.id == 0) return;
  if (!canvas_) canvas_ = std::make_unique<cadence::render::BackendCanvas>(backend);
  canvas_->set_flipped(false);
  canvas_->draw_quad(kb_.quad(cadence::render::Rect{static_cast<float>(screen.x), static_cast<float>(screen.y),
                                                  static_cast<float>(screen.width), static_cast<float>(screen.height)},
                            nowS));
}

}  // namespace cadence::screen::routes
