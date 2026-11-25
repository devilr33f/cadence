#include "support/http-server.h"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <openssl/crypto.h>
#include <openssl/pem.h>
#include <openssl/ssl.h>
#include <poll.h>
#include <sys/socket.h>
#include <unistd.h>

#include <algorithm>
#include <cctype>
#include <climits>
#include <csignal>
#include <cstring>
#include <stdexcept>
#include <utility>

namespace cadence::test_support {

namespace {

constexpr int kPollMs = 20;
constexpr std::size_t kMaxRequestBytes = 16384;

std::string lower(std::string_view text) {
  std::string out(text);
  std::transform(out.begin(), out.end(), out.begin(),
                 [](unsigned char c) { return static_cast<char>(std::tolower(c)); });
  return out;
}

std::string_view trim(std::string_view text) {
  while (!text.empty() && (text.front() == ' ' || text.front() == '\t'))
    text.remove_prefix(1);
  while (!text.empty() && (text.back() == ' ' || text.back() == '\t' || text.back() == '\r'))
    text.remove_suffix(1);
  return text;
}

std::string reason(int status) {
  switch (status) {
    case 200:
      return "OK";
    case 206:
      return "Partial Content";
    case 302:
      return "Found";
    case 404:
      return "Not Found";
    case 416:
      return "Range Not Satisfiable";
    default:
      return "Status";
  }
}

// the body with an icy metadata block after every metaint bytes
std::string withIcy(std::string_view body, std::size_t metaint, const std::map<std::size_t, std::string>& titles) {
  std::string out;
  for (std::size_t block = 0, first = 0; first < body.size(); ++block, first += metaint) {
    out.append(body.substr(first, metaint));
    if (first + metaint > body.size())
      break;
    const auto title = titles.find(block);
    if (title == titles.end()) {
      out.push_back('\0');
      continue;
    }
    std::string text = "StreamTitle='" + title->second + "';";
    const std::size_t blocks = (text.size() + 15) / 16;
    text.resize(blocks * 16, '\0');
    out.push_back(static_cast<char>(blocks));
    out.append(text);
  }
  return out;
}

bool waitReadable(int fd) {
  pollfd ready{.fd = fd, .events = POLLIN, .revents = 0};
  return ::poll(&ready, 1, kPollMs) == 1;
}

}  // namespace

struct HttpServer::Tls {
  explicit Tls(const TlsIdentity& identity) {
    // as audiod does: a decoder still closing its tls stream as the test process exits finds openssl intact
    OPENSSL_init_crypto(OPENSSL_INIT_NO_ATEXIT, nullptr);
    context = SSL_CTX_new(TLS_server_method());
    if (context == nullptr)
      throw std::runtime_error("tls context");
    BIO* certificate =
        BIO_new_mem_buf(identity.certificatePem.data(), static_cast<int>(identity.certificatePem.size()));
    BIO* key = BIO_new_mem_buf(identity.keyPem.data(), static_cast<int>(identity.keyPem.size()));
    X509* leaf = PEM_read_bio_X509(certificate, nullptr, nullptr, nullptr);
    EVP_PKEY* privateKey = PEM_read_bio_PrivateKey(key, nullptr, nullptr, nullptr);
    const bool loaded = leaf != nullptr && privateKey != nullptr && SSL_CTX_use_certificate(context, leaf) == 1 &&
                        SSL_CTX_use_PrivateKey(context, privateKey) == 1 && SSL_CTX_check_private_key(context) == 1;
    X509_free(leaf);
    EVP_PKEY_free(privateKey);
    BIO_free(certificate);
    BIO_free(key);
    if (!loaded)
      throw std::runtime_error("tls identity");
  }
  ~Tls() { SSL_CTX_free(context); }

  Tls(const Tls&) = delete;
  Tls& operator=(const Tls&) = delete;

  SSL_CTX* context = nullptr;
};

// one client's socket, through tls when the server has it
class HttpServer::Connection {
 public:
  Connection(int fd, SSL* ssl) : fd_(fd), ssl_(ssl) {}
  ~Connection() { SSL_free(ssl_); }

  Connection(const Connection&) = delete;
  Connection& operator=(const Connection&) = delete;

  // whether something waits to be read, in tls's own buffer or on the socket, within a poll
  bool readable() const { return (ssl_ != nullptr && SSL_pending(ssl_) > 0) || waitReadable(fd_); }
  // bytes read; 0 or less once the client is gone
  long receive(char* buffer, std::size_t size) {
    if (ssl_ == nullptr)
      return ::recv(fd_, buffer, size, 0);
    return SSL_read(ssl_, buffer, static_cast<int>(std::min<std::size_t>(size, INT_MAX)));
  }
  // false once the client is gone
  bool sendAll(std::string_view bytes) {
    while (!bytes.empty()) {
      const auto chunk = static_cast<int>(std::min<std::size_t>(bytes.size(), INT_MAX));
      const long sent = ssl_ == nullptr ? ::send(fd_, bytes.data(), bytes.size(), MSG_NOSIGNAL)
                                        : SSL_write(ssl_, bytes.data(), chunk);
      if (sent <= 0)
        return false;
      bytes.remove_prefix(static_cast<std::size_t>(sent));
    }
    return true;
  }
  // nothing more to send, then waits until the client closed its side, so it reads everything first
  void finish(const std::atomic<bool>& stopping) {
    if (ssl_ != nullptr)
      SSL_shutdown(ssl_);
    ::shutdown(fd_, SHUT_WR);
    char buffer[256];
    while (!stopping.load())
      if (waitReadable(fd_) && ::recv(fd_, buffer, sizeof buffer, 0) <= 0)
        return;
  }

 private:
  int fd_;
  SSL* ssl_;
};

HttpServer::HttpServer() { listen(); }

HttpServer::HttpServer(const TlsIdentity& identity) : tls_(std::make_unique<Tls>(identity)) {
  // openssl writes to the socket without MSG_NOSIGNAL: a client that hung up must not end the test
  std::signal(SIGPIPE, SIG_IGN);
  listen();
}

HttpServer::~HttpServer() {
  stopping_ = true;
  acceptor_.join();
  {
    std::scoped_lock lock(mutex_);
    for (const int fd : open_)
      ::shutdown(fd, SHUT_RDWR);
  }
  // no new connection after the acceptor is gone, so the list is final
  for (auto& connection : connections_)
    connection.join();
  ::close(listener_);
}

void HttpServer::listen() {
  listener_ = ::socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
  if (listener_ < 0)
    throw std::runtime_error("socket");
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  socklen_t size = sizeof address;
  if (::bind(listener_, reinterpret_cast<const sockaddr*>(&address), sizeof address) != 0 ||
      ::listen(listener_, 16) != 0 || ::getsockname(listener_, reinterpret_cast<sockaddr*>(&address), &size) != 0)
    throw std::runtime_error("listen");
  port_ = ntohs(address.sin_port);
  acceptor_ = std::thread([this] { accept(); });
}

void HttpServer::route(const std::string& path, HttpRoute route) {
  std::scoped_lock lock(mutex_);
  routes_[path] = std::move(route);
}

std::string HttpServer::url(std::string_view path) const {
  return std::string(tls_ ? "https" : "http") + "://127.0.0.1:" + std::to_string(port_) + std::string(path);
}

std::size_t HttpServer::requests(const std::string& path) const {
  std::scoped_lock lock(mutex_);
  const auto found = requests_.find(path);
  return found == requests_.end() ? 0 : found->second;
}

std::vector<std::string> HttpServer::authorizations(const std::string& path) const {
  std::scoped_lock lock(mutex_);
  const auto found = authorizations_.find(path);
  return found == authorizations_.end() ? std::vector<std::string>{} : found->second;
}

std::vector<std::string> HttpServer::ranges(const std::string& path) const {
  std::scoped_lock lock(mutex_);
  const auto found = ranges_.find(path);
  return found == ranges_.end() ? std::vector<std::string>{} : found->second;
}

std::size_t HttpServer::hangups(const std::string& path) const {
  std::scoped_lock lock(mutex_);
  const auto found = hangups_.find(path);
  return found == hangups_.end() ? 0 : found->second;
}

std::size_t HttpServer::stalled() const {
  std::scoped_lock lock(mutex_);
  return stalled_;
}

std::size_t HttpServer::handshakeFailures() const {
  std::scoped_lock lock(mutex_);
  return handshakeFailures_;
}

void HttpServer::accept() {
  while (!stopping()) {
    if (!waitReadable(listener_))
      continue;
    const int fd = ::accept4(listener_, nullptr, nullptr, SOCK_CLOEXEC);
    if (fd < 0)
      continue;
    std::scoped_lock lock(mutex_);
    open_.insert(fd);
    connections_.emplace_back([this, fd] { serve(fd); });
  }
}

void HttpServer::serve(int fd) {
  SSL* ssl = nullptr;
  bool accepted = true;
  if (tls_) {
    ssl = SSL_new(tls_->context);
    accepted = ssl != nullptr && SSL_set_fd(ssl, fd) == 1 && SSL_accept(ssl) == 1;
    if (!accepted) {
      std::scoped_lock lock(mutex_);
      ++handshakeFailures_;
    }
  }
  {
    Connection connection(fd, ssl);
    Request request;
    if (accepted && readRequest(connection, request)) {
      HttpRoute route;
      {
        std::scoped_lock lock(mutex_);
        ++requests_[request.path];
        authorizations_[request.path].push_back(request.authorization);
        if (!request.range.empty())
          ranges_[request.path].push_back(request.range);
        const auto found = routes_.find(request.path);
        route = found == routes_.end() ? HttpRoute{.status = 404, .contentType = "text/plain"} : found->second;
      }
      if (route.stall)
        stall(connection, request.path);
      else
        respond(connection, request, route);
    }
  }
  std::scoped_lock lock(mutex_);
  open_.erase(fd);
  ::close(fd);
}

bool HttpServer::readRequest(Connection& connection, Request& request) {
  std::string head;
  while (head.find("\r\n\r\n") == std::string::npos) {
    if (stopping() || head.size() > kMaxRequestBytes)
      return false;
    if (!connection.readable())
      continue;
    char buffer[1024];
    const auto got = connection.receive(buffer, sizeof buffer);
    if (got <= 0)
      return false;
    head.append(buffer, static_cast<std::size_t>(got));
  }
  const auto lineEnd = head.find("\r\n");
  const std::string_view line(head.data(), lineEnd);
  const auto first = line.find(' ');
  const auto second = line.find(' ', first + 1);
  if (first == std::string_view::npos || second == std::string_view::npos)
    return false;
  request.path = std::string(line.substr(first + 1, second - first - 1));
  for (std::size_t at = lineEnd + 2; at < head.size();) {
    const auto end = head.find("\r\n", at);
    if (end == std::string::npos || end == at)
      break;
    const std::string_view header(head.data() + at, end - at);
    const auto colon = header.find(':');
    if (colon != std::string_view::npos) {
      const auto name = lower(trim(header.substr(0, colon)));
      const auto value = trim(header.substr(colon + 1));
      if (name == "range")
        request.range = std::string(value);
      else if (name == "authorization")
        request.authorization = std::string(value);
      else if (name == "icy-metadata")
        request.icy = value == "1";
    }
    at = end + 2;
  }
  return true;
}

void HttpServer::respond(Connection& connection, const Request& request, const HttpRoute& route) {
  int status = route.status;
  std::string body = route.body;
  std::string extra;
  if (!route.location.empty())
    extra += "Location: " + route.location + "\r\n";
  if (route.ranges) {
    extra += "Accept-Ranges: bytes\r\n";
    constexpr std::string_view prefix = "bytes=";
    if (request.range.starts_with(prefix)) {
      const auto from = std::stoull(request.range.substr(prefix.size()));
      if (from >= route.body.size()) {
        status = 416;
        extra += "Content-Range: bytes */" + std::to_string(route.body.size()) + "\r\n";
        body.clear();
      } else {
        status = 206;
        extra += "Content-Range: bytes " + std::to_string(from) + "-" + std::to_string(route.body.size() - 1) + "/" +
                 std::to_string(route.body.size()) + "\r\n";
        body = route.body.substr(from);
      }
    }
  }
  if (route.icyMetaint > 0 && request.icy) {
    extra += "icy-metaint: " + std::to_string(route.icyMetaint) + "\r\n";
    body = withIcy(body, route.icyMetaint, route.icyTitles);
  }
  const auto head = "HTTP/1.1 " + std::to_string(status) + " " + reason(status) + "\r\nContent-Type: " +
                    route.contentType + "\r\nContent-Length: " + std::to_string(body.size()) +
                    "\r\nConnection: close\r\n" + extra + "\r\n";
  if (connection.sendAll(head))
    connection.sendAll(body);
  connection.finish(stopping_);
}

void HttpServer::stall(Connection& connection, const std::string& path) {
  {
    std::scoped_lock lock(mutex_);
    ++stalled_;
  }
  char buffer[256];
  bool hungUp = false;
  while (!stopping()) {
    if (connection.readable() && connection.receive(buffer, sizeof buffer) <= 0) {
      hungUp = true;
      break;
    }
  }
  std::scoped_lock lock(mutex_);
  --stalled_;
  if (hungUp)
    ++hangups_[path];
}

}  // namespace cadence::test_support
