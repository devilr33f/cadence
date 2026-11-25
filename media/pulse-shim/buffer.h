#pragma once

#include "pcmframe/codec.h"

#include <sys/time.h>

#include <cstddef>
#include <cstdint>
#include <deque>
#include <optional>
#include <span>
#include <variant>
#include <vector>

namespace cadence::pulse {

// a frame on its way to the socket
struct Next {
  std::vector<std::uint8_t> wire;
  // a Format: the server's reports count this stream's bytes from what went out before it
  bool format = false;
  // pcm payload bytes in it
  std::size_t pcm = 0;
  // the first bytes after playback (re)starts: initially, after an uncork, after an underrun
  bool starts = false;
};

struct Indexes {
  std::uint64_t write = 0;
  std::uint64_t read = 0;
  bool playing = false;
  std::uint64_t sinceUnderrun = 0;
  // when the server's last report arrived: the time the read index was current
  std::optional<timeval> reported;
};

// bytes in arrival order, taken from the front
class ByteQueue {
 public:
  [[nodiscard]] std::size_t size() const { return bytes_.size() - head_; }
  [[nodiscard]] bool empty() const { return size() == 0; }
  [[nodiscard]] std::span<const std::uint8_t> front(std::size_t n) const { return {bytes_.data() + head_, n}; }
  void append(std::span<const std::uint8_t> bytes);
  void drop(std::size_t n);
  void clear();

 private:
  std::vector<std::uint8_t> bytes_;
  std::size_t head_ = 0;
};

// one stream's playback buffer: the bytes that wait in the shim, the frames still to send, and the write and read
// indexes whose difference is the fill level the libpulse docs describe (an underrun is read >= write)
class Buffer {
 public:
  Buffer(pcmframe::Format format, std::size_t tlength, std::size_t maxlength, bool corked);

  // what writable_size reports: the room left below tlength
  [[nodiscard]] std::size_t room() const;
  // all of bytes or nothing: past the room is fine, past maxlength is not
  bool write(std::span<const std::uint8_t> bytes);
  void cork(bool on);
  // a flush is never an underrun: the dry spell it leaves ends with the next bytes sent
  void flush();
  // the next frame for the socket: control first, then up to kMaxPayload waiting bytes unless corked
  std::optional<Next> next();
  // the server's read index for this stream, reported at; true when the buffer just ran dry while playing
  bool played(std::uint64_t played, timeval at);
  [[nodiscard]] Indexes indexes() const;

 private:
  using Control = std::variant<pcmframe::Format, pcmframe::Cork, pcmframe::Flush>;

  struct Flushed {
    std::uint64_t sent;
    std::uint64_t skipped;
  };

  [[nodiscard]] std::uint64_t readIndex() const { return played_ + skipped_; }
  // flushed bytes count as read once the server is past the flush, whichever of the two came first
  void catchUp();

  std::size_t tlength_;
  // the most that may wait: a client may write past tlength, up to this
  std::size_t maxlength_;
  // accepted bytes not yet handed to the socket; whole frames, at most maxlength
  ByteQueue waiting_;
  // format, cork and flush frames, sent before any further pcm so they keep their place among the writes
  std::deque<Control> control_;
  bool corked_;
  std::uint64_t accepted_ = 0;
  std::uint64_t sent_ = 0;
  std::uint64_t played_ = 0;
  std::optional<timeval> reported_;
  // bytes a flush dropped before they reached the server: they count as read once it reads past the flush
  std::uint64_t skipped_ = 0;
  std::deque<Flushed> flushes_;
  // the next bytes sent start playback
  bool starting_ = true;
  // the read index when playback last (re)started
  std::uint64_t restarted_ = 0;
  // bytes went out since playback started or the last flush, so running dry is an underrun
  bool flowing_ = false;
};

}  // namespace cadence::pulse
