#include "audiod/traces.h"

#include "audiod/sparse.h"

#include <sys/epoll.h>
#include <sys/eventfd.h>
#include <unistd.h>

#include <algorithm>
#include <cerrno>
#include <cstddef>
#include <cstring>

namespace cadence::audiod {

namespace {

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  const auto size = std::min(text.size(), N - 1);
  std::memcpy(out, text.data(), size);
  out[size] = '\0';
}

}  // namespace

Traces::Traces(Loop& loop, IpcServer& ipc, const Log& log) : loop_(loop), ipc_(ipc), log_(log) {
  if constexpr (!trace::kEnabled)
    return;
  UniqueFd wake(::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK));
  if (!wake) {
    log_.warn("trace_failed", {{"call", "eventfd"}, {"errno", errno}});
    return;
  }
  auto watched = loop_.watch(wake.get(), EPOLLIN, [this](std::uint32_t) {
    std::uint64_t count = 0;
    [[maybe_unused]] const auto read = ::read(wake_.get(), &count, sizeof count);
    drain();
  });
  if (!watched.ok()) {
    log_.warn("trace_failed", {{"call", watched.error().call}, {"errno", watched.error().code}});
    return;
  }
  watch_ = watched.value();
  queue_ = std::make_unique<trace::Queue>(wake.get());
  wake_ = std::move(wake);
}

Traces::~Traces() {
  if (watch_)
    loop_.unwatch(*watch_);
}

trace::Tracer Traces::tracer() noexcept { return trace::Tracer(queue_.get()); }

void Traces::mark(trace::Stage stage, trace::Target target, std::string_view id,
                  std::initializer_list<trace::Value> values, std::int64_t atUnixNs, std::int64_t durationNs) {
  if constexpr (trace::kEnabled) {
    if (!queue_)
      return;
    drain();
    send(trace::makeMark(stage, target, id, values, atUnixNs, durationNs));
  } else {
    (void)stage, (void)target, (void)id, (void)values, (void)atUnixNs, (void)durationNs;
  }
}

void Traces::drain() {
  if (!queue_)
    return;
  queue_->rearm();
  trace::Mark mark;
  while (queue_->pop(mark))
    send(mark);
  if (const auto dropped = queue_->takeDropped(); dropped > 0) {
    dropped_ += dropped;
    if (sparse(++overflows_))
      log_.warn("trace_dropped", {{"count", dropped_}});
  }
}

void Traces::send(const trace::Mark& mark) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_trace_mark_tag;
  auto& out = envelope.body.trace_mark;
  out.stage = static_cast<cadence_audiod_v1_TraceStage>(mark.stage);
  out.at_unix_ns = mark.atUnixNs;
  out.duration_ns = mark.durationNs;
  if (mark.target == trace::Target::Session)
    copyText(out.session_id, mark.id.view());
  else if (mark.target == trace::Target::Stream)
    copyText(out.stream_id, mark.id.view());
  static_assert(sizeof out.values / sizeof out.values[0] >= trace::kMaxValues);
  for (std::size_t index = 0; index < mark.valueCount; ++index) {
    copyText(out.values[index].key, mark.values[index].key);
    out.values[index].value = mark.values[index].value;
  }
  out.values_count = mark.valueCount;
  ipc_.send(envelope);
}

}  // namespace cadence::audiod
