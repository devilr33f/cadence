#pragma once

#include "audiod/media-source.h"
#include "result.h"
#include "url/ffmpeg-options.h"

#include <cstddef>
#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <vector>

struct AVCodecContext;
struct AVFormatContext;
struct AVFrame;
struct AVIOContext;
struct AVPacket;
struct AVDictionary;

namespace cadence::url {

// the fixed codes a url fails with
inline constexpr std::string_view kUrlNotFound = "not_found";
inline constexpr std::string_view kUrlHttpError = "http_error";
inline constexpr std::string_view kUrlTimeout = "timeout";
inline constexpr std::string_view kUrlOpenFailed = "open_failed";
inline constexpr std::string_view kUrlUnsupported = "unsupported";
inline constexpr std::string_view kUrlReadFailed = "read_failed";
// the interrupt callback asked ffmpeg to stop; the caller knows why
inline constexpr std::string_view kUrlInterrupted = "interrupted";

// what opening found
struct InputInfo {
  std::uint32_t rate = 0;
  std::size_t channels = 0;
  // 0 = live or unknown
  std::uint64_t durationMs = 0;
  // a position can be sought: the input has a duration and seeks (http with ranges, a finished hls playlist)
  bool seekable = false;
  // the container's title, artist and album
  audiod::MediaMetadata tags;
};

// decoded audio, float interleaved, valid until the next call
struct Decoded {
  std::span<const float> samples;
  std::uint32_t rate = 0;
  std::size_t channels = 0;
  // nothing more: the decoder's last frames came before
  bool end = false;
};

// one url opened with ffmpeg: the format, its best audio stream and that stream's decoder, all freed with it. one
// thread at a time. every open it makes, the url's own and each playlist, segment or key behind it, gets the options
// and polls interrupt while it waits; true makes the call give up (kUrlInterrupted)
class UrlInput {
 public:
  using Interrupt = std::function<bool()>;

  static Result<std::unique_ptr<UrlInput>, std::string_view> open(const std::string& url,
                                                                 std::vector<FfmpegOption> options,
                                                                 Interrupt interrupt);
  ~UrlInput();

  UrlInput(const UrlInput&) = delete;
  UrlInput& operator=(const UrlInput&) = delete;

  [[nodiscard]] const InputInfo& info() const { return info_; }
  // the next decoded audio, or a code; 1 or 2 channels, more is kUrlUnsupported
  Result<Decoded, std::string_view> next();
  // to ms from the start; false when ffmpeg could not, and then it reads on from where it was
  bool seek(std::uint64_t ms);
  // the icy metadata packet the http stream carried last, when it has one
  [[nodiscard]] std::optional<std::string> icyPacket() const;

 private:
  UrlInput(std::vector<FfmpegOption> options, Interrupt interrupt);

  static int interrupted(void* self);
  static int openNested(AVFormatContext* format, AVIOContext** pb, const char* url, int flags,
                        AVDictionary** options);
  [[nodiscard]] std::optional<std::string_view> openStream();

  std::vector<FfmpegOption> options_;
  Interrupt interrupt_;
  int (*defaultOpen_)(AVFormatContext*, AVIOContext**, const char*, int, AVDictionary**) = nullptr;
  AVFormatContext* format_ = nullptr;
  AVCodecContext* codec_ = nullptr;
  AVPacket* packet_ = nullptr;
  AVFrame* frame_ = nullptr;
  int stream_ = -1;
  InputInfo info_;
  std::vector<float> samples_;
};

// a code for an ffmpeg error; opening tells an open from a read
std::string_view urlCode(int error, bool opening);

}  // namespace cadence::url
