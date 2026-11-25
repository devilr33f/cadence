#include "pulse-shim/link.h"

#include "pulse-shim/context.h"
#include "pulse-shim/mainloop.h"
#include "pulse-shim/stream.h"
#include "pulse-shim/util.h"

#include <fcntl.h>
#include <pthread.h>
#include <sys/socket.h>
#include <unistd.h>

#include <array>
#include <cerrno>
#include <span>
#include <thread>
#include <utility>

namespace cadence::pulse {

namespace {

// little sits in the socket, so the room a stream reports follows what the server really takes
constexpr int kSendBuffer = 32 * 1024;

bool sendAll(int fd, std::span<const std::uint8_t> bytes) {
  while (!bytes.empty()) {
    // MSG_NOSIGNAL: a server that went away must not raise SIGPIPE in the client's process
    const auto sent = ::send(fd, bytes.data(), bytes.size(), MSG_NOSIGNAL);
    if (sent < 0) {
      if (errno == EINTR)
        continue;
      return false;
    }
    bytes = bytes.subspan(static_cast<std::size_t>(sent));
  }
  return true;
}

}  // namespace

Link::Link(std::shared_ptr<Loop> loop, std::weak_ptr<Context> context)
    : loop_(std::move(loop)), context_(std::move(context)) {}

std::shared_ptr<Link> Link::start(const std::shared_ptr<Context>& context, int socket) {
  const int out = ::fcntl(socket, F_DUPFD_CLOEXEC, 0);
  const int in = out < 0 ? -1 : ::fcntl(socket, F_DUPFD_CLOEXEC, 0);
  if (in < 0) {
    if (out >= 0)
      ::close(out);
    return nullptr;
  }
  ::setsockopt(out, SOL_SOCKET, SO_SNDBUF, &kSendBuffer, sizeof kSendBuffer);
  std::shared_ptr<Link> link(new Link(context->loop(), context));
  try {
    std::thread([link, out] {
      ::pthread_setname_np(::pthread_self(), "pulse-pcm-out");
      // nothing may unwind out of a detached thread into soloist's process: a failure fails the context instead
      try {
        link->sendLoop(out);
      } catch (...) {
        link->lost(PA_ERR_INTERNAL);
      }
      ::close(out);
    }).detach();
  } catch (...) {
    ::close(out);
    ::close(in);
    link->stop();
    return nullptr;
  }
  try {
    std::thread([link, in] {
      ::pthread_setname_np(::pthread_self(), "pulse-pcm-in");
      try {
        link->readLoop(in);
      } catch (...) {
        link->lost(PA_ERR_INTERNAL);
      }
      ::close(in);
    }).detach();
  } catch (...) {
    ::close(in);
    link->stop();
    return nullptr;
  }
  return link;
}

std::optional<std::weak_ptr<Stream>> Link::attach(Served served) {
  std::optional<Served> replaced;
  {
    std::scoped_lock lock(mutex_);
    replaced = std::exchange(served_, std::move(served));
  }
  work_.notify_all();
  if (!replaced)
    return std::nullopt;
  return replaced->stream;
}

void Link::detach(std::uint32_t index) {
  std::scoped_lock lock(mutex_);
  if (served_ && served_->index == index)
    served_.reset();
}

bool Link::with(std::uint32_t index, const std::function<void(Buffer&)>& f) {
  bool served = false;
  {
    std::scoped_lock lock(mutex_);
    if (served_ && served_->index == index) {
      f(served_->buffer);
      served = true;
    }
  }
  work_.notify_all();
  return served;
}

bool Link::peek(std::uint32_t index, const std::function<void(const Buffer&)>& f) {
  std::scoped_lock lock(mutex_);
  if (!served_ || served_->index != index)
    return false;
  f(served_->buffer);
  return true;
}

std::optional<pa_sample_spec> Link::spec() {
  std::scoped_lock lock(mutex_);
  if (!served_)
    return std::nullopt;
  return served_->spec;
}

std::weak_ptr<Stream> Link::stop() {
  std::weak_ptr<Stream> served;
  {
    std::scoped_lock lock(mutex_);
    stopping_ = true;
    if (served_)
      served = served_->stream;
    served_.reset();
  }
  work_.notify_all();
  return served;
}

void Link::sendLoop(int fd) {
  while (auto next = this->next())
    if (!sendAll(fd, next->wire)) {
      lost(PA_ERR_CONNECTIONTERMINATED);
      return;
    }
}

std::optional<Next> Link::next() {
  std::unique_lock lock(mutex_);
  for (;;) {
    if (stopping_)
      return std::nullopt;
    if (served_) {
      if (auto next = served_->buffer.next()) {
        if (next->format)
          served_->base = sent_;
        sent_ += next->pcm;
        // posted before the bytes go out, so the job is queued by the time the server sees them
        if (next->starts)
          post(served_->stream, StreamEvent::Started);
        return next;
      }
    }
    work_.wait(lock);
  }
}

void Link::readLoop(int fd) {
  pcmframe::Decoder decoder;
  std::array<std::uint8_t, 4096> chunk{};
  int error = PA_ERR_CONNECTIONTERMINATED;
  for (;;) {
    const auto n = ::recv(fd, chunk.data(), chunk.size(), 0);
    if (n == 0)
      break;
    if (n < 0) {
      if (errno == EINTR)
        continue;
      break;
    }
    decoder.push(std::span(chunk).first(static_cast<std::size_t>(n)));
    if (!reports(decoder)) {
      error = PA_ERR_PROTOCOL;
      break;
    }
  }
  lost(error);
}

// a report below the served stream's base still counts an older stream's bytes
bool Link::reports(pcmframe::Decoder& decoder) {
  for (;;) {
    const auto next = decoder.nextToShim();
    if (!next.ok())
      return false;
    if (!next.value())
      return true;
    const auto total = next.value()->total;
    const auto at = now();
    std::scoped_lock lock(mutex_);
    // under the link's lock, like started: queued before the read index shows the underrun
    if (served_ && served_->base && total >= *served_->base && served_->buffer.played(total - *served_->base, at))
      post(served_->stream, StreamEvent::Underflow);
  }
}

// a notification is lost only when memory runs out, and never takes the thread down
void Link::post(const std::weak_ptr<Stream>& stream, StreamEvent event) noexcept {
  try {
    loop_->post([stream, event] {
      if (const auto served = stream.lock())
        served->event(event);
    });
  } catch (...) {
  }
}

void Link::lost(int error) noexcept {
  try {
    {
      std::scoped_lock lock(mutex_);
      if (stopping_ || lost_)
        return;
      lost_ = true;
    }
    loop_->post([context = context_, error] {
      if (const auto lostOne = context.lock(); lostOne && lostOne->state() == PA_CONTEXT_READY)
        lostOne->fail(error);
    });
  } catch (...) {
  }
}

}  // namespace cadence::pulse
