#include "audiod/media-sessions.h"

#include "text.h"

#include <algorithm>
#include <chrono>
#include <utility>

namespace cadence::audiod {

namespace {

std::string_view kindToken(MediaKind kind) {
  switch (kind) {
    case MediaKind::Url:
      return "url";
    case MediaKind::Soloist:
      return "soloist";
    case MediaKind::Airplay:
      return "airplay";
    case MediaKind::Stream:
      return "stream";
    case MediaKind::Video:
      return "video";
    case MediaKind::Screencast:
      return "screencast";
    case MediaKind::External:
      return "external";
    case MediaKind::Group:
      return "group";
    case MediaKind::Bluetooth:
      return "bluetooth";
  }
  return "unknown";
}

MediaMetadata bounded(const MediaMetadata& metadata) {
  return {text::boundedUtf8(metadata.title, kMetadataTextBytes),
          text::boundedUtf8(metadata.artist, kMetadataTextBytes),
          text::boundedUtf8(metadata.album, kMetadataTextBytes),
          text::boundedUtf8(metadata.artworkUrl, kArtworkUrlBytes),
          text::boundedUtf8(metadata.artworkId, kArtworkIdBytes),
          text::boundedUtf8(metadata.sender, kMetadataTextBytes)};
}

// what a MediaState says apart from where the position stands
bool sameButPosition(const MediaSession& a, const MediaSession& b) {
  return a.id == b.id && a.kind == b.kind && a.state == b.state && a.durationMs == b.durationMs &&
         a.metadata == b.metadata && a.actions == b.actions && a.route == b.route && a.controls == b.controls &&
         a.externalId == b.externalId;
}

}  // namespace

std::string_view MediaSource::command(MediaAction action, std::uint64_t positionMs, CommandDone /*done*/) {
  switch (action) {
    case MediaAction::Pause:
      return pause();
    case MediaAction::Resume:
      return resume();
    case MediaAction::Stop:
      return stop();
    case MediaAction::Seek:
      return seek(positionMs);
    case MediaAction::Next:
      return next();
    case MediaAction::Previous:
      return previous();
  }
  return kMediaUnsupported;
}

Result<std::unique_ptr<MediaSource>, std::string_view> refuseUrls(const UrlRequest& /*request*/,
                                                                  std::shared_ptr<MediaSourceEvents> /*events*/) {
  return kMediaUnsupported;
}

std::int64_t unixNowMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch())
      .count();
}

// one source's reports, told apart by its serial
class MediaSessions::Events final : public MediaSourceEvents {
 public:
  Events(MediaSessions& sessions, std::uint64_t serial) : sessions_(sessions), serial_(serial) {}

  void playing() override { report(EventKind::Playing); }
  void paused() override { report(EventKind::Paused); }
  void buffering() override { report(EventKind::Buffering); }
  void ended() override { report(EventKind::Ended); }
  void failed(std::string_view code) override {
    Event event{serial_, EventKind::Failed, 0, std::string(code), {}};
    sessions_.report(std::move(event));
  }
  void metadata(const MediaMetadata& metadata) override {
    Event event{serial_, EventKind::Metadata, 0, {}, bounded(metadata)};
    sessions_.report(std::move(event));
  }
  void duration(std::uint64_t ms) override { report(EventKind::Duration, ms); }
  void position(std::uint64_t ms) override { report(EventKind::Position, ms); }

 private:
  void report(EventKind kind, std::uint64_t value = 0) { sessions_.report({serial_, kind, value, {}, {}}); }

  MediaSessions& sessions_;
  std::uint64_t serial_;
};

class MediaSessions::Busy {
 public:
  explicit Busy(MediaSessions& sessions) : sessions_(sessions) { ++sessions_.busy_; }
  ~Busy() {
    if (sessions_.busy_ > 1) {
      --sessions_.busy_;
      return;
    }
    // still busy while the reports that came in meanwhile are handled, so theirs queue behind them
    while (!sessions_.pending_.empty()) {
      const auto event = std::move(sessions_.pending_.front());
      sessions_.pending_.pop_front();
      sessions_.handle(event);
    }
    --sessions_.busy_;
  }

  Busy(const Busy&) = delete;
  Busy& operator=(const Busy&) = delete;

 private:
  MediaSessions& sessions_;
};

MediaSessions::MediaSessions(const Log& log, PcmInput& music, std::function<bool()> gateOpen,
                             MediaSourceFactory factory, Publish publish, WallClock now)
    : log_(log),
      music_(&music),
      home_(&music),
      gateOpen_(std::move(gateOpen)),
      factory_(std::move(factory)),
      publish_(std::move(publish)),
      now_(std::move(now)) {}

// the sources stop as they go, and what they report on the way is dropped
MediaSessions::~MediaSessions() {
  ++busy_;
  entries_.clear();
  pending_.clear();
  music_->clear();
}

std::string_view MediaSessions::play(const UrlRequest& request, Done done) {
  Busy busy(*this);
  if (!gateOpen_())
    return kMediaRefused;
  const auto serial = ++serials_;
  auto events = std::make_shared<Events>(*this, serial);
  auto named = request;
  named.logId = "s-" + std::to_string(serial);
  auto made = factory_(named, events);
  if (!made.ok())
    return made.error();
  dropRequested(kMediaReplaced);
  Entry entry;
  entry.serial = serial;
  entry.logId = std::move(named.logId);
  entry.events = std::move(events);
  entry.source = std::move(made.value());
  entry.requested = true;
  entry.done = std::move(done);
  entry.metadata = bounded(request.metadata);
  entries_.push_back(std::move(entry));
  return kMediaOk;
}

void MediaSessions::attach(const LongLivedSource& make) {
  Busy busy(*this);
  const auto serial = ++serials_;
  auto events = std::make_shared<Events>(*this, serial);
  Entry entry;
  entry.serial = serial;
  entry.logId = "s-" + std::to_string(serial);
  entry.events = events;
  entry.source = make(std::move(events));
  entry.attached = true;
  entries_.push_back(std::move(entry));
}

std::uint64_t MediaSessions::add(const LongLivedSource& make) {
  Busy busy(*this);
  const auto serial = ++serials_;
  auto events = std::make_shared<Events>(*this, serial);
  Entry entry;
  entry.serial = serial;
  entry.logId = "s-" + std::to_string(serial);
  entry.events = events;
  entry.source = make(std::move(events));
  entries_.push_back(std::move(entry));
  return serial;
}

void MediaSessions::remove(std::uint64_t serial) {
  Busy busy(*this);
  if (owner_ == serial) {
    end("removed", true);
    return;
  }
  destroy(serial);
}

void MediaSessions::retarget(PcmInput& music) {
  Busy busy(*this);
  if (&music == music_)
    return;
  auto* current = owner();
  const bool open = current != nullptr && current->outputOpen;
  if (open)
    closeOutput(*current);
  music_->clear();
  music_ = &music;
  if (open)
    openOutput(*current);
  log_.info("group_source", {{"kind", current != nullptr ? kindToken(current->source->kind()) : "none"},
                             {"target", music_ == home_ ? "music" : "feed"}});
}

MediaSource* MediaSessions::ownerSource() {
  auto* current = owner();
  return current == nullptr ? nullptr : current->source.get();
}

std::string_view MediaSessions::command(MediaAction action, std::uint64_t positionMs, CommandDone later) {
  Busy busy(*this);
  auto* current = owner();
  if (current == nullptr) {
    if (action != MediaAction::Stop ||
        std::none_of(entries_.begin(), entries_.end(), [](const Entry& entry) { return entry.requested; }))
      return kMediaNoSession;
    dropRequested(kMediaStopped);
    return kMediaOk;
  }
  auto& source = *current->source;
  if (!source.actions().has(action))
    return kMediaUnsupported;
  switch (action) {
    case MediaAction::Pause:
    case MediaAction::Next:
    case MediaAction::Previous:
      return source.command(action, 0, std::move(later));
    case MediaAction::Resume:
      if (!gateOpen_())
        return kMediaRefused;
      // the gate closed its output
      if (!current->outputOpen) {
        music_->clear();
        openOutput(*current);
      }
      return source.command(action, 0, std::move(later));
    case MediaAction::Stop: {
      const auto code = source.command(action, 0, std::move(later));
      if (code.empty()) {
        dropRequested(kMediaStopped);
        end("stopped", true);
      }
      return code;
    }
    case MediaAction::Seek:
      if (current->durationMs > 0 && positionMs > current->durationMs)
        return kMediaInvalid;
      return source.command(action, positionMs, std::move(later));
  }
  return kMediaUnsupported;
}

void MediaSessions::gateShut() {
  Busy busy(*this);
  dropRequested(kMediaRefused);
  auto* current = owner();
  if (current == nullptr)
    return;
  current->source->setAsideBy("gated");
  if (current->source->keepsPaused()) {
    current->source->pause();
    // silent even when the pause failed
    closeOutput(*current);
    music_->clear();
    // paused for sure, whenever the source says so
    moveTo(*current, PlayState::Paused);
    publish(false);
    return;
  }
  current->source->stop();
  end("gated", true);
}

void MediaSessions::report(Event event) {
  if (busy_ > 0) {
    pending_.push_back(std::move(event));
    return;
  }
  Busy busy(*this);
  handle(event);
}

void MediaSessions::handle(const Event& event) {
  auto* entry = find(event.serial);
  // a report a destroyed source left behind
  if (entry == nullptr)
    return;
  const bool owns = owner_ == event.serial;
  switch (event.kind) {
    case EventKind::Playing:
      playing(*entry);
      return;
    case EventKind::Paused:
      moveTo(*entry, PlayState::Paused);
      break;
    case EventKind::Buffering:
      moveTo(*entry, PlayState::Buffering);
      break;
    case EventKind::Ended:
    case EventKind::Failed:
      if (event.kind == EventKind::Failed)
        log_.warn("media_failed", {{"code", event.code}, {"src", entry->logId}});
      if (owns) {
        end(event.kind == EventKind::Failed ? "failed" : "ended", false);
        return;
      }
      // a url that never played answers with why
      answer(*entry, event.kind == EventKind::Failed ? std::string_view(event.code) : kMediaFailed);
      destroy(event.serial);
      return;
    case EventKind::Metadata:
      entry->metadata = event.metadata;
      break;
    case EventKind::Duration:
      entry->durationMs = event.value;
      break;
    case EventKind::Position:
      entry->positionMs = event.value;
      entry->positionAtMs = now_();
      if (owns)
        publish(true);
      return;
  }
  if (owns)
    publish(false);
}

void MediaSessions::playing(Entry& entry) {
  // nothing plays while the gate is shut, also not a source that starts again on its own
  if (!gateOpen_()) {
    entry.source->setAsideBy("gated");
    if (entry.source->keepsPaused()) {
      entry.source->pause();
      closeOutput(entry);
      return;
    }
    entry.source->stop();
    if (owner_ == entry.serial) {
      end("gated", true);
      return;
    }
    answer(entry, kMediaRefused);
    destroy(entry.serial);
    return;
  }
  if (owner_ == entry.serial) {
    // it plays again on its own after the gate closed its output
    if (!entry.outputOpen) {
      music_->clear();
      openOutput(entry);
    }
    moveTo(entry, PlayState::Playing);
    publish(false);
    return;
  }
  if (auto* previous = owner()) {
    log_.info("media_session_end", {{"id", sessionId_}, {"reason", "displaced"}});
    owner_.reset();
    setAside(*previous);
  }
  // what the last producer left queued never plays after the winner's audio
  music_->clear();
  entry.requested = false;
  owner_ = entry.serial;
  sessionId_ = entry.source->sessionId().empty() ? "ms-" + std::to_string(++sessions_)
                                                 : std::string(entry.source->sessionId());
  openOutput(entry);
  moveTo(entry, PlayState::Playing);
  log_.info("media_session_start",
            {{"source", kindToken(entry.source->kind())}, {"id", sessionId_}, {"src", entry.logId}});
  answer(entry, kMediaOk);
  publish(false);
}

// the position runs while playing: leaving that state takes where it got to
void MediaSessions::moveTo(Entry& entry, PlayState state) {
  if (entry.state == state)
    return;
  const auto now = now_();
  if (entry.state == PlayState::Playing && now > entry.positionAtMs) {
    entry.positionMs += static_cast<std::uint64_t>(now - entry.positionAtMs);
    if (entry.durationMs > 0)
      entry.positionMs = std::min(entry.positionMs, entry.durationMs);
  }
  entry.positionAtMs = now;
  entry.state = state;
}

void MediaSessions::end(std::string_view reason, bool cut) {
  log_.info("media_session_end", {{"id", sessionId_}, {"reason", reason}});
  const auto serial = *owner_;
  owner_.reset();
  destroy(serial);
  if (cut)
    music_->clear();
  publish(false);
}

void MediaSessions::setAside(Entry& entry) {
  entry.source->setAsideBy("displaced");
  if (entry.source->keepsPaused()) {
    entry.source->pause();
    closeOutput(entry);
    // its position holds from here, should it take the session back
    moveTo(entry, PlayState::Paused);
    entry.setAside = ++setAsides_;
    trimKept();
    return;
  }
  entry.source->stop();
  destroy(entry.serial);
}

void MediaSessions::trimKept() {
  const auto waiting = [this](const Entry& entry) { return owner_ != entry.serial && !entry.requested; };
  while (static_cast<std::size_t>(std::count_if(entries_.begin(), entries_.end(), waiting)) > kMaxKeptSources) {
    Entry* oldest = nullptr;
    for (auto& entry : entries_)
      if (waiting(entry) && !entry.attached && (oldest == nullptr || entry.setAside < oldest->setAside))
        oldest = &entry;
    if (oldest == nullptr)
      return;
    log_.info("media_source_dropped", {{"src", oldest->logId}, {"reason", "kept"}});
    oldest->source->stop();
    destroy(oldest->serial);
  }
}

void MediaSessions::destroy(std::uint64_t serial) {
  auto* entry = find(serial);
  if (entry == nullptr)
    return;
  if (!entry->attached) {
    entries_.remove_if([serial](const Entry& candidate) { return candidate.serial == serial; });
    return;
  }
  closeOutput(*entry);
  entry->state = PlayState::Idle;
  entry->positionMs = 0;
  entry->positionAtMs = 0;
  entry->durationMs = 0;
  entry->metadata = {};
}

void MediaSessions::answer(Entry& entry, std::string_view code) {
  if (!entry.done)
    return;
  const auto done = std::move(entry.done);
  entry.done = nullptr;
  done(code);
}

void MediaSessions::openOutput(Entry& entry) {
  // a passthrough session (its bitstream to the tv's own decoder) cannot be mixed nor encoded: it plays here alone
  // and the group stays idle (D4)
  if (music_ != home_ && entry.source->audioRoute() == AudioRoute::Direct)
    log_.info("group_source_local", {{"reason", "direct"}});
  entry.source->openOutput(*music_);
  entry.outputOpen = true;
}

void MediaSessions::closeOutput(Entry& entry) {
  if (!entry.outputOpen)
    return;
  entry.source->closeOutput();
  entry.outputOpen = false;
}

void MediaSessions::dropRequested(std::string_view code) {
  for (auto entry = entries_.begin(); entry != entries_.end();) {
    if (!entry->requested) {
      ++entry;
      continue;
    }
    entry->source->stop();
    answer(*entry, code);
    entry = entries_.erase(entry);
  }
}

void MediaSessions::publish(bool discontinuity) {
  auto current = snapshot();
  if (!discontinuity && sameButPosition(current, published_))
    return;
  published_ = std::move(current);
  publish_(published_);
}

MediaSessions::Entry* MediaSessions::find(std::uint64_t serial) {
  const auto found =
      std::find_if(entries_.begin(), entries_.end(), [serial](const Entry& entry) { return entry.serial == serial; });
  return found == entries_.end() ? nullptr : &*found;
}

MediaSessions::Entry* MediaSessions::owner() { return owner_ ? find(*owner_) : nullptr; }

MediaSession MediaSessions::snapshot() {
  const auto* current = owner();
  if (current == nullptr)
    return {};
  return {sessionId_,
          current->source->kind(),
          current->state,
          current->positionMs,
          current->positionAtMs,
          current->durationMs,
          current->metadata,
          current->source->actions(),
          current->source->audioRoute(),
          current->source->audioControls(),
          std::string(current->source->sessionId())};
}

}  // namespace cadence::audiod
