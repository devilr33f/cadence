#pragma once

#include "app/routes/core/context.h"
#include "app/routes/core/rpc.h"
#include "app/ui-layer.h"
#include "ui/core/shell.h"
#include "ui/core/ui.h"
#include "ui/images/http-fetcher.h"
#include "ui/images/image-cache.h"

#include <functional>
#include <map>
#include <memory>
#include <optional>
#include <string>
#include <vector>

// the screens' navigation (wave 2): a stack of routes, each one page of the ui, with the screen context the agent
// reads. streams register their routes by ScreenRoute (registerBrowseRoutes is i1's: home, search, title,
// episodes, picker, resolving); cadenced's Route message opens one, BACK pops one. a route keeps its model while
// another sits above it, so BACK rebuilds it as it was and gives focus back to the element that opened the next
namespace cadence::screen::routes {

class Router;

// what opens a route: the item and the page's own state (a picker's options, a resolve's choices)
struct RouteArgs {
  std::string ref;
  std::string query;
  std::string episode;
  Json data = Json::object();
};

// what a route reaches: the ui, the server's methods, the router itself, playback
struct Services {
  ui::Ui& ui;
  RpcClient& rpc;
  Router& router;
  // the source names the last search named (provider/source -> name), for a variant's chip
  std::map<std::string, std::string>& sourceNames;
};

class Route {
 public:
  explicit Route(Services& s) : s_(s) {}
  virtual ~Route();
  Route(const Route&) = delete;
  Route& operator=(const Route&) = delete;

  [[nodiscard]] virtual cadence_v2_ScreenRoute id() const = 0;
  // the page's rml from the route's model: run on every (re)load, so it draws whatever has arrived so far
  virtual std::string body() = 0;
  [[nodiscard]] virtual std::string bodyClasses() const { return "page"; }
  // after the page loaded: initial focus (when focus is not being given back), dynamic parts
  virtual void loaded(Rml::ElementDocument& doc, bool restoring) {
    (void)doc;
    (void)restoring;
  }
  // once, when it is first shown: its requests start here
  virtual void start() {}
  virtual bool activate(const ui::Activation& a) {
    (void)a;
    return false;
  }
  // BACK: true when the route used it; false lets the router pop it
  virtual bool back() { return false; }
  virtual void update(std::int64_t nowMs) { (void)nowMs; }
  // the route's part of the screen context (page ref, selection, query); the router adds focus and visible refs
  virtual void context(ScreenContextData& c) const { (void)c; }
  virtual void playerState(const player::PlayerState& state) { (void)state; }
  // the link came back (offline -> online): a route that failed may ask again
  virtual void online() {}
  // back on top, the route placed focus itself in loaded(): the router does not give it back to the opener
  [[nodiscard]] virtual bool placesOwnFocus() const { return false; }

 protected:
  // a call the route owns: cancelled when the route goes
  std::string call(const std::string& method, const Json& args, RpcClient::Handlers h, bool subscribe = false);
  void cancel(std::string& id);
  // the route's model changed in a way the screen context sees
  void changed();
  // focuses the element once rmlui laid out what just changed (a scroll to it needs the new layout)
  void focusLater(const std::string& id);
  // fn once rmlui laid out what just changed, if the route is still there and on top
  void afterLayout(std::function<void()> fn);
  // the ui's page while this route is on top, else null: a route under another one keeps its model and touches nothing
  // on screen (its answers still arrive)
  [[nodiscard]] Rml::ElementDocument* page() const { return current() ? s_.ui.page() : nullptr; }
  [[nodiscard]] bool current() const;

  Services& s_;

 private:
  std::vector<std::string> calls_;
  std::shared_ptr<bool> alive_ = std::make_shared<bool>(true);
};

// a layer over the routes (i3's assistant drawer, tasks panels, airplay): documents of its own (Ui::addDocument),
// made at attach with the router's services and the outbox, gone before the routes. it sees each message before the
// routes (true: it was the layer's alone) and adds to the status and the screen context (drawer_open is the drawer's)
class Layer {
 public:
  virtual ~Layer() = default;
  virtual bool message(const cadence_screen_v1_Envelope& m) {
    (void)m;
    return false;
  }
  virtual void status(app::UiStatus& s) const { (void)s; }
  virtual void context(ScreenContextData& c) const { (void)c; }
};

// playback that goes on behind the routes (BACK from its page, a tv app's picture under its ui): home offers the way
// back, and the remote's transport keys still reach it
struct BackgroundPlay {
  std::string sessionId;
  std::string itemRef;
  // rml (escaped)
  std::string title;
  std::string artist;
  // "img:<id>", "gen:art:<seed>" or empty
  std::string art;
  // audio has no picture behind the page: home shows its cover as the backdrop and a compact now-playing entry
  bool audio = false;
  bool paused = false;
};

// a stream's part that lives beside the stack rather than in it (stream i2's playback: the session, idle and
// ambient, the tap's feed): it sees every message, player state and frame, and the remote's keys before the ui
class Service {
 public:
  virtual ~Service() = default;
  virtual void attach(Router& router, ui::Ui& ui, app::Outbox& outbox) = 0;
  virtual void detach() {}
  // the ui is gone already (Router::abandon): touch nothing of it
  virtual void abandon() {}
  // a key before the focus manager's: true when used
  virtual bool key(const platform::KeyEvent& event) {
    (void)event;
    return false;
  }
  virtual void message(const cadence_screen_v1_Envelope& m) { (void)m; }
  // cadenced's Route for a page the service shows itself (the queue as a sheet): true when it did
  virtual bool route(const cadence_screen_v1_Route& r) {
    (void)r;
    return false;
  }
  virtual void playerState(const player::PlayerState& state) { (void)state; }
  virtual void subtitle(const std::string& text) { (void)text; }
  virtual void update(std::int64_t nowMs) { (void)nowMs; }
  virtual app::VizFeed* vizFeed() { return nullptr; }
  // playback it keeps going behind the routes, if any
  [[nodiscard]] virtual std::optional<BackgroundPlay> background() const { return std::nullopt; }
  // brings that playback back to its own page: true when it did
  virtual bool foreground() { return false; }
};

class Router final : public ui::Pages {
 public:
  using Factory = std::function<std::unique_ptr<Route>(Services&, const RouteArgs&)>;
  using LayerFactory = std::function<std::unique_ptr<Layer>(Services&, app::Outbox&)>;

  struct Options {
    // the image cache to use (the gallery's fake); null makes one over the server's image proxy
    ui::images::ImageCache* images = nullptr;
    // the fetcher of the cache it makes (android's, over the platform's https); null makes a plain one
    std::shared_ptr<ui::images::HttpFetcher> fetcher;
    // the route opened at attach
    cadence_v2_ScreenRoute root = cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME;
    bool openRoot = true;
  };

  Router();
  explicit Router(Options options);
  ~Router() override;

  void registerRoute(cadence_v2_ScreenRoute id, Factory factory) { factories_[id] = std::move(factory); }
  [[nodiscard]] bool has(cadence_v2_ScreenRoute id) const { return factories_.count(id) != 0; }
  // a layer made at each attach, in the order added
  void addLayer(LayerFactory factory) { layerFactories_.push_back(std::move(factory)); }

  // pushes a route over the current one (the focused element is remembered as its opener)
  bool open(cadence_v2_ScreenRoute id, const RouteArgs& args = {});
  // replaces the top route (resolving after a skipped picker)
  bool replace(cadence_v2_ScreenRoute id, const RouteArgs& args = {});
  // pops the top route; false at the root
  bool pop();
  // pops down to the first route of that id (BACK from resolving to the title page); false when there is none
  bool popTo(cadence_v2_ScreenRoute id);
  [[nodiscard]] Route* top() const { return stack_.empty() ? nullptr : stack_.back().route.get(); }
  [[nodiscard]] std::size_t depth() const { return stack_.size(); }
  [[nodiscard]] Route* find(cadence_v2_ScreenRoute id) const;
  // a service beside the stack (attached with the router)
  void addService(std::unique_ptr<Service> service);
  // the local player, null until the app attaches one
  [[nodiscard]] app::PlayerControl* player() const { return player_; }
  // the top route's model changed: rebuild nothing, but compute the context again
  void contextChanged() { contextDirty_ = true; }
  // the top route's page built again from its model, focus kept on the element of the same id
  void refresh();
  // audio or video playing on behind the routes (a service's), and bringing it back to the front
  [[nodiscard]] std::optional<BackgroundPlay> background() const;
  bool foreground();

  // ui::Pages
  void attach(ui::Ui& ui, app::Outbox& outbox) override;
  void detach() override;
  // the ui went first (a test's harness): everything goes without touching it
  void abandon() {
    ui_ = nullptr;
    detach();
  }
  void loaded(Rml::ElementDocument& doc) override;
  bool activate(const ui::Activation& a) override;
  bool back() override;
  void message(const cadence_screen_v1_Envelope& m) override;
  void playerState(const player::PlayerState& state) override;
  void update(std::int64_t nowMs) override;
  [[nodiscard]] app::UiStatus status() const override;
  void attachPlayer(app::PlayerControl* player) override { player_ = player; }
  void subtitle(const std::string& text) override;
  app::VizFeed* vizFeed() override;

  [[nodiscard]] ContextPublisher* publisher() const { return publisher_.get(); }
  [[nodiscard]] RpcClient* rpc() const { return rpc_.get(); }
  [[nodiscard]] ui::images::ImageCache* images() const { return images_; }
  [[nodiscard]] bool offline() const { return offline_; }
  // what the context would say now (tests)
  [[nodiscard]] ScreenContextData computeContext() const;

 private:
  struct Entry {
    std::unique_ptr<Route> route;
    // the element that opened the route above, and each row's remembered index, to give back on return
    std::string openerId;
    std::map<std::string, int> rowMemory;
    bool started = false;
  };

  void show(bool restoring);
  void remember();
  bool push(std::unique_ptr<Route> r);

  Options options_;
  std::map<cadence_v2_ScreenRoute, Factory> factories_;
  std::vector<LayerFactory> layerFactories_;
  std::vector<std::unique_ptr<Layer>> layers_;
  std::vector<std::unique_ptr<Service>> services2_;
  app::PlayerControl* player_ = nullptr;
  // the services' key filter on the ui
  int keyHook_ = 0;
  std::vector<Entry> stack_;
  ui::Ui* ui_ = nullptr;
  app::Outbox* outbox_ = nullptr;
  std::unique_ptr<RpcClient> rpc_;
  std::unique_ptr<ContextPublisher> publisher_;
  std::unique_ptr<Services> services_;
  std::map<std::string, std::string> sourceNames_;
  std::shared_ptr<ui::images::HttpFetcher> fetcher_;
  std::unique_ptr<ui::images::ImageCache> ownImages_;
  ui::images::ImageCache* images_ = nullptr;
  Rml::Element* lastFocus_ = nullptr;
  bool contextDirty_ = true;
  bool restoring_ = false;
  bool offline_ = false;
  std::string turnId_;
  // routes dropped while one of their handlers ran are freed on the next update
  std::vector<std::unique_ptr<Route>> graveyard_;
};

// i1's routes: home, search, title, episodes, picker and resolving
void registerBrowseRoutes(Router& router);
// i3's: enrollment and settings (with room and microphone) as routes; the drawer, tasks and airplay as a layer
void registerSystemRoutes(Router& router);
// i2's: the player, audio mode, ambient (and the queue and panels as their sheets), with the playback service
void registerPlaybackRoutes(Router& router);

// the screen's ui with wave 2's routes (main.cpp): the shell, with the router as its pages
std::unique_ptr<app::UiLayer> makeRoutedUi();
// the same with the platform's options (android-main.cpp: its fetcher)
std::unique_ptr<app::UiLayer> makeRoutedUi(Router::Options options);

}  // namespace cadence::screen::routes
