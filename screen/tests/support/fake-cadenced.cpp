#include "support/fake-cadenced.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <poll.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

namespace cadence::screen::test {

FakeCadenced::FakeCadenced(std::string path) : path_(std::move(path)) {
  sockaddr_un addr{};
  unsigned length = 0;
  REQUIRE(ipc::fillAddress(path_, &addr, &length));
  if (path_[0] != '@') ::unlink(path_.c_str());
  listen_ = ::socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0);
  REQUIRE(listen_ >= 0);
  REQUIRE(::bind(listen_, reinterpret_cast<sockaddr*>(&addr), static_cast<socklen_t>(length)) == 0);
  REQUIRE(::listen(listen_, 4) == 0);
}

FakeCadenced::~FakeCadenced() {
  conn_.close();
  if (listen_ >= 0) ::close(listen_);
  if (path_[0] != '@') ::unlink(path_.c_str());
}

bool FakeCadenced::accept(int timeoutMs) {
  pollfd p{listen_, POLLIN, 0};
  if (::poll(&p, 1, timeoutMs) <= 0) return false;
  const int fd = ::accept4(listen_, nullptr, nullptr, SOCK_CLOEXEC);
  if (fd < 0) return false;
  conn_ = ipc::SeqpacketSocket(fd);
  return true;
}

bool FakeCadenced::send(const ipc::Envelope& envelope) {
  std::vector<std::uint8_t> frame;
  if (!ipc::encodeEnvelope(envelope, frame)) return false;
  return conn_.send(frame);
}

ipc::DecodedEnvelope FakeCadenced::next(int timeoutMs) {
  std::vector<std::uint8_t> frame;
  if (conn_.recv(frame, timeoutMs) != ipc::SeqpacketSocket::Recv::Frame) return nullptr;
  return ipc::decodeEnvelope(frame, nullptr);
}

ipc::DecodedEnvelope FakeCadenced::nextOf(pb_size_t which, int timeoutMs) {
  const auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(timeoutMs);
  for (;;) {
    const auto left =
        std::chrono::duration_cast<std::chrono::milliseconds>(deadline - std::chrono::steady_clock::now()).count();
    if (left <= 0) return nullptr;
    auto e = next(static_cast<int>(left));
    if (!e) return nullptr;
    if (e->which_body == which) return e;
  }
}

void FakeCadenced::drop() { conn_.close(); }

}  // namespace cadence::screen::test
