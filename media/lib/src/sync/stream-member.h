#pragma once

#include "sync/stream-socket.h"
#include "sync/stream-wire.h"

#include <atomic>
#include <condition_variable>
#include <cstdint>
#include <deque>
#include <functional>
#include <mutex>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

namespace cadence::sync {

struct MemberStreamConfig {
  std::string host;
  std::uint16_t port = 0;
  std::string groupId;
  std::string memberId;
  std::vector<std::uint8_t> key;
  int connectTimeoutMs = 2000;
  // between attempts: from min, doubling to max; back to min after a connection that got its Welcome
  int backoffMinMs = 250;
  int backoffMaxMs = 5000;
  // a leader that sends nothing (not even a Duck) for this long is gone
  std::int64_t silenceNs = 10'000'000'000;
  // runs first on the stream's thread (audiod raises its priority there)
  std::function<void()> onThread{};
};

// told on the stream's thread
struct MemberStreamEvents {
  // the handshake held: the latency the leader's frames include. a Join sent from here is the first message after it
  std::function<void(std::uint32_t latencyMs)> welcomed;
  // Format, Frame, Pause, Flush, Duck, Metadata
  std::function<void(const SyncEnvelope& envelope)> message;
  // the connection is gone or never came: a token (unreachable, auth_failed, wrong_group, refused, protocol, closed,
  // replaced, leaving, overloaded)
  std::function<void(std::string_view reason)> lost;
};

// the member's end of the group stream: dials the leader with backoff, runs the handshake (refusing a Challenge or
// Welcome the key did not make) and hands each message over as it arrives. a thread of its own; send and stop are
// safe from any thread
class MemberStream {
 public:
  MemberStream(MemberStreamConfig config, MemberStreamEvents events);
  ~MemberStream();
  MemberStream(const MemberStream&) = delete;
  MemberStream& operator=(const MemberStream&) = delete;

  void start();
  void stop();
  // queued for the connection in force; false while there is none (it is dropped)
  bool send(const SyncEnvelope& envelope);
  [[nodiscard]] bool connected() const { return connected_.load(); }

 private:
  void run();
  // one connection, until it goes; true when it got as far as Welcome
  bool session(int fd);
  // waits ms or until stop; false on stop
  bool rest(int ms);

  MemberStreamConfig config_;
  MemberStreamEvents events_;
  WakePipe wake_;
  std::thread thread_;
  std::atomic<bool> stop_{false};
  std::atomic<bool> connected_{false};
  std::mutex mutex_;
  std::condition_variable stopped_;
  std::deque<SyncEnvelope> outbox_;
};

}  // namespace cadence::sync
