#pragma once

#include "pulse-shim/abi.h"

#include <atomic>
#include <memory>
#include <utility>

namespace cadence::pulse {

// a pending request: the library keeps one reference until it finishes and the c caller holds the other
class Operation {
 public:
  // the library's reference, and the caller's as handed to c
  static std::pair<std::shared_ptr<Operation>, pa_operation*> start();
  // drops the caller's reference
  static void release(pa_operation* raw);
  static const Operation* from(const pa_operation* raw) { return reinterpret_cast<const Operation*>(raw); }

  void finish(pa_operation_state_t state) { state_.store(state, std::memory_order_release); }
  [[nodiscard]] pa_operation_state_t state() const { return state_.load(std::memory_order_acquire); }

 private:
  std::atomic<pa_operation_state_t> state_{PA_OPERATION_RUNNING};
  std::shared_ptr<Operation> caller_;
};

}  // namespace cadence::pulse
