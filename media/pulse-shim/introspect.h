#pragma once

#include "pulse-shim/abi.h"

#include <cstdint>
#include <optional>

namespace cadence::pulse {

// the client's one sink input, as a server would report it: event subscription, its stored volume and its info
struct SinkInput {
  pa_context_subscribe_cb_t onEvent = nullptr;
  void* eventData = nullptr;
  pa_subscription_mask_t mask = PA_SUBSCRIPTION_MASK_NULL;
  // what the client set on it; reported back, never applied to a sample (unity gain)
  pa_cvolume volume = stereoNorm();

  // true when it changed
  bool store(const pa_cvolume& stored);
  // the playing stream's sample spec when there is one, else the format soloist plays
  [[nodiscard]] pa_sink_input_info info(std::uint32_t index, std::optional<pa_sample_spec> playing) const;

  static pa_cvolume stereoNorm();
};

}  // namespace cadence::pulse
