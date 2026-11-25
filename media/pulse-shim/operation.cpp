#include "pulse-shim/operation.h"

#include "pulse-shim/guard.h"

namespace cadence::pulse {

std::pair<std::shared_ptr<Operation>, pa_operation*> Operation::start() {
  auto operation = std::make_shared<Operation>();
  operation->caller_ = operation;
  return {operation, reinterpret_cast<pa_operation*>(operation.get())};
}

void Operation::release(pa_operation* raw) {
  // the last reference may go here: nothing of it is touched afterwards
  const auto caller = std::move(reinterpret_cast<Operation*>(raw)->caller_);
}

}  // namespace cadence::pulse

using namespace cadence::pulse;

extern "C" {

pa_operation_state_t pa_operation_get_state(const pa_operation* o) {
  return guarded(pa_operation_state_t{PA_OPERATION_CANCELLED},
                 [&] { return o == nullptr ? PA_OPERATION_CANCELLED : Operation::from(o)->state(); });
}

void pa_operation_unref(pa_operation* o) {
  guarded([&] {
    if (o != nullptr)
      Operation::release(o);
  });
}

}  // extern "C"
