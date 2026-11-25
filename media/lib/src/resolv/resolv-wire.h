// soloist's name lookups, forwarded from the preload in its runtime to audiod's resolver over a unix socket (the
// android app's audiod: a glibc soloist beside bionic has no resolv.conf to read, and no listener on 127.0.0.1:53 an
// app could use, so glibc's own fallback answers nothing). the frame is one u32 little-endian body length, then the
// body; both sides are ours, and the client falls back to the real getaddrinfo whenever anything is off
#pragma once

#include <cstdint>

namespace cadence::resolv {

inline constexpr std::uint32_t kMaxFrame = 1024;
inline constexpr std::uint32_t kMaxName = 256;
inline constexpr std::uint32_t kMaxService = 32;
inline constexpr std::uint32_t kMaxAddrs = 16;

// one address on the wire: family (4), port (2) and 16 address bytes (an inet one in the first 4), packed
inline constexpr std::uint32_t kAddrBytes = 22;

// the request body: family (0 AF_UNSPEC, else 2 AF_INET / 10 AF_INET6 as the wire's own numbers), the flags that are
// passed on (AI_CANONNAME 0x1, AI_NUMERICSERV 0x2 as the wire's own bits), the name and the service
struct Request {
  std::int32_t family = 0;
  std::uint32_t flags = 0;
  const char* name = nullptr;
  std::uint32_t nameLen = 0;
  const char* service = nullptr;
  std::uint32_t serviceLen = 0;
};

// the answer's status: audiod's own getaddrinfo error becomes one of these, and the client maps it to its libc's
// EAI code (bionic's and glibc's differ, so the wire carries none of either)
enum Status : std::int32_t { kOk = 0, kNoname = 1, kAgain = 2, kFail = 3 };

// one address as the wire's own family numbers, a port and 16 address bytes (an inet one in the first 4)
struct Address {
  std::int32_t family = 0;
  std::uint16_t port = 0;
  std::uint8_t bytes[16] = {};
};

struct Response {
  std::int32_t status = kFail;
  const char* cname = nullptr;
  std::uint32_t cnameLen = 0;
  const Address* addrs = nullptr;
  std::uint32_t addrCount = 0;
};

// the body of each frame as the wire writes it: little-endian u32 counts, then the bytes they name
inline constexpr std::uint32_t kRequestHead = 16;
inline constexpr std::uint32_t kResponseHead = 12;

inline std::uint32_t get32(const std::uint8_t* p) {
  return static_cast<std::uint32_t>(p[0]) | static_cast<std::uint32_t>(p[1]) << 8 |
         static_cast<std::uint32_t>(p[2]) << 16 | static_cast<std::uint32_t>(p[3]) << 24;
}

inline void put32(std::uint8_t* p, std::uint32_t v) {
  p[0] = static_cast<std::uint8_t>(v);
  p[1] = static_cast<std::uint8_t>(v >> 8);
  p[2] = static_cast<std::uint8_t>(v >> 16);
  p[3] = static_cast<std::uint8_t>(v >> 24);
}

inline std::uint16_t get16(const std::uint8_t* p) {
  return static_cast<std::uint16_t>(static_cast<std::uint16_t>(p[0]) | static_cast<std::uint16_t>(p[1]) << 8);
}

inline void put16(std::uint8_t* p, std::uint16_t v) {
  p[0] = static_cast<std::uint8_t>(v);
  p[1] = static_cast<std::uint8_t>(v >> 8);
}

inline bool printable(const char* bytes, std::uint32_t len) {
  for (std::uint32_t i = 0; i < len; i++) {
    const auto c = static_cast<std::uint8_t>(bytes[i]);
    if (c < 0x20 || c > 0x7e)
      return false;
  }
  return true;
}

}  // namespace cadence::resolv
