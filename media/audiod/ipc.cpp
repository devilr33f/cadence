#include "audiod/ipc.h"

#include <sys/epoll.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>

#include <cerrno>
#include <cstddef>
#include <cstring>
#include <span>
#include <string_view>
#include <utility>

namespace cadence::audiod {

namespace {

// whether the peer runs as this process's uid
bool sameUser(int fd) {
  ucred peer{};
  socklen_t size = sizeof peer;
  return ::getsockopt(fd, SOL_SOCKET, SO_PEERCRED, &peer, &size) == 0 && size == sizeof peer && peer.uid == ::getuid();
}

// a refused second client only has to wait in the queue until it is closed
constexpr int kBacklog = 4;
// frames read per wake, so a chatty client cannot starve timers; level-triggered epoll reports the rest
constexpr int kFramesPerWake = 64;
// room for more fds than any frame may carry, so a frame with too many is seen whole and refused, not cut
constexpr std::size_t kMaxReceivedFds = 8;

// the fds a received message brought, owned from here
std::vector<UniqueFd> receivedFds(msghdr& message) {
  std::vector<UniqueFd> fds;
  for (cmsghdr* header = CMSG_FIRSTHDR(&message); header != nullptr; header = CMSG_NXTHDR(&message, header)) {
    if (header->cmsg_level != SOL_SOCKET || header->cmsg_type != SCM_RIGHTS || header->cmsg_len < CMSG_LEN(0))
      continue;
    const auto count = (header->cmsg_len - CMSG_LEN(0)) / sizeof(int);
    for (std::size_t i = 0; i < count; ++i) {
      int fd = -1;
      std::memcpy(&fd, CMSG_DATA(header) + i * sizeof(int), sizeof fd);
      if (fd >= 0)
        fds.emplace_back(fd);
    }
  }
  return fds;
}

}  // namespace

std::unique_ptr<IpcServer> IpcServer::listen(Loop& loop, const Log& log, std::string path) {
  const auto fail = [&](std::string_view reason, int code) {
    log.error("ipc_listen_failed", {{"reason", reason}, {"errno", code}});
    return nullptr;
  };
  sockaddr_un address{};
  address.sun_family = AF_UNIX;
  if (path.empty() || path.size() >= sizeof address.sun_path)
    return fail("path_length", 0);
  std::memcpy(address.sun_path, path.data(), path.size());
  // @name is an abstract socket (android: an app has no directory another of its processes shares a socket in),
  // which has no file to clean up or to hold a mode: its clients are checked by uid instead
  const bool abstract = path.front() == '@';
  socklen_t length = sizeof address;
  if (abstract) {
    address.sun_path[0] = '\0';
    length = static_cast<socklen_t>(offsetof(sockaddr_un, sun_path) + path.size());
  } else {
    // cadenced runs one audiod per socket, so a socket left at the path is a dead daemon's
    struct stat existing {};
    if (::lstat(path.c_str(), &existing) == 0) {
      if (!S_ISSOCK(existing.st_mode))
        return fail("not_socket", 0);
      if (::unlink(path.c_str()) < 0)
        return fail("unlink", errno);
    } else if (errno != ENOENT) {
      return fail("lstat", errno);
    }
  }

  UniqueFd listener(::socket(AF_UNIX, SOCK_SEQPACKET | SOCK_NONBLOCK | SOCK_CLOEXEC, 0));
  if (!listener)
    return fail("socket", errno);
  // linux gives the path bind creates the socket's own mode (less the umask), so it is never wider than 0600
  if (!abstract && ::fchmod(listener.get(), 0600) < 0)
    return fail("fchmod", errno);
  if (::bind(listener.get(), reinterpret_cast<const sockaddr*>(&address), length) < 0)
    return fail("bind", errno);
  if (::listen(listener.get(), kBacklog) < 0) {
    const int code = errno;
    if (!abstract)
      ::unlink(path.c_str());
    return fail("listen", code);
  }

  std::unique_ptr<IpcServer> server(new IpcServer(loop, log, std::move(path), std::move(listener)));
  auto* raw = server.get();
  const auto watched = loop.watch(raw->listener_.get(), EPOLLIN, [raw](std::uint32_t) { raw->acceptClients(); });
  if (!watched.ok())
    return fail("epoll_ctl", watched.error().code);
  raw->listenerWatch_ = watched.value();
  log.info("ipc_listening");
  return server;
}

IpcServer::IpcServer(Loop& loop, const Log& log, std::string path, UniqueFd listener)
    : loop_(loop), log_(log), path_(std::move(path)), listener_(std::move(listener)) {}

IpcServer::~IpcServer() {
  // the parts the hook reaches are gone before the server
  clientGone_ = nullptr;
  dropClient();
  if (listenerWatch_)
    loop_.unwatch(*listenerWatch_);
  if (!path_.empty() && path_.front() != '@')
    ::unlink(path_.c_str());
}

void IpcServer::on(pb_size_t tag, Handler handler) { handlers_.insert_or_assign(tag, std::move(handler)); }

void IpcServer::onFd(pb_size_t tag, FdHandler handler) { fdHandlers_.insert_or_assign(tag, std::move(handler)); }

void IpcServer::onClientGone(Loop::Callback hook) { clientGone_ = std::move(hook); }

bool IpcServer::send(const Envelope& envelope) {
  if (!client_ || broken_)
    return false;
  std::array<std::uint8_t, kMaxEncodedBytes> frame{};
  const auto size = encodeFrame(envelope, frame);
  if (!size) {
    log_.error("ipc_encode_failed", {{"type", envelope.which_body}});
    return false;
  }
  for (;;) {
    // a seqpacket send takes the whole frame or none of it
    if (::send(client_.get(), frame.data(), *size, MSG_NOSIGNAL | MSG_DONTWAIT) >= 0) {
      stalled_ = false;
      return true;
    }
    const int code = errno;
    if (code == EINTR)
      continue;
    if (code == EAGAIN || code == EWOULDBLOCK) {
      if (!stalled_)
        log_.warn("ipc_send_dropped", {{"type", envelope.which_body}});
      stalled_ = true;
      return false;
    }
    log_.warn("ipc_send_failed", {{"errno", code}});
    // the caller may be in the middle of its own state change, so the hook must not run under it: the
    // shut-down socket reads as the end of stream, and the receive path drops the client from the loop
    ::shutdown(client_.get(), SHUT_RDWR);
    broken_ = true;
    return false;
  }
}

void IpcServer::dropClient() {
  if (!client_)
    return;
  if (clientWatch_)
    loop_.unwatch(*clientWatch_);
  clientWatch_.reset();
  client_.reset();
  stalled_ = false;
  broken_ = false;
  if (clientGone_)
    clientGone_();
}

void IpcServer::acceptClients() {
  for (;;) {
    UniqueFd accepted(::accept4(listener_.get(), nullptr, nullptr, SOCK_NONBLOCK | SOCK_CLOEXEC));
    if (!accepted) {
      const int code = errno;
      if (code == EINTR || code == ECONNABORTED)
        continue;
      if (code != EAGAIN && code != EWOULDBLOCK)
        log_.warn("ipc_accept_failed", {{"call", "accept4"}, {"errno", code}});
      return;
    }
    if (client_) {
      // closed as it goes out of scope: the newcomer reads end of stream at once
      log_.warn("ipc_client_refused");
      continue;
    }
    if (path_.front() == '@' && !sameUser(accepted.get())) {
      // an abstract name has no mode: only this uid's processes (the app's own) may connect
      log_.warn("ipc_client_refused", {{"reason", "uid"}});
      continue;
    }
    const auto watched =
        loop_.watch(accepted.get(), EPOLLIN | EPOLLRDHUP, [this](std::uint32_t) { receiveFrames(); });
    if (!watched.ok()) {
      log_.warn("ipc_accept_failed", {{"call", "epoll_ctl"}, {"errno", watched.error().code}});
      continue;
    }
    client_ = std::move(accepted);
    clientWatch_ = watched.value();
    log_.info("ipc_client_connected");
  }
}

// called for any client event: a hangup or error shows as the end of stream or a failed read once the frames
// queued before it are read
void IpcServer::receiveFrames() {
  for (int frames = 0; frames < kFramesPerWake && client_; ++frames) {
    // a longer message is cut to the buffer, whose one spare byte makes it too long to decode
    iovec data{received_.data(), received_.size()};
    alignas(cmsghdr) std::array<char, CMSG_SPACE(sizeof(int) * kMaxReceivedFds)> control{};
    msghdr message{};
    message.msg_iov = &data;
    message.msg_iovlen = 1;
    message.msg_control = control.data();
    message.msg_controllen = control.size();
    const auto size = ::recvmsg(client_.get(), &message, MSG_DONTWAIT | MSG_CMSG_CLOEXEC);
    if (size < 0) {
      const int code = errno;
      if (code == EINTR)
        continue;
      if (code == EAGAIN || code == EWOULDBLOCK)
        return;
      log_.warn("ipc_recv_failed", {{"errno", code}});
      dropClient();
      return;
    }
    // owned before anything can return: whatever path the frame takes, no fd it brought outlives it unhandled
    auto fds = receivedFds(message);
    const bool truncated = (message.msg_flags & MSG_CTRUNC) != 0;
    // cadenced never sends an empty frame (every envelope has a body), so zero bytes is the end of the stream
    if (size == 0) {
      log_.info("ipc_client_gone");
      dropClient();
      return;
    }
    const auto frame = std::span<const std::uint8_t>(received_.data(), static_cast<std::size_t>(size));
    Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
    if (const auto error = decodeFrame(frame, envelope)) {
      log_.warn("ipc_invalid", {{"reason", reason(*error)}});
      dropClient();
      return;
    }
    dispatch(envelope, std::move(fds), truncated);
  }
}

void IpcServer::dispatch(const Envelope& envelope, std::vector<UniqueFd> fds, bool truncated) {
  const auto takesFd = fdHandlers_.find(envelope.which_body);
  if (takesFd == fdHandlers_.end()) {
    if (!fds.empty() || truncated) {
      log_.warn("ipc_fd_refused", {{"type", envelope.which_body}, {"fds", fds.size()}, {"reason", "unexpected"}});
      return;
    }
    const auto handler = handlers_.find(envelope.which_body);
    if (handler == handlers_.end()) {
      log_.warn("ipc_unhandled", {{"type", envelope.which_body}});
      return;
    }
    handler->second(envelope);
    return;
  }
  if (truncated || fds.size() != 1) {
    log_.warn("ipc_fd_refused",
              {{"type", envelope.which_body},
               {"fds", fds.size()},
               {"reason", truncated ? "truncated" : fds.empty() ? "missing" : "count"}});
    return;
  }
  takesFd->second(envelope, std::move(fds.front()));
}

}  // namespace cadence::audiod
