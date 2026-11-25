#pragma once

#include "text.h"

#include <cstddef>
#include <string>

namespace cadence {

inline constexpr std::size_t kMaxStockErrorLength = text::kMaxBoundedLength;

enum class StockErrorCode {
  LibraryLoadFailed,
  MissingSymbol,
  VqeConfigFailed,
  ConfigMismatch,
  VqeCreateFailed,
  SpotterConfigFailed,
  SpotterLayoutUnsupported,
  SpotterCreateFailed,
  NotInitialized,
  InvalidInput,
  VqeProcessFailed,
  VqeOutputFailed,
  NonFiniteOutput,
};

// detail is a symbol name, a size mismatch, or a sanitized stock message; never audio
struct StockError {
  StockErrorCode code;
  std::string detail;
};

// bounded single-line text, safe to log
std::string describe(const StockError& error);

}  // namespace cadence
