#pragma once

#include "pcmframe/codec.h"
#include "pulse-shim/abi.h"
#include "pulse-shim/buffer.h"

#include <condition_variable>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>

namespace cadence::pulse {

class Context;
class Loop;
class Stream;

enum class StreamEvent { Started, Underflow };

// the one playback stream a link serves
struct Served {
  std::uint32_t index = 0;
  std::weak_ptr<Stream> stream;
  pa_sample_spec spec{};
  Buffer buffer;
  // the link's pcm count when this stream's format went out: the server's reports count from it
  std::optional<std::uint64_t> base;
};

// a context's pcm link to the server: one thread sends the served stream's frames, one reads the server's reports.
// neither calls into c or changes a state: they post that to the loop, which holds the lock
class Link : public std::enable_shared_from_this<Link> {
 public:
  // both threads, on their own handles of the socket; null when they cannot start
  static std::shared_ptr<Link> start(const std::shared_ptr<Context>& context, int socket);

  // serves served from now on; the stream it replaces, if any, is returned, its unsent bytes dropped
  std::optional<std::weak_ptr<Stream>> attach(Served served);
  void detach(std::uint32_t index);
  // f(buffer of stream index) while the link serves it, then wakes the sender; false when it does not
  bool with(std::uint32_t index, const std::function<void(Buffer&)>& f);
  // reads it without waking the sender: a client polls the room often
  bool peek(std::uint32_t index, const std::function<void(const Buffer&)>& f);
  std::optional<pa_sample_spec> spec();
  // the context is closing: both threads end once its socket is shut down
  std::weak_ptr<Stream> stop();

 private:
  Link(std::shared_ptr<Loop> loop, std::weak_ptr<Context> context);

  // whole frames, one at a time, so a stream that replaces another starts on a frame boundary
  void sendLoop(int fd);
  std::optional<Next> next();
  void readLoop(int fd);
  // false on a frame the shim cannot read
  bool reports(pcmframe::Decoder& decoder);
  void post(const std::weak_ptr<Stream>& stream, StreamEvent event) noexcept;
  // the server went away, broke the protocol or a link thread failed: the context fails, once, unless it is closing
  // anyway (or memory ran out to tell the loop)
  void lost(int error) noexcept;

  const std::shared_ptr<Loop> loop_;
  const std::weak_ptr<Context> context_;

  std::mutex mutex_;
  std::condition_variable work_;
  // all of these under mutex_
  std::optional<Served> served_;
  // pcm bytes sent over the connection, every stream's; the server's played reports count the same bytes
  std::uint64_t sent_ = 0;
  bool stopping_ = false;
  bool lost_ = false;
};

}  // namespace cadence::pulse
