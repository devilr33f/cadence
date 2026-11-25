#include "audiod/child-logs.h"

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <cstring>
#include <deque>
#include <mutex>
#include <optional>
#include <string_view>
#include <utility>

namespace cadence::audiod {

namespace {

using Clock = std::chrono::steady_clock;

constexpr auto kReportEvery = std::chrono::seconds(1);

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  const auto size = std::min(text.size(), N - 1);
  std::memcpy(out, text.data(), size);
  out[size] = '\0';
}

Envelope envelopeOf(const ChildLine& line) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_log_line_tag;
  auto& body = envelope.body.log_line;
  copyText(body.source, line.source);
  body.level = line.level;
  copyText(body.text, line.text);
  body.truncated = line.truncated;
  body.continued = line.continued;
  return envelope;
}

static_assert(sizeof(cadence_audiod_v1_LogLine::text) > kChildLineBytes, "a child's line must fit LogLine.text");

}  // namespace

class ChildLogs::Outbox : public std::enable_shared_from_this<Outbox> {
 public:
  Outbox(Loop& loop, IpcServer& ipc, const Log& log) : loop_(loop), ipc_(ipc), log_(log) {}

  // any thread
  void push(ChildLine line) {
    std::scoped_lock lock(mutex_);
    if (closed_)
      return;
    if (queue_.size() == kChildLinesQueued) {
      queue_.pop_front();
      ++dropped_;
    }
    queue_.push_back(std::move(line));
    if (!posted_) {
      posted_ = true;
      loop_.post([self = shared_from_this()] { self->flush(); });
    }
  }

  void close() {
    std::scoped_lock lock(mutex_);
    closed_ = true;
    queue_.clear();
  }

 private:
  // the loop thread's
  void flush() {
    std::deque<ChildLine> lines;
    {
      std::scoped_lock lock(mutex_);
      lines.swap(queue_);
      posted_ = false;
      if (closed_)
        return;
    }
    std::uint64_t refused = 0;
    for (const auto& line : lines)
      refused += ipc_.send(envelopeOf(line)) ? 0U : 1U;
    {
      std::scoped_lock lock(mutex_);
      dropped_ += refused;
    }
    report();
  }

  // the loop thread's: what was dropped, at once or, within a second of the last report, once it passed
  void report() {
    std::uint64_t dropped = 0;
    {
      std::scoped_lock lock(mutex_);
      if (closed_)
        return;
      dropped = dropped_;
    }
    if (dropped == 0)
      return;
    const auto now = Clock::now();
    if (reported_ && now < *reported_ + kReportEvery) {
      if (!timer_)
        timer_ = loop_.at(*reported_ + kReportEvery, [self = shared_from_this()] {
          self->timer_.reset();
          self->report();
        });
      return;
    }
    {
      std::scoped_lock lock(mutex_);
      dropped_ -= dropped;
    }
    reported_ = now;
    log_.warn("child_log_dropped", {{"count", dropped}});
  }

  Loop& loop_;
  IpcServer& ipc_;
  const Log& log_;

  std::mutex mutex_;
  std::deque<ChildLine> queue_;
  bool posted_ = false;
  bool closed_ = false;
  std::uint64_t dropped_ = 0;

  std::optional<Clock::time_point> reported_;
  std::optional<Loop::TimerId> timer_;
};

ChildLogs::ChildLogs(Loop& loop, IpcServer& ipc, const Log& log)
    : outbox_(std::make_shared<Outbox>(loop, ipc, log)), forward_(std::make_shared<ChildLogSwitch>()) {
  ipc.on(cadence_audiod_v1_Envelope_logs_set_tag, [forward = forward_, &log](const Envelope& envelope) {
    const bool on = envelope.body.logs_set.third_party;
    if (forward->on() != on)
      log.info("child_logs", {{"on", on ? 1 : 0}});
    forward->set(on);
  });
}

ChildLogs::~ChildLogs() { outbox_->close(); }

ChildLogTarget ChildLogs::target() const {
  return {.forward = forward_, .sink = [outbox = outbox_](ChildLine line) { outbox->push(std::move(line)); }};
}

}  // namespace cadence::audiod
