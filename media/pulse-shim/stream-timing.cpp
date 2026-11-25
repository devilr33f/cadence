// timing info: a snapshot of the write and read indexes, taken when an update completes on the loop
#include "pulse-shim/guard.h"
#include "pulse-shim/stream.h"
#include "pulse-shim/util.h"

#include <limits>

namespace cadence::pulse {

namespace {

std::int64_t signedIndex(std::uint64_t index) {
  constexpr auto most = static_cast<std::uint64_t>(std::numeric_limits<std::int64_t>::max());
  return static_cast<std::int64_t>(index > most ? most : index);
}

// the buffered latency is write_index - read_index; nothing else is known, so the other times are zero
pa_timing_info info(const Indexes& indexes) {
  pa_timing_info timing{};
  // the read index is as of the server's last report
  timing.timestamp = indexes.reported ? *indexes.reported : now();
  timing.synchronized_clocks = 1;
  timing.playing = indexes.playing ? 1 : 0;
  timing.write_index = signedIndex(indexes.write);
  timing.read_index = signedIndex(indexes.read);
  timing.since_underrun = signedIndex(indexes.sinceUnderrun);
  return timing;
}

}  // namespace

void Stream::snapshot() {
  std::optional<Indexes> indexes;
  if (!peekBuffer([&](const Buffer& buffer) { indexes = buffer.indexes(); }))
    return;
  timing_ = info(*indexes);
  std::scoped_lock lock(mutex_);
  timed_ = true;
}

const pa_timing_info* Stream::timingInfo() {
  bool automatic = false;
  {
    std::scoped_lock lock(mutex_);
    automatic = autoTiming_;
  }
  // updated automatically: at least as fresh as the documented 100 ms
  if (automatic)
    snapshot();
  std::scoped_lock lock(mutex_);
  return timed_ ? &timing_ : nullptr;
}

pa_operation* Stream::updateTiming(pa_stream_success_cb_t cb, void* userdata) {
  if (state() != PA_STREAM_READY) {
    context_->setError(PA_ERR_BADSTATE);
    return nullptr;
  }
  return operation(cb, userdata, [](Stream& stream) { stream.snapshot(); });
}

}  // namespace cadence::pulse

using namespace cadence::pulse;

extern "C" {

pa_operation* pa_stream_update_timing_info(pa_stream* p, pa_stream_success_cb_t cb, void* userdata) {
  return guarded(static_cast<pa_operation*>(nullptr), [&]() -> pa_operation* {
    const auto stream = Stream::retain(p);
    return stream ? stream->updateTiming(cb, userdata) : nullptr;
  });
}

const pa_timing_info* pa_stream_get_timing_info(pa_stream* s) {
  return guarded(static_cast<const pa_timing_info*>(nullptr), [&]() -> const pa_timing_info* {
    const auto stream = Stream::retain(s);
    return stream ? stream->timingInfo() : nullptr;
  });
}

}  // extern "C"
