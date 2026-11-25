// libcadence-resolv.so, the preload of an unjailed soloist (android: media/audiod/soloist-resolv.cpp holds the
// other end). a glibc soloist beside bionic has no resolv.conf to read and no listener on 127.0.0.1:53 an app could
// use, so this answers getaddrinfo by asking audiod's socket (CADENCE_RESOLV_SOCK) over the wire resolv-wire.h
// names, and falls back to the real getaddrinfo whenever anything is off: no env, no socket, a short answer, a name
// the wire refuses. it exports getaddrinfo and freeaddrinfo alone (resolv-preload.map), allocates with malloc alone
// and needs nothing but libc: the runtime has no c++ library to link
#include "resolv/resolv-wire.h"

#include <dlfcn.h>
#include <netdb.h>
#include <netinet/in.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <sys/types.h>
#include <unistd.h>

// as the pulse shim's own exports (abi.h's CADENCE_PA_EXPORT): the library is compiled with hidden visibility, and
// a hidden symbol never reaches the dynamic table, however the version script names it — the two interposed
// functions carry the default visibility the map's globals are
#define CADENCE_RESOLV_EXPORT __attribute__((visibility("default")))

namespace {

using cadence::resolv::Address;
using cadence::resolv::get16;
using cadence::resolv::get32;
using cadence::resolv::kAddrBytes;
using cadence::resolv::kMaxFrame;
using cadence::resolv::kMaxName;
using cadence::resolv::kRequestHead;
using cadence::resolv::kResponseHead;

// a lookup the wire will not carry: nodeless ones (ai_passive's null node) and names past its bound
bool carriable(const char* node) {
  if (node == nullptr)
    return false;
  const auto len = strlen(node);
  return len > 0 && len <= kMaxName && cadence::resolv::printable(node, static_cast<std::uint32_t>(len));
}

const char* socketPath() {
  const char* path = ::getenv("CADENCE_RESOLV_SOCK");
  if (path == nullptr || path[0] != '/')
    return nullptr;
  return path;
}

bool writeAll(int fd, const void* bytes, size_t len) {
  const auto* out = static_cast<const std::uint8_t*>(bytes);
  while (len > 0) {
    const auto sent = ::send(fd, out, len, MSG_NOSIGNAL);
    if (sent <= 0)
      return false;
    out += sent;
    len -= static_cast<size_t>(sent);
  }
  return true;
}

// one frame: its head, then as much of the body as the socket gives
bool readFrame(int fd, std::uint8_t* body, std::uint32_t& len) {
  std::uint8_t head[4];
  size_t got = 0;
  while (got < sizeof head) {
    const auto size = ::recv(fd, head + got, sizeof head - got, 0);
    if (size <= 0)
      return false;
    got += static_cast<size_t>(size);
  }
  const auto want = get32(head);
  if (want == 0 || want > kMaxFrame || want > len)
    return false;
  got = 0;
  while (got < want) {
    const auto size = ::recv(fd, body + got, want - got, 0);
    if (size <= 0)
      return false;
    got += static_cast<size_t>(size);
  }
  len = want;
  return true;
}

// the whole exchange, or -1: whatever went wrong falls back to the real resolver
bool exchange(const std::uint8_t* request, std::uint32_t requestLen, std::uint8_t* answer, std::uint32_t& answerLen) {
  const char* path = socketPath();
  if (path == nullptr)
    return false;
  const int fd = ::socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
  if (fd < 0)
    return false;
  sockaddr_un address;
  memset(&address, 0, sizeof address);
  address.sun_family = AF_UNIX;
  if (strlen(path) >= sizeof address.sun_path) {
    ::close(fd);
    return false;
  }
  memcpy(address.sun_path, path, strlen(path) + 1);
  timeval timeout{};
  timeout.tv_sec = 8;
  ::setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof timeout);
  ::setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &timeout, sizeof timeout);
  // the frame: the body's length first, as the server reads it
  std::uint8_t head[4];
  cadence::resolv::put32(head, requestLen);
  if (::connect(fd, reinterpret_cast<const sockaddr*>(&address), sizeof address) != 0 ||
      !writeAll(fd, head, sizeof head) || !writeAll(fd, request, requestLen) || !readFrame(fd, answer, answerLen)) {
    ::close(fd);
    return false;
  }
  ::close(fd);
  return true;
}

int wireStatus(std::int32_t status) {
  switch (status) {
    case cadence::resolv::kOk:
      return 0;
    case cadence::resolv::kNoname:
      return EAI_NONAME;
    case cadence::resolv::kAgain:
      return EAI_AGAIN;
    default:
      return EAI_FAIL;
  }
}

}  // namespace

extern "C" CADENCE_RESOLV_EXPORT int getaddrinfo(const char* node, const char* service, const struct addrinfo* hints,
                                              struct addrinfo** out) {
  static int (*real)(const char*, const char*, const struct addrinfo*, struct addrinfo**) = nullptr;
  if (real == nullptr)
    real = reinterpret_cast<decltype(real)>(::dlsym(RTLD_NEXT, "getaddrinfo"));
  if (!carriable(node) || out == nullptr)
    return real != nullptr ? real(node, service, hints, out) : EAI_FAIL;

  const auto serviceLen = service != nullptr ? strlen(service) : 0;
  if (serviceLen > cadence::resolv::kMaxService)
    return real != nullptr ? real(node, service, hints, out) : EAI_FAIL;
  std::uint32_t flags = 0;
  if (hints != nullptr) {
    if ((hints->ai_flags & AI_CANONNAME) != 0)
      flags |= 0x1;
    if ((hints->ai_flags & AI_NUMERICSERV) != 0)
      flags |= 0x2;
  }
  std::int32_t family = 0;
  if (hints != nullptr && hints->ai_family != AF_UNSPEC) {
    if (hints->ai_family != AF_INET && hints->ai_family != AF_INET6)
      return EAI_FAMILY;
    family = hints->ai_family == AF_INET ? 2 : 10;
  }
  // the socket types an entry is made for: the one the hints name, else stream and datagram
  int types[2] = {SOCK_STREAM, SOCK_DGRAM};
  int typeCount = 2;
  if (hints != nullptr && hints->ai_socktype != 0) {
    if (hints->ai_socktype != SOCK_STREAM && hints->ai_socktype != SOCK_DGRAM)
      return real != nullptr ? real(node, service, hints, out) : EAI_FAIL;
    types[0] = hints->ai_socktype;
    typeCount = 1;
  }

  std::uint8_t request[kMaxFrame];
  const auto nameLen = static_cast<std::uint32_t>(strlen(node));
  const auto requestLen = kRequestHead + nameLen + static_cast<std::uint32_t>(serviceLen);
  if (requestLen > kMaxFrame)
    return EAI_FAIL;
  cadence::resolv::put32(request + 0, static_cast<std::uint32_t>(family));
  cadence::resolv::put32(request + 4, flags);
  cadence::resolv::put32(request + 8, nameLen);
  cadence::resolv::put32(request + 12, static_cast<std::uint32_t>(serviceLen));
  memcpy(request + kRequestHead, node, nameLen);
  if (serviceLen != 0)
    memcpy(request + kRequestHead + nameLen, service, serviceLen);

  std::uint8_t body[kMaxFrame];
  std::uint32_t bodyLen = sizeof body;
  if (!exchange(request, requestLen, body, bodyLen))
    return real != nullptr ? real(node, service, hints, out) : EAI_FAIL;
  if (bodyLen < kResponseHead)
    return real != nullptr ? real(node, service, hints, out) : EAI_FAIL;
  const auto status = static_cast<std::int32_t>(get32(body + 0));
  if (status != cadence::resolv::kOk)
    return wireStatus(status);
  const auto cnameLen = get32(body + 4);
  const auto count = get32(body + 8);
  if (kResponseHead + cnameLen + count * kAddrBytes != bodyLen || count == 0 ||
      count > cadence::resolv::kMaxAddrs)
    return EAI_FAIL;
  const std::uint8_t* cname = body + kResponseHead;
  const std::uint8_t* addrs = cname + cnameLen;

  // one entry per address and socket type, as getaddrinfo's callers expect to walk them
  struct addrinfo* first = nullptr;
  struct addrinfo* last = nullptr;
  for (std::uint32_t i = 0; i < count; i++) {
    const std::uint8_t* wire = addrs + i * kAddrBytes;
    const auto wireFamily = static_cast<std::int32_t>(get32(wire + 0));
    const auto port = get16(wire + 4);
    for (int t = 0; t < typeCount; t++) {
      const int type = types[t];
      // glibc's layout, an entry and its address in one block and the canonical name apart, so either freeaddrinfo
      // frees what the other made: a fallback's answer reaches ours
      auto* entry = static_cast<struct addrinfo*>(calloc(1, sizeof(struct addrinfo) + sizeof(struct sockaddr_in6)));
      if (entry == nullptr) {
        freeaddrinfo(first);
        return EAI_MEMORY;
      }
      void* address = entry + 1;
      entry->ai_flags = 0;
      entry->ai_family = wireFamily == 10 ? AF_INET6 : AF_INET;
      entry->ai_socktype = type;
      entry->ai_protocol = type == SOCK_STREAM ? IPPROTO_TCP : IPPROTO_UDP;
      entry->ai_addrlen = wireFamily == 10 ? sizeof(sockaddr_in6) : sizeof(sockaddr_in);
      entry->ai_addr = reinterpret_cast<struct sockaddr*>(address);
      if (wireFamily == 10) {
        auto* wide = reinterpret_cast<struct sockaddr_in6*>(address);
        wide->sin6_family = AF_INET6;
        wide->sin6_port = htons(port);
        memcpy(&wide->sin6_addr, wire + 6, 16);
      } else {
        auto* narrow = reinterpret_cast<struct sockaddr_in*>(address);
        narrow->sin_family = AF_INET;
        narrow->sin_port = htons(port);
        memcpy(&narrow->sin_addr, wire + 6, 4);
      }
      if (last != nullptr)
        last->ai_next = entry;
      else
        first = entry;
      last = entry;
    }
  }
  if (hints != nullptr && (hints->ai_flags & AI_CANONNAME) != 0 && cnameLen != 0) {
    first->ai_canonname = static_cast<char*>(malloc(cnameLen + 1));
    if (first->ai_canonname == nullptr) {
      freeaddrinfo(first);
      return EAI_MEMORY;
    }
    memcpy(first->ai_canonname, cname, cnameLen);
    first->ai_canonname[cnameLen] = '\0';
  }
  *out = first;
  return 0;
}

extern "C" CADENCE_RESOLV_EXPORT void freeaddrinfo(struct addrinfo* res) {
  while (res != nullptr) {
    struct addrinfo* next = res->ai_next;
    free(res->ai_canonname);
    free(res);
    res = next;
  }
}
