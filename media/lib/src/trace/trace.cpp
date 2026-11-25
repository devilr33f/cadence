#include "trace/trace.h"

#include <algorithm>
#include <ctime>

namespace cadence::trace {

namespace {

bool lowerHex(std::string_view text) {
  return std::all_of(text.begin(), text.end(),
                     [](char c) { return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'); });
}

}  // namespace

void Id::assign(std::string_view id) noexcept {
  const auto n = std::min(id.size(), kIdBytes - 1);
  std::copy_n(id.data(), n, text.data());
  text[n] = '\0';
  size = static_cast<std::uint8_t>(n);
}

std::int64_t unixNowNs() noexcept {
  timespec now{};
  ::clock_gettime(CLOCK_REALTIME, &now);
  return static_cast<std::int64_t>(now.tv_sec) * 1'000'000'000 + now.tv_nsec;
}

Mark makeMark(Stage stage, Target target, std::string_view id, std::initializer_list<Value> values,
              std::int64_t atUnixNs, std::int64_t durationNs) noexcept {
  Mark mark;
  mark.stage = stage;
  mark.target = target;
  mark.id.assign(id);
  mark.atUnixNs = atUnixNs != 0 ? atUnixNs : unixNowNs();
  mark.durationNs = durationNs;
  for (const auto& value : values) {
    if (mark.valueCount == kMaxValues)
      break;
    mark.values[mark.valueCount++] = value;
  }
  return mark;
}

std::string_view traceId(std::string_view traceparent) noexcept {
  // version, trace id, span id and flags: 2 + 1 + 32 + 1 + 16 + 1 + 2
  if (traceparent.size() != 55 || traceparent[2] != '-' || traceparent[35] != '-' || traceparent[52] != '-')
    return {};
  const auto id = traceparent.substr(3, 32);
  if (!lowerHex(traceparent.substr(0, 2)) || !lowerHex(id) || !lowerHex(traceparent.substr(36, 16)) ||
      !lowerHex(traceparent.substr(53, 2)) || id == std::string_view("00000000000000000000000000000000"))
    return {};
  return id;
}

}  // namespace cadence::trace
