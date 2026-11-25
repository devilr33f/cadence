#include "playback/alsa-mixer.h"

#include <alsa/asoundlib.h>

#include <array>
#include <cmath>
#include <cstddef>
#include <memory>
#include <string_view>
#include <utility>

namespace cadence {

namespace {

struct MixerClose {
  void operator()(snd_mixer_t* mixer) const { snd_mixer_close(mixer); }
};

struct SelemIdFree {
  void operator()(snd_mixer_selem_id_t* id) const { snd_mixer_selem_id_free(id); }
};

VolumeError failure(const std::string& device, const std::string& control, std::string_view what, int code = 0) {
  std::string detail = control + " on " + device + ": " + std::string(what);
  if (code < 0)
    detail.append(": ").append(snd_strerror(code));
  return {detail, std::nullopt};
}

}  // namespace

std::optional<ReadBackMismatch> mixerReadBackMismatch(long wanted, std::span<const long> held) {
  for (std::size_t channel = 0; channel < held.size(); ++channel) {
    if (held[channel] < 0)
      continue;
    const long off = held[channel] - wanted;
    if (off > 1 || off < -1)
      return ReadBackMismatch{wanted, held[channel], static_cast<int>(channel)};
  }
  return std::nullopt;
}

long mixerValueForPercent(int percent, long min, long max) {
  return min + std::lrint(static_cast<double>(percent) * static_cast<double>(max - min) * 0.01);
}

AlsaMixer::AlsaMixer(std::string device, std::string control)
    : device_(std::move(device)), control_(std::move(control)) {}

std::optional<VolumeError> AlsaMixer::setPercent(int percent) { return apply(percent, false); }

std::optional<VolumeError> AlsaMixer::verifyPercent(int percent) { return apply(percent, true); }

std::optional<VolumeError> AlsaMixer::apply(int percent, bool verify) {
  snd_mixer_t* raw = nullptr;
  if (const int code = snd_mixer_open(&raw, 0); code < 0)
    return failure(device_, control_, "cannot open mixer", code);
  const std::unique_ptr<snd_mixer_t, MixerClose> mixer(raw);
  if (const int code = snd_mixer_attach(raw, device_.c_str()); code < 0)
    return failure(device_, control_, "cannot attach", code);
  if (const int code = snd_mixer_selem_register(raw, nullptr, nullptr); code < 0)
    return failure(device_, control_, "cannot register", code);
  if (const int code = snd_mixer_load(raw); code < 0)
    return failure(device_, control_, "cannot load", code);

  snd_mixer_selem_id_t* rawId = nullptr;
  if (const int code = snd_mixer_selem_id_malloc(&rawId); code < 0)
    return failure(device_, control_, "cannot allocate", code);
  const std::unique_ptr<snd_mixer_selem_id_t, SelemIdFree> id(rawId);
  snd_mixer_selem_id_set_index(rawId, 0);
  snd_mixer_selem_id_set_name(rawId, control_.c_str());
  auto* element = snd_mixer_find_selem(raw, rawId);
  if (element == nullptr)
    return failure(device_, control_, "no such control");
  if (snd_mixer_selem_has_playback_volume(element) == 0)
    return failure(device_, control_, "not a playback volume");
  long min = 0;
  long max = 0;
  if (const int code = snd_mixer_selem_get_playback_volume_range(element, &min, &max); code < 0)
    return failure(device_, control_, "cannot read the range", code);
  const long wanted = mixerValueForPercent(percent, min, max);
  if (!verify) {
    if (const int code = snd_mixer_selem_set_playback_volume_all(element, wanted); code < 0)
      return failure(device_, control_, "cannot set", code);
    return std::nullopt;
  }
  std::array<long, SND_MIXER_SCHN_LAST + 1> held;
  held.fill(-1);
  for (int channel = 0; channel <= SND_MIXER_SCHN_LAST; ++channel) {
    const auto channelId = static_cast<snd_mixer_selem_channel_id_t>(channel);
    if (snd_mixer_selem_has_playback_channel(element, channelId) == 0)
      continue;
    if (const int code = snd_mixer_selem_get_playback_volume(element, channelId, &held[static_cast<std::size_t>(channel)]); code < 0)
      return failure(device_, control_, "cannot read back", code);
  }
  if (const auto off = mixerReadBackMismatch(wanted, held)) {
    auto error = failure(device_, control_, "read back differs");
    error.mismatch = off;
    return error;
  }
  return std::nullopt;
}

}  // namespace cadence
