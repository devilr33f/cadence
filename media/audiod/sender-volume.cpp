#include "audiod/sender-volume.h"

#include <algorithm>
#include <iterator>
#include <utility>

namespace cadence::audiod {

namespace {

std::uint16_t distance(std::uint16_t a, std::uint16_t b) {
  return static_cast<std::uint16_t>(a > b ? a - b : b - a);
}

}  // namespace

void SenderGuard::setCurrent(std::uint8_t level) { current_ = std::min(level, kMaxSenderLevel); }

Incoming SenderGuard::incoming(std::uint8_t level) {
  // nothing to compare with, and fresh stays, so the first value after the level is learnt is dropped too
  if (!current_)
    return Incoming::Dropped;
  if (level == *current_)
    return Incoming::Echo;
  if (fresh_) {
    fresh_ = false;
    return Incoming::Dropped;
  }
  return Incoming::Request;
}

void Echoes::sent(std::uint16_t raw, SenderClock::time_point at) {
  expire(at);
  if (!unseen_.empty() && unseen_.back().raw == raw) {
    unseen_.back().at = at;
    return;
  }
  if (unseen_.size() == kEchoBound)
    unseen_.pop_front();
  unseen_.push_back({raw, at});
}

bool Echoes::echoNear(std::uint16_t raw, std::uint16_t tolerance, SenderClock::time_point at) {
  expire(at);
  auto found = std::find_if(unseen_.begin(), unseen_.end(), [raw](const Sent& sent) { return sent.raw == raw; });
  if (found == unseen_.end())
    found = std::find_if(unseen_.begin(), unseen_.end(),
                         [&](const Sent& sent) { return distance(sent.raw, raw) <= tolerance; });
  if (found == unseen_.end())
    return false;
  unseen_.erase(unseen_.begin(), std::next(found));
  return true;
}

void Echoes::expire(SenderClock::time_point now) {
  while (!unseen_.empty() && now > unseen_.front().at && now - unseen_.front().at > kEchoWindow)
    unseen_.pop_front();
}

std::optional<std::uint8_t> Coalescer::push(std::uint8_t level, SenderClock::time_point now) {
  if (lastSent_ && now - *lastSent_ < interval_) {
    pending_ = level;
    return std::nullopt;
  }
  lastSent_ = now;
  pending_.reset();
  return level;
}

std::optional<std::uint8_t> Coalescer::poll(SenderClock::time_point now) {
  if (!lastSent_ || !pending_ || now - *lastSent_ < interval_)
    return std::nullopt;
  lastSent_ = now;
  return std::exchange(pending_, std::nullopt);
}

std::optional<SenderClock::time_point> Coalescer::deadline() const {
  if (!pending_ || !lastSent_)
    return std::nullopt;
  return *lastSent_ + interval_;
}

SoloistSession::SoloistSession(SenderGuard& guard) : guard_(guard) { guard_.connected(); }

SessionReaction SoloistSession::auth(bool loggedIn, bool active, SenderClock::time_point now) {
  return transition(loggedIn, active, now);
}

SessionReaction SoloistSession::device(bool active, SenderClock::time_point now) {
  return transition(loggedIn_, active, now);
}

// the guard is armed at every edge the app's stored level may follow: a login or an activation, and a logout or a
// lost activation, since that level may also come before the next login or activation event
SessionReaction SoloistSession::transition(bool loggedIn, bool active, SenderClock::time_point now) {
  const bool cameUp = (loggedIn && !loggedIn_) || (active && !active_);
  SessionReaction reaction;
  reaction.gone = active_ && !active;
  reaction.loggedOut = loggedIn_ && !loggedIn;
  loggedIn_ = loggedIn;
  active_ = active;
  if (cameUp || reaction.gone || reaction.loggedOut)
    guard_.connected();
  if (cameUp)
    if (const auto level = announce(now))
      reaction.send.push_back(*level);
  return reaction;
}

// our own level coming back is checked first: it must not use up the first-volume drop of a new session
SessionReaction SoloistSession::volume(std::uint8_t level, SenderClock::time_point now) {
  SessionReaction reaction;
  if (echoes_.echoNear(level, kSoloistEchoTolerance, now))
    return reaction;
  switch (guard_.incoming(level)) {
    case Incoming::Request:
      // a slider move needs a live session: from any other state a volume is the app's stored level
      if (loggedIn_ && active_)
        reaction.request = level;
      break;
    case Incoming::Echo:
      break;
    case Incoming::Dropped:
      if (const auto answer = announce(now))
        reaction.send.push_back(*answer);
      break;
  }
  return reaction;
}

std::optional<std::uint8_t> SoloistSession::speakerLevel(std::uint8_t level, SenderClock::time_point now) {
  guard_.setCurrent(level);
  return announce(now);
}

// logged out, a command only gets an error back, and the login's own announce sends the level anyway
std::optional<std::uint8_t> SoloistSession::announce(SenderClock::time_point now) {
  const auto level = guard_.current();
  if (!loggedIn_ || !level)
    return std::nullopt;
  echoes_.sent(*level, now);
  return level;
}

std::uint8_t initialVolume(const SenderGuard& guard, unsigned fallback) {
  if (const auto level = guard.current())
    return *level;
  return static_cast<std::uint8_t>(fallback <= kMaxSenderLevel ? fallback : kUnknownLevel);
}

}  // namespace cadence::audiod
