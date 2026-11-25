#include "app/routes/system/preview/fixture.h"

#include <RmlUi/Core/ElementDocument.h>

#include <cstring>
#include <fstream>
#include <iterator>
#include <map>

namespace cadence::screen::routes::syspreview {

namespace {

std::string read(const std::string& path) {
  std::ifstream in(path, std::ios::binary);
  return {std::istreambuf_iterator<char>(in), {}};
}

std::string fixturePath(const std::string& name) { return std::string(CADENCE_SCREEN_CARD_FIXTURES) + "/" + name; }

std::unique_ptr<Fixture>& current() {
  static std::unique_ptr<Fixture> f;
  return f;
}

}  // namespace

bool FakeLink::send(const cadence_screen_v1_Envelope& e) {
  if (!up) return false;
  Sent s;
  s.body = e.which_body;
  switch (e.which_body) {
    case cadence_screen_v1_Envelope_rpc_request_tag:
      s.id = e.body.rpc_request.id;
      s.method = e.body.rpc_request.method;
      s.json = sv(e.body.rpc_request.json);
      s.subscribe = e.body.rpc_request.subscribe;
      break;
    case cadence_screen_v1_Envelope_rpc_cancel_tag:
      s.id = e.body.rpc_cancel.id;
      s.method = "cancel";
      break;
    case cadence_screen_v1_Envelope_screen_context_tag:
      s.method = "screen_context";
      s.route = e.body.screen_context.route;
      s.drawerOpen = e.body.screen_context.drawer_open;
      s.pageRef = e.body.screen_context.page_ref;
      break;
    case cadence_screen_v1_Envelope_enroll_code_tag:
      s.method = "enroll_code";
      s.code = e.body.enroll_code.code;
      break;
    case cadence_screen_v1_Envelope_server_address_tag:
      s.method = "server_address";
      s.json = sv(e.body.server_address.url);
      break;
    case cadence_screen_v1_Envelope_mirror_stop_tag:
      s.method = "mirror_stop";
      s.id = e.body.mirror_stop.request_id;
      s.json = e.body.mirror_stop.session_id;
      break;
    case cadence_screen_v1_Envelope_mic_choice_tag:
      s.method = "mic_choice";
      s.code = std::to_string(static_cast<int>(e.body.mic_choice.kind));
      s.json = e.body.mic_choice.device_id;
      break;
    default:
      break;
  }
  sent.push_back(std::move(s));
  return true;
}

const Sent* FakeLink::last(std::string_view method) const {
  for (auto it = sent.rbegin(); it != sent.rend(); ++it) {
    if (it->method == method) return &*it;
  }
  return nullptr;
}

int FakeLink::count(std::string_view method) const {
  int n = 0;
  for (const auto& s : sent) n += s.method == method ? 1 : 0;
  return n;
}

namespace {

// no network: every image stays its fill
class NoImages final : public ui::images::Fetcher {
 public:
  bool fetch(const std::string&, std::string&, std::string& code) override {
    code = "offline";
    return false;
  }
};

// home's stand-in: the page a test or a gallery page gives
class HomeStandIn final : public Route {
 public:
  HomeStandIn(Services& s, std::function<std::string()> body) : Route(s), body_(std::move(body)) {}
  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME; }
  std::string body() override { return body_(); }

 private:
  std::function<std::string()> body_;
};

void tick(ui::Ui& ui, Router* r, std::shared_ptr<bool> alive) {
  ui.after(16, [&ui, r, alive] {
    if (!*alive) return;
    r->update(ui.nowMs());
    tick(ui, r, alive);
  });
}

}  // namespace

Fixture::Fixture(ui::Ui& ui, std::function<std::string()> body) : ui_(ui), startMs_(ui.nowMs()) {
  ui::images::CacheOptions co;
  co.inlineDecode = true;
  images_ = std::make_unique<ui::images::ImageCache>(std::make_shared<NoImages>(), co);
  Router::Options o;
  o.images = images_.get();
  o.openRoot = false;
  router_ = std::make_unique<Router>(o);
  router_->registerRoute(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME, [body](Services& s, const RouteArgs&) -> std::unique_ptr<Route> {
    return std::make_unique<HomeStandIn>(s, body);
  });
  registerSystemRoutes(*router_);
  ui_.onActivate([this](const ui::Activation& a) { router_->activate(a); });
  ui_.onBack([this] { return router_->back(); });
  ui_.onPageLoaded([this](Rml::ElementDocument& doc) { router_->loaded(doc); });
  router_->attach(ui_, link_);
  view_ = std::make_unique<SystemView>(*router_);
  // the device clock the countdowns read: the gallery's
  systemLayer()->state().epochMs = [this] { return epoch(); };
  router_->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  tick(ui_, router_.get(), alive_);
}

Fixture::~Fixture() {
  *alive_ = false;
  // a harness that went first took its ui with it: nothing of it may be touched
  if (!images_->bound()) {
    uiAbandoned() = true;
    router_->abandon();
    view_.reset();
    router_.reset();
    images_.reset();
    uiAbandoned() = false;
    return;
  }
  ui_.onPageLoaded(nullptr);
  ui_.onBack(nullptr);
  ui_.onActivate(nullptr);
  view_.reset();
  router_.reset();
  images_.reset();
}

std::int64_t Fixture::epoch() const { return kEpochMs + (ui_.nowMs() - startMs_); }

void Fixture::online(const std::string& room, const std::string& name) {
  static std::string roomS, nameS;
  roomS = room;
  nameS = name;
  cadence_screen_v1_Envelope r = cadence_screen_v1_Envelope_init_zero;
  r.which_body = cadence_screen_v1_Envelope_ready_tag;
  copyTo(r.body.ready.device_id, "tv-1");
  r.body.ready.device_name = nameS.data();
  r.body.ready.room_name = roomS.data();
  copyTo(r.body.ready.language, "ru");
  copyTo(r.body.ready.daemon_version, "2.0.0");
  deliver(r);
  static std::string url = "https://cadence.home.arpa";
  cadence_screen_v1_Envelope l = linkStatus(cadence_screen_v1_LinkState_LINK_STATE_ONLINE);
  l.body.link_status.server_url = url.data();
  copyTo(l.body.link_status.server_version, "2.0.0");
  deliver(l);
}

void Fixture::answer(std::string_view method, std::string_view status, std::string_view json, const cadence_cards_v1_Page* page) {
  const auto* s = link_.last(method);
  if (s == nullptr) return;
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_rpc_response_tag;
  copyTo(e.body.rpc_response.id, s->id);
  copyTo(e.body.rpc_response.status, status);
  std::string j(json);
  e.body.rpc_response.json = j.data();
  e.body.rpc_response.page = const_cast<cadence_cards_v1_Page*>(page);
  deliver(e);
}

void Fixture::event(std::string_view method, const cadence_cards_v1_Page* page, std::string_view json) {
  const auto* s = link_.last(method);
  if (s == nullptr) return;
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_rpc_event_tag;
  copyTo(e.body.rpc_event.id, s->id);
  e.body.rpc_event.seq = 1;
  e.body.rpc_event.page = const_cast<cadence_cards_v1_Page*>(page);
  std::string j(json);
  if (!j.empty()) e.body.rpc_event.json = j.data();
  deliver(e);
}

Fixture& fixtureFor(ui::Ui& ui, std::function<std::string()> body) {
  auto& f = current();
  // the old one goes first: its documents and hooks leave the ui before the new one adds its own
  f.reset();
  f = std::make_unique<Fixture>(ui, std::move(body));
  return *f;
}

void dropFixture() { current().reset(); }

cards::CardPtr loadCard(const std::string& file) {
  const auto bytes = read(fixturePath(file));
  return cards::decodeCard({reinterpret_cast<const std::uint8_t*>(bytes.data()), bytes.size()});
}

cards::PagePtr loadPage(const std::string& file) {
  const auto bytes = read(fixturePath(file));
  return cards::decodePage({reinterpret_cast<const std::uint8_t*>(bytes.data()), bytes.size()});
}

PageOf::PageOf(std::vector<cards::CardPtr> c) : owned(std::move(c)) {
  for (auto& p : owned) {
    if (p) cards.push_back(*p);
  }
  page.cards = cards.data();
  page.cards_count = static_cast<pb_size_t>(cards.size());
  page.id = id.data();
}

std::unique_ptr<BuiltCard> tailCard(const std::string& id, std::uint32_t revision, const std::string& text) {
  auto b = std::make_unique<BuiltCard>();
  auto& s = b->strings;
  s.push_back(id);
  auto* idp = s.back().data();
  s.push_back(text);
  auto* textp = s.back().data();
  s.push_back("Сейчас");
  auto* titlep = s.back().data();
  s.push_back("code");
  auto* iconp = s.back().data();
  s.push_back("Что делает задача сейчас");
  auto* fallbackp = s.back().data();
  b->numbers.push_back(1);
  auto* version = &b->numbers.back();
  b->numbers.push_back(revision);
  auto* rev = &b->numbers.back();
  b->numbers.push_back(6);
  auto* lines = &b->numbers.back();
  b->flags.push_back(true);
  auto* tail = &b->flags.back();
  b->tones.push_back(cadence_cards_v1_Tone_TONE_NEUTRAL);
  b->texts.push_back(cadence_cards_v1_LongText{textp, lines, nullptr, tail});
  cadence_cards_v1_Node child = cadence_cards_v1_Node_init_zero;
  child.which_kind = cadence_cards_v1_Node_long_text_tag;
  child.kind.long_text = &b->texts.back();
  b->nodes.push_back(child);
  auto* childp = &b->nodes.back();
  cadence_cards_v1_CardBlock block = cadence_cards_v1_CardBlock_init_zero;
  block.title = titlep;
  block.icon = iconp;
  block.tone = &b->tones.back();
  block.children = childp;
  block.children_count = 1;
  b->blocks.push_back(block);
  cadence_cards_v1_Node root = cadence_cards_v1_Node_init_zero;
  root.which_kind = cadence_cards_v1_Node_card_tag;
  root.kind.card = &b->blocks.back();
  b->nodes.push_back(root);
  b->card.version = version;
  b->card.id = idp;
  b->card.revision = rev;
  b->card.fallback_text = fallbackp;
  b->card.root = &b->nodes.back();
  return b;
}

const cadence_screen_v1_Envelope& Turn::envelope() {
  env_ = cadence_screen_v1_Envelope_init_zero;
  env_.which_body = cadence_screen_v1_Envelope_assistant_turn_tag;
  auto& t = env_.body.assistant_turn;
  copyTo(t.turn_id, id);
  t.revision = revision;
  t.trigger = trigger;
  t.state = state;
  t.has_mic_source = true;
  t.mic_source.kind = mic;
  copyTo(t.mic_source.device_id, mic == cadence_v2_MicSourceKind_MIC_SOURCE_KIND_SPEAKER ? "speaker-1" : "");
  t.has_transcript = true;
  t.transcript.stable = stable.data();
  t.transcript.unstable = unstable.data();
  steps_.clear();
  for (auto& s : steps) {
    cadence_v2_AssistantStep st = cadence_v2_AssistantStep_init_zero;
    copyTo(st.id, s.id);
    st.label = s.label.data();
    st.status = s.status;
    steps_.push_back(st);
  }
  t.steps = steps_.data();
  t.steps_count = static_cast<pb_size_t>(steps_.size());
  t.has_reply = true;
  t.reply.text = reply.data();
  t.reply.words = words.data();
  t.reply.words_count = static_cast<pb_size_t>(words.size());
  cites_.clear();
  for (auto& [n, card] : citations) {
    cadence_v2_Citation c = cadence_v2_Citation_init_zero;
    c.n = n;
    copyTo(c.card_id, card);
    cites_.push_back(c);
  }
  t.reply.citations = cites_.data();
  t.reply.citations_count = static_cast<pb_size_t>(cites_.size());
  if (owned_.size() != cards.size()) {
    owned_.clear();
    cards_.clear();
    for (const auto& f : cards) {
      owned_.push_back(loadCard(f));
      if (owned_.back()) cards_.push_back(*owned_.back());
    }
  }
  t.cards = cards_.data();
  t.cards_count = static_cast<pb_size_t>(cards_.size());
  t.follow_up_window_ms = followUpMs;
  copyTo(t.error_code, error);
  if (failure.has_value()) {
    t.has_failure = true;
    t.failure = *failure;
  }
  t.conversation_reset = conversationReset;
  return env_;
}

cadence_screen_v1_Envelope linkStatus(cadence_screen_v1_LinkState state, const char* code) {
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_link_status_tag;
  e.body.link_status.state = state;
  copyTo(e.body.link_status.code, code);
  return e;
}

cadence_screen_v1_Envelope claim(cadence_screen_v1_ClaimPhase phase, const char* code, std::int64_t expiresAtMs, const char* errorCode) {
  static std::string url = "https://cadence.home.arpa/claim/K7M4QX";
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_claim_state_tag;
  auto& c = e.body.claim_state;
  c.phase = phase;
  copyTo(c.code, code);
  c.url = url.data();
  c.expires_at_ms = expiresAtMs;
  copyTo(c.error_code, errorCode);
  return e;
}

cadence_screen_v1_Envelope route(cadence_v2_ScreenRoute r, const char* ref) {
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_route_tag;
  e.body.route.route = r;
  copyTo(e.body.route.ref, ref);
  return e;
}

cadence_screen_v1_Envelope voiceLevel(const char* turnId, std::uint32_t mic, std::uint32_t output) {
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_voice_level_tag;
  copyTo(e.body.voice_level.turn_id, turnId);
  e.body.voice_level.mic = mic;
  e.body.voice_level.output = output;
  return e;
}

}  // namespace cadence::screen::routes::syspreview
