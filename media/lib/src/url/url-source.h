#pragma once

#include "audiod/log.h"
#include "audiod/media-source.h"
#include "backend/pcm-input.h"

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <string_view>

namespace cadence::url {

// runs a task on audiod's loop thread
using Post = std::function<void(std::function<void()>)>;

// the defaults are M4b's; tests shorten them
struct UrlOptions {
  // opening (connect, request, probe, the first audio) gives up after this, as timeout
  std::chrono::milliseconds openDeadline{15000};
  // how often the icy title is looked at while decoding
  std::chrono::milliseconds icyEvery{1000};
  // how long the end waits for the backend to read what was queued: the ring's length and a second
  std::chrono::milliseconds drainBound{3000};
  // a test seam: runs on the decoder thread before ffmpeg opens, as a name lookup that ignores the interrupt would
  std::function<void()> beforeOpen{};
};

// a url session's or overlay's producer (M4b-D9): one decoder thread opens the url with ffmpeg, decodes its best audio
// stream and pushes it, float interleaved at the stream's own rate, into the input openOutput gave it (the input
// resamples). it reports playing once the first audio is decoded, with the duration, the metadata (the request's,
// then the container's tags for what it left empty, then the icy title, looked at about once a second) and the
// position before it; ended once the backend read the last of it; failed with a fixed code. the position is what the
// backend read, reported at the start, a seek and a resume only.
//
// pause and stop hold the decoder and clear what it queued before they return; a seekable input resumes where the
// pause left it, a live one from what comes next. it cannot wait in the background (keepsPaused false): a session it
// loses stops it. every report goes through the events handle, posted from the decoder thread.
//
// the loop thread never waits for ffmpeg: ffmpeg's name lookup ignores the interrupt and can take seconds. stop only
// waits until no push is under way and lets the thread go; it shares the decoder's state, finishes on its own, and
// from the stop on never logs, reports or touches the input again. destroying a source that ended or failed on its own
// leaves what it queued to play out
class UrlSource final : public audiod::MediaSource {
 public:
  // request.url passed acceptedUrl; the thread starts at once
  UrlSource(const audiod::Log& log, audiod::UrlRequest request, std::shared_ptr<audiod::MediaSourceEvents> events,
            Post post, UrlOptions options = {});
  ~UrlSource() override;

  UrlSource(const UrlSource&) = delete;
  UrlSource& operator=(const UrlSource&) = delete;

  [[nodiscard]] audiod::MediaKind kind() const override { return audiod::MediaKind::Url; }
  [[nodiscard]] audiod::MediaActions actions() const override;
  [[nodiscard]] bool keepsPaused() const override { return false; }

  void openOutput(PcmInput& music) override;
  void closeOutput() override;

  std::string_view pause() override;
  std::string_view resume() override;
  std::string_view stop() override;
  std::string_view seek(std::uint64_t ms) override;
  std::string_view next() override { return audiod::kMediaUnsupported; }
  std::string_view previous() override { return audiod::kMediaUnsupported; }

 private:
  // what the source and its decoder thread share
  struct State;
  // the decoder thread's own state and loop
  class Decoder;

  // the loop thread's side of the handoff: clears the output and waits until no push is under way. lock held
  void silence(std::unique_lock<std::mutex>& lock);

  std::shared_ptr<State> state_;
};

// what audiod makes url sources with, for sessions and overlays alike: a url that is not http or https is invalid at
// once and never reaches ffmpeg. it installs the ffmpeg log sink and keeps openssl from cleaning up at exit, so make it
// before anything uses ffmpeg or openssl
audiod::MediaSourceFactory urlSourceFactory(const audiod::Log& log, Post post, UrlOptions options = {});

// decoder threads still running, stopped ones included, a ring's (ring-stream.h) among them
std::size_t runningDecoders();
// a ring's decoder thread counts itself in and out
void decoderStarted();
void decoderFinished();
// waits up to limit for every decoder thread to finish; false when some still run, held by ffmpeg (a name lookup), and
// then the process should leave with _Exit rather than run exit's cleanup under them
bool decodersFinished(std::chrono::milliseconds limit);

}  // namespace cadence::url
