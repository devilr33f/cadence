#include "audiod/soloist-pcm.h"

#include "dsp/format.h"

#include <poll.h>
#include <sys/eventfd.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <bit>
#include <cerrno>
#include <cstring>
#include <string_view>
#include <utility>

namespace cadence::audiod {

namespace {

// the shim lives in soloist's jail as nobody, so the socket is handed to it
constexpr uid_t kNobody = 65534;
// a second connection only waits in the queue until the first is gone
constexpr int kBacklog = 1;
// about what the shim's 32 KiB send buffer holds
constexpr std::size_t kReadBytes = 32 * 1024;
// how often a slice that waits for room looks at the input again: a period of the playback thread frees 20 ms
constexpr auto kRoomPoll = std::chrono::milliseconds(5);
// a frame goes in slices of a playback period, so the input never runs more than one ahead of the target
constexpr std::uint32_t kSlicesPerSecond = 50;
// a broken listener is retried at this pace, so it cannot spin
constexpr auto kAcceptRetry = std::chrono::milliseconds(100);

static_assert(std::endian::native == std::endian::little, "the shim's samples are little-endian, as the host's");

// the umask is process-wide: one bind at a time may change it
std::mutex& umaskMutex() {
  static std::mutex mutex;
  return mutex;
}

bool sendAll(int fd, std::span<const std::uint8_t> bytes) {
  while (!bytes.empty()) {
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

std::string_view encodingToken(pcmframe::Encoding encoding) {
  return encoding == pcmframe::Encoding::F32Le ? "f32" : "s16";
}

}  // namespace

std::unique_ptr<SoloistPcm> SoloistPcm::listen(const Log& log, std::string socketPath,
                                               SoloistPcmOptions options) {
  const auto fail = [&](std::string_view reason, int code) {
    log.error("soloist_pcm_listen_failed", {{"reason", reason}, {"errno", code}});
    return nullptr;
  };
  sockaddr_un address{};
  address.sun_family = AF_UNIX;
  if (socketPath.empty() || socketPath.size() >= sizeof address.sun_path)
    return fail("path_length", 0);
  std::memcpy(address.sun_path, socketPath.data(), socketPath.size());

  // a socket left at the path is a dead server's; a symlink is removed, never followed
  struct stat existing {};
  if (::lstat(socketPath.c_str(), &existing) == 0) {
    if (!S_ISSOCK(existing.st_mode) && !S_ISLNK(existing.st_mode))
      return fail("not_socket", 0);
    if (::unlink(socketPath.c_str()) < 0)
      return fail("unlink", errno);
  } else if (errno != ENOENT) {
    return fail("lstat", errno);
  }

  UniqueFd listener(::socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0));
  if (!listener)
    return fail("socket", errno);
  // linux gives the path bind creates the socket's own mode less the umask: 0600 from the moment it exists
  if (::fchmod(listener.get(), 0600) < 0)
    return fail("fchmod", errno);
  {
    std::scoped_lock lock(umaskMutex());
    const auto previous = ::umask(0177);
    const int bound = ::bind(listener.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address);
    const int code = errno;
    ::umask(previous);
    if (bound < 0)
      return fail("bind", code);
  }
  const auto abandon = [&](std::string_view reason) {
    const int code = errno;
    ::unlink(socketPath.c_str());
    return fail(reason, code);
  };
  if (::geteuid() == 0 && ::lchown(socketPath.c_str(), kNobody, kNobody) < 0)
    return abandon("chown");
  if (::listen(listener.get(), kBacklog) < 0)
    return abandon("listen");
  UniqueFd wake(::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK));
  if (!wake)
    return abandon("eventfd");

  std::unique_ptr<SoloistPcm> server(
      new SoloistPcm(log, std::move(socketPath), std::move(listener), options));
  server->wake_ = std::move(wake);
  try {
    server->thread_ = std::thread([raw = server.get()] { raw->serve(); });
  } catch (...) {
    return fail("thread", 0);
  }
  log.info("soloist_pcm_listening");
  return server;
}

SoloistPcm::SoloistPcm(const Log& log, std::string path, UniqueFd listener, SoloistPcmOptions options)
    : log_(log), path_(std::move(path)), listener_(std::move(listener)), options_(options) {}

SoloistPcm::~SoloistPcm() {
  {
    std::scoped_lock lock(mutex_);
    stopping_ = true;
    // a push waiting on a full input lets go at a clear; the input is ours to clear only while we push into it
    if (pushing_ && open_)
      target_->clear();
    // a reply blocked on a shim that stopped reading fails at once
    if (connection_ >= 0)
      ::shutdown(connection_, SHUT_RDWR);
  }
  changed_.notify_all();
  const std::uint64_t one = 1;
  const auto woken = ::write(wake_.get(), &one, sizeof one);
  (void)woken;
  if (thread_.joinable())
    thread_.join();
  ::unlink(path_.c_str());
}

void SoloistPcm::openOutput(PcmInput& music) {
  {
    std::unique_lock lock(mutex_);
    if (open_ && target_ == &music)
      return;
    closeLocked(lock);
    target_ = &music;
    // a stall the last input had says nothing of this one
    stalledAt_.reset();
    open_ = true;
  }
  changed_.notify_all();
}

void SoloistPcm::closeOutput() {
  std::unique_lock lock(mutex_);
  closeLocked(lock);
}

void SoloistPcm::closeLocked(std::unique_lock<std::mutex>& lock) {
  if (!open_)
    return;
  open_ = false;
  ++clears_;
  target_->clear();
  changed_.notify_all();
  // a push lets go at the clear within a few milliseconds; its own clear after it needs the target still
  changed_.wait(lock, [this] { return !pushing_; });
  target_ = nullptr;
}

bool SoloistPcm::stopping() {
  std::scoped_lock lock(mutex_);
  return stopping_;
}

SoloistPcm::Wait SoloistPcm::readable(int fd, int& error) {
  for (;;) {
    std::array<pollfd, 2> fds{pollfd{fd, POLLIN, 0}, pollfd{wake_.get(), POLLIN, 0}};
    if (::poll(fds.data(), fds.size(), -1) < 0) {
      if (errno == EINTR)
        continue;
      error = errno;
      return stopping() ? Wait::Stopped : Wait::Failed;
    }
    if (fds[1].revents != 0 || stopping())
      return Wait::Stopped;
    if (fds[0].revents != 0)
      return Wait::Ready;
  }
}

// only a stop ends it: a listener that fails is retried, and only its first error of a run is logged, since a broken
// one would otherwise log ten lines a second
void SoloistPcm::serve() {
  bool failing = false;
  for (;;) {
    int code = 0;
    const auto wait = readable(listener_.get(), code);
    if (wait == Wait::Stopped)
      return;
    if (wait == Wait::Ready) {
      UniqueFd connection(::accept4(listener_.get(), nullptr, nullptr, SOCK_CLOEXEC));
      if (connection) {
        failing = false;
        accepted(std::move(connection));
        continue;
      }
      code = errno;
      if (code == EINTR || code == ECONNABORTED || code == EAGAIN)
        continue;
    }
    if (!failing)
      log_.warn("soloist_pcm_accept_failed", {{"errno", code}});
    failing = true;
    std::unique_lock lock(mutex_);
    changed_.wait_for(lock, kAcceptRetry, [this] { return stopping_; });
  }
}

void SoloistPcm::accepted(UniqueFd connection) {
  {
    std::scoped_lock lock(mutex_);
    if (stopping_)
      return;
    connection_ = connection.get();
  }
  log_.info("soloist_pcm_connected");
  Episode episode;
  std::vector<std::uint8_t> buffer(kReadBytes);
  bool open = true;
  int error = 0;
  while (open && readable(connection.get(), error) == Wait::Ready) {
    const auto n = ::recv(connection.get(), buffer.data(), buffer.size(), 0);
    if (n < 0 && errno == EINTR)
      continue;
    if (n <= 0)
      break;
    episode.decoder.push(std::span(buffer).first(static_cast<std::size_t>(n)));
    while (open) {
      const auto next = episode.decoder.nextToServer();
      if (!next.ok()) {
        log_.warn("soloist_pcm_invalid", {{"reason", "frame"}});
        open = false;
      } else if (!next.value()) {
        break;
      } else {
        open = handle(episode, connection.get(), *next.value());
      }
    }
  }
  // the end of a stream: what the resampler holds plays too, and the next connection starts afresh
  {
    std::unique_lock lock(mutex_);
    produce(lock, [](PcmInput& target) { target.finish(); });
    connection_ = -1;
  }
  log_.info("soloist_pcm_closed", {{"bytes", episode.consumed}});
}

bool SoloistPcm::handle(Episode& episode, int connection, const pcmframe::ToServer& message) {
  if (const auto* format = std::get_if<pcmframe::Format>(&message)) {
    episode.format = *format;
    log_.info("soloist_pcm_format", {{"encoding", encodingToken(format->encoding)},
                                     {"channels", format->channels},
                                     {"rate", format->rate}});
    return true;
  }
  if (const auto* pcm = std::get_if<pcmframe::Pcm>(&message))
    return this->pcm(episode, connection, pcm->bytes);
  // a cork drops what was queued (at most the target and a frame) as a flush does; an uncork needs nothing
  if (const auto* cork = std::get_if<pcmframe::Cork>(&message); cork != nullptr && !cork->on)
    return true;
  clearOwn();
  return true;
}

bool SoloistPcm::pcm(Episode& episode, int connection, std::span<const std::uint8_t> bytes) {
  if (!episode.format) {
    log_.warn("soloist_pcm_invalid", {{"reason", "no_format"}});
    return false;
  }
  if (bytes.size() % episode.format->frameBytes() != 0) {
    log_.warn("soloist_pcm_invalid", {{"reason", "ragged"}});
    return false;
  }
  if (!bytes.empty())
    deliver(*episode.format, bytes);
  if (stopping())
    return false;
  // consumed whether it played or not, so the shim's read index keeps up
  episode.consumed += bytes.size();
  std::vector<std::uint8_t> reply;
  pcmframe::encode(pcmframe::Played{episode.consumed}, reply);
  return sendAll(connection, reply);
}

void SoloistPcm::deliver(const pcmframe::Format& format, std::span<const std::uint8_t> bytes) {
  const std::size_t slice = format.rate / kSlicesPerSecond * format.frameBytes();
  std::unique_lock lock(mutex_);
  while (!bytes.empty() && room(lock)) {
    const auto piece = bytes.first(std::min(slice, bytes.size()));
    if (!produce(lock, [&](PcmInput& target) { push(target, format, piece); }))
      break;
    bytes = bytes.subspan(piece.size());
  }
  if (bytes.empty() || stopping_)
    return;
  const auto frames = bytes.size() / format.frameBytes();
  consumeInRealTime(lock, std::chrono::duration_cast<std::chrono::steady_clock::duration>(
                              std::chrono::duration<double>(static_cast<double>(frames) / format.rate)));
}

bool SoloistPcm::room(std::unique_lock<std::mutex>& lock) {
  const auto target =
      static_cast<std::uint64_t>(dsp::framesFor(static_cast<std::size_t>(options_.queueTarget.count())));
  const auto deadline = std::chrono::steady_clock::now() + options_.stallBound;
  for (;;) {
    if (!open_ || stopping_)
      return false;
    const auto status = target_->status();
    if (status.queued < target)
      return true;
    // nothing has read the input since a slice waited out the bound: no slice waits for it again until it is read
    if (stalledAt_ && *stalledAt_ == status.framesRead)
      return false;
    stalledAt_.reset();
    if (std::chrono::steady_clock::now() >= deadline) {
      stalledAt_ = status.framesRead;
      return false;
    }
    changed_.wait_for(lock, kRoomPoll);
  }
}

bool SoloistPcm::produce(std::unique_lock<std::mutex>& lock, const std::function<void(PcmInput& target)>& work) {
  if (!open_ || stopping_)
    return false;
  const auto clears = clears_;
  // a close waits for this push before it lets go of the target, so it stays valid unlocked
  auto& target = *target_;
  pushing_ = true;
  lock.unlock();
  work(target);
  lock.lock();
  // a closeOutput landed as the push began, which then queued after its clear: it goes too
  if (clears_ != clears)
    target.clear();
  pushing_ = false;
  changed_.notify_all();
  return true;
}

void SoloistPcm::push(PcmInput& target, const pcmframe::Format& format, std::span<const std::uint8_t> bytes) {
  // the decoder's bytes need not be aligned for their samples
  if (format.encoding == pcmframe::Encoding::S16Le) {
    s16_.resize(bytes.size() / sizeof(std::int16_t));
    std::memcpy(s16_.data(), bytes.data(), bytes.size());
    target.push(std::span<const std::int16_t>(s16_), format.rate, format.channels);
  } else {
    f32_.resize(bytes.size() / sizeof(float));
    std::memcpy(f32_.data(), bytes.data(), bytes.size());
    target.push(std::span<const float>(f32_), format.rate, format.channels);
  }
}

void SoloistPcm::consumeInRealTime(std::unique_lock<std::mutex>& lock, std::chrono::steady_clock::duration duration) {
  paceUntil_ = std::max(paceUntil_, std::chrono::steady_clock::now()) + duration;
  changed_.wait_until(lock, paceUntil_, [this] { return stopping_; });
}

void SoloistPcm::clearOwn() {
  std::scoped_lock lock(mutex_);
  if (open_)
    target_->clear();
}

}  // namespace cadence::audiod
