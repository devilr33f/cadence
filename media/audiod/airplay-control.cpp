#include "audiod/airplay-control.h"

#include <utility>
#include <vector>

namespace cadence::audiod {

namespace {

using Clock = AirplayReceiver::Clock;

// the changes held while the connect reads: a receiver that sends more floods it
constexpr std::size_t kMaxHeld = 64;

std::string_view member(airplay::Request request) {
  switch (request) {
    case airplay::Request::Play:
      return "Play";
    case airplay::Request::Pause:
      return "Pause";
    case airplay::Request::Next:
      return "Next";
    case airplay::Request::Previous:
      return "Previous";
    case airplay::Request::DropSession:
      return "DropSession";
    case airplay::Request::SetVolume:
      return "SetAirplayVolume";
  }
  return "Play";
}

}  // namespace

namespace airplay {

std::optional<Change> remoteChange(const dbus::PropertiesChanged& changed) {
  Change change;
  for (const auto& [name, value] : changed.changed) {
    if (name == "Client") {
      const auto* text = value.asString();
      if (text == nullptr)
        return std::nullopt;
      change.client = *text;
    } else if (name == "PlayerState") {
      const auto* text = value.asString();
      const auto state = text == nullptr ? std::nullopt : playerState(*text);
      if (!state)
        return std::nullopt;
      change.state = *state;
    } else if (name == "AirplayVolume") {
      const auto db = value.asDouble();
      if (!db)
        return std::nullopt;
      change.volumeDb = *db;
    }
    // metadata, Available, Active and every other property are deliberately not read
  }
  for (const auto& name : changed.invalidated)
    if (name == "Client" || name == "PlayerState" || name == "AirplayVolume")
      return std::nullopt;
  return change;
}

}  // namespace airplay

AirplayControl::AirplayControl(const Log& log, AirplayControlHooks hooks, AirplayControlTimings timings)
    : log_(log), hooks_(std::move(hooks)), timings_(timings), session_(log, *this, timings.session) {}

void AirplayControl::events(std::shared_ptr<MediaSourceEvents> events) { events_ = std::move(events); }

void AirplayControl::control(MediaAction action, CommandDone done, std::uint64_t pauseNumber) {
  session_.control(action, std::move(done), Clock::now(), pauseNumber);
  syncMetadata();
}

void AirplayControl::speakerLevel(std::uint8_t level) {
  session_.speakerLevel(level, Clock::now());
  syncMetadata();
}

void AirplayControl::attached(dbus::Client& client, const std::string& owner) {
  client_ = &client;
  owner_ = owner;
  ++attach_;
  connected_ = false;
  failure_ = {};
  wrongProtocol_ = false;
  retryAt_.reset();
  held_.clear();
  snapshot_ = {};
  connectUntil_ = Clock::now() + timings_.connectWait;
  // before any read, so no change between a read and the match is lost
  const auto rule = dbus::propertiesChangedRule(owner_, airplay::kShairportPath);
  if (!rule)
    return fail(airplay::kBusFailed);
  ask(dbus::addMatchCall(*rule), [this](const dbus::Reply& reply) {
    if (reply.status != dbus::CallStatus::Ok)
      return fail(airplay::kBusFailed);
    readProtocol();
  });
}

void AirplayControl::ask(dbus::Message message, std::function<void(const dbus::Reply& reply)> step) {
  const auto serial = client_->call(
      std::move(message),
      [this, attach = attach_, step = std::move(step)](const dbus::Reply& reply) {
        if (attach == attach_ && failure_.empty())
          step(reply);
      },
      timings_.call);
  if (serial == 0)
    fail(airplay::kBusFailed);
}

void AirplayControl::readProtocol() {
  ask(dbus::propertyGetCall(owner_, std::string(airplay::kShairportPath), std::string(airplay::kShairportInterface),
                            "Protocol"),
      [this](const dbus::Reply& reply) {
        const auto* value =
            reply.status == dbus::CallStatus::Ok && reply.body.size() == 1 ? reply.body.front().inner() : nullptr;
        const auto* text = value == nullptr ? nullptr : value->asString();
        if (text != nullptr && *text == airplay::kAirplay2) {
          // a later read that fails is no wrong protocol
          wrongProtocol_ = false;
          return readOwner();
        }
        // an answer, but not this receiver's: tried again all the same, as shairport may not be up yet (v1)
        wrongProtocol_ = value != nullptr;
        retry();
      });
}

void AirplayControl::readOwner() {
  ask(dbus::getNameOwnerCall(std::string(airplay::kShairportName)), [this](const dbus::Reply& reply) {
    const auto* owner =
        reply.status == dbus::CallStatus::Ok && reply.body.size() == 1 ? reply.body.front().asString() : nullptr;
    if (owner == nullptr)
      return retry();
    if (*owner != owner_)
      return fail(airplay::kSessionLost);
    readState();
  });
}

const dbus::Value* AirplayControl::property(const dbus::Reply& reply) {
  if (reply.status != dbus::CallStatus::Ok) {
    retry();
    return nullptr;
  }
  if (reply.body.size() != 1 || reply.body.front().inner() == nullptr) {
    fail(airplay::kInvalidState);
    return nullptr;
  }
  return reply.body.front().inner();
}

void AirplayControl::readState() {
  ask(dbus::propertyGetCall(owner_, std::string(airplay::kShairportPath), std::string(airplay::kRemoteInterface),
                            "PlayerState"),
      [this](const dbus::Reply& reply) {
        const auto* value = property(reply);
        if (value == nullptr)
          return;
        const auto* text = value->asString();
        const auto state = text == nullptr ? std::nullopt : airplay::playerState(*text);
        if (!state)
          return fail(airplay::kInvalidState);
        snapshot_.state = *state;
        readVolume();
      });
}

void AirplayControl::readVolume() {
  ask(dbus::propertyGetCall(owner_, std::string(airplay::kShairportPath), std::string(airplay::kRemoteInterface),
                            "AirplayVolume"),
      [this](const dbus::Reply& reply) {
        const auto* value = property(reply);
        if (value == nullptr)
          return;
        const auto db = value->asDouble();
        if (!db)
          return fail(airplay::kInvalidState);
        snapshot_.volumeDb = *db;
        readClient();
      });
}

void AirplayControl::readClient() {
  ask(dbus::propertyGetCall(owner_, std::string(airplay::kShairportPath), std::string(airplay::kRemoteInterface),
                            "Client"),
      [this](const dbus::Reply& reply) {
        const auto* value = property(reply);
        if (value == nullptr)
          return;
        const auto* client = value->asString();
        if (client == nullptr)
          return fail(airplay::kInvalidState);
        snapshot_.client = *client;
        connected();
      });
}

void AirplayControl::connected() {
  connected_ = true;
  const std::uint64_t held = held_.size();
  log_.info("airplay_control_ready", {{"state", airplay::playerStateToken(snapshot_.state)}, {"held", held}});
  const auto now = Clock::now();
  session_.attach(snapshot_, now);
  // what changed while the connect read comes after what it read
  while (!held_.empty() && session_.failure().empty()) {
    const auto change = std::move(held_.front());
    held_.pop_front();
    session_.change(change, now);
  }
  held_.clear();
  syncMetadata();
}

void AirplayControl::retry() {
  const auto now = Clock::now();
  if (now + timings_.connectRetry > connectUntil_)
    return fail(wrongProtocol_ ? airplay::kWrongProtocol : airplay::kBusFailed);
  retryAt_ = now + timings_.connectRetry;
}

void AirplayControl::fail(std::string_view code) {
  if (!failure_.empty())
    return;
  failure_ = code;
  retryAt_.reset();
  log_.warn("airplay_control_failed", {{"code", code}});
}

void AirplayControl::signal(const dbus::Message& message) {
  if (client_ == nullptr || !failure().empty())
    return;
  const auto changed = dbus::parsePropertiesChangedFrom(message, owner_, airplay::kShairportPath);
  if (!changed || changed->interface != airplay::kRemoteInterface)
    return;
  const auto change = airplay::remoteChange(*changed);
  if (!change) {
    log_.warn("airplay_invalid_signal");
    if (connected_)
      session_.invalid();
    else
      fail(airplay::kInvalidState);
    return;
  }
  if (!connected_) {
    if (held_.size() == kMaxHeld)
      return fail(airplay::kBusFailed);
    held_.push_back(*change);
    return;
  }
  session_.change(*change, Clock::now());
  syncMetadata();
}

void AirplayControl::detached() {
  session_.detach();
  client_ = nullptr;
  owner_.clear();
  ++attach_;
  connected_ = false;
  failure_ = {};
  retryAt_.reset();
  held_.clear();
  // the pipe goes with the receiver
  metadata_ = {};
  progress_.reset();
  progressShown_ = false;
  syncMetadata();
}

void AirplayControl::metadata(const MediaMetadata& metadata) {
  metadata_ = metadata;
  syncMetadata();
}

void AirplayControl::progress(const airplay::Progress& progress) {
  progress_ = progress;
  progressShown_ = false;
  syncProgress();
}

void AirplayControl::syncProgress() {
  if (!progress_ || progressShown_ || !session_.live())
    return;
  progressShown_ = true;
  report([where = *progress_](MediaSourceEvents& events) {
    events.duration(where.durationMs);
    events.position(where.positionMs);
  });
}

std::optional<Clock::time_point> AirplayControl::deadline() const {
  if (client_ == nullptr || !failure().empty())
    return std::nullopt;
  return connected_ ? session_.deadline() : retryAt_;
}

void AirplayControl::tick(Clock::time_point now) {
  if (client_ == nullptr || !failure().empty())
    return;
  if (!connected_) {
    if (retryAt_ && now >= *retryAt_) {
      retryAt_.reset();
      readProtocol();
    }
    return;
  }
  session_.tick(now);
  syncMetadata();
}

std::string_view AirplayControl::failure() const {
  if (!failure_.empty())
    return failure_;
  // a session's failure is of the receiver it was attached with
  return connected_ ? session_.failure() : std::string_view();
}

bool AirplayControl::call(const AirplaySession::Call& call) {
  if (client_ == nullptr)
    return false;
  const auto interface =
      call.request == airplay::Request::DropSession ? airplay::kShairportInterface : airplay::kRemoteInterface;
  std::vector<dbus::Value> body;
  if (call.request == airplay::Request::SetVolume)
    body.push_back(dbus::Value::float64(call.db));
  auto message = dbus::Message::methodCall(owner_, std::string(airplay::kShairportPath), std::string(interface),
                                           std::string(member(call.request)), std::move(body));
  return client_->call(
             std::move(message),
             [this, attach = attach_, id = call.id](const dbus::Reply& reply) {
               if (attach != attach_)
                 return;
               session_.reply(id, reply.status == dbus::CallStatus::Ok, Clock::now());
               syncMetadata();
             },
             timings_.call) != 0;
}

void AirplayControl::hold() {
  if (hooks_.hold)
    hooks_.hold();
}

void AirplayControl::release(std::uint64_t pausesTaken) {
  if (hooks_.release)
    hooks_.release(pausesTaken);
}

std::uint64_t AirplayControl::pausesAsked() const { return hooks_.pauses ? hooks_.pauses() : 0; }

void AirplayControl::playing() {
  // what it plays is shown as it starts
  syncMetadata();
  syncProgress();
  report([](MediaSourceEvents& events) { events.playing(); });
}

void AirplayControl::paused() {
  report([](MediaSourceEvents& events) { events.paused(); });
}

// the sessions forget an ended session's metadata themselves, and the next session shows only what the pipe says for
// it: the next sender never sees the last one's track
void AirplayControl::ended() {
  report([](MediaSourceEvents& events) { events.ended(); });
  metadata_ = {};
  shown_ = {};
  progress_.reset();
  progressShown_ = false;
}

void AirplayControl::request(std::uint8_t level) {
  if (hooks_.senderVolume)
    hooks_.senderVolume(level);
}

void AirplayControl::report(std::function<void(MediaSourceEvents&)> report) {
  if (!events_ || !hooks_.post)
    return;
  hooks_.post([events = events_, report = std::move(report)] { report(*events); });
}

void AirplayControl::syncMetadata() {
  auto wanted = session_.live() ? metadata_ : MediaMetadata{};
  if (wanted == shown_)
    return;
  shown_ = wanted;
  report([metadata = std::move(wanted)](MediaSourceEvents& events) { events.metadata(metadata); });
}

}  // namespace cadence::audiod
