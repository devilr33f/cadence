#include "audiod/keyword-capture.h"

#include "audiod/capture-engine.h"
#include "audiod/sparse.h"

#include <algorithm>
#include <chrono>
#include <system_error>
#include <utility>

namespace cadence::audiod {

namespace {

constexpr std::chrono::milliseconds kReadTimeout(20);
// a read that failed is tried again after this, as the vendor engines do
constexpr std::chrono::milliseconds kRetryDelay(100);
// one bad frame is skipped, a run of them ends the session's packets
constexpr std::uint64_t kEncodeFailureLimit = 5;

std::int64_t nowMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch())
      .count();
}

}  // namespace

// the model as the spotter calls it, timed and counted on the spotter thread
class KeywordCapture::Metered final : public kws::KwsModel {
 public:
  explicit Metered(std::unique_ptr<kws::KwsModel> model) : model_(std::move(model)) {
    timings_.reserve(kKeywordStartedCalls);
  }

  Result<kws::Outputs, std::string_view> invoke(std::span<const float, kFrames * kMels> features) override {
    const auto began = std::chrono::steady_clock::now();
    auto outputs = model_->invoke(features);
    if (timings_.size() < kKeywordStartedCalls)
      timings_.push_back(std::chrono::steady_clock::now() - began);
    ++calls_;
    if (outputs.ok()) {
      streak_ = 0;
    } else {
      ++failed_;
      ++streak_;
    }
    return outputs;
  }

  [[nodiscard]] std::uint64_t calls() const { return calls_; }
  [[nodiscard]] std::uint64_t failed() const { return failed_; }
  // the failed calls since the last that answered
  [[nodiscard]] std::uint64_t streak() const { return streak_; }
  // the median of the first kKeywordStartedCalls calls, once there were as many
  [[nodiscard]] std::optional<std::chrono::nanoseconds> median() const {
    if (timings_.size() < kKeywordStartedCalls)
      return std::nullopt;
    auto sorted = timings_;
    const auto middle = sorted.begin() + static_cast<std::ptrdiff_t>(sorted.size() / 2);
    std::nth_element(sorted.begin(), middle, sorted.end());
    return *middle;
  }

 private:
  std::unique_ptr<kws::KwsModel> model_;
  std::vector<std::chrono::nanoseconds> timings_;
  std::uint64_t calls_ = 0;
  std::uint64_t failed_ = 0;
  std::uint64_t streak_ = 0;
};

KeywordCapture::KeywordCapture(std::unique_ptr<PcmSource> source, KeywordSetup setup, KeywordEvents events,
                               const Log& log, std::size_t queueFrames)
    : source_(std::move(source)),
      events_(std::move(events)),
      log_(log),
      queueFrames_(queueFrames),
      arenaBytes_(setup.model.arenaBytes),
      spotter_(
          [&] {
            auto metered = std::make_unique<Metered>(std::move(setup.model.model));
            metered_ = metered.get();
            return metered;
          }(),
          kws::KwsFrontend(kws::FrontendParams::defaultCapsula()), setup.policy) {}

KeywordCapture::~KeywordCapture() { stop(); }

std::optional<std::string_view> KeywordCapture::start(const CaptureLayout& layout) {
  layout_ = layout;
  auto downmix = MicDownmix::create(layout_);
  if (!downmix.ok())
    return std::string_view("format");
  downmix_.emplace(std::move(downmix.value()));
  const auto granted = source_->open({.sampleRate = layout_.sampleRate,
                                      .channels = layout_.channels,
                                      .periodFrames = layout_.periodFrames,
                                      .bufferFrames = layout_.bufferFrames});
  if (!granted.ok())
    return std::string_view(granted.error().code == CaptureErrorCode::FormatMismatch ? "format" : "open");
  opened_ = true;
  ring_.resize(std::max<std::size_t>(queueFrames_, 1));
  try {
    listener_ = std::jthread([this](std::stop_token stop) { listen(stop); });
    reader_ = std::jthread([this](std::stop_token stop) { read(stop); });
  } catch (const std::system_error&) {
    stop();
    return std::string_view("thread");
  }
  return std::nullopt;
}

void KeywordCapture::stop() {
  if (stopped_)
    return;
  stopped_ = true;
  reader_.request_stop();
  listener_.request_stop();
  if (reader_.joinable())
    reader_.join();
  if (listener_.joinable())
    listener_.join();
  if (opened_)
    source_->close();
}

void KeywordCapture::setMuted(bool muted) {
  const auto epoch = muteEpoch_.load();
  if ((epoch % 2 == 1) != muted)
    muteEpoch_.store(epoch + 1);
}

void KeywordCapture::open(std::uint64_t session, bool preroll, std::int64_t prerollFromMs) {
  prerollFrom_.store(prerollFromMs);
  prerollFor_.store(preroll || prerollFromMs > 0 ? session : 0);
  session_.store(session);
}

void KeywordCapture::close() {
  session_.store(0);
  forget_.store(true);
  rearm_.store(true);
}

KeywordStats KeywordCapture::stats() const {
  return {.calls = metered_->calls(),
          .failedCalls = metered_->failed(),
          .wakes = wakes_,
          .droppedFrames = dropped_,
          .recoveries = recoveries_,
          .readErrors = readErrors_};
}

void KeywordCapture::read(const std::stop_token& stop) {
  std::vector<std::int16_t> captured(layout_.periodFrames * layout_.channels);
  // 16 kHz mono not yet in a frame
  std::vector<std::int16_t> mono;
  std::array<std::int16_t, kEncoderFrameSamples> frame{};
  std::uint64_t seenEpoch = muteEpoch_.load();
  // after a mute the device's buffer may still hold audio heard while muted
  std::size_t silence = 0;
  bool gap = false;
  std::mutex retryMutex;
  std::condition_variable_any retry;
  while (!stop.stop_requested()) {
    const auto got = source_->read(captured, kReadTimeout);
    switch (got.kind) {
      case CaptureRead::Kind::Idle:
        break;
      case CaptureRead::Kind::Frames: {
        const auto frames = std::min(got.frames, layout_.periodFrames);
        const auto samples = std::span(captured).first(frames * layout_.channels);
        const auto epoch = muteEpoch_.load();
        const bool muted = epoch % 2 == 1;
        const bool unmutedSince = !muted && epoch != seenEpoch;
        seenEpoch = epoch;
        // muted now: nothing of it reaches the spotter, the kept frames or a session
        if (muted) {
          mono.clear();
          preroll_.clear();
          gap = true;
          silence = layout_.bufferFrames;
          break;
        }
        // muted at some point since the last read: some of it, and of the buffer after it, may be muted audio
        if (unmutedSince) {
          mono.clear();
          preroll_.clear();
          gap = true;
          std::fill(samples.begin(), samples.end(), std::int16_t{0});
          silence = layout_.bufferFrames;
        } else if (silence > 0) {
          const auto silenced = std::min(frames, silence);
          std::fill_n(samples.begin(), silenced * layout_.channels, std::int16_t{0});
          silence -= silenced;
        }
        downmix_->push(samples, mono);
        const auto atMs = nowMs();
        std::size_t used = 0;
        for (; mono.size() - used >= frame.size(); used += frame.size()) {
          std::copy_n(mono.begin() + static_cast<std::ptrdiff_t>(used), frame.size(), frame.begin());
          heard(frame, atMs, std::exchange(gap, false));
        }
        mono.erase(mono.begin(), mono.begin() + static_cast<std::ptrdiff_t>(used));
        break;
      }
      case CaptureRead::Kind::Recovered:
        if (sparse(++recoveries_))
          log_.warn("capture_recovered", {{"count", recoveries_}});
        mono.clear();
        gap = true;
        break;
      case CaptureRead::Kind::Failed: {
        if (sparse(++readErrors_))
          log_.warn("capture_read_failed", {{"count", readErrors_}});
        mono.clear();
        gap = true;
        std::unique_lock lock(retryMutex);
        retry.wait_for(lock, stop, kRetryDelay, [] { return false; });
        break;
      }
    }
  }
}

void KeywordCapture::heard(const std::array<std::int16_t, kEncoderFrameSamples>& pcm, std::int64_t atMs, bool gap) {
  if (forget_.exchange(false)) {
    preroll_.clear();
    listenGap_ = true;
  }
  const auto session = session_.load();
  const Frame frame{.pcm = pcm, .atMs = atMs, .seq = ++seq_, .session = session, .gap = gap};
  if (session == 0) {
    preroll_.push_back(frame);
    if (preroll_.size() > kPrerollFrames)
      preroll_.pop_front();
    if (listen_.load() && events_.pcm) {
      const bool after = std::exchange(listenGap_, false) || gap;
      events_.pcm(seq_, atMs, std::vector<std::int16_t>(pcm.begin(), pcm.end()), after);
    }
  } else {
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
        // only what followed the keyword: the frame that completed the detection and those before it stay out
        const auto since = wakeSeq_.load();
        const auto from = prerollFrom_.load();
        for (const auto& kept : preroll_)
          if (from > 0 ? kept.atMs >= from : kept.seq > since)
            encode(session, kept.pcm);
      }
      preroll_.clear();
    }
    encode(session, frame.pcm);
  }
  queue(frame);
}

void KeywordCapture::encode(std::uint64_t session, std::span<const std::int16_t> pcm) {
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

void KeywordCapture::queue(const Frame& frame) {
  std::uint64_t burst = 0;
  {
    const std::lock_guard lock(queueMutex_);
    if (closed_)
      return;
    if (size_ == ring_.size()) {
      // the oldest goes, and the spotter re-arms before the one after it
      head_ = (head_ + 1) % ring_.size();
      --size_;
      ring_[head_].gap = true;
      ++dropped_;
      ++burstDrops_;
    } else if (burstDrops_ > 0) {
      burst = std::exchange(burstDrops_, 0);
    }
    ring_[(head_ + size_) % ring_.size()] = frame;
    ++size_;
  }
  queued_.notify_one();
  if (burst > 0)
    log_.warn("kws_frames_dropped", {{"count", burst}});
}

void KeywordCapture::listen(const std::stop_token& stop) {
  Frame frame;
  for (;;) {
    {
      std::unique_lock lock(queueMutex_);
      // the wait answers true for a queue that still holds frames once a stop is asked: a stop drains nothing more
      if (!queued_.wait(lock, stop, [this] { return size_ > 0; }) || stop.stop_requested())
        return;
      frame = ring_[head_];
      head_ = (head_ + 1) % ring_.size();
      --size_;
    }
    if (rearm_.exchange(false) || frame.gap)
      spotter_.reset();
    const auto detection = spotter_.push(frame.pcm, frame.atMs);
    if (!calls())
      return;
    if (detection) {
      wakeSeq_.store(frame.seq);
      ++wakes_;
      events_.wake(detection->score, detection->atMs, frame.session != 0);
    }
  }
}

bool KeywordCapture::calls() {
  if (!started_) {
    if (const auto median = metered_->median()) {
      started_ = true;
      const auto ms = std::chrono::duration_cast<std::chrono::milliseconds>(*median + std::chrono::microseconds(500));
      log_.info("kws_started", {{"arena_kb", (arenaBytes_ + 1023) / 1024}, {"invoke_ms", ms.count()}});
    }
  }
  if (metered_->streak() < kKeywordFailureLimit)
    return true;
  log_.error("kws_failed", {{"failed_calls", metered_->streak()}});
  {
    const std::lock_guard lock(queueMutex_);
    closed_ = true;
    size_ = 0;
  }
  events_.stopped("invoke_failed");
  return false;
}

}  // namespace cadence::audiod
