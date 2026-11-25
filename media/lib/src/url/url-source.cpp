#include "url/url-source.h"

#include "dsp/format.h"
#include "dsp/resampler.h"
#include "url/ffmpeg-log.h"
#include "url/ffmpeg-options.h"
#include "url/icy.h"
#include "url/url-input.h"

#include <openssl/crypto.h>

#include <atomic>
#include <condition_variable>
#include <optional>
#include <span>
#include <string>
#include <system_error>
#include <thread>
#include <utility>
#include <vector>

namespace cadence::url {

namespace {

using Clock = std::chrono::steady_clock;

// decoder threads still running, stopped ones included
std::atomic<std::size_t> running{0};

// how often the end looks whether the backend read the last of it
constexpr auto kDrainPoll = std::chrono::milliseconds(10);
// how often a silence clears its output again while a push still holds on
constexpr auto kClearRetry = std::chrono::milliseconds(5);

std::uint64_t framesToMs(std::uint64_t frames) { return frames * 1000 / dsp::kRate; }

bool pushable(std::uint32_t rate, std::size_t channels) {
  return channels >= 1 && channels <= dsp::kStereo && rate >= dsp::Resampler::kMinRate &&
         rate <= dsp::Resampler::kMaxRate;
}

audiod::MediaMetadata filled(audiod::MediaMetadata metadata, const audiod::MediaMetadata& tags) {
  for (auto [field, tag] : {std::pair{&metadata.title, &tags.title}, std::pair{&metadata.artist, &tags.artist},
                            std::pair{&metadata.album, &tags.album}})
    if (field->empty())
      *field = *tag;
  return metadata;
}

}  // namespace

struct UrlSource::State {
  State(const audiod::Log& logTo, audiod::UrlRequest asked, std::shared_ptr<audiod::MediaSourceEvents> reportTo,
        Post postTo, UrlOptions given)
      : log(logTo),
        request(std::move(asked)),
        events(std::move(reportTo)),
        post(std::move(postTo)),
        options(std::move(given)) {}

  // what the backend has read since the base, from the base's position; mutex held
  [[nodiscard]] std::uint64_t positionMs() const {
    if (output == nullptr)
      return baseMs;
    return baseMs + framesToMs(output->status().framesRead - readBase);
  }
  // the position counts from ms and the backend's read count now; mutex held
  void rebase(std::uint64_t ms) {
    baseMs = ms;
    readBase = output != nullptr ? output->status().framesRead : 0;
  }
  void report(std::function<void(audiod::MediaSourceEvents&)> event) const {
    post([events = events, event = std::move(event)] { event(*events); });
  }

  struct Seek {
    std::uint64_t ms = 0;
    // a resume reports the position itself
    bool reports = true;
  };

  // audiod's own: the decoder uses them only while stopping is false, under the mutex
  const audiod::Log& log;
  const audiod::UrlRequest request;
  const std::shared_ptr<audiod::MediaSourceEvents> events;
  const Post post;
  const UrlOptions options;

  std::mutex mutex;
  std::condition_variable changed;
  // all of these under mutex
  PcmInput* output = nullptr;
  bool paused = false;
  bool stopping = false;
  bool pushing = false;
  bool seekable = false;
  // the decoder ended or failed on its own and pushes nothing more
  bool finished = false;
  std::optional<Seek> seekTo;
  // counts the clears the loop thread made, so a push that began just before one is cleared again after it
  std::uint64_t clears = 0;
  std::uint64_t baseMs = 0;
  std::uint64_t readBase = 0;
  std::uint64_t pausedAtMs = 0;

  // read by ffmpeg's interrupt callback
  std::atomic<bool> stop{false};
};

// everything the decoder thread keeps to itself; it co-owns the shared state, so it may outlive the source
class UrlSource::Decoder {
 public:
  explicit Decoder(std::shared_ptr<State> state) : state_(std::move(state)) {}

  void run() {
    if (!open())
      return;
    loop();
  }

 private:
  enum class Stage : std::uint8_t { Decoding, Finishing, Draining };

  // through the first decoded audio, which is when it reports playing
  bool open() {
    const auto deadline = Clock::now() + state_->options.openDeadline;
    if (state_->options.beforeOpen)
      state_->options.beforeOpen();
    // a stop that came while the name lookup held this thread: nothing is opened
    if (state_->stop.load())
      return false;
    linesAtOpen_ = ffmpegLogCounts();
    auto opened = UrlInput::open(state_->request.url, ffmpegOptions(state_->request.caFile), [this, deadline] {
      return state_->stop.load() || (opening_.load() && Clock::now() > deadline);
    });
    const auto deadlinePassed = [deadline] { return Clock::now() > deadline; };
    if (!opened.ok())
      return fail(opened.error(), deadlinePassed());
    input_ = std::move(opened.value());
    const auto& info = input_->info();
    if (!whileLive([&] {
          state_->log.info("url_open", {{"src", state_->request.logId},
                                        {"rate", info.rate},
                                        {"channels", static_cast<std::uint64_t>(info.channels)},
                                        {"duration_ms", info.durationMs},
                                        {"seekable", info.seekable ? 1U : 0U}});
        }))
      return false;

    std::uint64_t start = 0;
    const auto asked = state_->request.startMs;
    if (asked > 0 && info.seekable && asked < info.durationMs && input_->seek(asked))
      start = asked;
    base_ = filled(state_->request.metadata, info.tags);
    current_ = base_;

    // the first audio, its icy title looked at as it comes
    if (!decode())
      return fail(failure_, deadlinePassed());
    // nothing to play: it ends without ever playing
    if (stage_ != Stage::Decoding) {
      end();
      return false;
    }
    opening_ = false;
    return whileLive([&] {
      if (current_ != state_->request.metadata)
        state_->report([metadata = current_](audiod::MediaSourceEvents& events) { events.metadata(metadata); });
      state_->report([ms = info.durationMs](audiod::MediaSourceEvents& events) { events.duration(ms); });
      state_->seekable = info.seekable;
      state_->rebase(start);
      state_->report([start](audiod::MediaSourceEvents& events) {
        events.position(start);
        events.playing();
      });
    });
  }

  void loop() {
    for (;;) {
      std::unique_lock lock(state_->mutex);
      if (!control(lock))
        return;
      auto* output = state_->output;
      if (stage_ == Stage::Draining) {
        if (output->status().queued == 0 || Clock::now() >= drainUntil_) {
          lock.unlock();
          return end();
        }
        state_->changed.wait_for(lock, kDrainPoll);
        continue;
      }
      const auto clears = state_->clears;
      state_->pushing = true;
      lock.unlock();
      if (stage_ == Stage::Finishing) {
        output->finish();
        stage_ = Stage::Draining;
        drainUntil_ = Clock::now() + state_->options.drainBound;
      } else if (!pending_.empty()) {
        output->push(std::span<const float>(pending_), rate_, channels_);
      }
      pending_.clear();
      lock.lock();
      // a clear landed as the push began, which then queued as the clear's successor: it goes too
      if (state_->clears != clears)
        output->clear();
      state_->pushing = false;
      state_->changed.notify_all();
      lock.unlock();

      if (stage_ == Stage::Decoding && !decode()) {
        fail(failure_, false);
        return;
      }
    }
  }

  // with the lock: does what a seek asked and waits while paused or without an output; false once stopping
  bool control(std::unique_lock<std::mutex>& lock) {
    for (;;) {
      if (state_->stopping)
        return false;
      if (state_->seekTo) {
        const auto seek = *state_->seekTo;
        state_->seekTo.reset();
        lock.unlock();
        const bool sought = input_->seek(seek.ms);
        lock.lock();
        if (state_->stopping)
          return false;
        if (sought) {
          pending_.clear();
          stage_ = Stage::Decoding;
          state_->rebase(seek.ms);
        }
        if (seek.reports)
          state_->report([ms = state_->positionMs()](audiod::MediaSourceEvents& events) { events.position(ms); });
        continue;
      }
      if (!state_->paused && state_->output != nullptr)
        return true;
      state_->changed.wait(lock);
    }
  }

  // the next audio into pending_, or the end; false with failure_ set
  bool decode() {
    auto next = input_->next();
    if (!next.ok()) {
      failure_ = next.error();
      return false;
    }
    const auto& decoded = next.value();
    if (decoded.end) {
      checkIcy(true);
      stage_ = Stage::Finishing;
      return true;
    }
    if (!pushable(decoded.rate, decoded.channels)) {
      failure_ = kUrlUnsupported;
      return false;
    }
    pending_.assign(decoded.samples.begin(), decoded.samples.end());
    rate_ = decoded.rate;
    channels_ = decoded.channels;
    checkIcy(false);
    return true;
  }

  // the icy title, about once a second: its "Artist - Title" over the title and artist, the rest as asked or tagged
  void checkIcy(bool now) {
    const auto time = Clock::now();
    if (!now && time < icyNext_)
      return;
    icyNext_ = time + state_->options.icyEvery;
    const auto packet = input_->icyPacket();
    const auto title = packet ? icyStreamTitle(*packet) : std::nullopt;
    if (!title)
      return;
    auto metadata = base_;
    if (!title->empty()) {
      auto split = splitIcyTitle(*title);
      metadata.title = std::move(split.title);
      if (!split.artist.empty())
        metadata.artist = std::move(split.artist);
    }
    if (metadata == current_)
      return;
    current_ = std::move(metadata);
    // before it plays, open reports it with the rest
    if (!opening_.load())
      whileLive([&] {
        state_->report([metadata = current_](audiod::MediaSourceEvents& events) { events.metadata(metadata); });
      });
  }

  void end() {
    whileLive([&] {
      state_->finished = true;
      const auto lines = linesSinceOpen();
      state_->log.info("url_ended", {{"src", state_->request.logId},
                                     {"ffmpeg_errors", lines.fatal + lines.error},
                                     {"ffmpeg_warnings", lines.warning}});
      state_->report([](audiod::MediaSourceEvents& events) { events.ended(); });
    });
  }

  // a stop is no failure; an interrupt that is no stop is the open deadline
  bool fail(std::string_view code, bool deadlinePassed) {
    if (code == kUrlInterrupted)
      code = deadlinePassed ? kUrlTimeout : kUrlReadFailed;
    whileLive([&] {
      state_->finished = true;
      const auto lines = linesSinceOpen();
      state_->log.warn("url_failed", {{"code", code},
                                      {"src", state_->request.logId},
                                      {"ffmpeg_errors", lines.fatal + lines.error},
                                      {"ffmpeg_warnings", lines.warning}});
      state_->report([code](audiod::MediaSourceEvents& events) { events.failed(code); });
    });
    return false;
  }

  // runs fn under the mutex unless the source was stopped, so nothing is logged or reported after a stop returned;
  // false when it was
  template <typename Fn>
  bool whileLive(Fn fn) {
    std::scoped_lock lock(state_->mutex);
    if (state_->stopping)
      return false;
    fn();
    return true;
  }

  // lines ffmpeg logged since this source opened, give or take another source's at the same time
  [[nodiscard]] FfmpegLogCounts linesSinceOpen() const {
    const auto now = ffmpegLogCounts();
    return {.fatal = now.fatal - linesAtOpen_.fatal,
            .error = now.error - linesAtOpen_.error,
            .warning = now.warning - linesAtOpen_.warning,
            .info = now.info - linesAtOpen_.info,
            .debug = now.debug - linesAtOpen_.debug};
  }

  std::shared_ptr<State> state_;
  // before input_: closing a tls stream as input_ goes may still ask the interrupt callback, which reads it
  std::atomic<bool> opening_{true};
  std::unique_ptr<UrlInput> input_;
  FfmpegLogCounts linesAtOpen_;
  Stage stage_ = Stage::Decoding;
  std::string_view failure_;
  std::vector<float> pending_;
  std::uint32_t rate_ = 0;
  std::size_t channels_ = 0;
  Clock::time_point drainUntil_;
  Clock::time_point icyNext_;
  audiod::MediaMetadata base_;
  audiod::MediaMetadata current_;
};

UrlSource::UrlSource(const audiod::Log& log, audiod::UrlRequest request,
                     std::shared_ptr<audiod::MediaSourceEvents> events, Post post, UrlOptions options)
    : state_(std::make_shared<State>(log, std::move(request), std::move(events), std::move(post),
                                     std::move(options))) {
  // never joined: the thread holds the state as long as it runs, and stop keeps it from everything else
  running.fetch_add(1);
  try {
    std::thread([state = state_] {
      Decoder(state).run();
      // ffmpeg and openssl are done with this thread
      running.fetch_sub(1);
    }).detach();
  } catch (...) {
    running.fetch_sub(1);
    throw;
  }
}

UrlSource::~UrlSource() { stop(); }

audiod::MediaActions UrlSource::actions() const {
  std::scoped_lock lock(state_->mutex);
  if (state_->seekable)
    return {audiod::MediaAction::Pause, audiod::MediaAction::Resume, audiod::MediaAction::Stop,
            audiod::MediaAction::Seek};
  return {audiod::MediaAction::Pause, audiod::MediaAction::Resume, audiod::MediaAction::Stop};
}

void UrlSource::openOutput(PcmInput& music) {
  std::scoped_lock lock(state_->mutex);
  state_->output = &music;
  state_->rebase(state_->baseMs);
  state_->changed.notify_all();
}

void UrlSource::closeOutput() {
  std::unique_lock lock(state_->mutex);
  if (state_->output == nullptr)
    return;
  silence(lock);
  state_->output = nullptr;
}

std::string_view UrlSource::pause() {
  std::unique_lock lock(state_->mutex);
  if (state_->paused || state_->stopping)
    return audiod::kMediaOk;
  state_->paused = true;
  silence(lock);
  state_->pausedAtMs = state_->positionMs();
  state_->report([](audiod::MediaSourceEvents& events) { events.paused(); });
  return audiod::kMediaOk;
}

std::string_view UrlSource::resume() {
  std::scoped_lock lock(state_->mutex);
  if (!state_->paused || state_->stopping)
    return audiod::kMediaOk;
  state_->paused = false;
  const auto at = state_->pausedAtMs;
  // a seekable input goes back to where the pause left it; a live one plays on from what comes next
  if (state_->seekable)
    state_->seekTo = State::Seek{at, false};
  state_->rebase(at);
  state_->changed.notify_all();
  state_->report([at](audiod::MediaSourceEvents& events) {
    events.position(at);
    events.playing();
  });
  return audiod::kMediaOk;
}

std::string_view UrlSource::stop() {
  std::unique_lock lock(state_->mutex);
  if (state_->stopping)
    return audiod::kMediaOk;
  state_->stopping = true;
  state_->stop = true;
  // a decoder that ended or failed on its own pushes nothing more: what it queued plays out
  if (!state_->finished)
    silence(lock);
  return audiod::kMediaOk;
}

std::string_view UrlSource::seek(std::uint64_t ms) {
  std::unique_lock lock(state_->mutex);
  if (!state_->seekable)
    return audiod::kMediaUnsupported;
  if (state_->paused) {
    // the resume goes there
    state_->pausedAtMs = ms;
    state_->report([ms](audiod::MediaSourceEvents& events) { events.position(ms); });
    return audiod::kMediaOk;
  }
  state_->seekTo = State::Seek{ms, true};
  silence(lock);
  return audiod::kMediaOk;
}

void UrlSource::silence(std::unique_lock<std::mutex>& lock) {
  ++state_->clears;
  if (state_->output != nullptr)
    state_->output->clear();
  state_->changed.notify_all();
  // a push lets go at the clear within a few milliseconds; ffmpeg itself is never waited for. a push that began just
  // after the clear took it as acknowledged and waits on, and on an input nobody reads (a group leader's feed once the
  // leader stopped) it would wait for good: clear again until it lets go
  while (state_->pushing) {
    if (state_->changed.wait_for(lock, kClearRetry, [this] { return !state_->pushing; }))
      break;
    if (state_->output != nullptr)
      state_->output->clear();
  }
}

audiod::MediaSourceFactory urlSourceFactory(const audiod::Log& log, Post post, UrlOptions options) {
  installFfmpegLogSink();
  // before ffmpeg first uses openssl: exit must not tear openssl down under a decoder still closing a tls stream
  OPENSSL_init_crypto(OPENSSL_INIT_NO_ATEXIT, nullptr);
  return [&log, post = std::move(post), options = std::move(options)](
             const audiod::UrlRequest& request, std::shared_ptr<audiod::MediaSourceEvents> events)
             -> Result<std::unique_ptr<audiod::MediaSource>, std::string_view> {
    auto url = acceptedUrl(request.url);
    if (!url)
      return audiod::kMediaInvalid;
    auto accepted = request;
    accepted.url = std::move(*url);
    try {
      return std::unique_ptr<audiod::MediaSource>(
          std::make_unique<UrlSource>(log, std::move(accepted), std::move(events), post, options));
    } catch (const std::system_error&) {
      // no thread to decode on
      return audiod::kMediaFailed;
    }
  };
}

std::size_t runningDecoders() { return running.load(); }

void decoderStarted() { running.fetch_add(1); }

void decoderFinished() { running.fetch_sub(1); }

bool decodersFinished(std::chrono::milliseconds limit) {
  const auto deadline = Clock::now() + limit;
  while (running.load() > 0) {
    if (Clock::now() >= deadline)
      return false;
    std::this_thread::sleep_for(std::chrono::milliseconds(5));
  }
  return true;
}

}  // namespace cadence::url
