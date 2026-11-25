#pragma once

#include <cstddef>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

struct AVDictionary;

namespace cadence::url {

// the protocols any open may use. the real barrier against a redirect or a playlist entry naming file:, concat:,
// data: or another stack is the build: those protocols are not compiled in (build-ffmpeg.sh, guarded by the
// url-ffmpeg test). the whitelist is defence in depth, should a build ever carry more. hls names no protocol in
// ffmpeg 9 and stays as M4b-D9 lists it
inline constexpr std::string_view kProtocolWhitelist = "http,https,tcp,tls,hls,crypto";

// the url as ffmpeg gets it, its scheme lowercased: only http:// and https:// (in any case), with no space or control
// character in it; nullopt for anything else, which never reaches ffmpeg
std::optional<std::string> acceptedUrl(std::string_view url);

struct FfmpegOption {
  std::string key;
  std::string value;

  bool operator==(const FfmpegOption&) const = default;
};

// what opening a url is given (M4b-D9): tls always verified, against caFile or, when it is empty, openssl's default
// paths; the whitelist above; time limits, reconnects, icy metadata and a redirect cap
std::vector<FfmpegOption> ffmpegOptions(std::string_view caFile);

// the options that belong to the format context, which a nested open (a playlist's segment or key) must not get
// again: ffmpeg hands it the whitelist itself
bool formatOption(std::string_view key);

// an AVDictionary freed with it
class FfmpegDictionary {
 public:
  explicit FfmpegDictionary(const std::vector<FfmpegOption>& options = {});
  ~FfmpegDictionary();

  FfmpegDictionary(const FfmpegDictionary&) = delete;
  FfmpegDictionary& operator=(const FfmpegDictionary&) = delete;

  [[nodiscard]] AVDictionary** get() { return &dictionary_; }
  // the value of key, if it is there
  [[nodiscard]] std::optional<std::string> find(std::string_view key) const;
  [[nodiscard]] std::size_t size() const;

 private:
  AVDictionary* dictionary_ = nullptr;
};

}  // namespace cadence::url
