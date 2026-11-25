// android's mirror decoder: MediaCodec (the ndk's AMediaCodec) onto the activity's video SurfaceView, the one mpv
// draws on (android-main.cpp hands its ANativeWindow here as it changes). the app stops the local player before a
// mirror session (app.cpp), as a surface takes one producer at a time. each output is released at its presentation
// time with releaseOutputBufferAtTime, which the compositor latches at that vsync (CLOCK_MONOTONIC, as System.nanoTime)
#include "player/mirror-presenter.h"
#include "player/video-fit.h"
#include "player/video-output.h"

#include "ipc/log.h"

#include <android/native_window.h>
#include <media/NdkMediaCodec.h>
#include <media/NdkMediaFormat.h>

#include <cstring>
#include <mutex>
#include <optional>
#include <vector>

namespace cadence::screen::player {

namespace {

// the surface and the codec on it: one lock, so a surface the activity takes back never has a codec left on it
std::mutex mu;
ANativeWindow* window = nullptr;
AMediaCodec* codec = nullptr;
// bumped each time a codec goes, so a handle of an older one is never released into a newer one
std::int64_t generation = 1;
// the last config, so a codec the surface took away is made again on the surface's return (a sender sends its config
// only when it changes), at the next keyframe
struct LastConfig {
  MirrorCodec kind = MirrorCodec::Unknown;
  int width = 0;
  int height = 0;
  std::vector<std::uint8_t> bytes;
} last;

void dropCodecLocked() {
  if (codec == nullptr) return;
  AMediaCodec_stop(codec);
  AMediaCodec_delete(codec);
  codec = nullptr;
  generation++;
}

std::int64_t handleOf(std::size_t index) { return (generation << 32) | static_cast<std::int64_t>(index); }

// the decoded picture's shape from the codec's output format: its crop (inclusive edges) where it has one, else its
// size, with the sample aspect and rotation it names. MediaCodec scales to fit the surface, so the plane takes this
// shape (VideoLayout); an ipad turned sends a new config and its new size arrives here
std::optional<VideoContent> contentOf(AMediaFormat* f) {
  VideoContent c;
  std::int32_t v = 0;
  if (AMediaFormat_getInt32(f, AMEDIAFORMAT_KEY_WIDTH, &v)) c.width = v;
  if (AMediaFormat_getInt32(f, AMEDIAFORMAT_KEY_HEIGHT, &v)) c.height = v;
  std::int32_t left = 0, top = 0, right = 0, bottom = 0;
  if (AMediaFormat_getRect(f, "crop", &left, &top, &right, &bottom) && right >= left && bottom >= top) {
    c.width = right - left + 1;
    c.height = bottom - top + 1;
  }
  std::int32_t sarW = 0, sarH = 0;
  if (AMediaFormat_getInt32(f, "sar-width", &sarW) && AMediaFormat_getInt32(f, "sar-height", &sarH) && sarW > 0 &&
      sarH > 0) {
    c.sarNum = sarW;
    c.sarDen = sarH;
  }
  if (AMediaFormat_getInt32(f, "rotation-degrees", &v)) c.rotation = v;
  if (c.width <= 0 || c.height <= 0) return std::nullopt;
  return c;
}

void showContent(std::optional<VideoContent> c) { videoLayout().setContent(VideoLayout::Source::Mirror, c); }

class AndroidMirrorDecoder final : public MirrorDecoder {
 public:
  ~AndroidMirrorDecoder() override {
    std::lock_guard lock(mu);
    dropCodecLocked();
    showContent(std::nullopt);
  }

  bool configure(MirrorCodec kind, int width, int height, std::span<const std::uint8_t> config) override {
    std::lock_guard lock(mu);
    last = LastConfig{kind, width, height, {config.begin(), config.end()}};
    // MirrorStart's size shapes the plane before the session's first picture; a later config (the ipad turned)
    // keeps the decoded shape until the codec names the new one, as MirrorStart's is the session's first
    if (!videoLayout().content(VideoLayout::Source::Mirror) && width > 0 && height > 0)
      showContent(VideoContent{width, height});
    return makeLocked();
  }

  Queued queue(std::span<const std::uint8_t> data, std::int64_t ptsNs, bool keyframe) override {
    std::lock_guard lock(mu);
    // the surface went (and the codec with it): frames are dropped until it is back and a keyframe comes
    if (codec == nullptr && (!keyframe || window == nullptr || last.bytes.empty() || !makeLocked())) {
      return Queued::Dropped;
    }
    return queueLocked(data, ptsNs, 0, 0) ? Queued::Taken : Queued::Full;
  }

  // the codec made again from the last config, under mu
  static bool makeLocked() {
    dropCodecLocked();
    if (window == nullptr) return false;
    const MirrorCodec kind = last.kind;
    const int width = last.width;
    const int height = last.height;
    const char* mime = kind == MirrorCodec::H265 ? "video/hevc" : "video/avc";
    AMediaCodec* c = AMediaCodec_createDecoderByType(mime);
    if (c == nullptr) return false;
    AMediaFormat* f = AMediaFormat_new();
    AMediaFormat_setString(f, AMEDIAFORMAT_KEY_MIME, mime);
    AMediaFormat_setInt32(f, AMEDIAFORMAT_KEY_WIDTH, width);
    AMediaFormat_setInt32(f, AMEDIAFORMAT_KEY_HEIGHT, height);
    // a mirror keyframe is one access unit, joined from the tap's fragments (a 4k h265 keyframe can pass 1 MiB)
    AMediaFormat_setInt32(f, AMEDIAFORMAT_KEY_MAX_INPUT_SIZE, 4 << 20);
    // android 11's low-latency decoding where the codec has it (ignored elsewhere)
    AMediaFormat_setInt32(f, "low-latency", 1);
    const auto configured = AMediaCodec_configure(c, f, window, nullptr, 0);
    AMediaFormat_delete(f);
    if (configured != AMEDIA_OK || AMediaCodec_start(c) != AMEDIA_OK) {
      AMediaCodec_delete(c);
      return false;
    }
    codec = c;
    // the config goes first, as codec config (a just-started codec has its input buffers free)
    queueLocked(last.bytes, 0, AMEDIACODEC_BUFFER_FLAG_CODEC_CONFIG, 20000);
    return true;
  }

  void outputs(std::vector<Output>& out, std::int64_t waitNs) override {
    std::lock_guard lock(mu);
    if (codec == nullptr) return;
    auto timeoutUs = waitNs / 1000;
    for (;;) {
      AMediaCodecBufferInfo info{};
      const auto index = AMediaCodec_dequeueOutputBuffer(codec, &info, timeoutUs);
      timeoutUs = 0;
      if (index == AMEDIACODEC_INFO_OUTPUT_FORMAT_CHANGED) {
        if (AMediaFormat* f = AMediaCodec_getOutputFormat(codec)) {
          if (const auto c = contentOf(f)) showContent(c);
          AMediaFormat_delete(f);
        }
        continue;
      }
      if (index == AMEDIACODEC_INFO_OUTPUT_BUFFERS_CHANGED) continue;
      if (index < 0) return;
      if ((info.flags & AMEDIACODEC_BUFFER_FLAG_CODEC_CONFIG) != 0) {
        AMediaCodec_releaseOutputBuffer(codec, static_cast<std::size_t>(index), false);
        continue;
      }
      out.push_back(Output{handleOf(static_cast<std::size_t>(index)), info.presentationTimeUs * 1000});
    }
  }

  void release(std::int64_t handle, bool render, std::int64_t atNs) override {
    std::lock_guard lock(mu);
    if (codec == nullptr || (handle >> 32) != generation) return;
    const auto index = static_cast<std::size_t>(handle & 0xffffffff);
    if (render && atNs > 0)
      AMediaCodec_releaseOutputBufferAtTime(codec, index, atNs);
    else
      AMediaCodec_releaseOutputBuffer(codec, index, render);
  }

  void close() override {
    std::lock_guard lock(mu);
    // the shape stays: a new config (the ipad turned) closes the decoder too, and the presenter clears the shape as
    // the session ends
    dropCodecLocked();
  }

  static bool queueLocked(std::span<const std::uint8_t> data, std::int64_t ptsNs, std::uint32_t flags,
                          std::int64_t timeoutUs) {
    const auto index = AMediaCodec_dequeueInputBuffer(codec, timeoutUs);
    if (index < 0) return false;
    std::size_t capacity = 0;
    std::uint8_t* buffer = AMediaCodec_getInputBuffer(codec, static_cast<std::size_t>(index), &capacity);
    // larger than the codec's buffer: given back empty, the frame lost (the schedule's next keyframe repairs it)
    const std::size_t n = buffer != nullptr && data.size() <= capacity ? data.size() : 0;
    if (n > 0) std::memcpy(buffer, data.data(), n);
    AMediaCodec_queueInputBuffer(codec, static_cast<std::size_t>(index), 0, n, static_cast<std::uint64_t>(ptsNs / 1000),
                                 flags);
    return true;
  }
};

}  // namespace

std::unique_ptr<MirrorPresenter> MirrorPresenter::create() {
  return std::make_unique<MirrorPresenter>(std::make_unique<AndroidMirrorDecoder>());
}

void androidMirrorWindowChanged(ANativeWindow* w) {
  std::lock_guard lock(mu);
  if (w == window) {
    if (w != nullptr) ANativeWindow_release(w);
    return;
  }
  // a codec on the old surface goes with it; the next keyframe makes it again on the new one (codec_lost says so)
  const bool lost = codec != nullptr;
  dropCodecLocked();
  if (window != nullptr) ANativeWindow_release(window);
  window = w;
  CADENCE_LOG(Info, "mirror_surface", {{"present", window != nullptr}, {"codec_lost", lost}});
}

}  // namespace cadence::screen::player
