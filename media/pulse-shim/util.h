#pragma once

#include <sys/time.h>

#include <cstdint>

namespace cadence::pulse {

timeval now();
std::int64_t usec(const timeval& tv);

}  // namespace cadence::pulse
