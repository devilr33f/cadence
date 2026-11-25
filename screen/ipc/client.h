#pragma once

#include "ipc/backoff.h"
#include "ipc/codec.h"
#include "ipc/seqpacket.h"

#include <atomic>
#include <condition_variable>
#include <functional>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <vector>

namespace cadence::screen::ipc {

struct ClientConfig {
  // cadenced's screen socket: a path, or @name for an abstract one
  std::string path;
  Backoff backoff;
  // the Hello sent first on every connection
  std::function<OutEnvelope()> hello;
  // how often the reader looks at its stop flag while nothing arrives
  int pollMs = 200;
};

// the screen/v1 link to cadenced. a thread of its own connects (and reconnects with backoff), sends Hello first,
// and reads frames; the main thread drains what arrived and sends at will. wake is called from that thread whenever
// something can be drained (the app pushes an sdl event)
class ScreenClient {
 public:
  struct Event {
    enum class Kind { Connected, Disconnected, Message };
    Kind kind = Kind::Message;
    DecodedEnvelope envelope;
  };

  ScreenClient(ClientConfig config, std::function<void()> wake);
  ~ScreenClient();
  ScreenClient(const ScreenClient&) = delete;
  ScreenClient& operator=(const ScreenClient&) = delete;

  void start();
  void stop();

  // in order: connection events between the messages they separate
  std::vector<Event> drain();
  // any thread; false while there is no link (nothing is queued: the app sends its state again on Connected)
  bool send(const Envelope& envelope);
  [[nodiscard]] bool connected() const { return connected_.load(); }
  // frames that did not decode, since start
  [[nodiscard]] std::uint64_t badFrames() const { return badFrames_.load(); }
  // frames send could not hand over while linked (cadenced not reading), since start
  [[nodiscard]] std::uint64_t droppedFrames() const { return dropped_.load(); }

 private:
  void run();
  void push(Event event);
  // false when stopped meanwhile
  bool sleepFor(std::int64_t ms);

  ClientConfig config_;
  std::function<void()> wake_;
  std::thread thread_;
  std::atomic<bool> stopping_{false};
  std::atomic<bool> connected_{false};
  std::atomic<std::uint64_t> badFrames_{0};
  std::atomic<std::uint64_t> dropped_{0};

  std::mutex sendMu_;
  SeqpacketSocket socket_;

  std::mutex queueMu_;
  std::condition_variable stopCv_;
  std::vector<Event> queue_;
};

}  // namespace cadence::screen::ipc
