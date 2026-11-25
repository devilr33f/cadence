#include "sync/stream-leader.h"

#include <algorithm>
#include <cstring>
#include <limits>
#include <poll.h>
#include <sys/socket.h>
#include <unistd.h>

namespace cadence::sync {

namespace {

constexpr int kPollMs = 50;
constexpr std::int64_t kKeepaliveNs = 2'000'000'000;

template <std::size_t N>
void copyText(char (&dst)[N], std::string_view src) {
  const auto n = std::min(src.size(), N - 1);
  std::memcpy(dst, src.data(), n);
  dst[n] = '\0';
}

}  // namespace

struct LeaderStream::Conn {
  std::unique_ptr<StreamConn> io;
  StreamNonce nonce{};
  std::int64_t acceptedNs = 0;
  bool live = false;
  bool closed = false;
  std::string id;
  bool joined = false;
  std::uint32_t calibrationVersion = 0;
  std::int64_t fromNs = std::numeric_limits<std::int64_t>::min();
  std::uint32_t cursorEpoch = 0;
  std::uint64_t cursorSeq = 0;
  std::uint32_t readyEpoch = 0;
  std::optional<cadence_sync_v1_Status> status;
};

Result<std::unique_ptr<LeaderStream>, SocketError> LeaderStream::listen(const std::string& host, std::uint16_t port,
                                                                LeaderStreamConfig config, LeaderStreamEvents events) {
  auto fd = listenTcp(host, port);
  if (!fd.ok())
    return fd.error();
  std::unique_ptr<LeaderStream> stream(new LeaderStream(fd.value(), std::move(config), std::move(events)));
  stream->thread_ = std::thread([s = stream.get()] { s->run(); });
  return stream;
}

LeaderStream::LeaderStream(int listener, LeaderStreamConfig config, LeaderStreamEvents events)
    : listener_(listener), port_(localPort(listener)), config_(std::move(config)), events_(std::move(events)) {}

LeaderStream::~LeaderStream() {
  stop();
  ::close(listener_);
}

void LeaderStream::stop() {
  if (stop_.exchange(true))
    return;
  wake_.wake();
  if (thread_.joinable())
    thread_.join();
  std::lock_guard lock(mutex_);
  for (auto& conn : conns_)
    if (conn->live && !conn->closed)
      goodbye(*conn, "leaving");
  conns_.clear();
}

void LeaderStream::beginEpoch(const cadence_sync_v1_Format& format) {
  {
    std::lock_guard lock(mutex_);
    backlog_.clear();
    format_ = format;
    SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
    e.which_body = cadence_sync_v1_Envelope_format_tag;
    e.body.format = format;
    broadcast(e);
    for (auto& conn : conns_) {
      conn->cursorEpoch = format.epoch;
      conn->cursorSeq = 0;
      conn->fromNs = std::numeric_limits<std::int64_t>::min();
    }
  }
  wake_.wake();
}

void LeaderStream::pushFrame(StreamFrame frame) {
  {
    std::lock_guard lock(mutex_);
    if (!format_ || frame.epoch != format_->epoch)
      return;
    // frames of an epoch are contiguous: a gap starts the backlog over rather than mislabel a seq
    if (!backlog_.empty() && frame.seq != backlog_.back().seq + 1)
      backlog_.clear();
    backlog_.push_back(std::move(frame));
  }
  wake_.wake();
}

void LeaderStream::pause(std::uint32_t epoch, std::int64_t tNs) {
  {
    std::lock_guard lock(mutex_);
    SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
    e.which_body = cadence_sync_v1_Envelope_pause_tag;
    e.body.pause.epoch = epoch;
    e.body.pause.t_ns = tNs;
    broadcast(e);
    // nobody hears what comes after: a join is not served it either
    while (!backlog_.empty() && backlog_.back().tNs >= tNs)
      backlog_.pop_back();
  }
  wake_.wake();
}

void LeaderStream::flush(std::uint32_t epoch) {
  {
    std::lock_guard lock(mutex_);
    SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
    e.which_body = cadence_sync_v1_Envelope_flush_tag;
    e.body.flush.epoch = epoch;
    broadcast(e);
    backlog_.clear();
    format_.reset();
  }
  wake_.wake();
}

void LeaderStream::duck(bool active) {
  {
    std::lock_guard lock(mutex_);
    if (duck_ == active)
      return;
    duck_ = active;
    SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
    e.which_body = cadence_sync_v1_Envelope_duck_tag;
    e.body.duck.active = active;
    broadcast(e);
  }
  wake_.wake();
}

void LeaderStream::metadata(const cadence_sync_v1_Metadata& metadata) {
  {
    std::lock_guard lock(mutex_);
    metadata_ = metadata;
    SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
    e.which_body = cadence_sync_v1_Envelope_metadata_tag;
    e.body.metadata = metadata;
    broadcast(e);
  }
  wake_.wake();
}

void LeaderStream::calibrate(const cadence_sync_v1_CalibrateStart& start) {
  {
    std::lock_guard lock(mutex_);
    SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
    e.which_body = cadence_sync_v1_Envelope_calibrate_start_tag;
    e.body.calibrate_start = start;
    broadcast(e);
  }
  wake_.wake();
}

void LeaderStream::track(const cadence_sync_v1_TrackCycle& cycle) {
  {
    std::lock_guard lock(mutex_);
    SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
    e.which_body = cadence_sync_v1_Envelope_track_cycle_tag;
    e.body.track_cycle = cycle;
    broadcast(e);
  }
  wake_.wake();
}

void LeaderStream::setLatency(std::uint32_t ms) {
  std::lock_guard lock(mutex_);
  config_.latencyMs = ms;
}

std::vector<LeaderMemberView> LeaderStream::members() const {
  std::lock_guard lock(mutex_);
  std::vector<LeaderMemberView> out;
  for (const auto& id : config_.members) {
    LeaderMemberView view;
    view.id = id;
    for (const auto& conn : conns_) {
      if (!conn->live || conn->closed || conn->id != id)
        continue;
      view.connected = true;
      view.joined = conn->joined;
      view.calibrationVersion = conn->calibrationVersion;
      view.readyEpoch = conn->readyEpoch;
      view.status = conn->status;
      if (conn->joined && format_ && !backlog_.empty() && conn->cursorEpoch == format_->epoch) {
        const auto sent = conn->cursorSeq > backlog_.front().seq ? conn->cursorSeq - backlog_.front().seq : 0;
        const auto left = backlog_.size() > sent ? backlog_.size() - sent : 0;
        view.unsentMs = static_cast<std::uint32_t>(std::min<std::uint64_t>(left * 20, 600'000));
      }
    }
    out.push_back(std::move(view));
  }
  return out;
}

std::optional<StreamFrame> LeaderStream::frameAt(std::int64_t tNs) const {
  std::lock_guard lock(mutex_);
  const StreamFrame* found = nullptr;
  for (const auto& frame : backlog_) {
    if (frame.tNs > tNs)
      break;
    found = &frame;
  }
  if (found == nullptr)
    return std::nullopt;
  StreamFrame copy = *found;
  copy.data.clear();
  return copy;
}

std::vector<StreamFrame> LeaderStream::backlogFrom(std::int64_t fromNs) const {
  std::lock_guard lock(mutex_);
  std::vector<StreamFrame> out;
  for (const auto& frame : backlog_)
    if (frame.tNs >= fromNs)
      out.push_back(frame);
  return out;
}

void LeaderStream::broadcast(const SyncEnvelope& envelope) {
  for (auto& conn : conns_)
    if (conn->live && !conn->closed)
      conn->io->send(envelope);
}

void LeaderStream::goodbye(Conn& conn, std::string_view reason) {
  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  e.which_body = cadence_sync_v1_Envelope_goodbye_tag;
  copyText(e.body.goodbye.reason, reason);
  conn.io->send(e);
  conn.io->flush();
}

void LeaderStream::close(Conn& conn, std::string_view reason, Deferred& later) {
  if (conn.closed)
    return;
  conn.closed = true;
  if (conn.live && events_.disconnected)
    later.push_back([this, id = conn.id, r = std::string(reason)] { events_.disconnected(id, r); });
}

void LeaderStream::accept() {
  for (;;) {
    const int fd = ::accept(listener_, nullptr, nullptr);
    if (fd < 0)
      return;
    auto conn = std::make_unique<Conn>();
    conn->io = std::make_unique<StreamConn>(fd, config_.key);
    conn->acceptedNs = monoNs();
    if (!randomNonce(conn->nonce))
      continue;
    SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
    e.which_body = cadence_sync_v1_Envelope_challenge_tag;
    e.body.challenge.protocol = kStreamProtocol;
    e.body.challenge.nonce.size = kStreamNonceBytes;
    std::memcpy(e.body.challenge.nonce.bytes, conn->nonce.data(), kStreamNonceBytes);
    conn->io->send(e);
    conn->io->flush();
    conns_.push_back(std::move(conn));
  }
}

bool LeaderStream::hello(Conn& conn, const cadence_sync_v1_Hello& hello, Deferred& later) {
  const auto refuse = [&](std::string_view reason) {
    goodbye(conn, reason);
    if (events_.refused)
      later.push_back([this, r = std::string(reason)] { events_.refused(r); });
    return false;
  };
  if (hello.protocol != kStreamProtocol)
    return refuse("protocol");
  if (std::string_view(hello.group_id) != config_.groupId)
    return refuse("wrong_group");
  const std::string id(hello.member_id);
  if (std::find(config_.members.begin(), config_.members.end(), id) == config_.members.end())
    return refuse("unknown_member");
  if (hello.nonce.size != kStreamNonceBytes || hello.mac.size != 32)
    return refuse("protocol");
  const std::span<const std::uint8_t> memberNonce(hello.nonce.bytes, kStreamNonceBytes);
  const auto want = helloMac(config_.key, conn.nonce, memberNonce, id);
  if (!equalConstantTime(want, std::span<const std::uint8_t>(hello.mac.bytes, 32)))
    return refuse("bad_mac");

  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  e.which_body = cadence_sync_v1_Envelope_welcome_tag;
  const auto mac = welcomeMac(config_.key, memberNonce, conn.nonce);
  e.body.welcome.mac.size = 32;
  std::memcpy(e.body.welcome.mac.bytes, mac.data(), 32);
  e.body.welcome.latency_ms = config_.latencyMs;
  conn.io->send(e);
  conn.io->rekey(sessionKey(config_.key, conn.nonce, memberNonce));
  for (auto& other : conns_) {
    if (other.get() != &conn && other->live && !other->closed && other->id == id) {
      goodbye(*other, "replaced");
      close(*other, "replaced", later);
    }
  }
  conn.live = true;
  conn.id = id;
  if (format_) {
    SyncEnvelope f = cadence_sync_v1_Envelope_init_zero;
    f.which_body = cadence_sync_v1_Envelope_format_tag;
    f.body.format = *format_;
    conn.io->send(f);
    conn.cursorEpoch = format_->epoch;
  }
  if (metadata_) {
    SyncEnvelope m = cadence_sync_v1_Envelope_init_zero;
    m.which_body = cadence_sync_v1_Envelope_metadata_tag;
    m.body.metadata = *metadata_;
    conn.io->send(m);
  }
  SyncEnvelope d = cadence_sync_v1_Envelope_init_zero;
  d.which_body = cadence_sync_v1_Envelope_duck_tag;
  d.body.duck.active = duck_;
  conn.io->send(d);
  if (events_.connected)
    later.push_back([this, id] { events_.connected(id); });
  return true;
}

bool LeaderStream::handle(Conn& conn, const SyncEnvelope& e, Deferred& later) {
  if (!conn.live) {
    if (e.which_body != cadence_sync_v1_Envelope_hello_tag) {
      goodbye(conn, "protocol");
      return false;
    }
    return hello(conn, e.body.hello, later);
  }
  const auto id = conn.id;
  switch (e.which_body) {
    case cadence_sync_v1_Envelope_join_tag:
      conn.joined = true;
      conn.fromNs = e.body.join.from_ns;
      if (format_)
        conn.cursorEpoch = format_->epoch;
      conn.cursorSeq = backlog_.empty() ? 0 : backlog_.front().seq;
      if (events_.joined)
        later.push_back([this, id, from = e.body.join.from_ns] { events_.joined(id, from); });
      return true;
    case cadence_sync_v1_Envelope_ready_tag:
      conn.readyEpoch = e.body.ready.epoch;
      if (events_.ready)
        later.push_back([this, id, r = e.body.ready] { events_.ready(id, r); });
      return true;
    case cadence_sync_v1_Envelope_status_tag:
      conn.status = e.body.status;
      conn.calibrationVersion = e.body.status.calibration_version;
      return true;
    case cadence_sync_v1_Envelope_control_tag:
      if (events_.control)
        later.push_back([this, id, c = e.body.control] { events_.control(id, c); });
      return true;
    case cadence_sync_v1_Envelope_duck_request_tag:
      if (events_.duck)
        later.push_back([this, id, a = e.body.duck_request.active] { events_.duck(id, a); });
      return true;
    case cadence_sync_v1_Envelope_calibrate_arrivals_tag:
      if (events_.calibrated)
        later.push_back([this, id, a = e.body.calibrate_arrivals] { events_.calibrated(id, a); });
      return true;
    case cadence_sync_v1_Envelope_calibrate_progress_tag:
      if (events_.calibrationProgress)
        later.push_back([this, id, p = e.body.calibrate_progress] { events_.calibrationProgress(id, p); });
      return true;
    case cadence_sync_v1_Envelope_track_arrivals_tag:
      if (events_.tracked)
        later.push_back([this, id, a = e.body.track_arrivals] { events_.tracked(id, a); });
      return true;
    case cadence_sync_v1_Envelope_goodbye_tag:
      return false;
    default:
      goodbye(conn, "protocol");
      return false;
  }
}

bool LeaderStream::pump(Conn& conn) {
  if (!conn.live || conn.closed || !conn.joined || !format_ || backlog_.empty())
    return false;
  const auto base = backlog_.front().seq;
  if (conn.cursorEpoch != format_->epoch) {
    conn.cursorEpoch = format_->epoch;
    conn.cursorSeq = base;
  }
  // what it had not had yet was pruned: it gets a gap, which its playout rejoins over
  conn.cursorSeq = std::max(conn.cursorSeq, base);
  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  e.which_body = cadence_sync_v1_Envelope_frame_tag;
  while (conn.cursorSeq - base < backlog_.size() && conn.io->pending() < config_.writeAheadBytes) {
    const auto& frame = backlog_[static_cast<std::size_t>(conn.cursorSeq - base)];
    ++conn.cursorSeq;
    if (frame.tNs < conn.fromNs)
      continue;
    auto& f = e.body.frame;
    f.epoch = frame.epoch;
    f.seq = frame.seq;
    f.t_ns = frame.tNs;
    f.media_ms = frame.mediaMs;
    f.data.size = static_cast<pb_size_t>(std::min(frame.data.size(), sizeof f.data.bytes));
    std::memcpy(f.data.bytes, frame.data.data(), f.data.size);
    conn.io->send(e);
  }
  return conn.cursorSeq - base < backlog_.size();
}

void LeaderStream::prune() {
  const auto cut = (config_.now ? config_.now() : config_.clock.now()) - config_.backlogNs;
  while (!backlog_.empty() && backlog_.front().tNs < cut)
    backlog_.pop_front();
}

void LeaderStream::run() {
  if (config_.onThread)
    config_.onThread();
  std::vector<pollfd> fds;
  while (!stop_.load()) {
    fds.clear();
    fds.push_back({.fd = wake_.fd(), .events = POLLIN, .revents = 0});
    fds.push_back({.fd = listener_, .events = POLLIN, .revents = 0});
    {
      std::lock_guard lock(mutex_);
      for (const auto& conn : conns_) {
        const short events = static_cast<short>(POLLIN | (conn->io->pending() > 0 ? POLLOUT : 0));
        fds.push_back({.fd = conn->io->fd(), .events = events, .revents = 0});
      }
    }
    ::poll(fds.data(), static_cast<nfds_t>(fds.size()), kPollMs);
    if (stop_.load())
      break;
    if ((fds[0].revents & POLLIN) != 0)
      wake_.drain();
    Deferred later;
    {
      std::lock_guard lock(mutex_);
      if ((fds[1].revents & POLLIN) != 0)
        accept();
      // the connections polled are the first ones: those accepted just now are read next time
      for (std::size_t i = 2; i < fds.size(); ++i) {
        auto& conn = *conns_[i - 2];
        if (conn.closed || (fds[i].revents & (POLLIN | POLLHUP | POLLERR)) == 0)
          continue;
        if (conn.io->receive() == StreamConn::Read::Closed) {
          // what it sent before it went still counts
          SyncEnvelope e;
          while (conn.io->next(e) == OpenResult::Message && handle(conn, e, later)) {
          }
          close(conn, "closed", later);
          continue;
        }
        for (;;) {
          SyncEnvelope e;
          const auto r = conn.io->next(e);
          if (r == OpenResult::Need)
            break;
          if (r == OpenResult::Refused) {
            if (!conn.live && events_.refused)
              later.push_back([this] { events_.refused("bad_tag"); });
            close(conn, "refused", later);
            break;
          }
          if (!handle(conn, e, later)) {
            close(conn, "refused", later);
            break;
          }
        }
      }
      prune();
      const auto now = monoNs();
      // an idle group sends nothing else: the duck in force, again, tells every member the leader is there
      if (now - keepaliveNs_ > kKeepaliveNs) {
        keepaliveNs_ = now;
        SyncEnvelope k = cadence_sync_v1_Envelope_init_zero;
        k.which_body = cadence_sync_v1_Envelope_duck_tag;
        k.body.duck.active = duck_;
        broadcast(k);
      }
      for (auto& conn : conns_) {
        if (conn->closed)
          continue;
        if (!conn->live && now - conn->acceptedNs > config_.helloTimeoutNs) {
          close(*conn, "timeout", later);
          continue;
        }
        // a join catches up from the backlog faster than real time: written while the socket takes it
        bool failed = false;
        for (int round = 0; round < 64; ++round) {
          const bool more = pump(*conn);
          if (!conn->io->flush()) {
            failed = true;
            break;
          }
          if (!more || conn->io->pending() > 0)
            break;
        }
        if (failed) {
          close(*conn, "closed", later);
          continue;
        }
        if (conn->io->pending() > config_.maxPendingBytes) {
          goodbye(*conn, "overloaded");
          close(*conn, "overloaded", later);
        }
      }
      std::erase_if(conns_, [](const auto& conn) { return conn->closed; });
    }
    for (auto& f : later)
      f();
  }
}

}  // namespace cadence::sync
