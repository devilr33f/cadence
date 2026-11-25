#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <initializer_list>
#include <string_view>

// cadence_media_trace's cmake target sets it (the CADENCE_TRACING option) for everything that links it
#ifndef CADENCE_TRACING
#error "CADENCE_TRACING is set by the cadence_media_trace target"
#endif

namespace cadence::trace {

// off, every mark compiles away: no queue is made and nothing is sent (WakeEvent.at_unix_ns is filled either way)
inline constexpr bool kEnabled = CADENCE_TRACING != 0;

// audiod.proto's TraceStage, by the same numbers
enum class Stage : std::uint8_t {
  WakeDetected = 1,
  CaptureStarted = 2,
  FirstOpusFrame = 3,
  CaptureStats = 4,
  CaptureStopped = 5,
  PlaybackOpened = 6,
  FirstSamplePlayed = 7,
  Underrun = 8,
  StreamDone = 9,
  VqeStats = 10,
};

// what a mark's id names: a capture's session (CaptureStart's id), a stream (StreamStart's), or nothing (a wake)
enum class Target : std::uint8_t { None, Session, Stream };

// TraceMark's id fields hold 127 characters and their nul
inline constexpr std::size_t kIdBytes = 128;
inline constexpr std::size_t kMaxValues = 8;

// an id in fixed storage, so a realtime thread hands one on without allocating; a longer one is cut
struct Id {
  std::array<char, kIdBytes> text{};
  std::uint8_t size = 0;

  void assign(std::string_view id) noexcept;
  [[nodiscard]] std::string_view view() const noexcept { return {text.data(), size}; }
  [[nodiscard]] bool empty() const noexcept { return size == 0; }
};

// a number a mark carries. key is a string literal of a fixed code (^[a-z][a-z0-9_]{0,31}$), never text of the
// user's (rule 8)
struct Value {
  const char* key = nullptr;
  double value = 0;
};

// one TraceMark, before it is encoded
struct Mark {
  Stage stage = Stage::WakeDetected;
  Target target = Target::None;
  Id id;
  // CLOCK_REALTIME, unix ns
  std::int64_t atUnixNs = 0;
  // 0: an instant; else the stage lasted this long from atUnixNs
  std::int64_t durationNs = 0;
  std::array<Value, kMaxValues> values{};
  std::uint8_t valueCount = 0;
};

// CLOCK_REALTIME in unix ns, the clock otel spans use
std::int64_t unixNowNs() noexcept;

// a mark at atUnixNs (0: now); values past kMaxValues are left out
Mark makeMark(Stage stage, Target target, std::string_view id, std::initializer_list<Value> values = {},
              std::int64_t atUnixNs = 0, std::int64_t durationNs = 0) noexcept;

// the trace id of a w3c traceparent (00-<32 hex>-<16 hex>-<2 hex>), empty when it is not one: an id audiod may log
// beside its lines
std::string_view traceId(std::string_view traceparent) noexcept;

}  // namespace cadence::trace
