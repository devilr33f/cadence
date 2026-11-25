#include "url/ffmpeg-options.h"

#include "url/ffmpeg.h"

#include <algorithm>
#include <cctype>

namespace cadence::url {

namespace {

constexpr std::string_view kSchemes[] = {"http://", "https://"};

bool startsWithIgnoringCase(std::string_view text, std::string_view prefix) {
  return text.size() >= prefix.size() &&
         std::equal(prefix.begin(), prefix.end(), text.begin(),
                    [](char a, char b) { return std::tolower(static_cast<unsigned char>(b)) == a; });
}

}  // namespace

std::optional<std::string> acceptedUrl(std::string_view url) {
  // a space or a control character could end the request line ffmpeg writes
  if (std::any_of(url.begin(), url.end(), [](char c) {
        const auto byte = static_cast<unsigned char>(c);
        return byte <= 0x20 || byte == 0x7f;
      }))
    return std::nullopt;
  for (const auto scheme : kSchemes) {
    if (!startsWithIgnoringCase(url, scheme) || url.size() == scheme.size())
      continue;
    std::string accepted(scheme);
    accepted.append(url.substr(scheme.size()));
    return accepted;
  }
  return std::nullopt;
}

std::vector<FfmpegOption> ffmpegOptions(std::string_view caFile) {
  std::vector<FfmpegOption> options = {{"protocol_whitelist", std::string(kProtocolWhitelist)}, {"tls_verify", "1"}};
  if (!caFile.empty())
    options.push_back({"ca_file", std::string(caFile)});
  options.insert(options.end(), {{"rw_timeout", "10000000"},
                                 {"reconnect", "1"},
                                 {"reconnect_streamed", "1"},
                                 {"reconnect_delay_max", "5"},
                                 {"icy", "1"},
                                 {"user_agent", "cadence"},
                                 {"max_redirects", "5"},
                                 {"probesize", "1048576"},
                                 {"analyzeduration", "3000000"}});
  return options;
}

bool formatOption(std::string_view key) {
  return key == "protocol_whitelist" || key == "probesize" || key == "analyzeduration";
}

FfmpegDictionary::FfmpegDictionary(const std::vector<FfmpegOption>& options) {
  for (const auto& option : options)
    av_dict_set(&dictionary_, option.key.c_str(), option.value.c_str(), 0);
}

FfmpegDictionary::~FfmpegDictionary() { av_dict_free(&dictionary_); }

std::optional<std::string> FfmpegDictionary::find(std::string_view key) const {
  const std::string name(key);
  const auto* entry = av_dict_get(dictionary_, name.c_str(), nullptr, AV_DICT_MATCH_CASE);
  if (entry == nullptr)
    return std::nullopt;
  return std::string(entry->value);
}

std::size_t FfmpegDictionary::size() const { return static_cast<std::size_t>(av_dict_count(dictionary_)); }

}  // namespace cadence::url
