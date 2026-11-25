#include "url/ring-stream.h"

#include "url/ffmpeg-options.h"
#include "url/url-input.h"
#include "url/url-source.h"

#include <algorithm>
#include <atomic>
#include <cmath>
#include <condition_variable>
#include <deque>
#include <limits>
#include <mutex>
#include <string>
#include <thread>
#include <utility>
#include <vector>

namespace cadence::url {

namespace {

using Clock = std::chrono::steady_clock;

constexpr std::uint32_t kRingRate = 48000;
constexpr std::string_view kInvalid = "invalid";
constexpr std::string_view kUnsupported = "unsupported";

// what the stream's handle and its thread share
struct Shared {
  Shared(audiod::RingStreamRequest given, RingStreamOptions opts) : request(std::move(given)), options(opts) {}

  const audiod::RingStreamRequest request;
  const RingStreamOptions options;

  std::mutex mutex;
  std::condition_variable changed;
  std::deque<std::int16_t> ahead;
  // the thread decoded this pass to its end
  bool passDone = false;
  // the reader looped back: the thread opens the url again
  bool wantPass = true;
  // the first pass, while it still fits keepSamples, and then whether it was whole
  std::vector<std::int16_t> kept;
  bool keeping = true;
  bool keptWhole = false;
  std::size_t keptAt = 0;

  // read by ffmpeg's interrupt callback
  std::atomic<bool> stop{false};
  std::atomic<Clock::rep> progress{0};
  // a static code's text, null while it has not failed
  std::atomic<const char*> failure{nullptr};
  std::atomic<std::size_t> failureSize{0};

  void fail(std::string_view code) {
    failureSize.store(code.size());
    failure.store(code.data());
    changed.notify_all();
  }

  void moved() { progress.store(Clock::now().time_since_epoch().count()); }

  [[nodiscard]] bool stalled() const {
    return Clock::now().time_since_epoch().count() - progress.load() >
           std::chrono::duration_cast<Clock::duration>(options.stall).count();
  }
};

// samples of a frame mixed down to mono s16
void mixDown(std::span<const float> samples, std::size_t channels, std::vector<std::int16_t>& out) {
  out.clear();
  const auto frames = samples.size() / channels;
  out.reserve(frames);
  for (std::size_t frame = 0; frame < frames; ++frame) {
    float sum = 0;
    for (std::size_t channel = 0; channel < channels; ++channel)
      sum += samples[frame * channels + channel];
    const float mono = std::clamp(sum / static_cast<float>(channels), -1.0f, 1.0f);
    out.push_back(static_cast<std::int16_t>(std::lround(mono * std::numeric_limits<std::int16_t>::max())));
  }
}

// one pass of the url into the shared buffer; false when the stream failed or stopped
bool pass(Shared& shared) {
  shared.moved();
  auto options = ffmpegOptions(shared.request.caFile);
  if (!shared.request.authorization.empty())
    options.push_back({"headers", "Authorization: " + shared.request.authorization + "\r\n"});
  auto opened = UrlInput::open(shared.request.url, std::move(options),
                               [&shared] { return shared.stop.load() || shared.stalled(); });
  if (!opened.ok()) {
    if (!shared.stop.load())
      shared.fail(opened.error() == kUrlInterrupted ? kUrlTimeout : opened.error());
    return false;
  }
  auto input = std::move(opened.value());
  const auto& info = input->info();
  if (info.rate != kRingRate || info.channels == 0 || info.channels > 2) {
    shared.fail(kUnsupported);
    return false;
  }
  std::vector<std::int16_t> mono;
  for (;;) {
    auto next = input->next();
    if (!next.ok()) {
      if (!shared.stop.load())
        shared.fail(next.error() == kUrlInterrupted ? kUrlTimeout : next.error());
      return false;
    }
    const auto& decoded = next.value();
    if (decoded.end)
      break;
    if (decoded.rate != kRingRate || decoded.channels == 0) {
      shared.fail(kUnsupported);
      return false;
    }
    mixDown(decoded.samples, decoded.channels, mono);
    std::unique_lock lock(shared.mutex);
    // waiting for room is no stall: the ring plays what is ahead. a frame larger than the room goes in once it is empty
    shared.changed.wait(lock, [&] {
      return shared.stop.load() || shared.ahead.empty() ||
             shared.ahead.size() + mono.size() <= shared.options.aheadSamples;
    });
    if (shared.stop.load())
      return false;
    shared.ahead.insert(shared.ahead.end(), mono.begin(), mono.end());
    if (shared.keeping) {
      if (shared.kept.size() + mono.size() <= shared.options.keepSamples) {
        shared.kept.insert(shared.kept.end(), mono.begin(), mono.end());
      } else {
        shared.keeping = false;
        std::vector<std::int16_t>().swap(shared.kept);
      }
    }
    shared.moved();
  }
  const std::scoped_lock lock(shared.mutex);
  shared.passDone = true;
  shared.keptWhole = shared.keeping;
  shared.keeping = false;
  return true;
}

void run(Shared& shared) {
  for (;;) {
    {
      std::unique_lock lock(shared.mutex);
      shared.changed.wait(lock, [&] { return shared.stop.load() || shared.wantPass; });
      if (shared.stop.load())
        return;
      shared.wantPass = false;
    }
    if (!pass(shared))
      return;
    const std::scoped_lock lock(shared.mutex);
    // a whole pass in memory plays again from there
    if (shared.keptWhole)
      return;
  }
}

class UrlRingStream final : public audiod::RingStream {
 public:
  explicit UrlRingStream(std::shared_ptr<Shared> shared) : shared_(std::move(shared)) {}
  ~UrlRingStream() override { stop(); }
  UrlRingStream(const UrlRingStream&) = delete;
  UrlRingStream& operator=(const UrlRingStream&) = delete;

  Read read(std::span<std::int16_t> out) override {
    const std::scoped_lock lock(shared_->mutex);
    if (shared_->stop.load())
      return {};
    Read result;
    if (replaying_) {
      const auto& kept = shared_->kept;
      result.samples = std::min(out.size(), kept.size() - shared_->keptAt);
      std::copy_n(kept.begin() + static_cast<std::ptrdiff_t>(shared_->keptAt), result.samples, out.begin());
      shared_->keptAt += result.samples;
      result.ended = shared_->keptAt == kept.size();
      return result;
    }
    auto& ahead = shared_->ahead;
    result.samples = std::min(out.size(), ahead.size());
    std::copy_n(ahead.begin(), result.samples, out.begin());
    ahead.erase(ahead.begin(), ahead.begin() + static_cast<std::ptrdiff_t>(result.samples));
    result.ended = shared_->passDone && ahead.empty();
    if (result.samples > 0)
      shared_->changed.notify_all();
    return result;
  }

  void rewind() override {
    const std::scoped_lock lock(shared_->mutex);
    if (shared_->keptWhole) {
      replaying_ = true;
      shared_->keptAt = 0;
      return;
    }
    shared_->ahead.clear();
    shared_->passDone = false;
    shared_->wantPass = true;
    shared_->changed.notify_all();
  }

  [[nodiscard]] std::string_view failure() const override {
    const char* text = shared_->failure.load();
    return text == nullptr ? std::string_view{} : std::string_view(text, shared_->failureSize.load());
  }

  void stop() override {
    {
      const std::scoped_lock lock(shared_->mutex);
      shared_->stop.store(true);
    }
    shared_->changed.notify_all();
  }

 private:
  std::shared_ptr<Shared> shared_;
  // read on the reader's thread alone
  bool replaying_ = false;
};

// what a url ffmpeg is never given fails with at once
class FailedRingStream final : public audiod::RingStream {
 public:
  explicit FailedRingStream(std::string_view code) : code_(code) {}
  Read read(std::span<std::int16_t>) override { return {}; }
  void rewind() override {}
  [[nodiscard]] std::string_view failure() const override { return code_; }
  void stop() override {}

 private:
  std::string_view code_;
};

}  // namespace

std::shared_ptr<audiod::RingStream> openRingStream(audiod::RingStreamRequest request, RingStreamOptions options) {
  auto accepted = acceptedUrl(request.url);
  if (!accepted)
    return std::make_shared<FailedRingStream>(kInvalid);
  request.url = std::move(*accepted);
  auto shared = std::make_shared<Shared>(std::move(request), options);
  decoderStarted();
  try {
    std::thread([shared] {
      run(*shared);
      decoderFinished();
    }).detach();
  } catch (...) {
    decoderFinished();
    throw;
  }
  return std::make_shared<UrlRingStream>(std::move(shared));
}

audiod::RingStreamFactory ringStreamFactory(RingStreamOptions options) {
  return [options](const audiod::RingStreamRequest& request) { return openRingStream(request, options); };
}

}  // namespace cadence::url
