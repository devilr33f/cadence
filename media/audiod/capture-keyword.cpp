#include "audiod/capture.h"

#include "audiod/sparse.h"

#include <utility>

namespace cadence::audiod {

void Capture::startKeyword() {
  auto setup = resolveKeyword(config_->keyword, vendor_.keywordModel);
  if (!setup.ok())
    return fallBack(setup.error());
  const auto layout = captureLayout(*config_);
  if (!layout.ok())
    return fallBack("layout_invalid");
  auto source = open_(*config_);
  if (!source.ok()) {
    log_.warn("capture_keyword_failed", {{"stage", "source"}, {"reason", source.error()}});
    return fallBack("start_failed");
  }
  keyword_ = std::make_unique<KeywordCapture>(std::move(source.value()), std::move(setup.value()),
                                              keywordEvents(epoch_), log_, vendor_.keywordQueueFrames);
  keyword_->setMuted(muted_.load());
  keyword_->setListen(listen_);
  if (const auto failed = keyword_->start(layout.value())) {
    log_.warn("capture_keyword_failed", {{"stage", *failed}});
    keyword_.reset();
    return fallBack("start_failed");
  }
  mode_ = Mode::Keyword;
  log_.info("capture_keyword_started", {{"engine", kKeywordKind}});
  report({.engines = true, .code = "keyword"});
}

KeywordEvents Capture::keywordEvents(std::uint64_t epoch) {
  return {
      .wake =
          [this, epoch](float score, std::int64_t atMs, bool inSession) {
            loop_.post([this, epoch, score, atMs, inSession] {
              if (epoch == epoch_)
                keywordWake(score, atMs, inSession);
            });
          },
      .packet = packetSink(),
      .failed = failureSink(),
      .stopped =
          [this, epoch](std::string_view code) {
            loop_.post([this, epoch, code] {
              if (epoch == epoch_)
                keywordStopped(code);
            });
          },
      .pcm = pcmSink(epoch),
  };
}

void Capture::keywordWake(float score, std::int64_t atMs, bool inSession) {
  // the stock stops listening for a turn; the spotter runs on through one, so the session's end only re-arms it. a
  // turn's audio wakes nothing, even when its detection comes after the session closed; the mute switch lets no wake
  // out, one the spotter heard before it either
  if (inSession || streaming_ || muted_.load()) {
    if (sparse(++heldWakes_))
      log_.info("wake_held", {{"engine", kKeywordKind}, {"count", heldWakes_}});
    return;
  }
  sendWake(kKeywordKind, score, atMs * 1'000'000);
}

void Capture::keywordStopped(std::string_view code) {
  const bool live = streaming_;
  stopKeyword();
  streaming_ = false;
  mode_ = Mode::Raw;
  report({.engines = false, .code = code});
  // the open session goes on, raw, its seq numbered on
  if (live)
    runRaw(generation_);
  settleListen();
}

void Capture::stopKeyword() {
  keyword_->stop();
  const auto stats = keyword_->stats();
  log_.info("capture_keyword_stopped", {{"calls", stats.calls},
                                        {"failed_calls", stats.failedCalls},
                                        {"wakes", stats.wakes},
                                        {"held_wakes", heldWakes_},
                                        {"dropped_frames", stats.droppedFrames},
                                        {"recoveries", stats.recoveries},
                                        {"read_errors", stats.readErrors}});
  keyword_.reset();
  heldWakes_ = 0;
}

}  // namespace cadence::audiod
