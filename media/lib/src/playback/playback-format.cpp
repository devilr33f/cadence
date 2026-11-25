#include "playback/alsa-playback.h"

#include "text.h"

#include <optional>
#include <string>

namespace cadence {

namespace {

const char* summary(PlaybackErrorCode code) {
  switch (code) {
    case PlaybackErrorCode::AlreadyRunning:
      return "playback already running";
    case PlaybackErrorCode::OpenFailed:
      return "playback device failed to open";
    case PlaybackErrorCode::ConfigureFailed:
      return "playback format refused";
    case PlaybackErrorCode::FormatMismatch:
      return "playback format mismatch";
    case PlaybackErrorCode::NotOpen:
      return "playback not open";
    case PlaybackErrorCode::Underrun:
      return "playback underrun";
    case PlaybackErrorCode::Suspended:
      return "playback suspended";
    case PlaybackErrorCode::WriteFailed:
      return "playback write failed";
    case PlaybackErrorCode::DiscardFailed:
      return "playback discard failed";
    case PlaybackErrorCode::DelayFailed:
      return "playback delay failed";
  }
  return "playback failed";
}

const char* formatName(SampleFormat format) {
  return format == SampleFormat::S16Le ? "s16_le" : "unsupported format";
}

std::string formatText(const PlaybackFormat& format) {
  return std::to_string(format.sampleRate) + " Hz x " + std::to_string(format.channels) + " " +
         formatName(format.sampleFormat);
}

}  // namespace

std::string describe(const PlaybackError& error) {
  std::string message = summary(error.code);
  if (!error.detail.empty())
    message.append(": ").append(error.detail);
  return text::boundedMessage(message.c_str());
}

std::optional<PlaybackError> checkPlaybackFormat(const PlaybackFormat& requested, const PlaybackFormat& granted) {
  if (granted.sampleRate != requested.sampleRate || granted.channels != requested.channels ||
      granted.sampleFormat != requested.sampleFormat)
    return PlaybackError{PlaybackErrorCode::FormatMismatch,
                         "granted " + formatText(granted) + "; need exactly " + formatText(requested)};
  if (granted.periodFrames == 0 || granted.periodFrames > granted.bufferFrames)
    return PlaybackError{PlaybackErrorCode::FormatMismatch, "granted period " + std::to_string(granted.periodFrames) +
                                                                " of buffer " + std::to_string(granted.bufferFrames)};
  if (requested.fillFrames < 2 * granted.periodFrames || requested.fillFrames > granted.bufferFrames)
    return PlaybackError{PlaybackErrorCode::FormatMismatch,
                         "fill " + std::to_string(requested.fillFrames) + " outside two of granted period " +
                             std::to_string(granted.periodFrames) + " to buffer " + std::to_string(granted.bufferFrames)};
  return std::nullopt;
}

}  // namespace cadence
