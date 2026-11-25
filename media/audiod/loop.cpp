#include "audiod/loop.h"

#include <sys/epoll.h>
#include <sys/eventfd.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <cerrno>
#include <cstddef>
#include <limits>
#include <utility>

namespace cadence::audiod {

namespace {

// epoll data of the wake fd; watches count from 1
constexpr Loop::WatchId kWakeId = 0;
constexpr std::size_t kEventsPerWait = 16;

// timers are few, so a scan beats keeping a second index by deadline; ties go to the lower id
template <typename Timers>
auto earliest(Timers& timers) {
  return std::min_element(timers.begin(), timers.end(), [](const auto& left, const auto& right) {
    return left.second.deadline < right.second.deadline;
  });
}

}  // namespace

std::unique_ptr<Loop> Loop::create(const Log& log) {
  UniqueFd epoll(::epoll_create1(EPOLL_CLOEXEC));
  if (!epoll) {
    log.error("loop_failed", {{"call", "epoll_create1"}, {"errno", errno}});
    return nullptr;
  }
  UniqueFd wake(::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK));
  if (!wake) {
    log.error("loop_failed", {{"call", "eventfd"}, {"errno", errno}});
    return nullptr;
  }
  epoll_event event{};
  event.events = EPOLLIN;
  event.data.u64 = kWakeId;
  if (::epoll_ctl(epoll.get(), EPOLL_CTL_ADD, wake.get(), &event) < 0) {
    log.error("loop_failed", {{"call", "epoll_ctl"}, {"errno", errno}});
    return nullptr;
  }
  return std::unique_ptr<Loop>(new Loop(std::move(epoll), std::move(wake)));
}

Loop::Loop(UniqueFd epoll, UniqueFd wake) : epoll_(std::move(epoll)), wake_(std::move(wake)) {}

Loop::~Loop() = default;

std::optional<SysError> Loop::run() {
  std::array<epoll_event, kEventsPerWait> events{};
  while (!stopping_) {
    const int count = ::epoll_wait(epoll_.get(), events.data(), static_cast<int>(events.size()), waitMs());
    if (count < 0) {
      if (errno == EINTR)
        continue;
      return SysError{"epoll_wait", errno};
    }
    for (std::size_t i = 0; i < static_cast<std::size_t>(count) && !stopping_; ++i)
      dispatch(events[i]);
    if (!stopping_)
      fireDueTimers();
  }
  return std::nullopt;
}

void Loop::stop() {
  stopping_ = true;
  wake();
}

Result<Loop::WatchId, SysError> Loop::watch(int fd, std::uint32_t events, IoCallback callback) {
  const auto id = nextWatch_++;
  epoll_event event{};
  event.events = events;
  event.data.u64 = id;
  if (::epoll_ctl(epoll_.get(), EPOLL_CTL_ADD, fd, &event) < 0)
    return SysError{"epoll_ctl", errno};
  watches_.emplace(id, Watch{fd, std::make_shared<IoCallback>(std::move(callback))});
  return id;
}

void Loop::unwatch(WatchId id) {
  const auto found = watches_.find(id);
  if (found == watches_.end())
    return;
  ::epoll_ctl(epoll_.get(), EPOLL_CTL_DEL, found->second.fd, nullptr);
  watches_.erase(found);
}

Loop::TimerId Loop::at(Clock::time_point deadline, Callback callback) {
  const auto id = nextTimer_++;
  timers_.emplace(id, Timer{deadline, std::move(callback)});
  return id;
}

Loop::TimerId Loop::after(Clock::duration delay, Callback callback) {
  return at(Clock::now() + delay, std::move(callback));
}

void Loop::cancel(TimerId id) { timers_.erase(id); }

void Loop::post(Callback callback) {
  {
    std::scoped_lock lock(postedMutex_);
    posted_.push_back(std::move(callback));
  }
  wake();
}

void Loop::dispatch(const epoll_event& event) {
  if (event.data.u64 == kWakeId) {
    runPosted();
    return;
  }
  const auto found = watches_.find(event.data.u64);
  if (found == watches_.end())
    return;
  const auto callback = found->second.callback;
  (*callback)(event.events);
}

void Loop::runPosted() {
  // the counter only wakes epoll; the queue says what to run
  std::uint64_t count = 0;
  [[maybe_unused]] const auto drained = ::read(wake_.get(), &count, sizeof count);
  std::vector<Callback> callbacks;
  {
    std::scoped_lock lock(postedMutex_);
    callbacks.swap(posted_);
  }
  for (auto& callback : callbacks)
    callback();
}

void Loop::fireDueTimers() {
  const auto now = Clock::now();
  while (!timers_.empty() && !stopping_) {
    const auto due = earliest(timers_);
    if (due->second.deadline > now)
      return;
    auto callback = std::move(due->second.callback);
    timers_.erase(due);
    callback();
  }
}

int Loop::waitMs() const {
  if (timers_.empty())
    return -1;
  const auto deadline = earliest(timers_)->second.deadline;
  const auto now = Clock::now();
  if (deadline <= now)
    return 0;
  // rounded up: a wait that ends before the deadline would only spin
  const auto ms = std::chrono::ceil<std::chrono::milliseconds>(deadline - now).count();
  return static_cast<int>(std::min<decltype(ms)>(ms, std::numeric_limits<int>::max()));
}

void Loop::wake() const {
  // only fails when the counter is about to overflow, and then epoll is woken already
  const std::uint64_t one = 1;
  [[maybe_unused]] const auto woken = ::write(wake_.get(), &one, sizeof one);
}

}  // namespace cadence::audiod
