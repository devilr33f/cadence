#include "audiod/framing.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/sys.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"

#include <catch2/catch_test_macros.hpp>

#include <fcntl.h>
#include <signal.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#include <array>
#include <atomic>
#include <cerrno>
#include <cstring>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <vector>

// fds over audiod's socket: what a frame brings is handed to the one body that takes it, or closed, never leaked
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

Envelope bluetoothStream() {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_bluetooth_stream_tag;
  std::strcpy(envelope.body.bluetooth_stream.stream_id, "a1b2c3");
  envelope.body.bluetooth_stream.rate = 44100;
  envelope.body.bluetooth_stream.channels = 2;
  return envelope;
}

Envelope bluetoothConfig() {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_bluetooth_config_tag;
  envelope.body.bluetooth_config.enabled = true;
  return envelope;
}

void sendWithFds(int socket, const Envelope& envelope, const std::vector<int>& fds) {
  std::array<std::uint8_t, kMaxEncodedBytes> frame{};
  const auto size = encodeFrame(envelope, frame);
  REQUIRE(size);
  iovec data{frame.data(), *size};
  alignas(cmsghdr) std::array<char, CMSG_SPACE(sizeof(int) * 4)> control{};
  msghdr message{};
  message.msg_iov = &data;
  message.msg_iovlen = 1;
  if (!fds.empty()) {
    message.msg_control = control.data();
    message.msg_controllen = CMSG_SPACE(sizeof(int) * fds.size());
    cmsghdr* header = CMSG_FIRSTHDR(&message);
    header->cmsg_level = SOL_SOCKET;
    header->cmsg_type = SCM_RIGHTS;
    header->cmsg_len = CMSG_LEN(sizeof(int) * fds.size());
    std::memcpy(CMSG_DATA(header), fds.data(), sizeof(int) * fds.size());
  }
  REQUIRE(::sendmsg(socket, &message, MSG_NOSIGNAL) >= 0);
}

// a pipe whose read end only the frame carries: once audiod closes its copy, a write finds no reader
struct Pipe {
  UniqueFd read;
  UniqueFd write;
  Pipe() {
    std::array<int, 2> ends{};
    REQUIRE(::pipe2(ends.data(), O_CLOEXEC) == 0);
    read.reset(ends[0]);
    write.reset(ends[1]);
  }
  // the test's own read end gone, whether any reader is left
  [[nodiscard]] bool readerLeft() const {
    const char byte = 0;
    return ::write(write.get(), &byte, 1) == 1;
  }
};

class Rig {
 public:
  Rig() {
    ::signal(SIGPIPE, SIG_IGN);
    loop_ = Loop::create(log_);
    REQUIRE(loop_);
    server_ = IpcServer::listen(*loop_, log_, dir_.file("audiod.sock"));
    REQUIRE(server_);
    server_->onFd(cadence_audiod_v1_Envelope_bluetooth_stream_tag, [this](const Envelope&, UniqueFd fd) {
      const std::scoped_lock lock(mutex_);
      const int flags = ::fcntl(fd.get(), F_GETFD);
      cloexec_ = flags >= 0 && (flags & FD_CLOEXEC) != 0;
      received_ = std::move(fd);
      ++taken_;
    });
    server_->on(cadence_audiod_v1_Envelope_bluetooth_config_tag, [this](const Envelope&) { ++plain_; });
    runner_ = std::thread([this] { (void)loop_->run(); });
    client_.reset(::socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0));
    sockaddr_un address{};
    address.sun_family = AF_UNIX;
    const auto path = dir_.file("audiod.sock");
    std::memcpy(address.sun_path, path.data(), path.size());
    REQUIRE(::connect(client_.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) == 0);
    REQUIRE(logs_.eventually("ipc_client_connected"));
  }
  ~Rig() {
    loop_->stop();
    runner_.join();
    server_.reset();
  }
  Rig(const Rig&) = delete;
  Rig& operator=(const Rig&) = delete;

  [[nodiscard]] int client() const { return client_.get(); }
  [[nodiscard]] const LogCapture& logs() const { return logs_; }
  [[nodiscard]] int taken() const { return taken_.load(); }
  [[nodiscard]] int plain() const { return plain_.load(); }
  [[nodiscard]] bool cloexec() {
    const std::scoped_lock lock(mutex_);
    return cloexec_;
  }
  // what the handler keeps, dropped from the test's side
  void dropReceived() {
    const std::scoped_lock lock(mutex_);
    received_.reset();
  }

 private:
  TempDir dir_;
  LogCapture logs_;
  Log log_ = logs_.log();
  std::unique_ptr<Loop> loop_;
  std::unique_ptr<IpcServer> server_;
  std::thread runner_;
  UniqueFd client_;
  std::mutex mutex_;
  UniqueFd received_;
  bool cloexec_ = false;
  std::atomic<int> taken_{0};
  std::atomic<int> plain_{0};
};

}  // namespace

TEST_CASE("a bluetooth stream's one fd reaches its handler, close-on-exec") {
  Rig rig;
  Pipe pipe;
  sendWithFds(rig.client(), bluetoothStream(), {pipe.read.get()});
  pipe.read.reset();
  REQUIRE(eventually([&] { return rig.taken() == 1; }));
  CHECK(rig.cloexec());
  // the handler's copy is the reader that is left
  CHECK(pipe.readerLeft());
  rig.dropReceived();
  CHECK_FALSE(pipe.readerLeft());
  CHECK(rig.logs().count("ipc_fd_refused") == 0);
}

TEST_CASE("an fd with any other frame is closed and the frame skipped") {
  Rig rig;
  Pipe pipe;
  sendWithFds(rig.client(), bluetoothConfig(), {pipe.read.get()});
  pipe.read.reset();
  REQUIRE(rig.logs().eventually("ipc_fd_refused"));
  CHECK(rig.logs().count("\"reason\":\"unexpected\"") == 1);
  CHECK(rig.plain() == 0);
  CHECK_FALSE(pipe.readerLeft());
  // the client stays: the next plain frame is handled
  sendWithFds(rig.client(), bluetoothConfig(), {});
  CHECK(eventually([&] { return rig.plain() == 1; }));
}

TEST_CASE("a bluetooth stream with two fds has both closed") {
  Rig rig;
  Pipe first;
  Pipe second;
  sendWithFds(rig.client(), bluetoothStream(), {first.read.get(), second.read.get()});
  first.read.reset();
  second.read.reset();
  REQUIRE(rig.logs().eventually("ipc_fd_refused"));
  CHECK(rig.logs().count("\"reason\":\"count\"") == 1);
  CHECK(rig.taken() == 0);
  CHECK_FALSE(first.readerLeft());
  CHECK_FALSE(second.readerLeft());
}

TEST_CASE("a bluetooth stream without its fd is refused") {
  Rig rig;
  sendWithFds(rig.client(), bluetoothStream(), {});
  REQUIRE(rig.logs().eventually("ipc_fd_refused"));
  CHECK(rig.logs().count("\"reason\":\"missing\"") == 1);
  CHECK(rig.taken() == 0);
}

TEST_CASE("too many fds for the control buffer are closed, none leaked") {
  Rig rig;
  std::vector<Pipe> pipes(12);
  std::vector<int> fds;
  for (auto& pipe : pipes)
    fds.push_back(pipe.read.get());
  // past the receive side's room: the kernel cuts the control data
  std::array<std::uint8_t, kMaxEncodedBytes> frame{};
  const auto size = encodeFrame(bluetoothStream(), frame);
  REQUIRE(size);
  iovec data{frame.data(), *size};
  alignas(cmsghdr) std::array<char, CMSG_SPACE(sizeof(int) * 12)> control{};
  msghdr message{};
  message.msg_iov = &data;
  message.msg_iovlen = 1;
  message.msg_control = control.data();
  message.msg_controllen = control.size();
  cmsghdr* header = CMSG_FIRSTHDR(&message);
  header->cmsg_level = SOL_SOCKET;
  header->cmsg_type = SCM_RIGHTS;
  header->cmsg_len = CMSG_LEN(sizeof(int) * fds.size());
  std::memcpy(CMSG_DATA(header), fds.data(), sizeof(int) * fds.size());
  REQUIRE(::sendmsg(rig.client(), &message, MSG_NOSIGNAL) >= 0);
  for (auto& pipe : pipes)
    pipe.read.reset();
  REQUIRE(rig.logs().eventually("ipc_fd_refused"));
  CHECK(rig.logs().count("\"reason\":\"truncated\"") == 1);
  CHECK(rig.taken() == 0);
  for (auto& pipe : pipes)
    CHECK_FALSE(pipe.readerLeft());
}
