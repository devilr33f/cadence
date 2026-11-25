#pragma once

#include "result.h"
#include "sync/stream-socket.h"
#include "sync/stream-wire.h"
#include "sync/sync-clock.h"

#include <atomic>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

namespace cadence::sync {

struct LeaderStreamConfig {
  std::string groupId;
  std::vector<std::uint8_t> key;
  // the member ids it accepts
  std::vector<std::string> members;
  std::uint32_t latencyMs = 1000;
  // what a join can still be served from
  std::int64_t backlogNs = 10'000'000'000;
  // a member whose socket leaves this much unsent is dropped (overloaded): it cannot keep up with the stream
  std::size_t maxPendingBytes = 1024 * 1024;
  // frames are written to a member while less than this is unsent, so a slow one never takes the whole backlog at once
  std::size_t writeAheadBytes = 64 * 1024;
  // a connection that sends no valid Hello in this long is closed
  std::int64_t helloTimeoutNs = 5'000'000'000;
  // the clock the backlog is pruned by: the leader's sync clock
  SyncClock clock;
  // the group's time, when another device serves its clock (the frames are stamped in the master's time): the backlog
  // is pruned by it instead. never blocks: called with the leader's lock held
  std::function<std::int64_t()> now{};
  // runs first on the stream's thread (audiod raises its priority there)
  std::function<void()> onThread{};
};

// one encoded frame of the timeline
struct StreamFrame {
  std::uint32_t epoch = 0;
  std::uint64_t seq = 0;
  std::int64_t tNs = 0;
  std::uint64_t mediaMs = 0;
  std::vector<std::uint8_t> data;
};

// a member as the leader sees it: numbers and its id only
struct LeaderMemberView {
  std::string id;
  bool connected = false;
  bool joined = false;
  // 0/1: legacy chirps only; 2: coded probes. learned from the initial Status before a calibration starts
  std::uint32_t calibrationVersion = 0;
  // the epoch of the last Ready it sent, 0 for none
  std::uint32_t readyEpoch = 0;
  std::optional<cadence_sync_v1_Status> status;
  // the epoch's frames in the backlog not written to it yet, in ms of audio (a member that falls behind)
  std::uint32_t unsentMs = 0;
};

// told on the stream's thread, without its lock held: a handler may call back into the stream
struct LeaderStreamEvents {
  std::function<void(const std::string& member)> connected;
  // reason: a token (closed, refused, overloaded, replaced, timeout)
  std::function<void(const std::string& member, std::string_view reason)> disconnected;
  std::function<void(const std::string& member, const cadence_sync_v1_Ready& ready)> ready;
  std::function<void(const std::string& member, const cadence_sync_v1_Control& control)> control;
  std::function<void(const std::string& member, bool active)> duck;
  std::function<void(const std::string& member, std::int64_t fromNs)> joined;
  std::function<void(const std::string& member, const cadence_sync_v1_CalibrateArrivals& arrivals)> calibrated;
  std::function<void(const std::string& member, const cadence_sync_v1_CalibrateProgress& progress)> calibrationProgress;
  std::function<void(const std::string& member, const cadence_sync_v1_TrackArrivals& arrivals)> tracked;
  // a connection refused before it named a member it may (bad tag, wrong group, unknown member, bad mac): a token
  std::function<void(std::string_view reason)> refused;
};

// the leader's end of the group stream: accepts members, runs the handshake, and serves each one from the backlog of
// the current epoch's frames (a cursor per member over it, written while the socket takes it), with the control
// messages in order between. a thread of its own; every public call is safe from any thread
class LeaderStream {
 public:
  // binds host:port (empty host: every v4 address); the error is an errno
  static Result<std::unique_ptr<LeaderStream>, SocketError> listen(const std::string& host, std::uint16_t port,
                                                           LeaderStreamConfig config, LeaderStreamEvents events);
  ~LeaderStream();
  LeaderStream(const LeaderStream&) = delete;
  LeaderStream& operator=(const LeaderStream&) = delete;

  [[nodiscard]] std::uint16_t port() const { return port_; }
  void stop();

  // a new epoch: its Format to every member, the backlog of the last one dropped, every joined member served from
  // the new epoch's first frame
  void beginEpoch(const cadence_sync_v1_Format& format);
  void pushFrame(StreamFrame frame);
  void pause(std::uint32_t epoch, std::int64_t tNs);
  // drops the epoch's backlog too: what comes next is a new epoch
  void flush(std::uint32_t epoch);
  void duck(bool active);
  void metadata(const cadence_sync_v1_Metadata& metadata);
  // an acoustic calibration's start, to every member connected now
  void calibrate(const cadence_sync_v1_CalibrateStart& start);
  // an opt-in cycle; GroupLeader calls this only when every connected member is version 3
  void track(const cadence_sync_v1_TrackCycle& cycle);
  // what Welcome says from now on
  void setLatency(std::uint32_t ms);
  [[nodiscard]] std::vector<LeaderMemberView> members() const;
  // the frame whose t_ns is the latest at or before tNs, if the backlog has it
  [[nodiscard]] std::optional<StreamFrame> frameAt(std::int64_t tNs) const;
  // the backlog's frames from fromNs on, data included: the leader's own playout rejoins from it
  [[nodiscard]] std::vector<StreamFrame> backlogFrom(std::int64_t fromNs) const;

 private:
  struct Conn;

  LeaderStream(int listener, LeaderStreamConfig config, LeaderStreamEvents events);
  void run();
  void accept();
  // what the handlers are told once the lock is let go
  using Deferred = std::vector<std::function<void()>>;
  bool handle(Conn& conn, const SyncEnvelope& envelope, Deferred& later);
  bool hello(Conn& conn, const cadence_sync_v1_Hello& hello, Deferred& later);
  void goodbye(Conn& conn, std::string_view reason);
  void broadcast(const SyncEnvelope& envelope);
  // true while frames are left that it did not write yet
  bool pump(Conn& conn);
  void prune();
  void close(Conn& conn, std::string_view reason, Deferred& later);

  int listener_;
  std::uint16_t port_ = 0;
  LeaderStreamConfig config_;
  LeaderStreamEvents events_;
  WakePipe wake_;
  std::thread thread_;
  std::atomic<bool> stop_{false};

  mutable std::mutex mutex_;
  std::vector<std::unique_ptr<Conn>> conns_;
  std::deque<StreamFrame> backlog_;
  std::optional<cadence_sync_v1_Format> format_;
  std::optional<cadence_sync_v1_Metadata> metadata_;
  bool duck_ = false;
  std::int64_t keepaliveNs_ = 0;
};

}  // namespace cadence::sync
