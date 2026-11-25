#include "audiod/capture-engine.h"

#include "sync/clock-socket.h"
#include "trace/trace.h"

#include <chrono>
#include <utility>

namespace cadence::audiod {

namespace {

// one bad frame is skipped, a run of them ends the session's packets
constexpr std::uint64_t kEncodeFailureLimit = 5;

std::int64_t nowMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch())
      .count();
}

}  // namespace

CaptureEngine::CaptureEngine(std::unique_ptr<PcmSource> source, EngineEvents events, CommandWords words,
                             StockLibraryLoader loader)
    : source_(std::move(source)),
      events_(std::move(events)),
      words_(std::move(words)),
      engine_(*source_,
              AudioEngineCallbacks{.onWake = [this] { events_.wake(trace::unixNowNs()); },
                                   .onPcm16 = [this](std::span<const std::int16_t> pcm) { frame(pcm); },
                                   .onCommand = [this](const std::string& phrase) { command(phrase); },
                                   .onPlaybackLevels = {}},
              std::move(loader)) {}

CaptureEngine::~CaptureEngine() { engine_.stop(); }

Result<AudioEngineInfo, AudioEngineError> CaptureEngine::start(const AudioEngineConfig& config) {
  return engine_.start(config);
}

void CaptureEngine::setMuted(bool muted) {
  engine_.setPhysicalMuted(muted);
  // no frame is delivered once the mute returned, so none reaches the kept output before it is forgotten
  if (muted)
    forget_.store(true);
  else if (session_.load() != 0 || listen_.load())
    engine_.setStreaming(true);
}

void CaptureEngine::open(std::uint64_t session, bool preroll, std::int64_t prerollFromMs) {
  prerollFrom_.store(prerollFromMs);
  prerollFor_.store(preroll || prerollFromMs > 0 ? session : 0);
  session_.store(session);
  engine_.setStreaming(true);
}

void CaptureEngine::close() {
  session_.store(0);
  // listening, the output keeps streaming between sessions
  engine_.setStreaming(listen_.load());
  forget_.store(true);
}

void CaptureEngine::setListen(bool listen) {
  listen_.store(listen);
  if (session_.load() == 0)
    engine_.setStreaming(listen);
}

void CaptureEngine::setTap(Tap tap) {
  if (!tap) {
    engine_.setRawTap({});
    return;
  }
  engine_.setRawTap([tap = std::move(tap)](std::span<const std::int16_t> pcm, std::size_t channels) {
    tap(pcm, channels, sync::monoNs());
  });
}

void CaptureEngine::frame(std::span<const std::int16_t> pcm) {
  if (forget_.exchange(false)) {
    preroll_.clear();
    prerollAt_.clear();
    listenGap_ = true;
  }
  const auto session = session_.load();
  if (session == 0) {
    const auto at = nowMs();
    preroll_.emplace_back(pcm.begin(), pcm.end());
    prerollAt_.push_back(at);
    if (preroll_.size() > kPrerollFrames) {
      preroll_.pop_front();
      prerollAt_.pop_front();
    }
    if (listen_.load() && events_.pcm)
      events_.pcm(++listened_, at, std::vector<std::int16_t>(pcm.begin(), pcm.end()), std::exchange(listenGap_, false));
    return;
  }
  listenGap_ = true;
  if (session != encoding_) {
    encoding_ = session;
    encodeStreak_ = 0;
    encodeFailed_ = false;
    encoder_ = std::make_unique<OpusEncoder>();
    if (!encoder_->open().ok()) {
      encodeFailed_ = true;
      events_.failed(session);
    }
    if (prerollFor_.load() == session) {
      const auto from = prerollFrom_.load();
      for (std::size_t i = 0; i < preroll_.size(); ++i)
        if (from == 0 || prerollAt_[i] >= from)
          encode(session, preroll_[i]);
    }
    preroll_.clear();
    prerollAt_.clear();
  }
  encode(session, pcm);
}

void CaptureEngine::encode(std::uint64_t session, std::span<const std::int16_t> pcm) {
  if (encodeFailed_)
    return;
  auto packet = encoder_->encode(pcm);
  if (!packet.ok()) {
    if (++encodeStreak_ == kEncodeFailureLimit) {
      encodeFailed_ = true;
      events_.failed(session);
    }
    return;
  }
  encodeStreak_ = 0;
  events_.packet(session, std::move(packet.value()));
}

void CaptureEngine::command(const std::string& phrase) {
  if (const auto id = words_.id(phrase))
    events_.command(*id, nowMs());
  else
    events_.unknownCommand();
}

}  // namespace cadence::audiod
