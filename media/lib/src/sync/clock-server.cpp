#include "sync/clock-server.h"

#include "sync/clock-wire.h"

#include <array>

namespace cadence::sync {

namespace {

// how often the loop looks at its stop flag while nobody probes
constexpr int kPollMs = 100;

}  // namespace

ClockServer::ClockServer(ClockSocket socket, std::vector<std::uint8_t> key, std::function<void()> onThread)
    : socket_(std::move(socket)), key_(std::move(key)), onThread_(std::move(onThread)) {}

ClockServer::~ClockServer() { stop(); }

void ClockServer::start() {
  if (thread_.joinable())
    return;
  stop_.store(false);
  thread_ = std::thread([this] { run(); });
}

void ClockServer::stop() {
  stop_.store(true);
  if (thread_.joinable())
    thread_.join();
}

ClockServerStats ClockServer::stats() const {
  return {.served = served_.load(), .refused = refused_.load(), .sendErrors = sendErrors_.load(),
          .kernelStamps = kernelStamps_.load()};
}

void ClockServer::run() {
  if (onThread_)
    onThread_();
  std::array<std::uint8_t, 128> buf{};
  while (!stop_.load(std::memory_order_relaxed)) {
    const auto d = socket_.receive(buf, kPollMs);
    if (!d)
      continue;
    const auto req = decodeRequest(key_, std::span<const std::uint8_t>(buf.data(), d->size));
    if (!req) {
      refused_.fetch_add(1, std::memory_order_relaxed);
      continue;
    }
    if (d->kernelStamp)
      kernelStamps_.store(true, std::memory_order_relaxed);
    ClockReply reply{.seq = req->seq, .t1 = req->t1, .t2 = clock_.toSync(d->arrivedNs), .t3 = 0};
    // t3 as late as it can be: the tag is computed over it, so it is taken just before that
    reply.t3 = clock_.toSync(monoNs());
    const auto packet = encodeReply(key_, reply);
    if (socket_.send(packet, d->from))
      served_.fetch_add(1, std::memory_order_relaxed);
    else
      sendErrors_.fetch_add(1, std::memory_order_relaxed);
  }
}

}  // namespace cadence::sync
