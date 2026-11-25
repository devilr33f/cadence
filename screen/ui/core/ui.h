#pragma once

#include "i18n/translator.h"
#include "platform/keys.h"
#include "render/render-backend.h"
#include "ui/core/focus.h"
#include "ui/core/motion.h"
#include "ui/images/binder.h"
#include "ui/rml/files.h"
#include "ui/rml/renderer.h"
#include "ui/rml/system.h"

#include <RmlUi/Core/Context.h>
#include <RmlUi/Core/DataModelHandle.h>
#include <RmlUi/Core/ElementDocument.h>

#include <cstdint>
#include <functional>
#include <map>
#include <memory>
#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::ui {

// one action of an element's long-press sheet: data-actions="id|label|icon;id|label|icon" (an entry without a
// label runs at once instead of opening the sheet: ё on a long press of е)
struct SecondaryAction {
  std::string id;
  std::string label;
  std::string icon;
};
std::vector<SecondaryAction> parseActions(const std::string& attr);

enum class ToastTone { Info, Success, Warning, Danger };

struct ToastSpec {
  std::string id;
  ToastTone tone = ToastTone::Info;
  // rml (esc() for server text)
  std::string text;
  std::string icon;
  std::string actionId;
  std::string actionLabel;
  int durationMs = 0;
};

// a «now playing» notice: what began to play, in a corner for a few seconds; never focusable
struct NowPlayingSpec {
  // an art ref (img:<id> or a stand-in), "" for none
  std::string art;
  // rml (esc() for server text)
  std::string title;
  std::string line;
  std::string source;
  int durationMs = 5000;
};

struct DialogSpec {
  std::string title;
  std::string body;
  std::string confirmLabel;
  std::string cancelLabel;
  // the confirm is the primary (right); focus starts on the safe choice, cancel
  std::function<void()> onConfirm;
  std::function<void()> onCancel;
};

// what activated: OK on an element with data-action (or a long-press entry, or a committed slider)
struct Activation {
  Rml::Element* element = nullptr;
  std::string action;
  // a secondary action from the long-press sheet
  bool secondary = false;
  // a slider's committed value, 600 ms after its last key; a toggle's new state
  std::optional<double> number;
  std::optional<bool> on;
};

// the ui core: rmlui over A's RenderBackend (its own render target, redrawn only when something changed and
// composited every frame), afterglow's documents, the focus manager, the remote's keys (press, long press with the
// hold arc, held keys), overlays (toasts, dialog, sheets, the actions sheet), sliders and toggles, the shared
// skeleton pulse and spinner frames, the ghost hand-off, reduced motion and the live language switch. main thread
// only, gl current in attach, detach and render. one at a time per process (rmlui's state is global)
class Ui {
 public:
  explicit Ui(i18n::Translator& translator, std::vector<std::string> assetRoots = defaultAssetRoots());
  ~Ui();
  Ui(const Ui&) = delete;
  Ui& operator=(const Ui&) = delete;

  // rmlui, the fonts, the context and the overlay document; false and a token in error when one fails
  bool attach(render::RenderBackend& backend, render::Size screen, double dpRatio, std::string* error);
  void detach();
  [[nodiscard]] bool attached() const { return context_ != nullptr; }
  void resize(render::Size screen, double dpRatio);

  // the page: its body's rml (a builder, run again when the language changes) and the classes of its body
  using Builder = std::function<std::string()>;
  void setPage(Builder builder, std::string bodyClasses = "page");
  // after a page (re)load: hook to fill dynamic parts and bind listeners
  void onPageLoaded(std::function<void(Rml::ElementDocument&)> hook) { pageLoaded_ = std::move(hook); }
  [[nodiscard]] Rml::ElementDocument* page() const { return page_; }
  [[nodiscard]] Rml::ElementDocument* overlay() const { return overlay_; }
  [[nodiscard]] Rml::Context* context() const { return context_; }

  bool key(const platform::KeyEvent& event);
  void onActivate(std::function<void(const Activation&)> handler) { activate_ = std::move(handler); }
  // BACK with no level open: true when the page used it
  void onBack(std::function<bool()> handler) { back_ = std::move(handler); }

  void update(std::int64_t nowMs);
  // ms from nowMs until update has something due (a timer, a transition, a spinner's next frame, a slider's commit,
  // a hold arc's step, a focus nudge, images still loading): 0 for now, -1 for nothing until an input comes. the
  // main loop sleeps this long at most, so nothing waits on its idle timeout
  [[nodiscard]] std::int64_t nextUpdateMs(std::int64_t nowMs) const;
  // a frame is owed: the layer changed, or a cross-fade is due or running (it composites only: the layer is not drawn
  // again for it)
  [[nodiscard]] bool dirty() const { return dirty_ || fadePending_ || fade_.startMs >= 0; }
  void markDirty() { dirty_ = true; }
  [[nodiscard]] bool ready() const { return page_ != nullptr && laidOut_; }
  // redraws the ui's layer if dirty, then composites it premultiplied over what is bound into dst; direct, and with
  // dst the whole screen, it draws straight into the frame instead (every frame: nothing is kept between frames)
  void render(render::RenderBackend& backend, render::Rect dst);
  // direct rendering (the app's, when no video draws under the ui in gl, so every frame is a ui change anyway);
  // off, the layer keeps the last frame, and a video frame composites it without rmlui (tests read layer() back)
  void setDirect(bool on) { direct_ = on; }
  // the layer as last drawn (tests and the gallery read it back)
  [[nodiscard]] const render::RenderTarget& layer() const { return target_; }

  Focus& focus() { return focus_; }
  i18n::Translator& translator() { return translator_; }
  [[nodiscard]] std::int64_t nowMs() const { return nowMs_; }
  [[nodiscard]] float dp() const { return dp_; }
  [[nodiscard]] const RmlSystem& system() const { return *system_; }
  [[nodiscard]] const RmlRenderer* renderer() const { return renderer_.get(); }
  RmlFiles& files() { return *files_; }

  // runs fn at nowMs + delayMs (0: on the next update, after rmlui laid out what changed)
  void after(std::int64_t delayMs, std::function<void()> fn);

  void toast(const ToastSpec& t);
  void clearToasts();
  // slides a «now playing» notice in at the top-left corner, replacing one still showing; it never takes focus
  void nowPlaying(const NowPlayingSpec& n);
  void dialog(DialogSpec d);
  // a bottom sheet with a title and a body; BACK closes it and focus returns to the opener. variant "right" is the
  // 720 dp right sheet (the source switcher, design r2 section 18.2)
  void sheet(const std::string& title, const std::string& bodyRml, const std::string& variant = {});
  // called when the top level closes by BACK or closeLevel (not when a sheet's own entry closed it)
  void onLevelClosed(std::function<void()> fn);
  // the long-press actions sheet: the item's name, its actions, and «Ассистент» last
  void actionsSheet(Rml::Element* item, const std::vector<SecondaryAction>& actions);
  // closes the top level (dialog or sheet); false when none is open
  bool closeLevel();
  [[nodiscard]] bool levelOpen() const { return !levels_.empty(); }

  // the last drawn frame fades out (kMotionPageMs, eased in and out) over what comes next. setPage does it on its
  // own; a route calls it before it swaps a part in place (a skeleton for its content). nothing is shown until the
  // new page is laid out, so no half-built frame is ever presented. costs one full-screen quad a frame while it runs
  void crossfade();
  // rmlui into the layer (the target the composite and the cross-fade read)
  void renderLayer(render::RenderBackend& backend);
  void setReducedMotion(bool on);
  [[nodiscard]] bool reducedMotion() const { return reduced_; }
  // a presented frame's interval: under 45 fps for 5 s turns reduced motion on, once, with a toast
  void frameInterval(double ms);

  // what a page draws under the ui's layer each frame, in the screen's pixels (the visualizer, ken burns)
  void setUnderlay(std::function<void(render::RenderBackend&, render::Rect)> fn) { underlay_ = std::move(fn); }
  // a picture under the gl surface (airplay mirroring on android's video SurfaceView): the page and its underlay
  // step aside so only the overlay layers (badge, toasts, drawer) draw over it; a page loaded meanwhile stays hidden
  void setSeeThrough(bool on);
  [[nodiscard]] bool seeThrough() const { return seeThrough_; }

  // network images (the server's image proxy): elements with .netimg and data-img get the cache's pixels once
  // decoded at their size; nullptr turns it off. the cache outlives the ui's use of it
  void setImages(images::ImageCache* cache);
  [[nodiscard]] images::ImageBinder* imageBinder() const { return binder_.get(); }
  [[nodiscard]] images::ImageCache* images() const { return images_; }

  // the clock tile's model
  struct ClockModel {
    Rml::String time = "21:40";
    Rml::String date;
    Rml::String temp;
    Rml::String weatherIcon;
  };
  void setClock(const ClockModel& clock);

  // the language switch: every open document is built again (rmlui translates text when it loads)
  void reload();

  // hooks for the layers and routes over the page (wave 2's drawer, panels, system screens). each returns an id for
  // remove(). key filters run newest first before anything else: true consumes the key
  using KeyFilter = std::function<bool(const platform::KeyEvent&)>;
  int addKeyFilter(KeyFilter filter);
  // runs on every update, before rmlui lays out
  using Ticker = std::function<void(std::int64_t nowMs)>;
  int addTicker(Ticker ticker);
  // an activation goes to these first (newest first): true when one took it
  using ActivateFilter = std::function<bool(const Activation&)>;
  int addActivateFilter(ActivateFilter filter);
  int addFocusListener(Focus::Listener listener);
  void remove(int hookId);

  // a document of its own above the page and below the overlay (the drawer, a panel, the pin screen), ordered by z
  // and built again with the page's language; a focus scope pushed on one of its elements survives a page change
  Rml::ElementDocument* addDocument(const std::string& name, int z, Builder builder,
                                    std::function<void(Rml::ElementDocument&)> loaded = {});
  void removeDocument(const std::string& name);
  [[nodiscard]] Rml::ElementDocument* documentNamed(const std::string& name) const;

  // design r2 section 10: a toast with a button takes focus only when the user presses down into it; true when the
  // newest such toast took it. BACK or up gives focus back
  bool enterToast();
  void leaveToast();
  [[nodiscard]] bool toastFocused() const { return toastScope_; }

 private:
  struct Level {
    Rml::ObserverPtr<Rml::Element> layer;
    std::function<void()> onClose;
  };
  struct Timer {
    std::int64_t atMs;
    std::uint64_t seq;
    std::function<void()> fn;
  };
  struct Hold {
    Rml::ObserverPtr<Rml::Element> element;
    std::int64_t downMs = 0;
    bool consumed = false;
    bool hasActions = false;
    int frame = -1;
  };
  struct Hook {
    int id;
    KeyFilter key;
    Ticker tick;
    ActivateFilter activate;
    Focus::Listener focus;
  };
  struct DocLayer {
    std::string name;
    int z = 0;
    Builder builder;
    std::function<void(Rml::ElementDocument&)> loaded;
    Rml::ElementDocument* doc = nullptr;
  };
  struct SliderEdit {
    Rml::ObserverPtr<Rml::Element> element;
    std::int64_t lastKeyMs = 0;
  };

  std::string document(const std::string& body, const std::string& bodyClasses) const;
  void loadOverlay();
  void loadPage();
  void openLevel(Rml::Element* layer, std::function<void()> onClose);
  void press(Rml::Element* e);
  void release(bool activate);
  void activate(Rml::Element* e);
  bool sliderKey(Rml::Element* e, Dir dir);
  void setSlider(Rml::Element* e, double value);
  void tickSprites();
  // dirty when a document changed since the last look
  void noteChanges();
  void tickHold();
  void tickSliders();
  void fire(const Activation& a);
  void loadDocument(DocLayer& d);
  void restack();
  bool foreign(Rml::Element* e) const;

  i18n::Translator& translator_;
  std::unique_ptr<RmlFiles> files_;
  std::unique_ptr<RmlSystem> system_;
  std::unique_ptr<RmlRenderer> renderer_;
  render::RenderBackend* backend_ = nullptr;
  render::RenderTarget target_;
  // the cross-fade's outgoing frame: a copy of the layer as last drawn
  render::RenderTarget prev_;
  // the layer holds a drawn frame (a snapshot of a never-drawn target would flash garbage)
  bool drawn_ = false;
  bool fadePending_ = false;
  // when the new frame could first show (update's clock), -1 until then
  std::int64_t fadeReadyMs_ = -1;
  motion::Fade fade_;
  render::Size screen_;
  float dp_ = 1.0f;
  Rml::Context* context_ = nullptr;
  Rml::ElementDocument* page_ = nullptr;
  Rml::ElementDocument* overlay_ = nullptr;
  Rml::DataModelHandle clockModel_;
  ClockModel clock_;
  Builder builder_;
  std::string bodyClasses_ = "page";
  std::function<void(Rml::ElementDocument&)> pageLoaded_;
  std::function<void(const Activation&)> activate_;
  std::function<bool()> back_;
  Focus focus_;
  std::vector<Level> levels_;
  std::vector<Timer> timers_;
  std::uint64_t timerSeq_ = 0;
  std::optional<Hold> hold_;
  std::vector<SliderEdit> sliders_;
  std::map<int, int> repeats_;
  bool held_ = false;
  std::int64_t nowMs_ = 0;
  bool dirty_ = true;
  bool laidOut_ = false;
  bool reduced_ = false;
  bool autoReduced_ = false;
  std::int64_t slowSinceMs_ = -1;
  int spinnerFrame_ = -1;
  int skeletonStep_ = -1;
  // a visible spinner or skeleton block at the last sprite step: its next frame is due
  bool spinnersShown_ = false;
  bool skeletonsShown_ = false;
  int toastSeq_ = 0;
  std::vector<Hook> hooks_;
  int hookSeq_ = 0;
  std::vector<DocLayer> docs_;
  bool toastScope_ = false;
  std::vector<std::string> fontBytes_;
  std::function<void(render::RenderBackend&, render::Rect)> underlay_;
  bool seeThrough_ = false;
  bool direct_ = false;
  // the layer holds the ui as it is now (false after direct frames)
  bool targetFresh_ = false;
  images::ImageCache* images_ = nullptr;
  std::unique_ptr<images::ImageBinder> binder_;
  std::int64_t lastScanMs_ = -1;
  // a change came while the scan was throttled: one more scan is due at lastScanMs_ + 100
  bool scanOwed_ = false;
  // the documents' change count when update last looked
  std::uint64_t seenChanges_ = 0;
  // the documents' structure count at the last image scan
  std::uint64_t scannedStructure_ = 0;
  // the translator outlives no ui: its listener holds this weakly
  std::shared_ptr<Ui*> alive_;
};

}  // namespace cadence::screen::ui
