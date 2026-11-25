#pragma once

#include "result.h"

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <variant>
#include <vector>

// frames between the libpulse shim and audiod's soloist pcm server: type u8, length u32 le, payload. the shim sends a
// stream's Format once, then its bytes, corks and flushes; the server answers Played after every Pcm. the shim links
// this too, so it allocates but never throws anything but bad_alloc
namespace cadence::pcmframe {

inline constexpr std::size_t kHeaderBytes = 5;
// the largest payload either side accepts; a longer frame is an error, never a wait
inline constexpr std::size_t kMaxPayload = 65536;

enum class Encoding : std::uint8_t { F32Le = 1, S16Le = 2 };

struct Format {
  Encoding encoding = Encoding::F32Le;
  std::uint8_t channels = 0;
  std::uint32_t rate = 0;

  // the formats a stream may carry: mono or stereo at 44.1 or 48 kHz
  [[nodiscard]] bool valid() const;
  [[nodiscard]] std::size_t frameBytes() const;
  bool operator==(const Format&) const = default;
};

// interleaved samples exactly as the client wrote them; a decoded one points into the decoder until its next push
struct Pcm {
  std::span<const std::uint8_t> bytes;

  bool operator==(const Pcm& other) const { return std::ranges::equal(bytes, other.bytes); }
};

struct Cork {
  bool on = false;
  bool operator==(const Cork&) const = default;
};

struct Flush {
  bool operator==(const Flush&) const = default;
};

using ToServer = std::variant<Format, Pcm, Cork, Flush>;

// the connection's total of pcm bytes the server has consumed, played or dropped. never reset, a Format included: the
// shim counts a stream's read index from the total when its Format went out
struct Played {
  std::uint64_t total = 0;
};

enum class FrameError : std::uint8_t { TooLong, Unknown, Malformed };

// appended to out; false, and nothing appended, for a payload over kMaxPayload
bool encode(const ToServer& message, std::vector<std::uint8_t>& out);
void encode(Played message, std::vector<std::uint8_t>& out);

// collects bytes off a socket and hands out whole frames, each judged from its header as soon as that arrives; after
// an error it keeps returning it
class Decoder {
 public:
  void push(std::span<const std::uint8_t> bytes);

  // the next frame the shim sent, none until one is whole
  [[nodiscard]] Result<std::optional<ToServer>, FrameError> nextToServer();
  // the next frame the server sent
  [[nodiscard]] Result<std::optional<Played>, FrameError> nextToShim();

 private:
  struct Frame {
    std::uint8_t kind;
    std::span<const std::uint8_t> payload;
  };

  [[nodiscard]] Result<std::optional<Frame>, FrameError> nextFrame(std::span<const std::uint8_t> known) const;
  void consume(std::size_t payload) { start_ += kHeaderBytes + payload; }

  std::vector<std::uint8_t> buffer_;
  std::size_t start_ = 0;
};

}  // namespace cadence::pcmframe
