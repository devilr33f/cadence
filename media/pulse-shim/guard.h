#pragma once

#include <utility>

namespace cadence::pulse {

// runs an entry point's body so that no exception unwinds into the c caller: it gets fallback instead
template <typename T, typename Body>
T guarded(T fallback, Body&& body) noexcept {
  try {
    return std::forward<Body>(body)();
  } catch (...) {
    return fallback;
  }
}

template <typename Body>
void guarded(Body&& body) noexcept {
  try {
    std::forward<Body>(body)();
  } catch (...) {
  }
}

}  // namespace cadence::pulse
