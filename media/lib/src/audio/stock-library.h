#pragma once

#include "audio/stock-error.h"
#include "result.h"

#include <filesystem>
#include <functional>
#include <memory>

namespace cadence {

class StockLibrary {
 public:
  virtual ~StockLibrary() = default;
  virtual void* symbol(const char* name) const = 0;
};

using StockLibraryResult = Result<std::shared_ptr<StockLibrary>, StockError>;
using StockLibraryLoader = std::function<StockLibraryResult(const std::filesystem::path& library)>;

StockLibraryResult openStockLibrary(const std::filesystem::path& library);

}  // namespace cadence
