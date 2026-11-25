#include "audiod/airplay-session.h"

#include <algorithm>
#include <utility>

namespace cadence::audiod {

namespace airplay {

std::optional<PlayerState> playerState(std::string_view text) {
  if (text == "Playing")
    return PlayerState::Playing;
  if (text == "Paused")
    return PlayerState::Paused;
  if (text == "Stopped")
    return PlayerState::Stopped;
  if (text == "Not Available")
    return PlayerState::NotAvailable;
  return std::nullopt;
}

std::string_view playerStateToken(PlayerState state) {
  switch (state) {
    case PlayerState::Playing:
      return "playing";
    case PlayerState::Paused:
      return "paused";
    case PlayerState::Stopped:
      return "stopped";
    case PlayerState::NotAvailable:
      return "not_available";
  }
  return "unknown";
}

std::string_view requestToken(Request request) {
  switch (request) {
    case Request::Play:
      return "play";
    case Request::Pause:
      return "pause";
    case Request::Next:
      return "next";
    case Request::Previous:
      return "previous";
    case Request::DropSession:
      return "drop_session";
    case Request::SetVolume:
      return "set_volume";
  }
  return "unknown";
}

}  // namespace airplay

using airplay::PlayerState;
using airplay::Request;

AirplaySession::AirplaySession(const Log& log, Port& port, Timings timings)
    : log_(log), port_(port), timings_(timings), coalescer_(timings.coalesce) {}

void AirplaySession::drop(Pending& pending) {
  if (!pending.done)
    return;
  const auto done = std::move(pending.done);
  pending.done = nullptr;
  done(airplay::kControlUnsupported);
}

void AirplaySession::clearTransient() {
  for (auto& pending : calls_)
    drop(pending);
  calls_.clear();
  pendingVolume_.reset();
  echoes_.clear();
  observedDb_.reset();
  watchdog_.reset();
  ++commandGeneration_;
}

void AirplaySession::resetGuard() {
  guard_ = SenderGuard{};
  if (level_)
    guard_.setCurrent(*level_);
  guard_.connected();
}

void AirplaySession::announceLevel() {
  if (live())
    pendingVolume_ = level_;
}

void AirplaySession::sessionGone() {
  port_.hold();
  clearTransient();
  ++generation_;
  raw_ = PlayerState::NotAvailable;
  client_.clear();
  effective_ = Effective::Idle;
  localPaused_ = false;
  resumePending_ = false;
  resetGuard();
  coalescer_.clearPending();
  if (!goneReported_) {
    goneReported_ = true;
    port_.ended();
  }
}

// what the old sender asked for and has not gone out yet goes with it
void AirplaySession::identityChanged() {
  clearTransient();
  ++generation_;
  effective_ = Effective::Idle;
  localPaused_ = false;
  resumePending_ = false;
  resetGuard();
  coalescer_.clearPending();
}

void AirplaySession::attach(const airplay::Snapshot& snapshot, Clock::time_point now) {
  failure_ = {};
  ++generation_;
  raw_ = PlayerState::NotAvailable;
  client_.clear();
  effective_ = Effective::Idle;
  localPaused_ = false;
  resumePending_ = false;
  goneReported_ = false;
  if (inFlight_)
    drop(*inFlight_);
  inFlight_.reset();
  clearTransient();
  resetGuard();
  if (snapshot.state == PlayerState::NotAvailable) {
    client_ = snapshot.client;
    observedDb_ = snapshot.volumeDb;
    goneReported_ = true;
    return;
  }
  apply({.client = snapshot.client, .state = snapshot.state, .volumeDb = snapshot.volumeDb}, true, now);
  pump(now);
}

void AirplaySession::detach() {
  sessionGone();
  if (inFlight_)
    drop(*inFlight_);
  inFlight_.reset();
}

void AirplaySession::change(const airplay::Change& change, Clock::time_point now) {
  apply(change, false, now);
  pump(now);
}

// the identity first, then the state, then the volume, whatever order the signal listed them in
void AirplaySession::apply(const airplay::Change& change, bool initial, Clock::time_point now) {
  bool newIdentity = false;
  if (change.client) {
    if (*change.client != client_ && !initial && live()) {
      identityChanged();
      newIdentity = true;
    }
    client_ = *change.client;
  }
  if (change.state)
    onState(*change.state);
  if (change.volumeDb)
    onVolume(*change.volumeDb, now);
  if (newIdentity && live()) {
    announceLevel();
    maybePlaying();
  }
}

void AirplaySession::onState(PlayerState state) {
  const auto previous = raw_;
  if (state != previous)
    log_.info("airplay_player_state", {{"state", airplay::playerStateToken(state)}});
  if (state == PlayerState::NotAvailable) {
    sessionGone();
    return;
  }
  raw_ = state;
  goneReported_ = false;
  if (previous == PlayerState::NotAvailable) {
    resetGuard();
    announceLevel();
  }
  if (state == PlayerState::Playing) {
    // each real entry into Playing guards the sender's volume afresh and calls an old pause watchdog off
    if (previous != PlayerState::Playing) {
      guard_.connected();
      cancelWatchdog();
      localPaused_ = false;
      resumePending_ = false;
    }
    maybePlaying();
    return;
  }
  cancelWatchdog();
  port_.hold();
  if (effective_ != Effective::Paused &&
      (effective_ == Effective::Playing || previous == PlayerState::NotAvailable || previous == PlayerState::Playing))
    port_.paused();
  effective_ = Effective::Paused;
}

// the one place the output is released: never optimistically
void AirplaySession::maybePlaying() {
  if (raw_ != PlayerState::Playing || !level_ || localPaused_ || effective_ == Effective::Playing ||
      port_.pausesAsked() > pausesTaken_)
    return;
  effective_ = Effective::Playing;
  port_.release(pausesTaken_);
  port_.playing();
  announceLevel();
}

// rule 7: only a deliberate slider move becomes a request
void AirplaySession::onVolume(double db, Clock::time_point now) {
  observedDb_ = db;
  const auto percent = airplay::toPercent(db);
  // with no session it is an observation for the echo table only, never a request to be
  if (!percent || !live())
    return;
  if (inFlight_ && inFlight_->request == Request::SetVolume) {
    echoes_.reportDuringWrite(*percent, now);
    return;
  }
  // our own level coming back must not use up the drop of the first value that differs
  if (echoes_.isEcho(*percent, now))
    return;
  switch (guard_.incoming(*percent)) {
    case Incoming::Echo:
      break;
    case Incoming::Dropped:
      announceLevel();
      break;
    case Incoming::Request:
      if (const auto level = coalescer_.push(*percent, now))
        request(*level);
      break;
  }
}

void AirplaySession::request(std::uint8_t level) {
  log_.info("airplay_sender_volume", {{"level", level}});
  port_.request(level);
}

void AirplaySession::pause(Clock::time_point now) {
  port_.hold();
  if (effective_ != Effective::Paused)
    port_.paused();
  effective_ = Effective::Paused;
  localPaused_ = true;
  resumePending_ = false;
  ++commandGeneration_;
  watchdog_ = Watchdog{.at = now + timings_.pauseReflection, .session = generation_, .command = commandGeneration_};
  queue(Request::Pause);
}

void AirplaySession::control(MediaAction action, CommandDone done, Clock::time_point now, std::uint64_t pauseNumber) {
  const auto answer = [&](std::string_view code) {
    if (done)
      done(code);
  };
  // taken whatever comes of it, or a pause asked with no session would hold the output for good
  if (action == MediaAction::Pause)
    pausesTaken_ = std::max(pausesTaken_, pauseNumber);
  if (!live())
    return answer(airplay::kControlUnsupported);
  switch (action) {
    case MediaAction::Pause:
      pause(now);
      answer(kMediaOk);
      break;
    case MediaAction::Resume:
      cancelWatchdog();
      // a repeated raw Playing must not reopen the output: only Play's ok may, from here
      localPaused_ = true;
      resumePending_ = true;
      queue(Request::Play, std::move(done));
      break;
    case MediaAction::Next:
      queue(Request::Next, std::move(done));
      break;
    case MediaAction::Previous:
      queue(Request::Previous, std::move(done));
      break;
    case MediaAction::Stop:
    case MediaAction::Seek:
      return answer(kMediaUnsupported);
  }
  pump(now);
}

void AirplaySession::speakerLevel(std::uint8_t level, Clock::time_point now) {
  level_ = std::min(level, kMaxSenderLevel);
  guard_.setCurrent(*level_);
  announceLevel();
  // a cast that came before the first level plays only now
  maybePlaying();
  pump(now);
}

void AirplaySession::cancelWatchdog() {
  ++commandGeneration_;
  watchdog_.reset();
  std::erase_if(calls_, [](const Pending& pending) { return pending.request == Request::DropSession; });
}

void AirplaySession::tick(Clock::time_point now) {
  if (watchdog_ && now >= watchdog_->at) {
    const auto watchdog = *watchdog_;
    watchdog_.reset();
    // the output is held already; a receiver still playing ignored the pause
    if (watchdog.session == generation_ && watchdog.command == commandGeneration_ && localPaused_ &&
        raw_ == PlayerState::Playing) {
      log_.warn("airplay_pause_ignored");
      queue(Request::DropSession);
    }
  }
  if (const auto level = coalescer_.poll(now))
    request(*level);
  pump(now);
}

std::optional<AirplaySession::Clock::time_point> AirplaySession::deadline() const {
  auto next = coalescer_.deadline();
  if (watchdog_ && (!next || watchdog_->at < *next))
    next = watchdog_->at;
  return next;
}

void AirplaySession::queue(Request request, CommandDone done) {
  calls_.push_back(Pending{.id = nextCall_++,
                           .generation = generation_,
                           .command = commandGeneration_,
                           .request = request,
                           .db = 0,
                           .done = std::move(done)});
}

// a volume write goes last: only when nothing else waits, from where the receiver last said it stands
void AirplaySession::pump(Clock::time_point now) {
  if (inFlight_ || !failure_.empty())
    return;
  if (!calls_.empty()) {
    inFlight_ = std::move(calls_.front());
    calls_.pop_front();
  } else if (pendingVolume_) {
    const auto level = *std::exchange(pendingVolume_, std::nullopt);
    echoes_.dispatched(level, observedDb_, now);
    inFlight_ = Pending{.id = nextCall_++,
                        .generation = generation_,
                        .command = commandGeneration_,
                        .request = Request::SetVolume,
                        .db = airplay::toDb(level),
                        .done = {}};
  } else {
    return;
  }
  if (port_.call({.id = inFlight_->id, .request = inFlight_->request, .db = inFlight_->db}))
    return;
  log_.warn("airplay_call_failed", {{"call", airplay::requestToken(inFlight_->request)}, {"reason", "send"}});
  drop(*inFlight_);
  inFlight_.reset();
  failure_ = airplay::kSessionLost;
}

void AirplaySession::reply(std::uint64_t id, bool ok, Clock::time_point now) {
  if (!inFlight_ || inFlight_->id != id)
    return;
  auto call = std::move(*inFlight_);
  inFlight_.reset();
  const auto answer = [&] {
    if (!call.done)
      return;
    const auto done = std::move(call.done);
    call.done = nullptr;
    done(ok ? kMediaOk : airplay::kControlUnsupported);
  };
  if (!ok)
    log_.info("airplay_call_failed", {{"call", airplay::requestToken(call.request)}, {"reason", "reply"}});
  if (call.generation != generation_) {
    drop(call);
  } else {
    switch (call.request) {
      case Request::Play:
        if (ok && resumePending_ && raw_ == PlayerState::Playing) {
          localPaused_ = false;
          resumePending_ = false;
          maybePlaying();
        } else if (!ok) {
          resumePending_ = false;
        }
        answer();
        break;
      case Request::Pause:
      case Request::Next:
      case Request::Previous:
        answer();
        break;
      case Request::DropSession:
        if (call.command != commandGeneration_)
          break;
        log_.info("airplay_session_dropped", {{"ok", ok ? 1 : 0}});
        if (ok)
          sessionGone();
        else
          failure_ = airplay::kSessionLost;
        break;
      case Request::SetVolume:
        // a failure or a timeout keeps the echoes: the write may still have reached the sender
        break;
    }
  }
  pump(now);
}

void AirplaySession::invalid() {
  port_.hold();
  failure_ = airplay::kInvalidState;
}

}  // namespace cadence::audiod
