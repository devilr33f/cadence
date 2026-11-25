#pragma once

#include "app/ui-layer.h"
#include "i18n/translator.h"
#include "ui/cards/host.h"
#include "ui/core/ui.h"
#include "ui/core/volume-overlay.h"

#include <memory>

namespace cadence::screen::ui {

// the screens a ShellLayer shows instead of its own page (wave 2's routes, screen/app/routes): they own the ui's page
// once attached. the shell keeps its card host, toasts and clock, and hands them what it does not use itself
class Pages {
 public:
  virtual ~Pages() = default;
  virtual void attach(Ui& ui, app::Outbox& outbox) = 0;
  virtual void detach() {}
  // after every page load
  virtual void loaded(Rml::ElementDocument&) {}
  // true when the page used it
  virtual bool activate(const Activation& a) = 0;
  // BACK with no level open: true when the page used it
  virtual bool back() = 0;
  virtual void message(const cadence_screen_v1_Envelope& m) = 0;
  virtual void playerState(const player::PlayerState&) {}
  virtual void update(std::int64_t nowMs) = 0;
  [[nodiscard]] virtual app::UiStatus status() const = 0;
  // the local player (the osd's transport, ambient's clips), the subtitle line now playing, the tap's feed
  virtual void attachPlayer(app::PlayerControl* player) { (void)player; }
  virtual void subtitle(const std::string& text) { (void)text; }
  virtual app::VizFeed* vizFeed() { return nullptr; }
};

// a Toast as the ui shows it: a typed action (screen/v1 ToastAction) becomes the button's activation (toast:task:<ref>
// for a task's panel, toast:<id> for an rpc toast.action), else the untyped action_id of an older cadenced
ToastSpec toastSpecOf(const cadence_screen_v1_Toast& toast);

// wave 1's UiLayer on stream b's library: the ink page with the clock tile, the link's state, the cards the link
// sends (newest at the bottom, at most 6) and its toasts. the screens of wave 2 (home, the player's osd, the
// assistant drawer) replace the page; the layer, its library and the card host stay
class ShellLayer final : public app::UiLayer {
 public:
  ShellLayer();
  ~ShellLayer() override;

  void attach(render::RenderBackend& backend, const app::UiGeometry& geometry, app::Outbox& outbox) override;
  void detach() override;
  void resize(const app::UiGeometry& geometry) override;
  bool key(const platform::KeyEvent& event) override;
  void message(const cadence_screen_v1_Envelope& envelope) override;
  void playerState(const player::PlayerState& state) override;
  void update(std::int64_t nowMs) override;
  [[nodiscard]] bool dirty() const override;
  [[nodiscard]] std::int64_t nextUpdateMs(std::int64_t nowMs) const override;
  [[nodiscard]] bool ready() const override;
  void render(render::RenderBackend& backend) override;
  void setVideoUnder(bool video) override { ui_->setDirect(!video); }
  [[nodiscard]] app::UiStatus status() const override { return pages_ ? pages_->status() : app::UiStatus{}; }
  [[nodiscard]] std::vector<std::uint32_t> cardVersions() const override { return cards::versions(); }
  [[nodiscard]] std::vector<std::string> cardFeatures() const override { return cards::features(); }

  void attachPlayer(app::PlayerControl* player) override {
    if (pages_) pages_->attachPlayer(player);
  }
  void subtitle(const std::string& text) override {
    if (pages_) pages_->subtitle(text);
  }
  app::VizFeed* vizFeed() override { return pages_ ? pages_->vizFeed() : nullptr; }

  [[nodiscard]] Ui& ui() { return *ui_; }
  // before attach: the screens take the page over
  void setPages(std::unique_ptr<Pages> pages) { pages_ = std::move(pages); }

 private:
  std::string page() const;
  void tickClock();

  i18n::Translator translator_;
  std::unique_ptr<Ui> ui_;
  std::unique_ptr<cards::CardHost> cards_;
  std::unique_ptr<Pages> pages_;
  // over every page, from attach to detach
  std::unique_ptr<VolumeOverlay> volume_;
  app::Outbox* outbox_ = nullptr;
  app::UiGeometry geometry_;
  bool failed_ = false;
  bool offline_ = false;
  std::int64_t clockMinute_ = -1;
};

std::unique_ptr<app::UiLayer> makeUiLayer();

}  // namespace cadence::screen::ui
