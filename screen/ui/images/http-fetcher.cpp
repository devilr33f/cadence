#include "ui/images/http-fetcher.h"

#include <fcntl.h>
#include <netdb.h>
#include <poll.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <unistd.h>

#include <cerrno>
#include <chrono>
#include <cstring>

#if defined(CADENCE_SCREEN_CURL)
#include <curl/curl.h>
#endif

namespace cadence::screen::ui::images {

namespace {

using Clock = std::chrono::steady_clock;

int remainingMs(Clock::time_point deadline) {
  const auto left = std::chrono::duration_cast<std::chrono::milliseconds>(deadline - Clock::now()).count();
  return left > 0 ? static_cast<int>(left) : 0;
}

bool waitFor(int fd, short events, Clock::time_point deadline) {
  pollfd p{fd, events, 0};
  for (;;) {
    const int n = ::poll(&p, 1, remainingMs(deadline));
    if (n > 0) return true;
    if (n == 0) return false;
    if (errno != EINTR) return false;
  }
}

int connectTo(const Url& url, Clock::time_point deadline, std::string& code) {
  addrinfo hints{};
  hints.ai_family = AF_UNSPEC;
  hints.ai_socktype = SOCK_STREAM;
  addrinfo* res = nullptr;
  if (::getaddrinfo(url.host.c_str(), url.port.c_str(), &hints, &res) != 0 || res == nullptr) {
    code = "dns";
    return -1;
  }
  int fd = -1;
  for (auto* a = res; a != nullptr && fd < 0; a = a->ai_next) {
    // darwin has no SOCK_NONBLOCK/SOCK_CLOEXEC socket flags
    fd = ::socket(a->ai_family, a->ai_socktype, a->ai_protocol);
    if (fd < 0) continue;
    const int flags = ::fcntl(fd, F_GETFL, 0);
    if (flags >= 0) ::fcntl(fd, F_SETFL, flags | O_NONBLOCK);
    ::fcntl(fd, F_SETFD, FD_CLOEXEC);
    if (::connect(fd, a->ai_addr, a->ai_addrlen) == 0) break;
    int err = 0;
    socklen_t len = sizeof err;
    if (errno == EINPROGRESS && waitFor(fd, POLLOUT, deadline) && ::getsockopt(fd, SOL_SOCKET, SO_ERROR, &err, &len) == 0 &&
        err == 0) {
      break;
    }
    ::close(fd);
    fd = -1;
  }
  ::freeaddrinfo(res);
  if (fd < 0) code = "connect";
  return fd;
}

bool sendAll(int fd, const std::string& data, Clock::time_point deadline) {
  std::size_t off = 0;
  while (off < data.size()) {
    if (!waitFor(fd, POLLOUT, deadline)) return false;
    const auto n = ::send(fd, data.data() + off, data.size() - off, MSG_NOSIGNAL);
    if (n < 0) {
      if (errno == EAGAIN || errno == EINTR) continue;
      return false;
    }
    off += static_cast<std::size_t>(n);
  }
  return true;
}

std::string lower(std::string s) {
  for (auto& c : s) c = static_cast<char>(std::tolower(static_cast<unsigned char>(c)));
  return s;
}

// a chunked body decoded in place; false when it is malformed
bool dechunk(const std::string& in, std::string& out) {
  std::size_t pos = 0;
  out.clear();
  for (;;) {
    const auto eol = in.find("\r\n", pos);
    if (eol == std::string::npos) return false;
    std::size_t size = 0;
    try {
      size = std::stoul(in.substr(pos, eol - pos), nullptr, 16);
    } catch (...) {
      return false;
    }
    pos = eol + 2;
    if (size == 0) return true;
    if (pos + size > in.size()) return false;
    out.append(in, pos, size);
    pos += size + 2;
  }
}

}  // namespace

bool parseUrl(const std::string& url, Url& out) {
  const auto sep = url.find("://");
  if (sep == std::string::npos) return false;
  out.scheme = lower(url.substr(0, sep));
  if (out.scheme != "http" && out.scheme != "https") return false;
  auto rest = url.substr(sep + 3);
  const auto slash = rest.find('/');
  auto authority = rest.substr(0, slash);
  out.path = slash == std::string::npos ? "" : rest.substr(slash);
  while (!out.path.empty() && out.path.back() == '/') out.path.pop_back();
  if (authority.empty() || authority.find('@') != std::string::npos) return false;
  out.port = out.scheme == "https" ? "443" : "80";
  if (authority.front() == '[') {
    const auto close = authority.find(']');
    if (close == std::string::npos) return false;
    out.host = authority.substr(1, close - 1);
    if (close + 1 < authority.size()) {
      if (authority[close + 1] != ':') return false;
      out.port = authority.substr(close + 2);
    }
  } else if (const auto colon = authority.find(':'); colon != std::string::npos) {
    out.host = authority.substr(0, colon);
    out.port = authority.substr(colon + 1);
  } else {
    out.host = authority;
  }
  return !out.host.empty() && !out.port.empty();
}

bool httpGet(const Url& url, const std::string& path, std::size_t maxBytes, int timeoutMs, std::string& body,
             std::string& code) {
  const auto deadline = Clock::now() + std::chrono::milliseconds(timeoutMs);
  const int fd = connectTo(url, deadline, code);
  if (fd < 0) return false;
  const bool v6 = url.host.find(':') != std::string::npos;
  const std::string host = (v6 ? "[" + url.host + "]" : url.host) + ((url.port == "80") ? "" : ":" + url.port);
  const std::string req = "GET " + url.path + path + " HTTP/1.1\r\nHost: " + host +
                          "\r\nUser-Agent: cadence-screen\r\nAccept: " + std::string(kAccept) + "\r\nConnection: close\r\n\r\n";
  std::string raw;
  bool ok = sendAll(fd, req, deadline);
  char buf[16384];
  while (ok) {
    if (!waitFor(fd, POLLIN, deadline)) {
      code = "timeout";
      ok = false;
      break;
    }
    const auto n = ::recv(fd, buf, sizeof buf, 0);
    if (n < 0) {
      if (errno == EAGAIN || errno == EINTR) continue;
      code = "read";
      ok = false;
      break;
    }
    if (n == 0) break;
    raw.append(buf, static_cast<std::size_t>(n));
    if (raw.size() > maxBytes + 16384) {
      code = "too_large";
      ok = false;
    }
  }
  ::close(fd);
  if (!ok) {
    if (code.empty()) code = "write";
    return false;
  }
  const auto head = raw.find("\r\n\r\n");
  if (head == std::string::npos || !raw.starts_with("HTTP/1.")) {
    code = "protocol";
    return false;
  }
  const auto statusAt = raw.find(' ');
  const int status = statusAt != std::string::npos ? std::atoi(raw.c_str() + statusAt + 1) : 0;
  if (status != 200) {
    code = status == 404 ? "not_found" : status == 406 ? "not_acceptable" : status == 502 ? "unavailable" : "status";
    return false;
  }
  const auto headers = lower(raw.substr(0, head));
  body = raw.substr(head + 4);
  if (headers.find("transfer-encoding: chunked") != std::string::npos) {
    std::string plain;
    if (!dechunk(body, plain)) {
      code = "protocol";
      return false;
    }
    body.swap(plain);
  } else if (const auto cl = headers.find("content-length:"); cl != std::string::npos) {
    const auto want = std::strtoull(headers.c_str() + cl + 15, nullptr, 10);
    if (body.size() < want) {
      code = "short";
      return false;
    }
    // want is at most body.size() here, so it fits a 32-bit size_t
    body.resize(static_cast<std::size_t>(want));
  }
  if (body.size() > maxBytes) {
    code = "too_large";
    return false;
  }
  return true;
}

std::string HttpFetcher::originOf(std::string url) {
  if (url.starts_with("wss://")) url = "https://" + url.substr(6);
  if (url.starts_with("ws://")) url = "http://" + url.substr(5);
  if (!url.starts_with("http://") && !url.starts_with("https://")) return {};
  const auto sep = url.find("://");
  const auto slash = url.find('/', sep + 3);
  return slash == std::string::npos ? url : url.substr(0, slash);
}

void HttpFetcher::setServer(std::string serverUrl) {
  auto origin = originOf(std::move(serverUrl));
  std::lock_guard lock(mu_);
  base_ = std::move(origin);
}

#if defined(CADENCE_SCREEN_CURL)
namespace {
std::size_t curlWrite(char* data, std::size_t size, std::size_t n, void* user) {
  auto* out = static_cast<std::string*>(user);
  if (out->size() + size * n > (4u << 20)) return 0;
  out->append(data, size * n);
  return size * n;
}
}  // namespace
#endif

bool HttpFetcher::fetch(const std::string& id, std::string& bytes, std::string& code) {
  if (!validId(id)) {
    code = "invalid";
    return false;
  }
  std::string base;
  {
    std::lock_guard lock(mu_);
    base = base_;
  }
  if (base.empty() && fallback_) base = originOf(fallback_());
  Url url;
  if (base.empty() || !parseUrl(base, url)) {
    code = "no_server";
    return false;
  }
  const std::string path = "/v2/images/" + id;
  constexpr std::size_t kMax = 4u << 20;
  if (url.scheme == "https" && https_) return https_(base + path, kMax, 10000, bytes, code);
  if (url.scheme == "https") {
#if defined(CADENCE_SCREEN_CURL)
    CURL* c = curl_easy_init();
    if (c == nullptr) {
      code = "tls";
      return false;
    }
    const std::string full = base + path;
    curl_easy_setopt(c, CURLOPT_URL, full.c_str());
    curl_easy_setopt(c, CURLOPT_TIMEOUT_MS, 10000L);
    curl_easy_setopt(c, CURLOPT_NOSIGNAL, 1L);
    curl_easy_setopt(c, CURLOPT_WRITEFUNCTION, curlWrite);
    curl_easy_setopt(c, CURLOPT_WRITEDATA, &bytes);
    curl_easy_setopt(c, CURLOPT_USERAGENT, "cadence-screen");
    curl_slist* headers = curl_slist_append(nullptr, (std::string("Accept: ") + kAccept).c_str());
    curl_easy_setopt(c, CURLOPT_HTTPHEADER, headers);
    const auto rc = curl_easy_perform(c);
    curl_slist_free_all(headers);
    long status = 0;
    curl_easy_getinfo(c, CURLINFO_RESPONSE_CODE, &status);
    curl_easy_cleanup(c);
    if (rc != CURLE_OK || status != 200) {
      code = status == 404 ? "not_found" : status == 406 ? "not_acceptable" : "unavailable";
      return false;
    }
    return true;
#else
    code = "tls_unsupported";
    return false;
#endif
  }
  return httpGet(url, path, kMax, 10000, bytes, code);
}

}  // namespace cadence::screen::ui::images
