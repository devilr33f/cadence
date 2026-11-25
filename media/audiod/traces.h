#pragma once

#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/sys.h"
#include "trace/trace-queue.h"

#include <cstdint>
#include <initializer_list>
#include <memory>
#include <optional>
#include <string_view>

namespace cadence::audiod {

// audiod's trace marks to cadenced, as TraceMark (Envelope.trace_mark): ids, numbers and times only (rule 8), whether
// or not cadenced traces (it drops what names no span of its own). the playback thread pushes through tracer() into a
// bounded queue (trace/trace-queue.h), never waiting or allocating; the queue wakes the loop through an eventfd and is
// drained there. the loop's own parts mark through mark(), which sends what the queue holds first, so a mark goes out
// before a frame its part sends next (STREAM_DONE before StreamDone, which ends cadenced's span). a mark that finds the
// queue full is dropped and counted (trace_dropped). built with CADENCE_TRACING off it makes no queue, no fd and sends
// nothing. every call but tracer()'s pushes belongs to the loop thread; it outlives every part that marks through it
class Traces {
 public:
  Traces(Loop& loop, IpcServer& ipc, const Log& log);
  ~Traces();

  Traces(const Traces&) = delete;
  Traces& operator=(const Traces&) = delete;

  // for any thread; inactive when tracing is off or the queue could not be made
  [[nodiscard]] trace::Tracer tracer() noexcept;
  void mark(trace::Stage stage, trace::Target target, std::string_view id,
            std::initializer_list<trace::Value> values = {}, std::int64_t atUnixNs = 0, std::int64_t durationNs = 0);
  // what the threads queued, out now
  void drain();

 private:
  void send(const trace::Mark& mark);

  Loop& loop_;
  IpcServer& ipc_;
  const Log& log_;
  UniqueFd wake_;
  std::optional<Loop::WatchId> watch_;
  std::unique_ptr<trace::Queue> queue_;
  // marks dropped in all, and the drains that found some dropped
  std::uint64_t dropped_ = 0;
  std::uint64_t overflows_ = 0;
};

}  // namespace cadence::audiod
