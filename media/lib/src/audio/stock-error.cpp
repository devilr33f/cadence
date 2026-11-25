#include "audio/stock-error.h"

namespace cadence {

namespace {

const char* summary(StockErrorCode code) {
  switch (code) {
    case StockErrorCode::LibraryLoadFailed:
      return "stock library failed to load";
    case StockErrorCode::MissingSymbol:
      return "stock library lacks symbol";
    case StockErrorCode::VqeConfigFailed:
      return "vqe preset failed";
    case StockErrorCode::ConfigMismatch:
      return "vqe config mismatch";
    case StockErrorCode::VqeCreateFailed:
      return "vqe engine failed";
    case StockErrorCode::SpotterConfigFailed:
      return "spotter model failed";
    case StockErrorCode::SpotterLayoutUnsupported:
      return "spotter layout unsupported";
    case StockErrorCode::SpotterCreateFailed:
      return "spotter failed";
    case StockErrorCode::NotInitialized:
      return "stock audio not initialized";
    case StockErrorCode::InvalidInput:
      return "invalid vqe input";
    case StockErrorCode::VqeProcessFailed:
      return "vqe process failed";
    case StockErrorCode::VqeOutputFailed:
      return "vqe output failed";
    case StockErrorCode::NonFiniteOutput:
      return "vqe output not finite";
  }
  return "stock audio failed";
}

}  // namespace

std::string describe(const StockError& error) {
  std::string message = summary(error.code);
  if (!error.detail.empty())
    message.append(": ").append(error.detail);
  return text::boundedMessage(message.c_str());
}

}  // namespace cadence
