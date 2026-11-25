#include "app/routes/core/router.h"

#include "app/convert.h"
#include "app/routes/core/view.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>

namespace cadence::screen::routes {

namespace {

std::string refOf(Rml::Element* e) {
  for (auto* p = e; p != nullptr; p = p->GetParentNode()) {
    const auto ref = p->GetAttribute<Rml::String>("data-ref", "");
    if (!ref.empty()) return ref;
  }
  return {};
}

}  // namespace

Route::~Route() {
  *alive_ = false;
  for (auto& id : calls_) s_.rpc.cancel(id);
}

void Route::afterLayout(std::function<void()> fn) {
  auto alive = alive_;
  s_.ui.after(0, [this, alive, fn = std::move(fn)] {
    if (*alive && current()) fn();
  });
}

void Route::focusLater(const std::string& id) {
  auto alive = alive_;
  s_.ui.after(0, [this, alive, id] {
    if (!*alive || !current()) return;
    if (auto* e = page()->GetElementById(id)) s_.ui.focus().focus(e);
  });
}

std::string Route::call(const std::string& method, const Json& args, RpcClient::Handlers h, bool subscribe) {
  // ids of finished calls stay until trimmed: cancelling one is a no-op
  auto id = s_.rpc.call(method, args, std::move(h), subscribe);
  if (!id.empty()) calls_.push_back(id);
  if (calls_.size() > 64) calls_.erase(calls_.begin(), calls_.begin() + 32);
  return id;
}

void Route::cancel(std::string& id) {
  if (id.empty()) return;
  s_.rpc.cancel(id);
  calls_.erase(std::remove(calls_.begin(), calls_.end(), id), calls_.end());
  id.clear();
}

void Route::changed() { s_.router.contextChanged(); }

bool Route::current() const { return s_.router.top() == this; }

Router::Router() : Router(Options{}) {}

Router::Router(Options options) : options_(std::move(options)) {}

Router::~Router() { detach(); }

void Router::attach(ui::Ui& ui, app::Outbox& outbox) {
  ui_ = &ui;
  outbox_ = &outbox;
  const auto send = [this](const cadence_screen_v1_Envelope& e) { return outbox_ != nullptr && outbox_->send(e); };
  rpc_ = std::make_unique<RpcClient>(send);
  publisher_ = std::make_unique<ContextPublisher>(send);
  if (options_.images != nullptr) {
    images_ = options_.images;
  } else {
    fetcher_ = options_.fetcher ? options_.fetcher : std::make_shared<ui::images::HttpFetcher>();
    ownImages_ = std::make_unique<ui::images::ImageCache>(fetcher_);
    images_ = ownImages_.get();
  }
  ui.setImages(images_);
  services_ = std::make_unique<Services>(Services{ui, *rpc_, *this, sourceNames_});
  for (auto& f : layerFactories_) {
    if (auto l = f(*services_, outbox)) layers_.push_back(std::move(l));
  }
  // the services read the remote first (the player's osd), then the focus manager
  keyHook_ = ui.addKeyFilter([this](const platform::KeyEvent& e) {
    for (auto& s : services2_)
      if (s->key(e)) return true;
    return false;
  });
  for (auto& s : services2_) s->attach(*this, ui, outbox);
  if (options_.openRoot && stack_.empty()) open(options_.root);
}

void Router::detach() {
  // abandoned (the ui went first): the services forget it before any route goes
  if (ui_ == nullptr)
    for (auto& s : services2_) s->abandon();
  layers_.clear();
  stack_.clear();
  graveyard_.clear();
  for (auto& s : services2_) s->detach();
  if (ui_ != nullptr && keyHook_ != 0) ui_->remove(keyHook_);
  keyHook_ = 0;
  if (ui_ != nullptr) ui_->setImages(nullptr);
  services_.reset();
  publisher_.reset();
  rpc_.reset();
  ownImages_.reset();
  fetcher_.reset();
  images_ = nullptr;
  ui_ = nullptr;
  outbox_ = nullptr;
  lastFocus_ = nullptr;
}

Route* Router::find(cadence_v2_ScreenRoute id) const {
  for (const auto& e : stack_) {
    if (e.route->id() == id) return e.route.get();
  }
  return nullptr;
}

void Router::remember() {
  if (stack_.empty() || ui_ == nullptr || ui_->page() == nullptr) return;
  auto& e = stack_.back();
  e.openerId.clear();
  if (auto* f = ui_->focus().current()) e.openerId = f->GetId();
  e.rowMemory.clear();
  Rml::ElementList rows;
  ui_->page()->QuerySelectorAll(rows, ".row");
  for (auto* r : rows) {
    const int last = r->GetAttribute<int>("data-last", -1);
    if (!r->GetId().empty() && last >= 0) e.rowMemory[r->GetId()] = last;
  }
}

bool Router::push(std::unique_ptr<Route> r) {
  if (!r || ui_ == nullptr) return false;
  remember();
  stack_.push_back(Entry{std::move(r), {}, {}, false});
  show(false);
  return true;
}

bool Router::open(cadence_v2_ScreenRoute id, const RouteArgs& args) {
  const auto it = factories_.find(id);
  if (it == factories_.end() || services_ == nullptr) return false;
  return push(it->second(*services_, args));
}

bool Router::replace(cadence_v2_ScreenRoute id, const RouteArgs& args) {
  const auto it = factories_.find(id);
  if (it == factories_.end() || services_ == nullptr || stack_.empty()) return false;
  auto r = it->second(*services_, args);
  if (!r) return false;
  graveyard_.push_back(std::move(stack_.back().route));
  stack_.back() = Entry{std::move(r), {}, {}, false};
  show(false);
  return true;
}

bool Router::pop() {
  if (stack_.size() <= 1) return false;
  graveyard_.push_back(std::move(stack_.back().route));
  stack_.pop_back();
  show(true);
  return true;
}

bool Router::popTo(cadence_v2_ScreenRoute id) {
  const auto it = std::find_if(stack_.rbegin(), stack_.rend(), [&](const Entry& e) { return e.route->id() == id; });
  if (it == stack_.rend()) return false;
  const auto keep = static_cast<std::size_t>(stack_.rend() - it);
  if (keep == stack_.size()) return true;
  while (stack_.size() > keep) {
    graveyard_.push_back(std::move(stack_.back().route));
    stack_.pop_back();
  }
  show(true);
  return true;
}

void Router::show(bool restoring) {
  if (ui_ == nullptr || stack_.empty()) return;
  restoring_ = restoring;
  auto* r = stack_.back().route.get();
  lastFocus_ = nullptr;
  contextDirty_ = true;
  ui_->setPage([this] { return stack_.empty() ? std::string() : stack_.back().route->body(); }, r->bodyClasses());
}

void Router::loaded(Rml::ElementDocument& doc) {
  if (stack_.empty()) return;
  auto& e = stack_.back();
  const bool restoring = restoring_;
  restoring_ = false;
  if (restoring) {
    for (const auto& [id, last] : e.rowMemory) {
      if (auto* row = doc.GetElementById(id)) row->SetAttribute("data-last", last);
    }
  }
  e.route->loaded(doc, restoring);
  if (restoring && !e.openerId.empty() && !e.route->placesOwnFocus()) {
    // after layout, so the opener's row scrolls to it as the viewer left it
    const auto opener = e.openerId;
    ui_->after(0, [this, opener] {
      if (ui_ == nullptr || ui_->page() == nullptr) return;
      if (auto* el = ui_->page()->GetElementById(opener)) ui_->focus().focus(el);
    });
  }
  if (!e.started) {
    e.started = true;
    e.route->start();
  }
  contextDirty_ = true;
}

bool Router::activate(const ui::Activation& a) {
  auto* r = top();
  return r != nullptr && r->activate(a);
}

bool Router::back() {
  auto* r = top();
  if (r == nullptr) return false;
  if (r->back()) return true;
  return pop();
}

void Router::message(const cadence_screen_v1_Envelope& m) {
  for (auto& s : services2_) s->message(m);
  for (auto& l : layers_) {
    if (l->message(m)) return;
  }
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_rpc_response_tag:
      if (rpc_) rpc_->response(m.body.rpc_response);
      break;
    case cadence_screen_v1_Envelope_rpc_event_tag:
      if (rpc_) rpc_->event(m.body.rpc_event);
      break;
    case cadence_screen_v1_Envelope_link_status_tag: {
      const auto& l = m.body.link_status;
      if (fetcher_ && l.server_url != nullptr && l.server_url[0] != 0) fetcher_->setServer(l.server_url);
      const bool offline = l.state == cadence_screen_v1_LinkState_LINK_STATE_OFFLINE;
      if (offline && !offline_) {
        offline_ = true;
        if (rpc_) rpc_->failAll();
      } else if (!offline && l.state == cadence_screen_v1_LinkState_LINK_STATE_ONLINE) {
        offline_ = false;
        // the server is reachable (again): what failed for want of it asks again
        if (images_ != nullptr) images_->forgetFailures();
        if (ui_ != nullptr && ui_->page() != nullptr) {
          Rml::ElementList imgs;
          ui_->page()->QuerySelectorAll(imgs, ".netimg");
          for (auto* i : imgs) i->RemoveAttribute("data-img-failed");
        }
        for (auto& e : stack_) e.route->online();
      }
      break;
    }
    case cadence_screen_v1_Envelope_ready_tag:
      // cadenced is there now: calls made before it (home's first rows at start) are asked again
      for (auto& e : stack_) e.route->online();
      break;
    case cadence_screen_v1_Envelope_route_tag: {
      const auto& r = m.body.route;
      bool ok = false;
      bool taken = false;
      for (auto& s : services2_) taken = taken || s->route(r);
      if (taken) {
        ok = true;
      } else if (r.back) {
        ok = pop();
      } else {
        RouteArgs args;
        args.ref = r.ref;
        args.query = r.query != nullptr ? r.query : "";
        args.episode = r.episode;
        // home is the root: going there pops everything above it
        ok = r.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME && !stack_.empty() &&
                     stack_.front().route->id() == r.route
                 ? (popTo(r.route), true)
                 : open(r.route, args);
      }
      if (outbox_ != nullptr && r.request_id[0] != '\0') {
        auto out = app::commandResultEnvelope(r.request_id, ok ? "" : "unsupported");
        outbox_->send(out.get());
      }
      break;
    }
    case cadence_screen_v1_Envelope_assistant_turn_tag: {
      // a turn's start: the agent reads the screen as it is now
      const std::string turn = m.body.assistant_turn.turn_id;
      if (turn != turnId_ && publisher_ && !stack_.empty()) {
        turnId_ = turn;
        publisher_->set(computeContext(), ui_ != nullptr ? ui_->nowMs() : 0);
        publisher_->flush();
      }
      break;
    }
    case cadence_screen_v1_Envelope_voice_state_tag: {
      const auto s = m.body.voice_state.state;
      if ((s == cadence_v2_AssistantState_ASSISTANT_STATE_INVOKED || s == cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING) &&
          publisher_ && !stack_.empty()) {
        publisher_->set(computeContext(), ui_ != nullptr ? ui_->nowMs() : 0);
        publisher_->flush();
      }
      break;
    }
    default:
      break;
  }
}

void Router::playerState(const player::PlayerState& state) {
  for (auto& s : services2_) s->playerState(state);
  if (auto* r = top()) r->playerState(state);
}

void Router::addService(std::unique_ptr<Service> service) {
  services2_.push_back(std::move(service));
  if (ui_ != nullptr && outbox_ != nullptr) services2_.back()->attach(*this, *ui_, *outbox_);
}

void Router::subtitle(const std::string& text) {
  for (auto& s : services2_) s->subtitle(text);
}

app::VizFeed* Router::vizFeed() {
  for (auto& s : services2_)
    if (auto* f = s->vizFeed()) return f;
  return nullptr;
}

ScreenContextData Router::computeContext() const {
  ScreenContextData c;
  auto* r = top();
  if (r == nullptr || ui_ == nullptr) return c;
  c.route = r->id();
  r->context(c);
  for (const auto& l : layers_) l->context(c);
  auto* page = ui_->page();
  if (page == nullptr) return c;
  if (auto* f = ui_->focus().current()) c.focusedRef = refOf(f);
  const auto* ctx = ui_->context();
  const float w = ctx != nullptr ? static_cast<float>(ctx->GetDimensions().x) : 0;
  const float h = ctx != nullptr ? static_cast<float>(ctx->GetDimensions().y) : 0;
  Rml::ElementList list;
  page->QuerySelectorAll(list, ".has-ref");
  for (auto* e : list) {
    if (c.visibleRefs.size() >= kMaxVisibleRefs) break;
    if (!e->IsVisible(true)) continue;
    const auto rect = ui_->focus().rect(e);
    // on screen: at least half of it inside the canvas
    if (rect.cx() < 0 || rect.cx() > w || rect.cy() < 0 || rect.cy() > h) continue;
    const auto ref = e->GetAttribute<Rml::String>("data-ref", "");
    if (ref.empty() || std::find(c.visibleRefs.begin(), c.visibleRefs.end(), ref) != c.visibleRefs.end()) continue;
    c.visibleRefs.push_back(ref);
  }
  return c;
}

void Router::update(std::int64_t nowMs) {
  if (rpc_) rpc_->expire(nowMs);
  graveyard_.clear();
  if (ui_ == nullptr || !publisher_) return;
  for (auto& s : services2_) s->update(nowMs);
  if (auto* r = top()) r->update(nowMs);
  auto* f = ui_->focus().current();
  if (f != lastFocus_) {
    lastFocus_ = f;
    contextDirty_ = true;
  }
  if (contextDirty_ && ui_->ready()) {
    contextDirty_ = false;
    publisher_->set(computeContext(), nowMs);
  }
  publisher_->update(nowMs);
}

app::UiStatus Router::status() const {
  app::UiStatus s;
  if (auto* r = top()) s.route = r->id();
  for (const auto& l : layers_) l->status(s);
  return s;
}

void Router::refresh() {
  if (ui_ == nullptr || stack_.empty()) return;
  std::string focused;
  if (auto* f = ui_->focus().current(); f != nullptr && ui_->page() != nullptr && ui_->page()->Contains(f)) focused = f->GetId();
  show(false);
  if (focused.empty()) return;
  ui_->after(0, [this, focused] {
    if (ui_ == nullptr || ui_->page() == nullptr) return;
    if (auto* e = ui_->page()->GetElementById(focused)) ui_->focus().focus(e);
  });
}

std::optional<BackgroundPlay> Router::background() const {
  for (const auto& s : services2_) {
    if (auto b = s->background()) return b;
  }
  return std::nullopt;
}

bool Router::foreground() {
  for (auto& s : services2_) {
    if (s->foreground()) return true;
  }
  return false;
}

std::unique_ptr<app::UiLayer> makeRoutedUi() { return makeRoutedUi(Router::Options{}); }

std::unique_ptr<app::UiLayer> makeRoutedUi(Router::Options options) {
  auto shell = std::make_unique<ui::ShellLayer>();
  auto router = std::make_unique<Router>(std::move(options));
  registerBrowseRoutes(*router);
  registerSystemRoutes(*router);
  registerPlaybackRoutes(*router);
  shell->setPages(std::move(router));
  return shell;
}

}  // namespace cadence::screen::routes
