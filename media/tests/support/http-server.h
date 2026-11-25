#pragma once

#include <atomic>
#include <cstddef>
#include <cstdint>
#include <map>
#include <memory>
#include <mutex>
#include <set>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

namespace cadence::test_support {

// what the server answers on one path
struct HttpRoute {
  int status = 200;
  std::string contentType = "application/octet-stream";
  std::string body{};
  // sent as Location when not empty
  std::string location{};
  // Accept-Ranges: bytes, and a "bytes=<first>-" request gets 206 with the rest
  bool ranges = false;
  // reads the request, then says nothing until the client hangs up or the server stops
  bool stall = false;
  // above 0, and when the client asks for icy metadata: a metadata block after every icyMetaint bytes of the body,
  // block n carrying StreamTitle='icyTitles[n]'; and an empty one where the map has none
  std::size_t icyMetaint = 0;
  std::map<std::size_t, std::string> icyTitles{};
};

// the certificate chain (leaf first) and private key an https server presents, in pem
struct TlsIdentity {
  std::string certificatePem;
  std::string keyPem;
};

// a minimal http/1.1 server on 127.0.0.1 and a port of its own, one thread per connection, one request each, or https
// with a tls identity. every response but a stall carries Content-Length and closes the connection. destroying it
// hangs up on every client
class HttpServer {
 public:
  HttpServer();
  explicit HttpServer(const TlsIdentity& identity);
  ~HttpServer();

  HttpServer(const HttpServer&) = delete;
  HttpServer& operator=(const HttpServer&) = delete;

  // before a client asks for it
  void route(const std::string& path, HttpRoute route);
  [[nodiscard]] std::string url(std::string_view path) const;

  // requests for the path, and the Range header of each that had one
  [[nodiscard]] std::size_t requests(const std::string& path) const;
  [[nodiscard]] std::vector<std::string> ranges(const std::string& path) const;
  // the Authorization header of each request for the path, "" for one without
  [[nodiscard]] std::vector<std::string> authorizations(const std::string& path) const;
  // connections the client closed while the server stalled them
  [[nodiscard]] std::size_t hangups(const std::string& path) const;
  // stalls under way
  [[nodiscard]] std::size_t stalled() const;
  // tls handshakes that failed, as when the client refused the certificate
  [[nodiscard]] std::size_t handshakeFailures() const;

 private:
  struct Tls;
  class Connection;
  struct Request {
    std::string path;
    std::string range;
    std::string authorization;
    bool icy = false;
  };

  void listen();
  void accept();
  void serve(int fd);
  // false when the client went or the server stops
  bool readRequest(Connection& connection, Request& request);
  void respond(Connection& connection, const Request& request, const HttpRoute& route);
  void stall(Connection& connection, const std::string& path);
  [[nodiscard]] bool stopping() const { return stopping_.load(); }

  std::unique_ptr<Tls> tls_;
  int listener_ = -1;
  std::uint16_t port_ = 0;
  std::atomic<bool> stopping_{false};
  std::thread acceptor_;

  mutable std::mutex mutex_;
  std::map<std::string, HttpRoute> routes_;
  std::map<std::string, std::size_t> requests_;
  std::map<std::string, std::vector<std::string>> ranges_;
  std::map<std::string, std::vector<std::string>> authorizations_;
  std::map<std::string, std::size_t> hangups_;
  std::size_t stalled_ = 0;
  std::size_t handshakeFailures_ = 0;
  std::set<int> open_;
  std::vector<std::thread> connections_;
};

}  // namespace cadence::test_support
