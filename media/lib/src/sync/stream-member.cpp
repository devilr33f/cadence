#include "sync/stream-member.h"

#include "sync/clock-socket.h"

#include <algorithm>
#include <cerrno>
#include <cstring>
#include <optional>
#include <poll.h>

namespace cadence::sync {

namespace {

constexpr int kPollMs = 100;
constexpr std::int64_t kHandshakeNs = 5'000'000'000;

template <std::size_t N>
void copyText(char (&dst)[N], std::string_view src) {
  const auto n = std::min(src.size(), N - 1);
  std::memcpy(dst, src.data(), n);
  dst[n] = '\0';
}

// the leader's goodbye as the member's error token
std::string_view refusal(std::string_view reason) {
  if (reason == "bad_mac")
    return "auth_failed";
  if (reason == "unknown_member")
    return "refused";
  return reason;
}

}  // namespace

MemberStream::MemberStream(MemberStreamConfig config, MemberStreamEvents events)
    : config_(std::move(config)), events_(std::move(events)) {}

MemberStream::~MemberStream() { stop(); }

void MemberStream::start() {
  if (thread_.joinable())
    return;
  stop_.store(false);
  thread_ = std::thread([this] { run(); });
}

void MemberStream::stop() {
  {
    std::lock_guard lock(mutex_);
    stop_.store(true);
  }
  stopped_.notify_all();
  wake_.wake();
  if (thread_.joinable())
    thread_.join();
}

bool MemberStream::send(const SyncEnvelope& envelope) {
  if (!connected_.load())
    return false;
  {
    std::lock_guard lock(mutex_);
    outbox_.push_back(envelope);
  }
  wake_.wake();
  return true;
}

bool MemberStream::rest(int ms) {
  std::unique_lock lock(mutex_);
  return !stopped_.wait_for(lock, std::chrono::milliseconds(ms), [this] { return stop_.load(); });
}

void MemberStream::run() {
  if (config_.onThread)
    config_.onThread();
  int backoff = config_.backoffMinMs;
  while (!stop_.load()) {
    auto fd = connectTcp(config_.host, config_.port, config_.connectTimeoutMs);
    if (!fd.ok()) {
      if (events_.lost)
        events_.lost(fd.error().code == ECONNREFUSED ? "refused" : "unreachable");
    } else if (session(fd.value())) {
      backoff = config_.backoffMinMs;
    }
    if (!rest(backoff))
      return;
    backoff = std::min(backoff * 2, config_.backoffMaxMs);
  }
}

bool MemberStream::session(int fd) {
  StreamConn io(fd, config_.key);
  const auto lost = [this](std::string_view reason) {
    connected_.store(false);
    if (events_.lost)
      events_.lost(reason);
  };
  StreamNonce nonce{};
  if (!randomNonce(nonce)) {
    lost("unreachable");
    return false;
  }
  // the handshake: Challenge in, Hello out, Welcome in, each within the bound
  const auto deadline = monoNs() + kHandshakeNs;
  const auto await = [&](SyncEnvelope& out) -> std::optional<std::string_view> {
    for (;;) {
      const auto r = io.next(out);
      if (r == OpenResult::Message)
        return std::nullopt;
      if (r == OpenResult::Refused)
        return "auth_failed";
      if (stop_.load())
        return "leaving";
      if (monoNs() > deadline)
        return "closed";
      pollfd p{.fd = io.fd(), .events = POLLIN, .revents = 0};
      ::poll(&p, 1, kPollMs);
      if ((p.revents & (POLLIN | POLLHUP | POLLERR)) != 0 && io.receive() == StreamConn::Read::Closed &&
          io.next(out) != OpenResult::Message)
        return "closed";
      if (out.which_body != 0)
        return std::nullopt;
    }
  };
  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  if (const auto failed = await(e)) {
    lost(*failed);
    return false;
  }
  if (e.which_body != cadence_sync_v1_Envelope_challenge_tag || e.body.challenge.protocol != kStreamProtocol ||
      e.body.challenge.nonce.size != kStreamNonceBytes) {
    lost("protocol");
    return false;
  }
  StreamNonce leaderNonce{};
  std::memcpy(leaderNonce.data(), e.body.challenge.nonce.bytes, kStreamNonceBytes);

  SyncEnvelope hello = cadence_sync_v1_Envelope_init_zero;
  hello.which_body = cadence_sync_v1_Envelope_hello_tag;
  auto& h = hello.body.hello;
  h.protocol = kStreamProtocol;
  copyText(h.group_id, config_.groupId);
  copyText(h.member_id, config_.memberId);
  h.nonce.size = kStreamNonceBytes;
  std::memcpy(h.nonce.bytes, nonce.data(), kStreamNonceBytes);
  const auto mac = helloMac(config_.key, leaderNonce, nonce, std::string_view(h.member_id));
  h.mac.size = 32;
  std::memcpy(h.mac.bytes, mac.data(), 32);
  io.send(hello);
  if (!io.flush()) {
    lost("closed");
    return false;
  }

  e = cadence_sync_v1_Envelope_init_zero;
  if (const auto failed = await(e)) {
    lost(*failed);
    return false;
  }
  if (e.which_body == cadence_sync_v1_Envelope_goodbye_tag) {
    lost(refusal(e.body.goodbye.reason));
    return false;
  }
  if (e.which_body != cadence_sync_v1_Envelope_welcome_tag || e.body.welcome.mac.size != 32) {
    lost("protocol");
    return false;
  }
  const auto want = welcomeMac(config_.key, nonce, leaderNonce);
  if (!equalConstantTime(want, std::span<const std::uint8_t>(e.body.welcome.mac.bytes, 32))) {
    lost("auth_failed");
    return false;
  }
  io.rekey(sessionKey(config_.key, leaderNonce, nonce));
  {
    std::lock_guard lock(mutex_);
    outbox_.clear();
  }
  connected_.store(true);
  if (events_.welcomed)
    events_.welcomed(e.body.welcome.latency_ms);

  auto heardNs = monoNs();
  std::deque<SyncEnvelope> sending;
  while (!stop_.load()) {
    {
      std::lock_guard lock(mutex_);
      sending.swap(outbox_);
    }
    for (const auto& out : sending)
      io.send(out);
    sending.clear();
    if (!io.flush()) {
      lost("closed");
      return true;
    }
    pollfd fds[2] = {{.fd = io.fd(), .events = static_cast<short>(POLLIN | (io.pending() > 0 ? POLLOUT : 0)),
                      .revents = 0},
                     {.fd = wake_.fd(), .events = POLLIN, .revents = 0}};
    ::poll(fds, 2, kPollMs);
    if ((fds[1].revents & POLLIN) != 0)
      wake_.drain();
    bool closed = false;
    if ((fds[0].revents & (POLLIN | POLLHUP | POLLERR)) != 0) {
      closed = io.receive() == StreamConn::Read::Closed;
      heardNs = monoNs();
    }
    for (;;) {
      SyncEnvelope in;
      const auto r = io.next(in);
      if (r == OpenResult::Need)
        break;
      if (r == OpenResult::Refused) {
        lost("protocol");
        return true;
      }
      if (in.which_body == cadence_sync_v1_Envelope_goodbye_tag) {
        lost(refusal(in.body.goodbye.reason));
        return true;
      }
      if (events_.message)
        events_.message(in);
    }
    if (closed || monoNs() - heardNs > config_.silenceNs) {
      lost("closed");
      return true;
    }
  }
  connected_.store(false);
  SyncEnvelope bye = cadence_sync_v1_Envelope_init_zero;
  bye.which_body = cadence_sync_v1_Envelope_goodbye_tag;
  copyText(bye.body.goodbye.reason, "leaving");
  io.send(bye);
  io.flush();
  return true;
}

}  // namespace cadence::sync
