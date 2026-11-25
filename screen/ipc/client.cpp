#include "ipc/client.h"

#include "ipc/log.h"

#include <chrono>

namespace cadence::screen::ipc {

namespace {

std::int64_t monotonicMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now().time_since_epoch())
      .count();
}

}  // namespace

ScreenClient::ScreenClient(ClientConfig config, std::function<void()> wake)
    : config_(std::move(config)), wake_(std::move(wake)) {}

ScreenClient::~ScreenClient() { stop(); }

void ScreenClient::start() {
  if (thread_.joinable()) return;
  stopping_ = false;
  thread_ = std::thread([this] { run(); });
}

void ScreenClient::stop() {
  stopping_ = true;
  {
    std::lock_guard lock(queueMu_);
    stopCv_.notify_all();
  }
  {
    std::lock_guard lock(sendMu_);
    socket_.shutdown();
  }
  if (thread_.joinable()) thread_.join();
}

std::vector<ScreenClient::Event> ScreenClient::drain() {
  std::lock_guard lock(queueMu_);
  std::vector<Event> out;
  out.swap(queue_);
  return out;
}

bool ScreenClient::send(const Envelope& envelope) {
  std::vector<std::uint8_t> frame;
  if (!encodeEnvelope(envelope, frame)) return false;
  std::lock_guard lock(sendMu_);
  if (!connected_.load()) return false;
  if (socket_.send(frame)) return true;
  dropped_++;
  return false;
}

void ScreenClient::push(Event event) {
  {
    std::lock_guard lock(queueMu_);
    queue_.push_back(std::move(event));
  }
  if (wake_) wake_();
}

bool ScreenClient::sleepFor(std::int64_t ms) {
  std::unique_lock lock(queueMu_);
  stopCv_.wait_for(lock, std::chrono::milliseconds(ms), [this] { return stopping_.load(); });
  return !stopping_.load();
}

void ScreenClient::run() {
  std::uint64_t attempt = 0;
  while (!stopping_.load()) {
    std::string error;
    auto socket = SeqpacketSocket::connect(config_.path, &error);
    if (!socket) {
      // the first failure and then every 20th, so a missing cadenced does not flood the log
      if (attempt % 20 == 0) CADENCE_LOG(Warn, "link_connect_failed", {{"code", Token(error)}, {"attempt", attempt}});
      attempt++;
      if (!sleepFor(config_.backoff.next())) break;
      continue;
    }
    {
      std::lock_guard lock(sendMu_);
      socket_ = std::move(*socket);
      connected_ = true;
    }
    const std::int64_t since = monotonicMs();
    bool helloSent = false;
    if (config_.hello) {
      OutEnvelope hello = config_.hello();
      helloSent = send(hello.get());
    }
    if (!helloSent && config_.hello) {
      CADENCE_LOG(Warn, "link_hello_failed");
    } else {
      CADENCE_LOG(Info, "link_connected", {{"attempts", attempt + 1}});
      attempt = 0;
      push(Event{Event::Kind::Connected, nullptr});
      std::vector<std::uint8_t> frame;
      for (;;) {
        const auto got = socket_.recv(frame, config_.pollMs);
        if (stopping_.load()) break;
        if (got == SeqpacketSocket::Recv::WouldBlock) continue;
        if (got != SeqpacketSocket::Recv::Frame) break;
        std::string decodeError;
        auto envelope = decodeEnvelope(frame, &decodeError);
        if (!envelope) {
          badFrames_++;
          CADENCE_LOG(Warn, "link_bad_frame", {{"bytes", frame.size()}});
          continue;
        }
        push(Event{Event::Kind::Message, std::move(envelope)});
      }
    }
    {
      std::lock_guard lock(sendMu_);
      connected_ = false;
      socket_.close();
    }
    const std::int64_t held = monotonicMs() - since;
    if (helloSent || !config_.hello) push(Event{Event::Kind::Disconnected, nullptr});
    if (stopping_.load()) break;
    CADENCE_LOG(Warn, "link_closed", {{"held_ms", held}});
    config_.backoff.connectedFor(held);
    if (!sleepFor(config_.backoff.next())) break;
  }
}

}  // namespace cadence::screen::ipc
