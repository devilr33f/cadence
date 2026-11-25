#include "playback/alsa-playback.h"

#include <alsa/asoundlib.h>

#include <algorithm>
#include <cerrno>
#include <memory>
#include <optional>
#include <string>
#include <utility>

namespace cadence {

namespace {

std::string alsaText(const char* step, int error) { return std::string(step).append(": ").append(snd_strerror(error)); }

PlaybackError alsaError(PlaybackErrorCode code, const char* step, int error) { return {code, alsaText(step, error)}; }

std::optional<PlaybackError> configureHardware(snd_pcm_t* pcm, const PlaybackFormat& requested) {
  snd_pcm_hw_params_t* raw = nullptr;
  if (const int error = snd_pcm_hw_params_malloc(&raw); error < 0)
    return alsaError(PlaybackErrorCode::ConfigureFailed, "hw params", error);
  const std::unique_ptr<snd_pcm_hw_params_t, decltype(&snd_pcm_hw_params_free)> params(raw, snd_pcm_hw_params_free);

  const auto refused = [](const char* step, int error) { return alsaError(PlaybackErrorCode::ConfigureFailed, step, error); };
  auto period = static_cast<snd_pcm_uframes_t>(requested.periodFrames);
  auto buffer = static_cast<snd_pcm_uframes_t>(requested.bufferFrames);
  int direction = 0;
  if (const int error = snd_pcm_hw_params_any(pcm, params.get()); error < 0)
    return refused("any", error);
  if (const int error = snd_pcm_hw_params_set_access(pcm, params.get(), SND_PCM_ACCESS_RW_INTERLEAVED); error < 0)
    return refused("interleaved access", error);
  if (const int error = snd_pcm_hw_params_set_format(pcm, params.get(), SND_PCM_FORMAT_S16_LE); error < 0)
    return refused("s16_le", error);
  if (const int error = snd_pcm_hw_params_set_rate_resample(pcm, params.get(), 0); error < 0)
    return refused("disable resampling", error);
  if (const int error = snd_pcm_hw_params_set_channels(pcm, params.get(), static_cast<unsigned>(requested.channels));
      error < 0)
    return refused(("channels " + std::to_string(requested.channels)).c_str(), error);
  if (const int error = snd_pcm_hw_params_set_rate(pcm, params.get(), static_cast<unsigned>(requested.sampleRate), 0);
      error < 0)
    return refused(("rate " + std::to_string(requested.sampleRate)).c_str(), error);
  if (const int error = snd_pcm_hw_params_set_period_size_near(pcm, params.get(), &period, &direction); error < 0)
    return refused("period", error);
  if (const int error = snd_pcm_hw_params_set_buffer_size_near(pcm, params.get(), &buffer); error < 0)
    return refused("buffer", error);
  if (const int error = snd_pcm_hw_params(pcm, params.get()); error < 0)
    return refused("install", error);
  return std::nullopt;
}

// starts once the device is filled to the cap and stops when it runs dry, so an underrun never loops old
// audio. a wait for room ends once the fill is at least a period below the cap, on a period boundary: dmix
// wakes waiters from its period timer, and any sooner would spin
std::optional<PlaybackError> configureSoftware(snd_pcm_t* pcm, const PlaybackFormat& format) {
  const auto period = static_cast<snd_pcm_uframes_t>(format.periodFrames);
  const auto buffer = static_cast<snd_pcm_uframes_t>(format.bufferFrames);
  const auto fill = static_cast<snd_pcm_uframes_t>(format.fillFrames);
  const auto wake = ((buffer - fill) / period + 1) * period;
  snd_pcm_sw_params_t* raw = nullptr;
  if (const int error = snd_pcm_sw_params_malloc(&raw); error < 0)
    return alsaError(PlaybackErrorCode::ConfigureFailed, "sw params", error);
  const std::unique_ptr<snd_pcm_sw_params_t, decltype(&snd_pcm_sw_params_free)> params(raw, snd_pcm_sw_params_free);

  const auto refused = [](const char* step, int error) { return alsaError(PlaybackErrorCode::ConfigureFailed, step, error); };
  if (const int error = snd_pcm_sw_params_current(pcm, params.get()); error < 0)
    return refused("current sw params", error);
  if (const int error = snd_pcm_sw_params_set_start_threshold(pcm, params.get(), fill); error < 0)
    return refused("start threshold", error);
  if (const int error = snd_pcm_sw_params_set_stop_threshold(pcm, params.get(), buffer); error < 0)
    return refused("stop threshold", error);
  if (const int error = snd_pcm_sw_params_set_avail_min(pcm, params.get(), wake); error < 0)
    return refused("avail min", error);
  if (const int error = snd_pcm_sw_params(pcm, params.get()); error < 0)
    return refused("install sw params", error);
  return std::nullopt;
}

Result<PlaybackFormat, PlaybackError> grantedFormat(snd_pcm_t* pcm) {
  snd_pcm_hw_params_t* raw = nullptr;
  if (const int error = snd_pcm_hw_params_malloc(&raw); error < 0)
    return alsaError(PlaybackErrorCode::ConfigureFailed, "hw params", error);
  const std::unique_ptr<snd_pcm_hw_params_t, decltype(&snd_pcm_hw_params_free)> params(raw, snd_pcm_hw_params_free);

  const auto unreadable = [](const char* step, int error) {
    return alsaError(PlaybackErrorCode::ConfigureFailed, step, error);
  };
  unsigned rate = 0;
  unsigned channels = 0;
  snd_pcm_format_t format = SND_PCM_FORMAT_UNKNOWN;
  snd_pcm_uframes_t period = 0;
  snd_pcm_uframes_t buffer = 0;
  int direction = 0;
  if (const int error = snd_pcm_hw_params_current(pcm, params.get()); error < 0)
    return unreadable("granted params", error);
  if (const int error = snd_pcm_hw_params_get_rate(params.get(), &rate, &direction); error < 0)
    return unreadable("granted rate", error);
  if (const int error = snd_pcm_hw_params_get_channels(params.get(), &channels); error < 0)
    return unreadable("granted channels", error);
  if (const int error = snd_pcm_hw_params_get_format(params.get(), &format); error < 0)
    return unreadable("granted format", error);
  if (const int error = snd_pcm_hw_params_get_period_size(params.get(), &period, &direction); error < 0)
    return unreadable("granted period", error);
  if (const int error = snd_pcm_hw_params_get_buffer_size(params.get(), &buffer); error < 0)
    return unreadable("granted buffer", error);
  return PlaybackFormat{static_cast<int>(rate), channels,
                        format == SND_PCM_FORMAT_S16_LE ? SampleFormat::S16Le : SampleFormat::Unsupported, period, buffer};
}

}  // namespace

struct AlsaPlayback::Pcm {
  explicit Pcm(snd_pcm_t* opened) : handle(opened) {}
  ~Pcm() { snd_pcm_close(handle); }

  Pcm(const Pcm&) = delete;
  Pcm& operator=(const Pcm&) = delete;

  snd_pcm_t* handle;
};

AlsaPlayback::AlsaPlayback(std::string device) : device_(std::move(device)) {}

AlsaPlayback::~AlsaPlayback() = default;

Result<PlaybackFormat, PlaybackError> AlsaPlayback::open(const PlaybackFormat& requested) {
  close();
  snd_pcm_t* handle = nullptr;
  if (const int error = snd_pcm_open(&handle, device_.c_str(), SND_PCM_STREAM_PLAYBACK, SND_PCM_NONBLOCK); error < 0)
    return PlaybackError{PlaybackErrorCode::OpenFailed, alsaText(device_.c_str(), error)};
  auto pcm = std::make_unique<Pcm>(handle);

  if (auto refused = configureHardware(handle, requested))
    return *refused;
  const auto granted = grantedFormat(handle);
  if (!granted.ok())
    return granted.error();
  auto format = granted.value();
  format.fillFrames = requested.fillFrames;
  if (auto mismatch = checkPlaybackFormat(requested, format))
    return *mismatch;
  if (auto refused = configureSoftware(handle, format))
    return *refused;
  pcm_ = std::move(pcm);
  channels_ = format.channels;
  reservedFrames_ = format.bufferFrames - format.fillFrames;
  return format;
}

PlaybackWrite AlsaPlayback::write(std::span<const std::int16_t> interleaved, std::chrono::milliseconds timeout) {
  if (!pcm_)
    return {PlaybackWrite::Kind::Failed, 0, PlaybackError{PlaybackErrorCode::NotOpen, device_}};
  auto* pcm = pcm_->handle;
  const auto reserved = static_cast<snd_pcm_sframes_t>(reservedFrames_);

  // writes only into the room below the fill cap
  snd_pcm_sframes_t result = snd_pcm_avail(pcm);
  if (result >= 0 && result <= reserved) {
    const int ready = snd_pcm_wait(pcm, static_cast<int>(timeout.count()));
    result = ready < 0 ? ready : snd_pcm_avail(pcm);
  }
  if (result > reserved) {
    const auto room = static_cast<snd_pcm_uframes_t>(result - reserved);
    const auto frames = static_cast<snd_pcm_uframes_t>(interleaved.size() / channels_);
    result = snd_pcm_writei(pcm, interleaved.data(), std::min(room, frames));
  } else if (result >= 0) {
    return {};
  }
  if (result >= 0)
    return {PlaybackWrite::Kind::Frames, static_cast<std::size_t>(result), std::nullopt};
  // nothing was lost: no room yet, or a signal interrupted the wait
  if (result == -EAGAIN || result == -EINTR)
    return {};

  const int error = static_cast<int>(result);
  const auto code = error == -EPIPE      ? PlaybackErrorCode::Underrun
                    : error == -ESTRPIPE ? PlaybackErrorCode::Suspended
                                         : PlaybackErrorCode::WriteFailed;
  PlaybackError lost = alsaError(code, "write", error);
  if (const int recovered = snd_pcm_recover(pcm, error, 1); recovered < 0) {
    lost.code = PlaybackErrorCode::WriteFailed;
    lost.detail.append(", ").append(alsaText("recover", recovered));
    return {PlaybackWrite::Kind::Failed, 0, std::move(lost)};
  }
  return {PlaybackWrite::Kind::Recovered, 0, std::move(lost)};
}

Result<std::size_t, PlaybackError> AlsaPlayback::rewind() {
  if (!pcm_)
    return PlaybackError{PlaybackErrorCode::NotOpen, device_};
  auto* pcm = pcm_->handle;
  const auto rewindable = snd_pcm_rewindable(pcm);
  // a stream that ran dry or was suspended has nothing left to take back
  if (rewindable == -EPIPE || rewindable == -ESTRPIPE || rewindable == 0)
    return std::size_t{0};
  if (rewindable < 0)
    return alsaError(PlaybackErrorCode::DiscardFailed, "rewindable", static_cast<int>(rewindable));
  const auto rewound = snd_pcm_rewind(pcm, static_cast<snd_pcm_uframes_t>(rewindable));
  if (rewound < 0)
    return alsaError(PlaybackErrorCode::DiscardFailed, "rewind", static_cast<int>(rewound));
  return static_cast<std::size_t>(rewound);
}

Result<std::size_t, PlaybackError> AlsaPlayback::delayFrames() {
  if (!pcm_)
    return PlaybackError{PlaybackErrorCode::NotOpen, device_};
  snd_pcm_sframes_t delay = 0;
  if (const int error = snd_pcm_delay(pcm_->handle, &delay); error < 0)
    return alsaError(PlaybackErrorCode::DelayFailed, "delay", error);
  // a stream that ran dry can report a negative delay; nothing is unplayed then
  return static_cast<std::size_t>(std::max<snd_pcm_sframes_t>(delay, 0));
}

std::optional<PlaybackError> AlsaPlayback::drop() {
  if (!pcm_)
    return PlaybackError{PlaybackErrorCode::NotOpen, device_};
  if (const int error = snd_pcm_drop(pcm_->handle); error < 0)
    return alsaError(PlaybackErrorCode::DiscardFailed, "drop", error);
  if (const int error = snd_pcm_prepare(pcm_->handle); error < 0)
    return alsaError(PlaybackErrorCode::DiscardFailed, "prepare", error);
  return std::nullopt;
}

void AlsaPlayback::close() {
  pcm_.reset();
  channels_ = 0;
  reservedFrames_ = 0;
}

}  // namespace cadence
