#include "capture/alsa-capture.h"

#include <alsa/asoundlib.h>

#include <cerrno>
#include <memory>
#include <optional>
#include <string>
#include <utility>

namespace cadence {

namespace {

std::string alsaText(const char* step, int error) {
  return std::string(step).append(": ").append(snd_strerror(error));
}

CaptureError alsaError(CaptureErrorCode code, const char* step, int error) { return {code, alsaText(step, error)}; }

Result<CaptureFormat, CaptureError> configure(snd_pcm_t* pcm, const CaptureFormat& requested) {
  snd_pcm_hw_params_t* raw = nullptr;
  if (const int error = snd_pcm_hw_params_malloc(&raw); error < 0)
    return alsaError(CaptureErrorCode::ConfigureFailed, "hw params", error);
  const std::unique_ptr<snd_pcm_hw_params_t, decltype(&snd_pcm_hw_params_free)> params(raw, snd_pcm_hw_params_free);

  const auto refused = [](const char* step, int error) { return alsaError(CaptureErrorCode::ConfigureFailed, step, error); };
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

  unsigned rate = 0;
  unsigned channels = 0;
  if (const int error = snd_pcm_hw_params_get_rate(params.get(), &rate, &direction); error < 0)
    return alsaError(CaptureErrorCode::ConfigureFailed, "granted rate", error);
  if (const int error = snd_pcm_hw_params_get_channels(params.get(), &channels); error < 0)
    return alsaError(CaptureErrorCode::ConfigureFailed, "granted channels", error);
  if (const int error = snd_pcm_hw_params_get_period_size(params.get(), &period, &direction); error < 0)
    return alsaError(CaptureErrorCode::ConfigureFailed, "granted period", error);
  if (const int error = snd_pcm_hw_params_get_buffer_size(params.get(), &buffer); error < 0)
    return alsaError(CaptureErrorCode::ConfigureFailed, "granted buffer", error);
  return CaptureFormat{static_cast<int>(rate), channels, period, buffer};
}

}  // namespace

struct AlsaCapture::Pcm {
  explicit Pcm(snd_pcm_t* opened) : handle(opened) {}
  ~Pcm() { snd_pcm_close(handle); }

  Pcm(const Pcm&) = delete;
  Pcm& operator=(const Pcm&) = delete;

  snd_pcm_t* handle;
};

AlsaCapture::AlsaCapture(std::string device) : device_(std::move(device)) {}

AlsaCapture::~AlsaCapture() = default;

Result<CaptureFormat, CaptureError> AlsaCapture::open(const CaptureFormat& requested) {
  close();
  snd_pcm_t* handle = nullptr;
  if (const int error = snd_pcm_open(&handle, device_.c_str(), SND_PCM_STREAM_CAPTURE, SND_PCM_NONBLOCK); error < 0)
    return CaptureError{CaptureErrorCode::OpenFailed, alsaText(device_.c_str(), error)};
  auto pcm = std::make_unique<Pcm>(handle);

  auto granted = configure(handle, requested);
  if (!granted.ok())
    return granted.error();
  if (auto mismatch = checkExactFormat(requested, granted.value()))
    return *mismatch;
  pcm_ = std::move(pcm);
  channels_ = granted.value().channels;
  return granted;
}

CaptureRead AlsaCapture::read(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout) {
  if (!pcm_)
    return {CaptureRead::Kind::Failed, 0, CaptureError{CaptureErrorCode::NotOpen, device_}};
  auto* pcm = pcm_->handle;
  const auto frames = static_cast<snd_pcm_uframes_t>(interleaved.size() / channels_);

  // a fresh or recovered stream waits in the prepared state until started
  snd_pcm_sframes_t result = 0;
  if (snd_pcm_state(pcm) == SND_PCM_STATE_PREPARED)
    result = snd_pcm_start(pcm);
  if (result == 0) {
    result = snd_pcm_readi(pcm, interleaved.data(), frames);
    if (result == -EAGAIN) {
      const int ready = snd_pcm_wait(pcm, static_cast<int>(timeout.count()));
      result = ready > 0 ? snd_pcm_readi(pcm, interleaved.data(), frames) : ready < 0 ? ready : -EAGAIN;
    }
  }
  if (result >= 0)
    return {CaptureRead::Kind::Frames, static_cast<std::size_t>(result), std::nullopt};
  // nothing was lost: no audio yet, or a signal interrupted the wait
  if (result == -EAGAIN || result == -EINTR)
    return {};

  const int error = static_cast<int>(result);
  const auto code = error == -EPIPE      ? CaptureErrorCode::Overrun
                    : error == -ESTRPIPE ? CaptureErrorCode::Suspended
                                         : CaptureErrorCode::ReadFailed;
  CaptureError lost = alsaError(code, "read", error);
  if (const int recovered = snd_pcm_recover(pcm, error, 1); recovered < 0) {
    lost.code = CaptureErrorCode::ReadFailed;
    lost.detail.append(", ").append(alsaText("recover", recovered));
    return {CaptureRead::Kind::Failed, 0, std::move(lost)};
  }
  return {CaptureRead::Kind::Recovered, 0, std::move(lost)};
}

void AlsaCapture::close() {
  pcm_.reset();
  channels_ = 0;
}

}  // namespace cadence
