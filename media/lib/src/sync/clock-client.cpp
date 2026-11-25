#include "sync/clock-client.h"

#include "sync/clock-wire.h"

#include <algorithm>
#include <array>

namespace cadence::sync {

namespace {

// probes in flight that a reply may still close
constexpr std::size_t kPending = 32;

}  // namespace

ClockClient::ClockClient(ClockSocket socket, ClockPeer leader, std::vector<std::uint8_t> key,
                         ClockClientConfig config, std::function<void()> onThread)
    : socket_(std::move(socket)),
      leader_(leader),
      key_(std::move(key)),
      config_(config),
      onThread_(std::move(onThread)),
      estimator_(config.estimator),
      smoother_(config.smoother),
      pending_(kPending, 0) {}

ClockClient::~ClockClient() { stop(); }

void ClockClient::start() {
  if (thread_.joinable())
    return;
  stop_.store(false);
  thread_ = std::thread([this] { run(); });
}

void ClockClient::stop() {
  stop_.store(true);
  if (thread_.joinable())
    thread_.join();
}

ClockClientStats ClockClient::stats() const {
  std::lock_guard lock(statsMutex_);
  return stats_;
}

void ClockClient::probe(std::int64_t now) {
  ++seq_;
  // seq 0 marks an empty slot
  if (seq_ == 0)
    ++seq_;
  pending_[seq_ % kPending] = seq_;
  const auto packet = encodeRequest(key_, ClockRequest{.seq = seq_, .t1 = config_.clock.toSync(now)});
  socket_.send(packet, leader_);
  std::lock_guard lock(statsMutex_);
  ++stats_.sent;
}

void ClockClient::received(const ClockDatagram& d, std::span<const std::uint8_t> packet) {
  const auto reply = decodeReply(key_, packet);
  auto& slot = pending_[reply ? reply->seq % kPending : 0];
  const auto arrived = config_.clock.toSync(d.arrivedNs);
  if (!reply || slot != reply->seq || arrived - reply->t1 > config_.maxRttNs || arrived < reply->t1) {
    std::lock_guard lock(statsMutex_);
    ++stats_.refused;
    return;
  }
  slot = 0;
  lastReplyNs_ = d.arrivedNs;
  estimator_.add(ClockSample{.t1 = reply->t1, .t2 = reply->t2, .t3 = reply->t3, .t4 = arrived});
  const auto e = estimator_.estimate(arrived);
  if (e)
    mapping_.store(smoother_.update(*e));
  std::lock_guard lock(statsMutex_);
  ++stats_.replies;
  stats_.kernelStamps = stats_.kernelStamps || d.kernelStamp;
  if (e)
    stats_.last = e;
  stats_.lastStepNs = static_cast<std::int64_t>(smoother_.lastStepNs());
}

void ClockClient::run() {
  if (onThread_)
    onThread_();
  std::array<std::uint8_t, 128> buf{};
  auto next = monoNs();
  lastReplyNs_ = next;
  while (!stop_.load(std::memory_order_relaxed)) {
    const auto now = monoNs();
    if (now >= next) {
      probe(now);
      next += config_.intervalNs;
      // a thread that slept through several probes sends one, not a burst
      if (next < now)
        next = now + config_.intervalNs;
    }
    if (now - lastReplyNs_ > config_.lostAfterNs && estimator_.size() > 0) {
      estimator_.reset();
      smoother_.reset();
      mapping_.clear();
      std::lock_guard lock(statsMutex_);
      ++stats_.losses;
      stats_.last.reset();
    }
    const auto waitMs = static_cast<int>(std::clamp<std::int64_t>((next - monoNs()) / 1'000'000, 0, 100));
    if (const auto d = socket_.receive(buf, waitMs))
      received(*d, std::span<const std::uint8_t>(buf.data(), std::min(d->size, buf.size())));
  }
}

}  // namespace cadence::sync
