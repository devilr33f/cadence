#include "pulse-shim/util.h"

#include "pulse-shim/abi.h"
#include "pulse-shim/guard.h"

#include <algorithm>
#include <array>
#include <cstdint>
#include <cstdlib>

namespace cadence::pulse {

namespace {

// indexed by error code, worded after the enumerator documentation
constexpr std::array<const char*, PA_ERR_MAX> kErrors{
    "No error",
    "Access failure",
    "Unknown command",
    "Invalid argument",
    "Entity exists",
    "No such entity",
    "Connection refused",
    "Protocol error",
    "Timeout",
    "No authentication key",
    "Internal error",
    "Connection terminated",
    "Entity killed",
    "Invalid server",
    "Module initialization failed",
    "Bad state",
    "No data",
    "Incompatible protocol version",
    "Data too large",
    "Operation not supported",
    "The error code was unknown to the client",
    "Extension does not exist",
    "Obsolete functionality",
    "Missing implementation",
    "The caller forked without calling execve() and tried to reuse the context",
    "An IO error happened",
    "Device or resource busy",
};
constexpr const char* kUnknownError = "Unknown error code";

}  // namespace

timeval now() {
  timeval tv{};
  ::gettimeofday(&tv, nullptr);
  return tv;
}

std::int64_t usec(const timeval& tv) { return static_cast<std::int64_t>(tv.tv_sec) * 1000000 + tv.tv_usec; }

}  // namespace cadence::pulse

using namespace cadence::pulse;

extern "C" {

const char* pa_strerror(int error) {
  return guarded(kUnknownError, [&] {
    return error >= 0 && error < PA_ERR_MAX ? kErrors[static_cast<std::size_t>(error)] : kUnknownError;
  });
}

timeval* pa_gettimeofday(timeval* tv) {
  return guarded(static_cast<timeval*>(nullptr), [&] {
    if (tv != nullptr)
      *tv = now();
    return tv;
  });
}

pa_usec_t pa_timeval_diff(const timeval* a, const timeval* b) {
  return guarded(pa_usec_t{0}, [&] {
    if (a == nullptr || b == nullptr)
      return pa_usec_t{0};
    const auto difference = usec(*a) - usec(*b);
    return static_cast<pa_usec_t>(difference < 0 ? -difference : difference);
  });
}

pa_cvolume* pa_cvolume_set(pa_cvolume* a, unsigned channels, pa_volume_t v) {
  return guarded(static_cast<pa_cvolume*>(nullptr), [&]() -> pa_cvolume* {
    if (a == nullptr || channels == 0 || channels > PA_CHANNELS_MAX || v > PA_VOLUME_MAX)
      return nullptr;
    a->channels = static_cast<std::uint8_t>(channels);
    for (unsigned i = 0; i < channels; ++i)
      a->values[i] = v;
    return a;
  });
}

pa_volume_t pa_cvolume_avg(const pa_cvolume* a) {
  return guarded(pa_volume_t{PA_VOLUME_MUTED}, [&]() -> pa_volume_t {
    if (a == nullptr || a->channels == 0)
      return PA_VOLUME_MUTED;
    const std::size_t n = std::min<std::size_t>(a->channels, PA_CHANNELS_MAX);
    std::uint64_t sum = 0;
    for (std::size_t i = 0; i < n; ++i)
      sum += a->values[i];
    return static_cast<pa_volume_t>(sum / n);
  });
}

}  // extern "C"
