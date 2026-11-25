#include "capture/alsa-capture.h"

#include "text.h"

#include <optional>
#include <string>

namespace cadence {

namespace {

const char* summary(CaptureErrorCode code) {
  switch (code) {
    case CaptureErrorCode::OpenFailed:
      return "capture device failed to open";
    case CaptureErrorCode::ConfigureFailed:
      return "capture format refused";
    case CaptureErrorCode::FormatMismatch:
      return "capture format mismatch";
    case CaptureErrorCode::NotOpen:
      return "capture not open";
    case CaptureErrorCode::Overrun:
      return "capture overrun";
    case CaptureErrorCode::Suspended:
      return "capture suspended";
    case CaptureErrorCode::ReadFailed:
      return "capture read failed";
  }
  return "capture failed";
}

std::string formatText(const CaptureFormat& format) {
  return std::to_string(format.sampleRate) + " Hz x " + std::to_string(format.channels) + ", period " +
         std::to_string(format.periodFrames);
}

}  // namespace

std::string describe(const CaptureError& error) {
  std::string message = summary(error.code);
  if (!error.detail.empty())
    message.append(": ").append(error.detail);
  return text::boundedMessage(message.c_str());
}

std::optional<CaptureError> checkExactFormat(const CaptureFormat& requested, const CaptureFormat& granted) {
  if (granted.sampleRate == requested.sampleRate && granted.channels == requested.channels &&
      granted.periodFrames == requested.periodFrames)
    return std::nullopt;
  return CaptureError{CaptureErrorCode::FormatMismatch,
                      "granted " + formatText(granted) + "; need exactly " + formatText(requested)};
}

}  // namespace cadence
