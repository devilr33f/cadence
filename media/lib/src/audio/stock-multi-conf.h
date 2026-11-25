#pragma once

#include "audio/stock-error.h"
#include "audio/stock-symbols.h"
#include "audio/stock-types.h"
#include "result.h"

#include <optional>
#include <vector>

namespace cadence {

// a stock multi conf and the components attached to it. it owns every conf handed to attach and every
// component handle; reset detaches each component, then frees the multi conf, so destroy the spotter built
// from it first
class MultiConfHandle {
 public:
  explicit MultiConfHandle(const StockSymbols& symbols) : symbols_(symbols) {}
  ~MultiConfHandle() { reset(); }

  MultiConfHandle(const MultiConfHandle&) = delete;
  MultiConfHandle& operator=(const MultiConfHandle&) = delete;

  // releases any previous multi conf first
  std::optional<StockError> open();
  // the library quick_exits on a conf it cannot take, so check rates and channels first. the conf is the
  // library's from the call on, even when no component comes back, so nothing frees it after
  Result<stock_abi::Component*, StockError> attach(SpotterConfHandle conf);
  void reset();

  [[nodiscard]] stock_abi::MultiConf* get() const { return multi_; }

 private:
  const StockSymbols& symbols_;
  stock_abi::MultiConf* multi_ = nullptr;
  std::vector<stock_abi::Component*> components_;
};

}  // namespace cadence
