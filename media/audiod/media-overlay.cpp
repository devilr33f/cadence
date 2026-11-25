#include "audiod/media-overlay.h"

#include <utility>

namespace cadence::audiod {

// one overlay's reports, told apart by its serial; only whether it plays, ends or fails matters here
class MediaOverlay::Events final : public MediaSourceEvents {
 public:
  Events(MediaOverlay& overlay, std::uint64_t serial) : overlay_(overlay), serial_(serial) {}

  void playing() override { overlay_.report({serial_, EventKind::Playing, {}}); }
  void paused() override {}
  void buffering() override {}
  void ended() override { overlay_.report({serial_, EventKind::Ended, {}}); }
  void failed(std::string_view code) override { overlay_.report({serial_, EventKind::Failed, std::string(code)}); }
  void metadata(const MediaMetadata& /*metadata*/) override {}
  void duration(std::uint64_t /*ms*/) override {}
  void position(std::uint64_t /*ms*/) override {}

 private:
  MediaOverlay& overlay_;
  std::uint64_t serial_;
};

class MediaOverlay::Busy {
 public:
  explicit Busy(MediaOverlay& overlay) : overlay_(overlay) { ++overlay_.busy_; }
  ~Busy() {
    if (overlay_.busy_ > 1) {
      --overlay_.busy_;
      return;
    }
    while (!overlay_.pending_.empty()) {
      const auto event = std::move(overlay_.pending_.front());
      overlay_.pending_.pop_front();
      overlay_.handle(event);
    }
    --overlay_.busy_;
  }

  Busy(const Busy&) = delete;
  Busy& operator=(const Busy&) = delete;

 private:
  MediaOverlay& overlay_;
};

MediaOverlay::MediaOverlay(const Log& log, PcmInput& overlay, std::function<bool()> gateOpen,
                           MediaSourceFactory factory)
    : log_(log), overlay_(overlay), gateOpen_(std::move(gateOpen)), factory_(std::move(factory)) {}

// the overlay stops as it goes, and what it reports on the way is dropped
MediaOverlay::~MediaOverlay() {
  ++busy_;
  current_.reset();
  pending_.clear();
  overlay_.clear();
}

std::string_view MediaOverlay::play(const UrlRequest& request, Done done) {
  Busy busy(*this);
  if (!gateOpen_())
    return kMediaRefused;
  const auto serial = ++serials_;
  auto events = std::make_shared<Events>(*this, serial);
  auto named = request;
  named.logId = "o-" + std::to_string(serial);
  auto made = factory_(named, events);
  if (!made.ok())
    return made.error();
  if (current_)
    stopCurrent(kMediaReplaced, "replaced");
  current_ = Current{serial, std::move(named.logId), std::move(events), std::move(made.value()), std::move(done)};
  // the one before was done pushing when its stop returned: this one is the input's only producer
  current_->source->openOutput(overlay_);
  return kMediaOk;
}

void MediaOverlay::gateShut() {
  Busy busy(*this);
  if (current_)
    stopCurrent(kMediaRefused, "gated");
}

void MediaOverlay::report(Event event) {
  if (busy_ > 0) {
    pending_.push_back(std::move(event));
    return;
  }
  Busy busy(*this);
  handle(event);
}

void MediaOverlay::handle(const Event& event) {
  // a report a replaced or stopped overlay left behind
  if (!current_ || current_->serial != event.serial)
    return;
  switch (event.kind) {
    case EventKind::Playing:
      answer(*current_, kMediaOk);
      return;
    case EventKind::Ended:
      // one that never played answers failed; what it queued plays out
      answer(*current_, kMediaFailed);
      log_.info("media_overlay_end", {{"reason", "ended"}, {"src", current_->logId}});
      current_.reset();
      return;
    case EventKind::Failed:
      answer(*current_, event.code);
      log_.warn("media_overlay_end", {{"reason", "failed"}, {"code", event.code}, {"src", current_->logId}});
      current_.reset();
      return;
  }
}

void MediaOverlay::stopCurrent(std::string_view code, std::string_view reason) {
  current_->source->stop();
  answer(*current_, code);
  log_.info("media_overlay_end", {{"reason", reason}, {"src", current_->logId}});
  current_.reset();
  overlay_.clear();
}

void MediaOverlay::answer(Current& current, std::string_view code) {
  if (!current.done)
    return;
  const auto done = std::move(current.done);
  current.done = nullptr;
  done(code);
}

}  // namespace cadence::audiod
