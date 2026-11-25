#include "audio/stock-multi-conf.h"

#include <utility>

namespace cadence {

std::optional<StockError> MultiConfHandle::open() {
  reset();
  multi_ = symbols_.multiConfCreate(nullptr, false);
  if (multi_ == nullptr)
    return StockError{StockErrorCode::SpotterCreateFailed, "no multi conf"};
  return std::nullopt;
}

Result<stock_abi::Component*, StockError> MultiConfHandle::attach(SpotterConfHandle conf) {
  // attach owns the conf even when it returns null, so it is never freed here
  auto* raw = conf.release();
  char* message = nullptr;
  auto* component = symbols_.multiConfAttach(multi_, raw, &message);
  auto detail = takeSpotterMessage(symbols_, message, "conf not attached");
  if (component == nullptr)
    return StockError{StockErrorCode::SpotterCreateFailed, std::move(detail)};
  components_.push_back(component);
  return component;
}

void MultiConfHandle::reset() {
  if (multi_ == nullptr)
    return;
  for (auto* component : components_)
    symbols_.multiConfDetach(multi_, component);
  components_.clear();
  symbols_.multiConfFree(std::exchange(multi_, nullptr));
}

}  // namespace cadence
