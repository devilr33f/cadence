#pragma once

#include <chrono>
#include <cstdint>
#include <span>

namespace cadence::dbus {

using Clock = std::chrono::steady_clock;

enum class Wait : std::uint8_t { Ready, Timeout, Stopped, Failed };

// until fd has one of events, the deadline passes or stopFd (skipped when negative) turns readable
Wait waitFor(int fd, short events, int stopFd, Clock::time_point deadline);
// all of bytes on a non-blocking socket before the deadline
Wait writeAll(int fd, std::span<const std::uint8_t> bytes, int stopFd, Clock::time_point deadline);
// poll's timeout until the deadline: rounded up, so a wait never ends early; -1 for none
int pollTimeout(const Clock::time_point* deadline);

}  // namespace cadence::dbus
