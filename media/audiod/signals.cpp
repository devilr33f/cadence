#include "audiod/signals.h"

#include <signal.h>
#include <sys/epoll.h>
#include <sys/signalfd.h>
#include <unistd.h>

#include <cerrno>
#include <utility>

namespace cadence::audiod {

std::unique_ptr<SignalStop> SignalStop::install(Loop& loop, const Log& log) {
  const auto fail = [&](std::string_view call, int code) {
    log.error("signals_failed", {{"call", call}, {"errno", code}});
    return nullptr;
  };
  if (::signal(SIGPIPE, SIG_IGN) == SIG_ERR)
    return fail("signal", errno);
  sigset_t stopping;
  sigemptyset(&stopping);
  sigaddset(&stopping, SIGTERM);
  sigaddset(&stopping, SIGINT);
  if (const int code = ::pthread_sigmask(SIG_BLOCK, &stopping, nullptr); code != 0)
    return fail("pthread_sigmask", code);
  UniqueFd fd(::signalfd(-1, &stopping, SFD_NONBLOCK | SFD_CLOEXEC));
  if (!fd)
    return fail("signalfd", errno);

  std::unique_ptr<SignalStop> stop(new SignalStop(loop, log, std::move(fd)));
  auto* raw = stop.get();
  const auto watched = loop.watch(raw->fd_.get(), EPOLLIN, [raw](std::uint32_t) { raw->onSignal(); });
  if (!watched.ok())
    return fail("epoll_ctl", watched.error().code);
  raw->watch_ = watched.value();
  return stop;
}

SignalStop::SignalStop(Loop& loop, const Log& log, UniqueFd fd) : loop_(loop), log_(log), fd_(std::move(fd)) {}

SignalStop::~SignalStop() {
  if (watch_)
    loop_.unwatch(*watch_);
}

void SignalStop::onSignal() {
  signalfd_siginfo info{};
  while (::read(fd_.get(), &info, sizeof info) == static_cast<ssize_t>(sizeof info)) {
    log_.info("audiod_stop", {{"signal", info.ssi_signo}});
    loop_.stop();
  }
}

}  // namespace cadence::audiod
