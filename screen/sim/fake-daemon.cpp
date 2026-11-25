#include "sim/fake-daemon.h"

#include "app/routes/core/methods.h"
#include "app/routes/preview/fake-catalog.h"
#include "app/routes/preview/fake-devices.h"
#include "ipc/codec.h"
#include "sim/names.h"
#include "sim/send.h"
#include "ui/cards/model.h"

#include <fcntl.h>
#include <poll.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstdio>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <iterator>

namespace cadence::screen::sim {

namespace rpc = ::cadence::screen::routes::rpc;
namespace fake = ::cadence::screen::routes::fake;

namespace {

// the screen's own clock (epoch ms): a card's timeline anchored at it runs from the moment it was answered
std::int64_t wallMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch())
      .count();
}

std::int64_t steadyMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now().time_since_epoch())
      .count();
}

#if defined(__APPLE__)
constexpr int kListenType = SOCK_STREAM;
#else
constexpr int kListenType = SOCK_SEQPACKET;
#endif

std::string mediaToken(cadence_v2_PlaybackState state, bool gated) {
  switch (state) {
    case cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING:
      return "playing";
    case cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED:
      return gated ? "gated" : "paused";
    case cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING:
      return "buffering";
    default:
      return "idle";
  }
}

}  // namespace

namespace {

// a fixture's "page": card fixtures by file name, or "devices" (the fake devices panel)
bool pageOf(const Json& j, std::vector<std::uint8_t>& out, std::string* error) {
  if (!j.contains("page")) return true;
  const auto& p = j["page"];
  if (p.is_string() && p.get<std::string>() == "devices") {
    out = fake::devicesPage(wallMs());
    return true;
  }
  if (!p.is_array()) {
    if (error != nullptr) *error = "page: an array of card fixtures or \"devices\"";
    return false;
  }
  std::vector<std::string> cards;
  for (const auto& name : p) {
    std::ifstream in(std::string(CADENCE_SIM_CARDS) + "/" + (name.is_string() ? name.get<std::string>() : ""),
                     std::ios::binary);
    if (!name.is_string() || !in) {
      if (error != nullptr) *error = "page: no card fixture " + name.dump();
      return false;
    }
    cards.emplace_back(std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>());
  }
  out = fake::pageOf(cards);
  return true;
}

}  // namespace

std::optional<FakeAnswer> parseAnswer(const std::string& path, std::string* error) {
  std::ifstream in(path, std::ios::binary);
  if (!in) {
    if (error != nullptr) *error = "cannot read " + path;
    return std::nullopt;
  }
  const std::string text{std::istreambuf_iterator<char>(in), {}};
  Json j = Json::parse(text, nullptr, false);
  if (j.is_null()) {
    if (error != nullptr) *error = "not json: " + path;
    return std::nullopt;
  }
  if (!j.is_object()) j = Json{{"json", j}};
  FakeAnswer a;
  a.delayOnly = !j.contains("json") && !j.contains("status") && !j.contains("events") && j.contains("delayMs");
  a.status = j.value("status", "ok");
  a.json = j.value("json", Json::object());
  a.delayMs = j.value("delayMs", 30);
  a.withMediaPlay = j.value("withMediaPlay", false);
  if (!pageOf(j, a.page, error)) return std::nullopt;
  for (const auto& e : j.value("events", Json::array())) {
    FakeAnswer::Event ev;
    ev.atMs = e.value("atMs", a.delayMs);
    ev.json = e.value("json", Json::object());
    ev.last = e.value("last", false);
    if (!e.is_object() || !pageOf(e, ev.page, error)) return std::nullopt;
    a.events.push_back(std::move(ev));
  }
  return a;
}

FakeDaemon::FakeDaemon(Options options) : options_(std::move(options)) {
  path_ = options_.path.empty() ? "/tmp/cadence-screen-sim." + std::to_string(::getpid()) + ".sock" : options_.path;
}

FakeDaemon::~FakeDaemon() { stop(); }

void FakeDaemon::start() {
  sockaddr_un addr{};
  unsigned length = 0;
  if (!ipc::fillAddress(path_, &addr, &length) || path_[0] == '@') {
    std::fprintf(stderr, "cadence-screen-sim: bad socket path %s\n", path_.c_str());
    return;
  }
  ::unlink(path_.c_str());
  listen_ = ::socket(AF_UNIX, kListenType, 0);
  if (listen_ < 0) return;
  ::fcntl(listen_, F_SETFD, FD_CLOEXEC);
  if (::bind(listen_, reinterpret_cast<sockaddr*>(&addr), static_cast<socklen_t>(length)) != 0 ||
      ::listen(listen_, 2) != 0) {
    ::close(listen_);
    listen_ = -1;
    return;
  }
  stopping_ = false;
  thread_ = std::thread([this] { run(); });
}

void FakeDaemon::stop() {
  if (stopping_.exchange(true)) return;
  if (listen_ >= 0) {
    ::shutdown(listen_, SHUT_RDWR);
    ::close(listen_);
    listen_ = -1;
  }
  conn_.shutdown();
  if (thread_.joinable()) thread_.join();
  conn_.close();
  if (!path_.empty() && path_[0] != '@') ::unlink(path_.c_str());
}

void FakeDaemon::dropLink() {
  conn_.shutdown();
}

void FakeDaemon::setLanguage(const std::string& language) {
  std::lock_guard lock(mu_);
  options_.language = language;
}

std::string FakeDaemon::language() const {
  std::lock_guard lock(mu_);
  return options_.language;
}

void FakeDaemon::setAnswer(const std::string& method, FakeAnswer answer) {
  std::lock_guard lock(mu_);
  answers_[method] = std::move(answer);
}

bool FakeDaemon::loadFixtures(const std::string& dir, std::string* error) {
  if (dir.empty()) return true;
  std::error_code ec;
  for (const auto& entry : std::filesystem::directory_iterator(dir, ec)) {
    const auto path = entry.path();
    if (path.extension() != ".json") continue;
    auto answer = parseAnswer(path.string(), error);
    if (!answer) return false;
    std::lock_guard lock(mu_);
    answers_[path.stem().string()] = std::move(*answer);
  }
  if (ec) {
    if (error != nullptr) *error = "cannot read " + dir + ": " + ec.message();
    return false;
  }
  return true;
}

std::string FakeDaemon::lastMediaState() const {
  std::lock_guard lock(mu_);
  return lastMediaState_;
}

std::string FakeDaemon::lastContextRoute() const {
  std::lock_guard lock(mu_);
  return lastContextRoute_;
}

void FakeDaemon::sendBuilt(const std::function<void(ipc::OutEnvelope&)>& build) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_hello_tag);
  build(out);
  sendEnvelope(out.get());
}

void FakeDaemon::rememberAlarms(const Json& summary) {
  Json copy = summary;
  const auto now = wallMs();
  const auto absolute = [now](Json& o) {
    if (!o.contains("nextInS")) return;
    o["nextUnixMs"] = now + o.value("nextInS", std::int64_t{0}) * 1000;
    o.erase("nextInS");
  };
  if (copy.contains("devices")) {
    for (auto& d : copy["devices"]) {
      if (!d.contains("items")) continue;
      for (auto& it : d["items"]) absolute(it);
    }
  }
  std::lock_guard lock(mu_);
  alarms_ = std::move(copy);
}

bool FakeDaemon::actOnAlarms(const Json& args) {
  std::lock_guard lock(mu_);
  if (!alarms_.is_object()) return false;
  const auto device = args.value("deviceId", "");
  const auto kind = args.value("kind", "");
  const auto id = args.value("id", "");
  const auto action = args.value("action", "");
  const auto same = [&](const Json& o, const char* deviceKey) {
    return o.value(deviceKey, "") == device && o.value("kind", "alarm") == kind && o.value("id", "") == id;
  };
  bool found = false;
  if (action == "stop" || action == "snooze") {
    auto& rings = alarms_["ringing"];
    if (rings.is_array()) {
      for (auto it = rings.begin(); it != rings.end(); ++it) {
        if (!same(*it, "deviceId")) continue;
        rings.erase(it);
        found = true;
        break;
      }
    }
  } else if (action == "cancel") {
    for (auto& d : alarms_.contains("devices") ? alarms_["devices"] : alarms_["ringing"]) {
      if (!d.is_object() || d.value("deviceId", "") != device || !d.contains("items")) continue;
      auto& items = d["items"];
      for (auto it = items.begin(); it != items.end(); ++it) {
        if (it->value("kind", "alarm") != kind || it->value("id", "") != id) continue;
        items.erase(it);
        found = true;
        break;
      }
    }
  }
  if (found) alarms_["revision"] = alarms_.value("revision", 1) + 1;
  return found;
}

void FakeDaemon::sendRaw(const std::uint8_t* data, std::size_t size) {
  std::lock_guard lock(sendMu_);
  if (!conn_.valid()) return;
  conn_.send(std::span<const std::uint8_t>(data, size));
}

void FakeDaemon::sendEnvelope(const cadence_screen_v1_Envelope& m) {
  // the fake's thread (its timers) and whatever called sendBuilt (the main thread, between steps): both may ship
  std::lock_guard lock(sendMu_);
  if (!conn_.valid()) return;
  std::vector<std::uint8_t> frame;
  if (!ipc::encodeEnvelope(m, frame)) return;
  conn_.send(frame);
}

void FakeDaemon::later(int ms, std::function<void()> fn) {
  due_.push_back(Due{steadyMs() + ms, std::move(fn)});
}

void FakeDaemon::run() {
  while (!stopping_.load()) {
    pollfd p{listen_, POLLIN, 0};
    const int ready = ::poll(&p, 1, 200);
    if (ready <= 0) continue;
    const int fd = ::accept(listen_, nullptr, nullptr);
    if (fd < 0) continue;
    ::fcntl(fd, F_SETFD, FD_CLOEXEC);
    serve(ipc::SeqpacketSocket(fd));
  }
}

void FakeDaemon::serve(ipc::SeqpacketSocket conn) {
  {
    std::lock_guard lock(sendMu_);
    conn_ = std::move(conn);
  }
  linked_ = true;
  std::vector<std::uint8_t> frame;
  while (!stopping_.load()) {
    // the next timer decides how long to wait for the screen's next frame
    std::int64_t wait = 200;
    for (const auto& d : due_) wait = std::min(wait, std::max<std::int64_t>(d.atMs - steadyMs(), 0));
    const auto got = conn_.recv(frame, static_cast<int>(wait));
    if (got == ipc::SeqpacketSocket::Recv::Frame) {
      auto envelope = ipc::decodeEnvelope(frame, nullptr);
      if (envelope) handle(*envelope);
      continue;
    }
    if (got == ipc::SeqpacketSocket::Recv::WouldBlock) {
      // run whatever came due
      const auto now = steadyMs();
      for (auto it = due_.begin(); it != due_.end();) {
        if (it->atMs > now) {
          ++it;
          continue;
        }
        auto fn = std::move(it->fn);
        it = due_.erase(it);
        fn();
      }
      continue;
    }
    // closed or broken: the link went, as cadenced's would
    break;
  }
  linked_ = false;
  {
    std::lock_guard lock(sendMu_);
    conn_.close();
  }
  // its callbacks belong to a connection that is gone
  due_.clear();
}

namespace {

std::string castToken(cadence_screen_v1_MediaAction a) {
  switch (a) {
    case cadence_screen_v1_MediaAction_MEDIA_ACTION_PAUSE: return "pause";
    case cadence_screen_v1_MediaAction_MEDIA_ACTION_RESUME: return "resume";
    case cadence_screen_v1_MediaAction_MEDIA_ACTION_STOP: return "stop";
    case cadence_screen_v1_MediaAction_MEDIA_ACTION_SEEK: return "seek";
    case cadence_screen_v1_MediaAction_MEDIA_ACTION_NEXT: return "next";
    case cadence_screen_v1_MediaAction_MEDIA_ACTION_PREVIOUS: return "previous";
    default: return "other";
  }
}

}  // namespace

std::string FakeDaemon::lastCastCommand() const {
  std::lock_guard lock(mu_);
  return lastCastCommand_;
}

void FakeDaemon::handle(const cadence_screen_v1_Envelope& m) {
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_hello_tag:
      hellos_++;
      boot();
      return;
    case cadence_screen_v1_Envelope_rpc_request_tag: {
      const auto& r = m.body.rpc_request;
      Json args = r.json != nullptr ? Json::parse(r.json, nullptr, false) : Json::object();
      if (args.is_null()) args = Json::object();
      answerRpc(r.id, r.method, args);
      return;
    }
    case cadence_screen_v1_Envelope_rpc_cancel_tag:
      return;
    case cadence_screen_v1_Envelope_media_state_tag: {
      std::lock_guard lock(mu_);
      lastMediaState_ = mediaToken(m.body.media_state.state, m.body.media_state.gated);
      return;
    }
    case cadence_screen_v1_Envelope_input_event_tag:
      volumeKey(m.body.input_event);
      return;
    case cadence_screen_v1_Envelope_now_playing_command_tag: {
      std::lock_guard lock(mu_);
      lastCastCommand_ = castToken(m.body.now_playing_command.action);
      return;
    }
    case cadence_screen_v1_Envelope_screen_context_tag: {
      std::lock_guard lock(mu_);
      lastContextRoute_ = routeName(m.body.screen_context.route);
      return;
    }
    default:
      return;
  }
}

void FakeDaemon::volumeKey(const cadence_screen_v1_InputEvent& e) {
  const auto a = e.action;
  // a press acts once, on its down edge, as cadenced's input does; an api key never moves the level (rule 7)
  if ((a != cadence_screen_v1_KeyAction_KEY_ACTION_DOWN && a != cadence_screen_v1_KeyAction_KEY_ACTION_PRESS) ||
      e.source == cadence_screen_v1_InputSource_INPUT_SOURCE_API)
    return;
  {
    std::lock_guard lock(mu_);
    switch (e.key) {
      case cadence_screen_v1_Key_KEY_VOLUME_UP:
        level_ = std::min(100, level_ + 5);
        break;
      case cadence_screen_v1_Key_KEY_VOLUME_DOWN:
        level_ = std::max(0, level_ - 5);
        break;
      case cadence_screen_v1_Key_KEY_VOLUME_MUTE:
        muted_ = !muted_;
        break;
      default:
        return;
    }
  }
  later(0, [this] { sendVolume(true); });
}

void FakeDaemon::sendVolume(bool show) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_volume_state_tag);
  {
    std::lock_guard lock(mu_);
    out->body.volume_state.level = static_cast<std::uint32_t>(level_);
    out->body.volume_state.muted = muted_;
  }
  out->body.volume_state.show = show;
  sendEnvelope(out.get());
}

void FakeDaemon::boot() {
  // the level in force, as cadenced sends it on attach: the state, not shown
  later(0, [this] { sendVolume(false); });
  // what cadenced sends a screen that just said Hello: ready names the device and opens the playback gate (rule 7's
  // level is a sim's own, so it opens at once), the link is up and the device claimed
  later(0, [this] {
    ipc::OutEnvelope out(cadence_screen_v1_Envelope_ready_tag);
    auto& r = out->body.ready;
    ipc::copyField(r.daemon_version, "sim-1.0");
    ipc::copyField(r.device_id, "sim-device-1");
    r.device_name = out.keep(options_.deviceName);
    r.room_name = out.keep("Студия");
    ipc::copyField(r.language, options_.language);
    r.gate_open = true;
    sendEnvelope(out.get());
  });
  later(30, [this] {
    ipc::OutEnvelope out(cadence_screen_v1_Envelope_link_status_tag);
    out->body.link_status.state = cadence_screen_v1_LinkState_LINK_STATE_ONLINE;
    out->body.link_status.server_url = out.keep("http://cadence-sim.invalid");
    ipc::copyField(out->body.link_status.server_version, "v2-sim");
    sendEnvelope(out.get());
  });
  later(60, [this] {
    ipc::OutEnvelope out(cadence_screen_v1_Envelope_claim_state_tag);
    out->body.claim_state.phase = cadence_screen_v1_ClaimPhase_CLAIM_PHASE_APPROVED;
    sendEnvelope(out.get());
  });
}

FakeAnswer FakeDaemon::builtin(const std::string& method, const Json& args) {
  // the preview fake catalog's answers, the same shapes on the fake's wall clock
  const fake::Script s;
  FakeAnswer a;
  if (method == rpc::kCatalogBrowse) {
    const std::string path = args.value("path", "");
    if (path == "ytmusic") {
      a.status = "unavailable";
      a.json = {{"code", "unavailable"}, {"failure", "timeout"}};
      a.delayMs = 60;
    } else if (path == "spotify") {
      a.status = "not_found";
      a.json = {{"code", "not_found"}};
      a.delayMs = 30;
    } else {
      a.json = fake::homeSections(path.empty() ? "torrserver" : path);
      // the facts the server's cache lays over home's items: a rank, the owner's own list status
      if (a.json.contains("sections")) {
        for (auto& sec : a.json["sections"]) {
          if (!sec.contains("items")) continue;
          int n = 0;
          for (auto& it : sec["items"]) {
            if (!it.contains("show") || n++ >= 3) continue;
            it["show"]["rank"] = 148 + n * 37;
            if (n == 1) it["show"]["mine"] = {{"status", "watching"}, {"watched", 3}};
          }
        }
      }
      a.delayMs = path == "torrplayer" ? 120 : 60;
    }
    return a;
  }
  if (method == rpc::kCatalogSearch) {
    a.json = fake::searchEvent(0, false);
    a.delayMs = 20;
    const int times[] = {80, 200, 350, 500, 650, 800, 900};
    for (int i = 1; i <= 7; i++) {
      FakeAnswer::Event e;
      e.atMs = times[i - 1];
      e.json = fake::searchEvent(i, false);
      e.last = i == 7;
      a.events.push_back(std::move(e));
    }
    return a;
  }
  if (method == rpc::kCatalogItem) {
    a.json = fake::titleDetail(s, args.value("ref", ""));
    // the facts the server lays over a title page (Shikimori's, MyAnimeList's, the owner's own list entry)
    auto& show = a.json["item"]["show"];
    show["rank"] = 148;
    show["popularity"] = 120;
    show["airedOn"] = "2023-10-01";
    show["releasedOn"] = "2024-03-24";
    show["studios"] = {"Студия «Ветер»"};
    show["related"] = Json::array({Json{{"relation", "Продолжение"}, {"title", "Лунный аптекарь 2"}, {"kind", "tv"}, {"year", 2025}},
                                   Json{{"relation", "Спин-офф"}, {"title", "Лунный аптекарь: дневник"}, {"kind", "ona"}, {"year", 2024}}});
    show["mine"] = {{"status", "watching"}, {"score", 9}, {"watched", 12}};
    a.delayMs = 40;
    return a;
  }
  if (method == rpc::kCatalogAlternatives) {
    const auto alt = [](const char* ref, const char* source, const char* name, int eps, Json voices, int q, bool current) {
      Json vs = Json::array();
      for (const auto& v : voices) vs.push_back(Json{{"label", v}});
      Json j{{"ref", ref},    {"provider", "torrplayer"}, {"source", source}, {"sourceName", name}, {"title", "Лунный аптекарь"},
             {"match", "shikimori"}, {"episodesAired", eps}, {"voiceovers", vs}, {"quality", q}};
      if (current) j["current"] = true;
      return j;
    };
    a.json = {{"asked", 5}, {"failed", 1},
              {"items", Json::array({alt("ref-v-aniliberty", "aniliberty", "AniLiberty", 24, {"AniLibria"}, 1080, true),
                                     alt("ref-v-anime365", "anime365", "anime365", 24, {"AniDUB", "Studio Band", "SHIZA", "Субтитры"}, 1080, false),
                                     alt("ref-v-yummy", "yummyanime", "YummyAnime", 22, {"Dream Cast", "AnimeVost"}, 720, false)})}};
    a.delayMs = 600;
    return a;
  }
  if (method == rpc::kCatalogOptions) {
    a.json = fake::titleOptions(s, args.value("episode", ""));
    a.delayMs = 40;
    return a;
  }
  if (method == rpc::kCatalogResolve) {
    const bool torrent = args.contains("choices") && args["choices"].contains("torrent");
    a.json = {{"resolving", true}};
    a.delayMs = 20;
    FakeAnswer::Event stage1;
    stage1.atMs = torrent ? 150 : 150;
    stage1.json = Json{{"stage", torrent ? "connecting_peers" : "reading_stream"}};
    a.events.push_back(std::move(stage1));
    FakeAnswer::Event stage2;
    stage2.atMs = torrent ? 400 : 450;
    stage2.json = Json{{"stage", torrent ? "reading_stream" : "starting"}};
    a.events.push_back(std::move(stage2));
    if (torrent) {
      FakeAnswer::Event stage3;
      stage3.atMs = 650;
      stage3.json = Json{{"stage", "matching"}};
      a.events.push_back(std::move(stage3));
    }
    FakeAnswer::Event done;
    done.atMs = 900;
    done.json = Json{{"playable", fake::playable(args.value("ref", ""), args.value("episode", ""))}};
    done.last = true;
    a.events.push_back(std::move(done));
    return a;
  }
  if (method == rpc::kCatalogAct) {
    auto acts = fake::titleDetail(s, "ref-tg-1")["actions"];
    acts[0]["value"] = args.value("value", "");
    a.json = {{"actions", acts}};
    a.delayMs = 40;
    return a;
  }
  // the quick panel's page (devices.media itself answers nothing by default: a script gives it its items)
  if (method == "devices.panel") {
    a.page = fake::devicesPage(wallMs());
    a.delayMs = 60;
    return a;
  }
  if (method == rpc::kMediaPlay) {
    a.withMediaPlay = true;
    a.delayMs = 900;
    a.json = Json{{"sessionId", "cad-?"}};
    return a;
  }
  a.json = Json::object();
  a.delayMs = 10;
  return a;
}

std::pair<int, std::string> FakeDaemon::calls(const std::string& method) const {
  std::lock_guard lock(mu_);
  const auto it = calls_.find(method);
  return it != calls_.end() ? it->second : std::pair<int, std::string>{0, {}};
}

void FakeDaemon::answerRpc(const std::string& id, const std::string& method, const Json& args) {
  FakeAnswer answer;
  {
    std::lock_guard lock(mu_);
    auto& seen = calls_[method];
    ++seen.first;
    seen.second = args.dump();
    // METHOD#path: a call with that path alone (one provider's or one feed's browse), counted and answered apart
    const std::string path = args.is_object() && args.contains("path") && args["path"].is_string() ? args["path"].get<std::string>() : "";
    if (!path.empty()) {
      auto& one = calls_[method + "#" + path];
      ++one.first;
      one.second = args.dump();
    }
    auto it = path.empty() ? answers_.end() : answers_.find(method + "#" + path);
    if (it == answers_.end()) it = answers_.find(method);
    answer = it != answers_.end() ? it->second : builtin(method, args);
    if (it != answers_.end() && it->second.delayOnly) {
      answer = builtin(method, args);
      answer.delayMs = it->second.delayMs;
    }
  }
  const auto respond = [this, id](const std::string& status, const Json& json, const std::vector<std::uint8_t>& pageBytes = {}) {
    ipc::OutEnvelope out(cadence_screen_v1_Envelope_rpc_response_tag);
    ipc::copyField(out->body.rpc_response.id, id);
    ipc::copyField(out->body.rpc_response.status, status);
    out->body.rpc_response.json = out.keep(json.dump());
    // the page lives until the frame is encoded
    const auto page = pageBytes.empty() ? nullptr : ui::cards::decodePage(pageBytes);
    out->body.rpc_response.page = page.get();
    sendEnvelope(out.get());
  };
  const auto event = [this, id](const Json& json, bool last, const std::vector<std::uint8_t>& pageBytes = {}) {
    ipc::OutEnvelope out(cadence_screen_v1_Envelope_rpc_event_tag);
    ipc::copyField(out->body.rpc_event.id, id);
    out->body.rpc_event.seq = ++seqs_[id];
    out->body.rpc_event.json = out.keep(json.dump());
    out->body.rpc_event.last = last;
    const auto page = pageBytes.empty() ? nullptr : ui::cards::decodePage(pageBytes);
    out->body.rpc_event.page = page.get();
    sendEnvelope(out.get());
  };
  if (method == "assistant.reset" && answer.status == "ok") {
    const std::string reply = language() == "en" ? "Starting over." : "Начинаем заново.";
    later(answer.delayMs, [this, answer, respond, reply] {
      Json message{{"assistant_turn", {{"turnId", "sim-reset"}, {"revision", 1}, {"state", "resting"},
                                       {"conversationReset", true}, {"reply", reply}}}};
      if (auto built = buildSend(message.dump(), "", nullptr)) sendEnvelope(built->get());
      respond(answer.status, answer.json);
    });
    return;
  }
  if (method == rpc::kMediaPlay && answer.withMediaPlay) {
    // the server resolved it and cadenced got its play: the screen sees the MediaPlay first, then the answer
    const auto ref = args.value("ref", "");
    const auto episode = args.value("episode", "");
    const std::string sid = "cad-" + std::to_string(++plays_);
    auto pl = fake::playable(ref, episode);
    if (args.contains("startMs")) pl["stream"]["resumeMs"] = args["startMs"];
    if (args.value("from", "") == "start") pl["stream"]["resumeMs"] = 0;
    later(answer.delayMs, [this, pl, sid, id, episode, respond] {
      ipc::OutEnvelope out(cadence_screen_v1_Envelope_media_play_tag);
      if (fake::playEnvelope(pl, sid, out)) sendEnvelope(out.get());
      Json json = Json{{"sessionId", sid}};
      if (!episode.empty()) json["episode"] = episode;
      respond("ok", json);
    });
    return;
  }
  if (method == "alarms.act" && answer.status == "ok") {
    // as the server would: the answer, then the summary without what it ended (or not_found)
    later(answer.delayMs, [this, args, respond] {
      if (!actOnAlarms(args)) {
        respond("refused", Json{{"code", "not_found"}});
        return;
      }
      respond("ok", Json::object());
      Json summary;
      {
        std::lock_guard lock(mu_);
        summary = alarms_;
      }
      if (auto built = buildSend(Json{{"alarms_summary", summary}}.dump(), "", nullptr)) sendEnvelope(built->get());
    });
    return;
  }
  later(answer.delayMs, [answer, id, respond] { respond(answer.status, answer.json, answer.page); });
  for (const auto& e : answer.events) {
    later(e.atMs, [e, id, event] { event(e.json, e.last, e.page); });
  }
}

}  // namespace cadence::screen::sim
