#include "ui/core/ui.h"

#include "render/perf.h"
#include "ui/cards/timeline.h"
#include "ui/core/rml.h"
#include "ui/rml/fallback-fonts.h"
#include "ui/tokens.h"

#include <RmlUi/Core.h>

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cstdlib>

namespace cadence::screen::ui {

namespace {

// a spinner's turn: the skeleton pulse's period, so the gallery's fixed phase (a whole number of both) draws the arc
// at rest, as the old frame 0 did
constexpr std::int64_t kSpinnerTurnMs = tokens::kMotionPulseMs;
constexpr int kShimmerSteps = 16;
constexpr const char* kShimmerClass[kShimmerSteps] = {"s0", "s1", "s2",  "s3",  "s4",  "s5",  "s6",  "s7",
                                                      "s8", "s9", "s10", "s11", "s12", "s13", "s14", "s15"};

struct Face {
  const char* file;
  const char* family;
  Rml::Style::FontWeight weight;
  bool fallback;
};

// design r2 section 4: inter 400/500/600, inter display 700, inter figures 500 (tnum baked), jetbrains mono 400,
// and the icon font cut from lucide's set
constexpr Face kFaces[] = {
    {"fonts/Inter-Regular.ttf", "Inter", Rml::Style::FontWeight::Normal, true},
    {"fonts/Inter-Medium.ttf", "Inter", Rml::Style::FontWeight(500), false},
    {"fonts/Inter-SemiBold.ttf", "Inter", Rml::Style::FontWeight(600), false},
    {"fonts/InterDisplay-Bold.ttf", "Inter Display", Rml::Style::FontWeight::Bold, false},
    {"fonts/InterFigures-Medium.ttf", "Inter Figures", Rml::Style::FontWeight(500), false},
    {"fonts/JetBrainsMono-Regular.ttf", "JetBrains Mono", Rml::Style::FontWeight::Normal, false},
    {"fonts/CadenceIcons.ttf", "Cadence Icons", Rml::Style::FontWeight::Normal, false},
};

std::string splitField(const std::string& s, std::size_t& pos, char sep) {
  const auto end = s.find(sep, pos);
  std::string out = s.substr(pos, end == std::string::npos ? std::string::npos : end - pos);
  pos = end == std::string::npos ? s.size() + 1 : end + 1;
  return out;
}

const char* toneIcon(ToastTone t) {
  switch (t) {
    case ToastTone::Info:
      return "info";
    case ToastTone::Success:
      return "check";
    case ToastTone::Warning:
      return "triangle";
    case ToastTone::Danger:
      return "octagon";
  }
  return "info";
}

const char* toneClass(ToastTone t) {
  switch (t) {
    case ToastTone::Info:
      return "tone-info";
    case ToastTone::Success:
      return "tone-success";
    case ToastTone::Warning:
      return "tone-warning";
    case ToastTone::Danger:
      return "tone-danger";
  }
  return "tone-info";
}

// what the routes change in a document (a class, a property, an attribute, a child, text) bumps this, so the ui
// knows a frame is owed without each route saying so (a preview's new text and backdrop under a host already shown
// started no transition, and stayed off screen until the next key). generic elements only: what rmlui instances
// itself (img, input, text) reaches it through its parent's layout or children
std::uint64_t gChanges = 0;
// the subset that can bring a network image in or out: a child added or removed, an attribute set (a data-img, the
// binder's own keys). an animation's frames change properties alone, so they no longer rescan the documents
std::uint64_t gStructure = 0;

class TrackedElement final : public Rml::Element {
 public:
  explicit TrackedElement(const Rml::String& name) : Rml::Element(name) {}

 protected:
  void OnPropertyChange(const Rml::PropertyIdSet& changed) override {
    Rml::Element::OnPropertyChange(changed);
    ++gChanges;
  }
  void OnAttributeChange(const Rml::ElementAttributes& changed) override {
    Rml::Element::OnAttributeChange(changed);
    ++gChanges;
    ++gStructure;
  }
  void OnChildAdd(Rml::Element* child) override {
    Rml::Element::OnChildAdd(child);
    ++gChanges;
    ++gStructure;
  }
  void OnChildRemove(Rml::Element* child) override {
    Rml::Element::OnChildRemove(child);
    ++gChanges;
    ++gStructure;
  }
  void DirtyLayout() override {
    Rml::Element::DirtyLayout();
    ++gChanges;
  }
};

Rml::ElementInstancerGeneric<TrackedElement>& trackedInstancer() {
  static Rml::ElementInstancerGeneric<TrackedElement> instancer;
  return instancer;
}

void removeElement(Rml::Element* e) {
  if (e == nullptr) return;
  if (auto* p = e->GetParentNode()) p->RemoveChild(e);
}

}  // namespace

std::vector<SecondaryAction> parseActions(const std::string& attr) {
  std::vector<SecondaryAction> out;
  std::size_t pos = 0;
  while (pos < attr.size()) {
    const std::string entry = splitField(attr, pos, ';');
    if (entry.empty()) continue;
    std::size_t p = 0;
    SecondaryAction a;
    a.id = splitField(entry, p, '|');
    if (p <= entry.size()) a.label = splitField(entry, p, '|');
    if (p <= entry.size()) a.icon = splitField(entry, p, '|');
    out.push_back(std::move(a));
  }
  return out;
}

Ui::Ui(i18n::Translator& translator, std::vector<std::string> assetRoots)
    : translator_(translator), files_(std::make_unique<RmlFiles>(std::move(assetRoots))) {
  alive_ = std::make_shared<Ui*>(this);
  translator_.on_change([weak = std::weak_ptr<Ui*>(alive_)](i18n::Lang) {
    if (auto self = weak.lock()) (*self)->reload();
  });
  focus_.onFocus([this](Rml::Element* e) {
    dirty_ = true;
    // rmlui pulls a focused element's document to the front: the layers go back over the page
    if (!docs_.empty()) restack();
    for (const auto& h : std::vector<Hook>(hooks_)) {
      if (h.focus) h.focus(e);
    }
  });
}

Ui::~Ui() { detach(); }

bool Ui::attach(render::RenderBackend& backend, render::Size screen, double dpRatio, std::string* error) {
  const auto fail = [&](const char* code) {
    if (error != nullptr) *error = code;
    detach();
    return false;
  };
  backend_ = &backend;
  screen_ = screen;
  dp_ = static_cast<float>(dpRatio);
  focus_.setDpRatio(dp_);
  system_ = std::make_unique<RmlSystem>(translator_);
  system_->setNowMs(nowMs_);
  renderer_ = std::make_unique<RmlRenderer>(*files_);
  if (!*renderer_) return fail("ui_renderer");
  renderer_->setImageSource(
      [this](const std::string& source, Pixels& out) {
        return images_ != nullptr && images_->take(images::ImageBinder::keyOfSource(source), out);
      },
      [this](const std::string& source) {
        if (binder_) binder_->lost(source);
      });
  Rml::SetSystemInterface(system_.get());
  Rml::SetFileInterface(files_.get());
  Rml::SetRenderInterface(renderer_.get());
  if (!Rml::Initialise()) return fail("ui_rmlui");
  Rml::Factory::RegisterElementInstancer("*", &trackedInstancer());
  fontBytes_.clear();
  fontBytes_.reserve(std::size(kFaces));
  for (const auto& f : kFaces) {
    std::string bytes;
    if (!files_->read(f.file, bytes)) return fail("ui_font_missing");
    fontBytes_.push_back(std::move(bytes));
    const auto& b = fontBytes_.back();
    if (!Rml::LoadFontFace({reinterpret_cast<const Rml::byte*>(b.data()), b.size()}, f.family, Rml::Style::FontStyle::Normal,
                           f.weight, f.fallback)) {
      return fail("ui_font");
    }
  }
  // what inter lacks (cjk, emoji) from the system's own faces, mapped and paged in on demand; a face that will not
  // load is left out, never a failure
  // (a family each: rmlui refuses a second face of one family's style and weight)
  int fallbacks = 0;
  for (const auto& f : rml::fallbackFonts()) {
    (void)Rml::LoadFontFace({reinterpret_cast<const Rml::byte*>(f.bytes.data()), f.bytes.size()},
                            "Cadence Fallback " + std::to_string(++fallbacks), Rml::Style::FontStyle::Normal,
                            Rml::Style::FontWeight::Normal, true);
  }
  context_ = Rml::CreateContext("cadence", {screen.width, screen.height});
  if (context_ == nullptr) return fail("ui_context");
  context_->SetDensityIndependentPixelRatio(dp_);
  if (auto c = context_->CreateDataModel("clock")) {
    c.Bind("time", &clock_.time);
    c.Bind("date", &clock_.date);
    c.Bind("temp", &clock_.temp);
    c.Bind("weatherIcon", &clock_.weatherIcon);
    clockModel_ = c.GetModelHandle();
  }
  target_ = backend.createRenderTarget(screen, true);
  if (!target_) return fail("ui_target");
  loadOverlay();
  if (overlay_ == nullptr) return fail("ui_overlay");
  if (builder_) loadPage();
  for (auto& d : docs_) loadDocument(d);
  restack();
  dirty_ = true;
  return true;
}

void Ui::detach() {
  if (context_ != nullptr) {
    context_->UnloadAllDocuments();
    context_->Update();
    Rml::RemoveContext("cadence");
    context_ = nullptr;
  }
  page_ = nullptr;
  overlay_ = nullptr;
  for (auto& d : docs_) d.doc = nullptr;
  toastScope_ = false;
  levels_.clear();
  timers_.clear();
  hold_.reset();
  sliders_.clear();
  focus_.setRoot(nullptr);
  if (system_) Rml::Shutdown();
  renderer_.reset();
  system_.reset();
  if (backend_ != nullptr && target_) backend_->destroyRenderTarget(target_);
  if (backend_ != nullptr && prev_) backend_->destroyRenderTarget(prev_);
  target_ = {};
  prev_ = {};
  drawn_ = false;
  fadePending_ = false;
  fadeReadyMs_ = -1;
  fade_.stop();
  backend_ = nullptr;
  laidOut_ = false;
}

void Ui::resize(render::Size screen, double dpRatio) {
  if (context_ == nullptr || backend_ == nullptr) return;
  if (screen != screen_) {
    screen_ = screen;
    backend_->destroyRenderTarget(target_);
    if (prev_) backend_->destroyRenderTarget(prev_);
    prev_ = {};
    target_ = backend_->createRenderTarget(screen, true);
    drawn_ = false;
    fadePending_ = false;
    fadeReadyMs_ = -1;
    fade_.stop();
    context_->SetDimensions({screen.width, screen.height});
  }
  dp_ = static_cast<float>(dpRatio);
  focus_.setDpRatio(dp_);
  context_->SetDensityIndependentPixelRatio(dp_);
  dirty_ = true;
}

std::string Ui::document(const std::string& body, const std::string& bodyClasses) const {
  // the screens' own sheets (assets/rcss/routes-*.rcss, each stream's), linked after the library's
  std::string extra;
#if defined(CADENCE_SCREEN_UI_ROUTE_SHEETS)
  const std::string sheets = CADENCE_SCREEN_UI_ROUTE_SHEETS;
  std::size_t pos = 0;
  while (pos < sheets.size()) {
    const auto name = splitField(sheets, pos, ',');
    if (!name.empty()) extra += "<link type=\"text/rcss\" href=\"rcss/" + name + "\"/>";
  }
#endif
  return "<rml><head>"
         "<link type=\"text/rcss\" href=\"rcss/tokens.rcss\"/>"
         "<link type=\"text/rcss\" href=\"rcss/base.rcss\"/>"
         "<link type=\"text/rcss\" href=\"rcss/components.rcss\"/>"
         "<link type=\"text/rcss\" href=\"rcss/cards.rcss\"/>" +
         extra + "</head><body class=\"" +
         bodyClasses + (reduced_ ? " reduced" : "") + "\">" + body + "</body></rml>";
}

void Ui::loadOverlay() {
  const std::string body =
      "<div id=\"toasts\"/><div id=\"nowplaying\"/>"
      "<div class=\"layer\" id=\"dialog-layer\"><div class=\"scrim-sheet\"/><div class=\"dialog\" id=\"dialog\"/></div>"
      "<div class=\"layer\" id=\"sheet-layer\"><div class=\"scrim-sheet\"/><div class=\"bsheet\" id=\"sheet\"/></div>";
  overlay_ = context_->LoadDocumentFromMemory(document(body, "overlay"), "overlay.rml");
  if (overlay_ != nullptr) overlay_->Show(Rml::ModalFlag::None, Rml::FocusFlag::None);
}

void Ui::setPage(Builder builder, std::string bodyClasses) {
  builder_ = std::move(builder);
  bodyClasses_ = std::move(bodyClasses);
  if (context_ != nullptr) loadPage();
}

void Ui::loadPage() {
  while (!levels_.empty()) {
    if (auto* l = levels_.back().layer.get()) l->SetClass("open", false);
    levels_.pop_back();
  }
  // the page before fades out over this one, which shows only once laid out (its frame is taken before it goes)
  crossfade();
  if (page_ != nullptr) page_->Close();
  page_ = context_->LoadDocumentFromMemory(document(builder_(), bodyClasses_), "page.rml");
  laidOut_ = false;
  dirty_ = true;
  hold_.reset();
  sliders_.clear();
  if (page_ == nullptr) {
    focus_.setRoot(nullptr);
    return;
  }
  page_->Show(Rml::ModalFlag::None, Rml::FocusFlag::None);
  if (seeThrough_) page_->Hide();
  restack();
  // a scope on a layer of its own (the drawer, a panel) outlives the page under it
  focus_.setRoot(page_, [this](Rml::Element* e) { return foreign(e); });
  toastScope_ = false;
  if (pageLoaded_) pageLoaded_(*page_);
}

void Ui::reload() {
  if (context_ == nullptr) return;
  // the focused element's id survives the reload when it has one
  std::string focusedId;
  if (auto* f = focus_.current()) focusedId = f->GetId();
  if (overlay_ != nullptr) overlay_->Close();
  loadOverlay();
  if (builder_) loadPage();
  for (auto& d : docs_) loadDocument(d);
  restack();
  if (!focusedId.empty()) {
    after(0, [this, focusedId] {
      if (page_ == nullptr) return;
      if (auto* e = page_->GetElementById(focusedId)) focus_.focus(e);
    });
  }
}

void Ui::after(std::int64_t delayMs, std::function<void()> fn) {
  timers_.push_back(Timer{nowMs_ + std::max<std::int64_t>(0, delayMs), timerSeq_++, std::move(fn)});
}

bool Ui::key(const platform::KeyEvent& event) {
  if (context_ == nullptr || page_ == nullptr) return false;
  const auto k = event.key;
  const auto a = event.action;
  dirty_ = true;
  {
    const auto hooks = hooks_;
    for (auto it = hooks.rbegin(); it != hooks.rend(); ++it) {
      if (it->key && it->key(event)) return true;
    }
  }
  const auto dirOf = [](cadence_screen_v1_Key key) -> std::optional<Dir> {
    switch (key) {
      case cadence_screen_v1_Key_KEY_UP:
        return Dir::Up;
      case cadence_screen_v1_Key_KEY_DOWN:
        return Dir::Down;
      case cadence_screen_v1_Key_KEY_LEFT:
        return Dir::Left;
      case cadence_screen_v1_Key_KEY_RIGHT:
        return Dir::Right;
      default:
        return std::nullopt;
    }
  };
  if (const auto dir = dirOf(k)) {
    if (a == cadence_screen_v1_KeyAction_KEY_ACTION_UP) {
      repeats_.erase(k);
      if (held_ && repeats_.empty()) {
        held_ = false;
        focus_.setHeld(false);
      }
      return true;
    }
    if (a == cadence_screen_v1_KeyAction_KEY_ACTION_LONG) return true;
    if (a == cadence_screen_v1_KeyAction_KEY_ACTION_REPEAT) {
      // after the third repeat, transitions shorten to 80 ms
      if (++repeats_[k] >= tokens::kHeldRepeat && !held_) {
        held_ = true;
        focus_.setHeld(true);
      }
    }
    if (hold_) release(false);
    auto* cur = focus_.current();
    if (cur != nullptr && (dir == Dir::Left || dir == Dir::Right) &&
        cur->GetAttribute<Rml::String>("data-kind", "") == "slider") {
      return sliderKey(cur, *dir);
    }
    // a code block, or an expanded long text, scrolls a line at a time before focus moves on
    if (cur != nullptr && (dir == Dir::Up || dir == Dir::Down)) {
      const auto kind = cur->GetAttribute<Rml::String>("data-kind", "");
      if (kind == "scroll" || (kind == "expand" && cur->IsClassSet("expanded"))) {
        const float line = 32.0f * dp_;
        const float top = cur->GetScrollTop();
        const float max = cur->GetScrollHeight() - cur->GetClientHeight();
        const float next = std::clamp(top + (dir == Dir::Down ? line : -line), 0.0f, std::max(0.0f, max));
        if (std::fabs(next - top) > 0.5f) {
          cur->SetScrollTop(next);
          return true;
        }
      }
    }
    const bool moved = focus_.move(*dir);
    // down past the last element goes into a toast with a button; up out of one comes back
    if (!moved && dir == Dir::Down && !toastScope_) enterToast();
    if (!moved && dir == Dir::Up && toastScope_) leaveToast();
    return true;
  }
  if (k == cadence_screen_v1_Key_KEY_OK) {
    switch (a) {
      case cadence_screen_v1_KeyAction_KEY_ACTION_DOWN:
        press(focus_.current());
        return true;
      case cadence_screen_v1_KeyAction_KEY_ACTION_UP:
        release(true);
        return true;
      case cadence_screen_v1_KeyAction_KEY_ACTION_LONG:
        if (hold_ && !hold_->consumed) {
          auto* e = hold_->element.get();
          if (e != nullptr && hold_->hasActions) {
            hold_->consumed = true;
            const auto actions = parseActions(e->GetAttribute<Rml::String>("data-actions", ""));
            e->SetClass("holding", false);
            e->SetClass("pressed", false);
            if (actions.size() == 1 && actions[0].label.empty()) {
              fire(Activation{e, actions[0].id, true, std::nullopt, std::nullopt});
            } else {
              actionsSheet(e, actions);
            }
          }
          // without secondary actions a long press is a short one, at its up
        }
        return true;
      case cadence_screen_v1_KeyAction_KEY_ACTION_PRESS:
        press(focus_.current());
        release(true);
        return true;
      default:
        return true;
    }
  }
  if (k == cadence_screen_v1_Key_KEY_BACK) {
    if (a != cadence_screen_v1_KeyAction_KEY_ACTION_PRESS && a != cadence_screen_v1_KeyAction_KEY_ACTION_UP) return true;
    if (toastScope_) {
      leaveToast();
      return true;
    }
    if (closeLevel()) return true;
    return back_ && back_();
  }
  return false;
}

void Ui::press(Rml::Element* e) {
  if (e == nullptr) return;
  Hold h;
  h.element = e->GetObserverPtr();
  h.downMs = nowMs_;
  h.hasActions = !e->GetAttribute<Rml::String>("data-actions", "").empty();
  hold_ = h;
  e->SetClass("pressed", true);
}

void Ui::release(bool doActivate) {
  if (!hold_) return;
  auto h = *hold_;
  hold_.reset();
  auto* e = h.element.get();
  if (e == nullptr) return;
  e->SetClass("pressed", false);
  e->SetClass("holding", false);
  if (doActivate && !h.consumed) activate(e);
}

void Ui::activate(Rml::Element* e) {
  const auto action = e->GetAttribute<Rml::String>("data-action", "");
  // a toast's button gives focus back as it runs
  if (toastScope_ && Focus::ancestor(e, "toast") != nullptr) leaveToast();
  if (action.starts_with("dialog:") || action.starts_with("sheet:")) {
    // the overlay's own buttons
    const auto handler = e->GetAttribute<Rml::String>("data-handler", "");
    (void)handler;
  }
  if (e->GetAttribute<Rml::String>("data-kind", "") == "expand") {
    e->SetClass("expanded", !e->IsClassSet("expanded"));
    if (e->IsClassSet("expanded")) e->SetProperty("max-height", "none");
    else e->SetProperty("max-height", e->GetAttribute<Rml::String>("data-collapsed", "144dp"));
    dirty_ = true;
    return;
  }
  if (e->GetAttribute<Rml::String>("data-kind", "") == "toggle" && !e->IsClassSet("unavailable")) {
    // optimistic: flips now, pending until the server answers (section 9); the card host marks it pending as it
    // sends (marked here first, the host took it for an answer still awaited and sent nothing)
    if (e->IsClassSet("pending")) return;
    const bool on = !e->IsClassSet("on");
    e->SetClass("on", on);
    if (auto* w = e->QuerySelector(".tword")) w->SetInnerRML(on ? "[[ui.on]]" : "[[ui.off]]");
    fire(Activation{e, action, false, std::nullopt, on});
    return;
  }
  if (action.empty()) return;
  fire(Activation{e, action, false, std::nullopt, std::nullopt});
}

bool Ui::sliderKey(Rml::Element* e, Dir dir) {
  const double step = e->GetAttribute<double>("data-step", 1.0);
  const double mul = held_ ? 5.0 : 1.0;
  const double v = e->GetAttribute<double>("data-value", 0.0) + (dir == Dir::Right ? step : -step) * mul;
  setSlider(e, v);
  // a timeline stops running while it moves, and stays at the choice until the next revision or a refusal
  if (e->HasAttribute("data-timeline")) e->SetAttribute("data-moved", 1);
  for (auto& s : sliders_) {
    if (s.element.get() == e) {
      s.lastKeyMs = nowMs_;
      return true;
    }
  }
  sliders_.push_back(SliderEdit{e->GetObserverPtr(), nowMs_});
  return true;
}

void Ui::setSlider(Rml::Element* e, double value) {
  const double lo = e->GetAttribute<double>("data-min", 0.0);
  const double hi = e->GetAttribute<double>("data-max", 100.0);
  value = std::clamp(value, lo, hi);
  e->SetAttribute("data-value", value);
  const double f = hi > lo ? (value - lo) / (hi - lo) : 0;
  char buf[64];
  std::snprintf(buf, sizeof buf, "scaleX(%.4f)", f);
  if (auto* fill = e->QuerySelector(".sfill")) fill->SetProperty("transform", buf);
  std::snprintf(buf, sizeof buf, "%.2f%%", f * 100);
  if (auto* thumb = e->QuerySelector(".thumb")) thumb->SetProperty("left", buf);
  if (auto* label = e->QuerySelector(".svalue")) {
    label->SetInnerRML(rml::esc(e->HasAttribute("data-timeline")
                                    ? cards::timelineText(value, hi)
                                    : rml::sliderLabel(value, e->GetAttribute<Rml::String>("data-unit", ""))));
  }
  dirty_ = true;
}

void Ui::tickSliders() {
  for (auto it = sliders_.begin(); it != sliders_.end();) {
    auto* e = it->element.get();
    if (e == nullptr) {
      it = sliders_.erase(it);
      continue;
    }
    // one action with the value, 600 ms after the last key
    if (nowMs_ - it->lastKeyMs >= tokens::kSliderCommitMs) {
      const double v = e->GetAttribute<double>("data-value", 0.0);
      const auto action = e->GetAttribute<Rml::String>("data-action", "");
      it = sliders_.erase(it);
      if (!action.empty()) fire(Activation{e, action, false, v, std::nullopt});
      continue;
    }
    ++it;
  }
}

void Ui::tickHold() {
  if (!hold_ || hold_->consumed || !hold_->hasActions) return;
  auto* e = hold_->element.get();
  if (e == nullptr) return;
  const auto elapsed = nowMs_ - hold_->downMs;
  if (elapsed < tokens::kHoldArcStartMs) return;
  const auto span = tokens::kLongPressMs - tokens::kHoldArcStartMs;
  const int frame = static_cast<int>(std::clamp<std::int64_t>((elapsed - tokens::kHoldArcStartMs) * 8 / span, 0, 7));
  if (frame == hold_->frame) return;
  hold_->frame = frame;
  e->SetClass("holding", true);
  if (auto* arc = e->QuerySelector(".hold")) {
    for (int i = 0; i < 8; ++i) arc->SetClass("a" + std::to_string(i), i == frame);
  }
  dirty_ = true;
}

void Ui::tickSprites() {
  // spinners: one arc turned a step every frame (kSpinnerFrameMs, a vsync), a turn per pulse period, only while
  // one shows: the loop wakes for them alone and sleeps again once none is visible
  const auto frame = static_cast<int>(nowMs_ / tokens::kSpinnerFrameMs);
  // the skeleton: one shared shimmer sweep, in 16 steps, for every block at once (components.rcss' .skel.s0-s15)
  const auto period = tokens::kMotionShimmerMs;
  const int step = static_cast<int>((nowMs_ % period) * kShimmerSteps / period);
  std::vector<Rml::ElementDocument*> docs{page_, overlay_};
  for (auto& d : docs_) docs.push_back(d.doc);
  const bool spinnerStep = frame != spinnerFrame_;
  const bool skeletonStep = step != skeletonStep_;
  if (spinnerStep) spinnersShown_ = false;
  if (skeletonStep) skeletonsShown_ = false;
  for (auto* doc : docs) {
    if (doc == nullptr) continue;
    if (spinnerStep) {
      Rml::ElementList spinners;
      doc->QuerySelectorAll(spinners, ".spinner");
      for (auto* s : spinners) {
        if (!s->IsVisible(true)) continue;
        char deg[32];
        std::snprintf(deg, sizeof deg, "rotate(%.1fdeg)", static_cast<double>(nowMs_ % kSpinnerTurnMs) * 360.0 / kSpinnerTurnMs);
        s->SetProperty("transform", deg);
        spinnersShown_ = true;
        dirty_ = true;
      }
    }
    if (skeletonStep) {
      Rml::ElementList skels;
      doc->QuerySelectorAll(skels, ".skel");
      for (auto* s : skels) {
        if (!s->IsVisible(true)) continue;
        for (int i = 0; i < kShimmerSteps; ++i) s->SetClass(kShimmerClass[i], i == step);
        skeletonsShown_ = true;
        dirty_ = true;
      }
    }
  }
  spinnerFrame_ = frame;
  skeletonStep_ = step;
}

void Ui::update(std::int64_t nowMs) {
  nowMs_ = nowMs;
  if (system_) system_->setNowMs(nowMs);
  if (context_ == nullptr) return;
  // due timers in order; one may add more
  std::sort(timers_.begin(), timers_.end(), [](const Timer& a, const Timer& b) {
    return a.atMs != b.atMs ? a.atMs < b.atMs : a.seq < b.seq;
  });
  std::vector<Timer> due;
  while (!timers_.empty() && timers_.front().atMs <= nowMs) {
    due.push_back(std::move(timers_.front()));
    timers_.erase(timers_.begin());
  }
  for (auto& t : due) {
    t.fn();
    dirty_ = true;
  }
  for (const auto& h : std::vector<Hook>(hooks_)) {
    if (h.tick) h.tick(nowMs);
  }
  if (focus_.tick(nowMs)) dirty_ = true;
  tickHold();
  tickSliders();
  tickSprites();
  {
    render::perf::Scope rml(render::perf::Span::RmlUpdate);
    context_->Update();
  }
  if (page_ != nullptr && !laidOut_) {
    laidOut_ = true;
    dirty_ = true;
  }
  // a cross-fade runs from the moment its new frame could first be shown (a renderer that draws later, the gallery's
  // capture, finds it over or partway, as the clock says)
  if (fadePending_ && fadeReadyMs_ < 0 && (page_ == nullptr || laidOut_)) fadeReadyMs_ = nowMs;
  // a focused toast that left gives focus back
  if (toastScope_) {
    auto* host = overlay_ != nullptr ? overlay_->GetElementById("toasts") : nullptr;
    Rml::ElementList buttons;
    if (host != nullptr) host->QuerySelectorAll(buttons, ".f");
    bool any = false;
    for (auto* b : buttons) any = any || (b->IsVisible(true) && !b->GetParentNode()->IsClassSet("leaving"));
    if (!any) leaveToast();
  }
  focus_.repair();
  // network images: a scan after a change to the documents' structure (throttled to 10 a second) or when a decode
  // finished, and once a second while anything else changes (an image a style change alone revealed). a change the
  // throttle held back stays owed (nextUpdateMs wakes the loop for it): a lone change, a preview's new backdrop, would
  // otherwise wait for the next unrelated one. a scan walks every document (2-3 ms on the stick): an animation's
  // frames scanned 10 times a second before
  if (binder_) {
    noteChanges();
    if (gStructure != scannedStructure_ || (dirty_ && nowMs - lastScanMs_ >= 1000)) scanOwed_ = true;
    const bool fresh = binder_->poll();
    if (fresh || (scanOwed_ && nowMs - lastScanMs_ >= 100) || (binder_->waiting() > 0 && nowMs - lastScanMs_ >= 250)) {
      lastScanMs_ = nowMs;
      scanOwed_ = false;
      render::perf::Scope scanSpan(render::perf::Span::ImageScan);
      bool changed = false;
      std::vector<Rml::ElementDocument*> scan{page_, overlay_};
      for (auto& d : docs_) scan.push_back(d.doc);
      for (auto* doc : scan) {
        if (doc != nullptr) changed = binder_->scan(doc) || changed;
      }
      // the binder's own attributes (its keys on what it bound) are no change to scan for
      scannedStructure_ = gStructure;
      if (changed) {
        dirty_ = true;
        render::perf::Scope rml(render::perf::Span::RmlUpdate);
        context_->Update();
      }
    }
  }
  if (context_->GetNextUpdateDelay() <= 0) dirty_ = true;
  noteChanges();
}

void Ui::noteChanges() {
  // anything a route changed since the last look (the documents say so through their elements)
  if (gChanges != seenChanges_) {
    seenChanges_ = gChanges;
    dirty_ = true;
  }
}

std::int64_t Ui::nextUpdateMs(std::int64_t nowMs) const {
  if (context_ == nullptr) return -1;
  if (dirty()) return 0;
  std::int64_t best = -1;
  const auto due = [&](std::int64_t atMs) {
    const std::int64_t d = atMs > nowMs ? atMs - nowMs : 0;
    if (best < 0 || d < best) best = d;
  };
  for (const auto& t : timers_) due(t.atMs);
  // rmlui's own: 0 while a transition or an animation runs, a caret's blink, else infinity
  if (const double s = context_->GetNextUpdateDelay(); std::isfinite(s)) {
    due(nowMs + (s > 0 ? static_cast<std::int64_t>(std::ceil(s * 1000.0)) : 0));
  }
  if (const auto f = focus_.nextDueMs(nowMs); f >= 0) due(nowMs + f);
  for (const auto& sl : sliders_) due(sl.lastKeyMs + tokens::kSliderCommitMs);
  if (hold_ && !hold_->consumed && hold_->hasActions) {
    const auto span = tokens::kLongPressMs - tokens::kHoldArcStartMs;
    const auto next = hold_->frame < 0 ? tokens::kHoldArcStartMs : tokens::kHoldArcStartMs + (hold_->frame + 1) * span / 8;
    if (hold_->frame < 7) due(hold_->downMs + next);
  }
  // the sprites step on their own grid (every block at once): the next boundary
  if (spinnersShown_) due((nowMs / tokens::kSpinnerFrameMs + 1) * tokens::kSpinnerFrameMs);
  if (skeletonsShown_) {
    const auto stepMs = std::max<std::int64_t>(1, tokens::kMotionShimmerMs / kShimmerSteps);
    due((nowMs / stepMs + 1) * stepMs);
  }
  // decodes in flight: update's poll sees them land (it scans at most every 250 ms while they wait)
  if (binder_ && binder_->waiting() > 0) due(nowMs + 50);
  if (binder_ && scanOwed_) due(lastScanMs_ + 100);
  // images the upload budget held back go up in the next frame
  if (binder_ && binder_->deferred() > 0) due(nowMs);
  return best;
}

void Ui::setImages(images::ImageCache* cache) {
  binder_.reset();
  scanOwed_ = false;
  images_ = cache;
  if (cache != nullptr) binder_ = std::make_unique<images::ImageBinder>(*cache);
  lastScanMs_ = -1;
  dirty_ = true;
}

void Ui::crossfade() {
  if (!drawn_) return;
  // the frame on screen is the outgoing one: direct frames left the layer behind, so it is drawn there now, before
  // the change (a fade already owed keeps its snapshot: the frame before both changes)
  if (!fadePending_ && !targetFresh_ && backend_ != nullptr) renderLayer(*backend_);
  fadePending_ = true;
}

void Ui::renderLayer(render::RenderBackend& backend) {
  backend.bindRenderTarget(&target_);
  renderer_->SetViewport(target_.color.width, target_.color.height);
  renderer_->SetExternalTarget(target_.fbo, target_.stencil);
  renderer_->resetFrame(target_.color.width, target_.color.height);
  renderer_->BeginFrame();
  {
    render::perf::Scope rml(render::perf::Span::RmlRender);
    context_->Render();
  }
  renderer_->EndFrame();
  backend.resetState();
  backend.bindRenderTarget(nullptr);
  targetFresh_ = true;
}

void Ui::render(render::RenderBackend& backend, render::Rect dst) {
  if (context_ == nullptr || !target_) return;
  const render::Rect full{0, 0, screen_.width, screen_.height};
  // what a page draws under the layer (the visualizer's texture, ken burns), on the whole screen
  const auto underlay = [&] {
    if (underlay_ && !seeThrough_) underlay_(backend, full);
  };
  if (direct_ && !fadePending_ && fade_.startMs < 0 && dst == full && backend.frameSize() == screen_) {
    // straight into the frame: no target of its own to clear, resolve and composite (a full-screen pass and its
    // memory traffic less a frame). the frame began with a clear stencil (RenderBackend::beginFrame). a page's
    // cross-fade takes the layer for its run
    underlay();
    backend.bindRenderTarget(nullptr);
    renderer_->SetViewport(screen_.width, screen_.height);
    renderer_->SetExternalTarget(0, 0, false);
    renderer_->resetFrame(screen_.width, screen_.height);
    renderer_->BeginFrame();
    {
      render::perf::Scope rml(render::perf::Span::RmlRender);
      context_->Render();
    }
    renderer_->EndFrame();
    backend.resetState();
    dirty_ = false;
    drawn_ = true;
    // the layer holds an older frame now
    targetFresh_ = false;
    return;
  }
  // a new page not laid out yet: the last frame stays as it is (never a half-built one)
  const bool hold = fadePending_ && (fadeReadyMs_ < 0 || (page_ != nullptr && !laidOut_));
  if (fadePending_ && !hold) {
    fadePending_ = false;
    const std::int64_t startMs = fadeReadyMs_;
    const int ms = reduced_ ? tokens::kMotionReducedMs : tokens::kMotionPageMs;
    fadeReadyMs_ = -1;
    if (nowMs_ < startMs + ms && !prev_) prev_ = backend.createRenderTarget(screen_, false);
    if (nowMs_ < startMs + ms && prev_) {
      // the last drawn frame, copied once, is the outgoing one (rmlui keeps drawing into its one target: a new base
      // target would cost it every layer it holds)
      backend.bindRenderTarget(&prev_);
      backend.clear(0, 0, 0, 0);
      render::QuadParams copy;
      copy.dst = render::Rect{0, 0, screen_.width, screen_.height};
      copy.blend = render::Blend::None;
      backend.drawTexturedQuad(target_.color, copy);
      backend.bindRenderTarget(nullptr);
      fade_.start(startMs, ms);
    }
    dirty_ = true;
  }
  if ((dirty_ || !targetFresh_) && !hold) {
    renderLayer(backend);
    dirty_ = false;
    drawn_ = true;
  }
  underlay();
  render::QuadParams q;
  q.dst = dst;
  q.blend = render::Blend::Premultiplied;
  backend.drawTexturedQuad(target_.color, q);
  if (fade_.startMs >= 0) {
    if (fade_.running(nowMs_) && prev_) {
      q.opacity = static_cast<float>(fade_.opacity(nowMs_));
      backend.drawTexturedQuad(prev_.color, q);
    } else {
      fade_.stop();
    }
  }
}

void Ui::setSeeThrough(bool on) {
  if (seeThrough_ == on) return;
  seeThrough_ = on;
  if (page_ != nullptr) {
    if (on) page_->Hide();
    else page_->Show(Rml::ModalFlag::None, Rml::FocusFlag::None);
    if (!on) restack();
  }
  dirty_ = true;
}

void Ui::toast(const ToastSpec& t) {
  if (overlay_ == nullptr) return;
  auto* host = overlay_->GetElementById("toasts");
  if (host == nullptr) return;
  auto el = overlay_->CreateElement("div");
  auto* e = host->AppendChild(std::move(el));
  e->SetClassNames("toast entering");
  e->SetId("toast-" + std::to_string(++toastSeq_));
  std::string inner = rml::icon(t.icon.empty() ? toneIcon(t.tone) : t.icon, toneClass(t.tone)) + "<div class=\"ttext\">" +
                      t.text + "</div>";
  if (!t.actionLabel.empty()) {
    inner += rml::button(rml::Button{t.actionLabel, {}, "toast:" + t.actionId, false, false, false, {}, {}});
  }
  e->SetInnerRML(inner);
  auto ptr = e->GetObserverPtr();
  after(0, [ptr] {
    if (auto* x = ptr.get()) x->SetClass("entering", false);
  });
  const int duration = t.durationMs > 0 ? t.durationMs : tokens::kToastMs;
  // a focused toast stays while it holds focus
  auto leave = std::make_shared<std::function<void()>>();
  *leave = [this, ptr, leave] {
    auto* x = ptr.get();
    if (x == nullptr) return;
    if (auto* f = focus_.current(); f != nullptr && Focus::ancestor(f, "toast") == x) {
      after(1000, *leave);
      return;
    }
    x->SetClass("leaving", true);
    after(tokens::kMotionQuickExitMs, [ptr] { removeElement(ptr.get()); });
  };
  after(duration, [leave] {
    (*leave)();
    *leave = nullptr;
  });
  dirty_ = true;
}

void Ui::nowPlaying(const NowPlayingSpec& n) {
  if (overlay_ == nullptr) return;
  auto* host = overlay_->GetElementById("nowplaying");
  if (host == nullptr) return;
  // one at a time: a newer start replaces what still shows
  while (host->GetNumChildren() > 0) host->RemoveChild(host->GetChild(0));
  auto* e = host->AppendChild(overlay_->CreateElement("div"));
  e->SetClassNames("nowplay entering");
  std::string inner;
  if (!n.art.empty()) inner += "<div class=\"npart\">" + rml::artImage(n.art, "npimg") + "</div>";
  inner += "<div class=\"nptext\"><div class=\"nplabel\">[[nowplaying.label]]</div><div class=\"nptitle\">" + n.title + "</div>";
  if (!n.line.empty()) inner += "<div class=\"npline\">" + n.line + "</div>";
  if (!n.source.empty()) inner += "<div class=\"npsource\">" + rml::badge(rml::BadgeKind::Provider, n.source, {}) + "</div>";
  inner += "</div>";
  e->SetInnerRML(inner);
  auto ptr = e->GetObserverPtr();
  after(0, [ptr] {
    if (auto* x = ptr.get()) x->SetClass("entering", false);
  });
  after(n.durationMs > 0 ? n.durationMs : 5000, [this, ptr] {
    auto* x = ptr.get();
    if (x == nullptr) return;
    x->SetClass("leaving", true);
    after(tokens::kMotionQuickExitMs, [ptr] { removeElement(ptr.get()); });
  });
  dirty_ = true;
}

void Ui::clearToasts() {
  if (overlay_ == nullptr) return;
  if (toastScope_) leaveToast();
  if (auto* host = overlay_->GetElementById("toasts")) {
    while (host->GetNumChildren() > 0) host->RemoveChild(host->GetChild(0));
  }
  dirty_ = true;
}

void Ui::openLevel(Rml::Element* layer, std::function<void()> onClose) {
  layer->SetClass("leaving", false);
  layer->SetClass("open", true);
  layer->SetClass("entering", true);
  auto ptr = layer->GetObserverPtr();
  after(0, [ptr] {
    if (auto* l = ptr.get()) l->SetClass("entering", false);
  });
  levels_.push_back(Level{ptr, std::move(onClose)});
  focus_.pushScope(layer);
  dirty_ = true;
}

bool Ui::closeLevel() {
  if (levels_.empty()) return false;
  auto level = std::move(levels_.back());
  levels_.pop_back();
  if (auto* l = level.layer.get()) {
    l->SetClass("leaving", true);
    auto ptr = level.layer;
    after(tokens::kMotionSheetExitMs, [ptr] {
      // reopened meanwhile: openLevel took leaving off
      if (auto* x = ptr.get(); x != nullptr && x->IsClassSet("leaving")) {
        x->SetClass("open", false);
        x->SetClass("leaving", false);
      }
    });
  }
  focus_.popScope();
  if (level.onClose) level.onClose();
  dirty_ = true;
  return true;
}

void Ui::dialog(DialogSpec d) {
  if (overlay_ == nullptr) return;
  auto* layer = overlay_->GetElementById("dialog-layer");
  auto* box = overlay_->GetElementById("dialog");
  if (layer == nullptr || box == nullptr) return;
  const std::string cancel = d.cancelLabel.empty() ? "[[common.cancel]]" : d.cancelLabel;
  const std::string confirm = d.confirmLabel.empty() ? "[[ui.confirm]]" : d.confirmLabel;
  box->SetInnerRML("<div class=\"dtitle\">" + d.title + "</div><div class=\"dbody\">" + d.body +
                   "</div><div class=\"buttons\">" +
                   rml::button(rml::Button{cancel, {}, "dialog:cancel", false, false, false, "dialog-cancel", "default-focus"}) +
                   rml::button(rml::Button{confirm, {}, "dialog:confirm", true, false, false, "dialog-confirm", {}}) +
                   "</div>");
  auto onConfirm = std::move(d.onConfirm);
  auto onCancel = std::move(d.onCancel);
  // the dialog's buttons answer through the activate hook, wrapped here so the page's handler never sees them
  auto prev = activate_;
  activate_ = [this, prev, onConfirm, onCancel](const Activation& a) {
    if (a.action == "dialog:confirm" || a.action == "dialog:cancel") {
      // this lambda dies when activate_ is put back: keep what it needs first
      auto self = this;
      auto confirmFn = onConfirm;
      auto cancelFn = onCancel;
      const bool yes = a.action == "dialog:confirm";
      self->activate_ = prev;
      // closing calls onClose (the cancel path) only for BACK
      self->levels_.back().onClose = nullptr;
      self->closeLevel();
      if (yes && confirmFn) confirmFn();
      if (!yes && cancelFn) cancelFn();
      return;
    }
    if (prev) prev(a);
  };
  openLevel(layer, [this, prev, onCancel] {
    activate_ = prev;
    if (onCancel) onCancel();
  });
}

void Ui::sheet(const std::string& title, const std::string& bodyRml, const std::string& variant) {
  if (overlay_ == nullptr) return;
  auto* layer = overlay_->GetElementById("sheet-layer");
  auto* box = overlay_->GetElementById("sheet");
  if (layer == nullptr || box == nullptr) return;
  box->SetClass("right", variant == "right");
  box->SetInnerRML("<div class=\"stitle\">" + title + "</div><div class=\"stagger\">" + bodyRml + "</div>");
  openLevel(layer, nullptr);
}

void Ui::onLevelClosed(std::function<void()> fn) {
  if (!levels_.empty()) levels_.back().onClose = std::move(fn);
}

void Ui::actionsSheet(Rml::Element* item, const std::vector<SecondaryAction>& actions) {
  if (overlay_ == nullptr || item == nullptr) return;
  std::string title;
  if (auto* t = item->QuerySelector(".title")) title = t->GetInnerRML();
  if (title.empty()) {
    if (auto* t = item->QuerySelector(".ititle")) title = t->GetInnerRML();
  }
  std::string body = "<div class=\"list\">";
  for (std::size_t i = 0; i < actions.size(); ++i) {
    rml::ListItem li;
    li.leadIcon = actions[i].icon.empty() ? "more" : actions[i].icon;
    li.title = actions[i].label;
    li.action = "sheet:" + std::to_string(i);
    if (i == 0) li.id = "sheet-first";
    body += rml::listItem(li);
  }
  // «Ассистент» closes every actions sheet
  rml::ListItem assistant;
  assistant.leadIcon = "sparkle";
  assistant.title = "[[assistant.title]]";
  assistant.action = "sheet:assistant";
  body += rml::listItem(assistant) + "</div>";
  auto target = item->GetObserverPtr();
  auto prev = activate_;
  activate_ = [this, prev, target, actions](const Activation& a) {
    if (!a.action.starts_with("sheet:")) {
      if (prev) prev(a);
      return;
    }
    // this lambda dies when activate_ is put back: keep what it needs first
    auto* self = this;
    auto keepPrev = prev;
    auto keepActions = actions;
    auto* e = target.get();
    const auto rest = a.action.substr(6);
    self->activate_ = keepPrev;
    self->levels_.back().onClose = nullptr;
    self->closeLevel();
    if (rest == "assistant") {
      if (keepPrev) keepPrev(Activation{e, "assistant", true, std::nullopt, std::nullopt});
      return;
    }
    const auto i = static_cast<std::size_t>(std::stoul(rest));
    if (i < keepActions.size() && keepPrev) keepPrev(Activation{e, keepActions[i].id, true, std::nullopt, std::nullopt});
  };
  sheet(title, body);
  levels_.back().onClose = [this, prev] { activate_ = prev; };
}

void Ui::setReducedMotion(bool on) {
  reduced_ = on;
  focus_.setReduced(on);
  for (auto* d : {page_, overlay_}) {
    if (d != nullptr) d->SetClass("reduced", on);
  }
  for (auto& d : docs_) {
    if (d.doc != nullptr) d.doc->SetClass("reduced", on);
  }
  dirty_ = true;
}

void Ui::frameInterval(double ms) {
  if (ms > 1000.0 / 45.0) {
    if (slowSinceMs_ < 0) slowSinceMs_ = nowMs_;
    if (!autoReduced_ && !reduced_ && nowMs_ - slowSinceMs_ >= 5000) {
      autoReduced_ = true;
      setReducedMotion(true);
      toast(ToastSpec{"reduced-motion", ToastTone::Info, "[[toast.reduced_motion_auto]]", "info", {}, {}, 0});
    }
  } else {
    slowSinceMs_ = -1;
  }
}

void Ui::setClock(const ClockModel& clock) {
  clock_ = clock;
  if (clockModel_) clockModel_.DirtyAllVariables();
  dirty_ = true;
}

void Ui::fire(const Activation& a) {
  const auto hooks = hooks_;
  for (auto it = hooks.rbegin(); it != hooks.rend(); ++it) {
    if (it->activate && it->activate(a)) return;
  }
  if (activate_) activate_(a);
}

int Ui::addKeyFilter(KeyFilter filter) {
  hooks_.push_back(Hook{++hookSeq_, std::move(filter), {}, {}, {}});
  return hookSeq_;
}

int Ui::addTicker(Ticker ticker) {
  hooks_.push_back(Hook{++hookSeq_, {}, std::move(ticker), {}, {}});
  return hookSeq_;
}

int Ui::addActivateFilter(ActivateFilter filter) {
  hooks_.push_back(Hook{++hookSeq_, {}, {}, std::move(filter), {}});
  return hookSeq_;
}

int Ui::addFocusListener(Focus::Listener listener) {
  hooks_.push_back(Hook{++hookSeq_, {}, {}, {}, std::move(listener)});
  return hookSeq_;
}

void Ui::remove(int hookId) {
  hooks_.erase(std::remove_if(hooks_.begin(), hooks_.end(), [hookId](const Hook& h) { return h.id == hookId; }), hooks_.end());
}

Rml::ElementDocument* Ui::addDocument(const std::string& name, int z, Builder builder,
                                      std::function<void(Rml::ElementDocument&)> loaded) {
  removeDocument(name);
  docs_.push_back(DocLayer{name, z, std::move(builder), std::move(loaded), nullptr});
  std::stable_sort(docs_.begin(), docs_.end(), [](const DocLayer& a, const DocLayer& b) { return a.z < b.z; });
  for (auto& d : docs_) {
    if (d.name != name) continue;
    if (context_ != nullptr) {
      loadDocument(d);
      restack();
    }
    return d.doc;
  }
  return nullptr;
}

void Ui::removeDocument(const std::string& name) {
  for (auto it = docs_.begin(); it != docs_.end(); ++it) {
    if (it->name != name) continue;
    if (it->doc != nullptr) it->doc->Close();
    docs_.erase(it);
    dirty_ = true;
    return;
  }
}

Rml::ElementDocument* Ui::documentNamed(const std::string& name) const {
  for (const auto& d : docs_) {
    if (d.name == name) return d.doc;
  }
  return nullptr;
}

void Ui::loadDocument(DocLayer& d) {
  if (context_ == nullptr) return;
  if (d.doc != nullptr) d.doc->Close();
  d.doc = context_->LoadDocumentFromMemory(document(d.builder(), "layerdoc"), d.name + ".rml");
  if (d.doc == nullptr) return;
  d.doc->Show(Rml::ModalFlag::None, Rml::FocusFlag::None);
  if (reduced_) d.doc->SetClass("reduced", true);
  if (d.loaded) d.loaded(*d.doc);
  dirty_ = true;
}

void Ui::restack() {
  // the page, then the layers by z, then the overlay (toasts, dialogs, sheets) on top
  for (auto& d : docs_) {
    if (d.doc != nullptr) d.doc->PullToFront();
  }
  if (overlay_ != nullptr) overlay_->PullToFront();
}

bool Ui::foreign(Rml::Element* e) const {
  if (e == nullptr) return false;
  auto* doc = e->GetOwnerDocument();
  for (const auto& d : docs_) {
    if (d.doc != nullptr && d.doc == doc) return true;
  }
  return false;
}

bool Ui::enterToast() {
  if (overlay_ == nullptr || toastScope_) return false;
  auto* host = overlay_->GetElementById("toasts");
  if (host == nullptr) return false;
  // the newest toast with a button that is not leaving
  for (int i = host->GetNumChildren() - 1; i >= 0; --i) {
    auto* t = host->GetChild(i);
    if (t->IsClassSet("leaving")) continue;
    auto* b = t->QuerySelector(".f");
    if (b == nullptr) continue;
    focus_.pushScope(host);
    toastScope_ = true;
    focus_.focus(b);
    dirty_ = true;
    return true;
  }
  return false;
}

void Ui::leaveToast() {
  if (!toastScope_) return;
  toastScope_ = false;
  focus_.popScope();
  dirty_ = true;
}

}  // namespace cadence::screen::ui
