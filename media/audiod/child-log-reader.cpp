#include "audiod/child-log-reader.h"

#include <poll.h>
#include <sys/eventfd.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <cerrno>
#include <chrono>
#include <cstdint>
#include <optional>
#include <span>
#include <utility>

namespace cadence::audiod {

namespace {

using Clock = ChildLog::Clock;
using Stream = ChildLog::Stream;

// a poll wakes at least this often, so a deadline far off never overflows its timeout
constexpr std::chrono::milliseconds kLongestWait{60'000};
constexpr std::size_t kReadChunk = 4096;

}  // namespace

struct ChildLogReader::Child {
  ChildLog log;
  UniqueFd out;
  UniqueFd err;

  [[nodiscard]] UniqueFd& of(Stream stream) { return stream == Stream::Out ? out : err; }

  // what fd holds, at most the budget; its end finishes the stream and closes it
  void read(Stream stream, Clock::time_point now) {
    auto& fd = of(stream);
    std::array<char, kReadChunk> chunk{};
    for (std::size_t taken = 0; fd && taken < kChildReadBudget;) {
      const auto size = ::read(fd.get(), chunk.data(), chunk.size());
      if (size < 0 && errno == EINTR)
        continue;
      if (size < 0 && (errno == EAGAIN || errno == EWOULDBLOCK))
        return;
      if (size <= 0) {
        log.finish(stream, now);
        fd.reset();
        return;
      }
      taken += static_cast<std::size_t>(size);
      log.feed(stream, std::span<const char>(chunk.data(), static_cast<std::size_t>(size)), now);
    }
  }
};

ChildLogReader::ChildLogReader(ChildLogTarget target)
    : target_(std::move(target)), wake_(::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK)), thread_([this] { run(); }) {}

ChildLogReader::~ChildLogReader() {
  {
    std::scoped_lock lock(mutex_);
    stopping_ = true;
  }
  const std::uint64_t one = 1;
  [[maybe_unused]] const auto written = ::write(wake_.get(), &one, sizeof one);
  thread_.join();
}

void ChildLogReader::add(std::string_view source, UniqueFd out, UniqueFd err) {
  {
    std::scoped_lock lock(mutex_);
    added_.push_back({.source = source, .out = std::move(out), .err = std::move(err)});
  }
  const std::uint64_t one = 1;
  [[maybe_unused]] const auto written = ::write(wake_.get(), &one, sizeof one);
}

void ChildLogReader::run() {
  std::vector<std::unique_ptr<Child>> children;
  for (;;) {
    bool stopping = false;
    {
      std::scoped_lock lock(mutex_);
      for (auto& added : added_)
        children.push_back(std::make_unique<Child>(
            Child{.log = ChildLog(added.source, target_), .out = std::move(added.out), .err = std::move(added.err)}));
      added_.clear();
      stopping = stopping_;
    }
    if (stopping) {
      const auto now = Clock::now();
      for (auto& child : children) {
        child->read(Stream::Out, now);
        child->read(Stream::Err, now);
        child->log.close(now);
      }
      return;
    }

    std::vector<pollfd> fds{{.fd = wake_.get(), .events = POLLIN, .revents = 0}};
    std::optional<Clock::time_point> next;
    for (const auto& child : children) {
      for (const auto* fd : {&child->out, &child->err})
        fds.push_back({.fd = fd->valid() ? fd->get() : -1, .events = POLLIN, .revents = 0});
      if (const auto at = child->log.deadline(); at && (!next || *at < *next))
        next = at;
    }
    auto wait = kLongestWait;
    if (next)
      wait = std::clamp(std::chrono::ceil<std::chrono::milliseconds>(*next - Clock::now()),
                        std::chrono::milliseconds::zero(), kLongestWait);
    if (::poll(fds.data(), fds.size(), static_cast<int>(wait.count())) < 0 && errno != EINTR)
      std::this_thread::sleep_for(std::chrono::milliseconds(100));
    if ((fds[0].revents & POLLIN) != 0) {
      std::uint64_t ignored = 0;
      [[maybe_unused]] const auto drained = ::read(wake_.get(), &ignored, sizeof ignored);
    }

    const auto now = Clock::now();
    for (std::size_t i = 0; i < children.size(); ++i) {
      auto& child = *children[i];
      if (fds[1 + 2 * i].revents != 0)
        child.read(Stream::Out, now);
      if (fds[2 + 2 * i].revents != 0)
        child.read(Stream::Err, now);
      child.log.tick(now);
    }
    std::erase_if(children, [&](const std::unique_ptr<Child>& child) {
      if (child->out || child->err)
        return false;
      child->log.close(now);
      return true;
    });
  }
}

}  // namespace cadence::audiod
