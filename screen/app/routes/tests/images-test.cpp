// the image cache: decoded at the slot's size off the ui thread, least recently used out within the budget, never
// what is shown; the binder putting it on elements; the plain-http fetch from the server's image proxy
#include "app/routes/preview/fake-catalog.h"
#include "app/routes/tests/rig.h"
#include "ui/images/binder.h"
#include "ui/images/http-fetcher.h"
#include "ui/images/image-cache.h"

#include <catch2/catch_test_macros.hpp>

#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>

#include "ipc/log.h"

#include <atomic>
#include <chrono>
#include <mutex>
#include <thread>

using namespace cadence::screen;
using namespace cadence::screen::ui::images;
using cadence::screen::ui::Pixels;
using cadence::screen::routes::RouteArgs;

namespace {

std::string png(int seed) {
  routes::fake::FakeImages f;
  std::string bytes, code;
  REQUIRE(f.fetch(routes::fake::imageId(seed), bytes, code));
  return bytes;
}

struct Counting final : Fetcher {
  std::atomic<int> calls{0};
  bool fetch(const std::string& id, std::string& bytes, std::string& code) override {
    calls++;
    routes::fake::FakeImages f;
    return f.fetch(id, bytes, code);
  }
};

template <class F>
bool eventually(F f, int ms = 5000) {
  const auto until = std::chrono::steady_clock::now() + std::chrono::milliseconds(ms);
  while (std::chrono::steady_clock::now() < until) {
    if (f()) return true;
    std::this_thread::sleep_for(std::chrono::milliseconds(5));
  }
  return false;
}

// one connection per request, answering what respond() returns for the request line
struct TinyServer {
  int fd = -1;
  int port = 0;
  std::thread t;
  std::atomic<bool> stop{false};
  std::function<std::string(const std::string&)> respond;
  std::mutex mu;
  // the last request, headers and all
  std::string last;

  explicit TinyServer(std::function<std::string(const std::string&)> r) : respond(std::move(r)) {
    fd = ::socket(AF_INET, SOCK_STREAM, 0);
    sockaddr_in a{};
    a.sin_family = AF_INET;
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    REQUIRE(::bind(fd, reinterpret_cast<sockaddr*>(&a), sizeof a) == 0);
    socklen_t len = sizeof a;
    ::getsockname(fd, reinterpret_cast<sockaddr*>(&a), &len);
    port = ntohs(a.sin_port);
    ::listen(fd, 4);
    t = std::thread([this] {
      while (!stop) {
        const int c = ::accept(fd, nullptr, nullptr);
        if (c < 0) return;
        char buf[4096];
        const auto n = ::recv(c, buf, sizeof buf, 0);
        const std::string req(buf, n > 0 ? static_cast<std::size_t>(n) : 0);
        {
          std::lock_guard lock(mu);
          last = req;
        }
        const auto out = respond(req.substr(0, req.find("\r\n")));
        ::send(c, out.data(), out.size(), 0);
        ::close(c);
      }
    });
  }
  ~TinyServer() {
    stop = true;
    ::shutdown(fd, SHUT_RDWR);
    ::close(fd);
    t.join();
  }
};

}  // namespace

TEST_CASE("images decode to cover the slot, never above their own size, premultiplied") {
  Pixels px;
  REQUIRE(decodeToCover(png(3), 134, 201, px));
  REQUIRE(px.width == 134);
  REQUIRE(px.height == 201);
  // a slot larger than the image: decoded at its own size, the decorator scales it
  REQUIRE(decodeToCover(png(3), 1000, 1000, px));
  REQUIRE(px.width == 268);
  // cover, not fit: a wide slot takes the full width
  REQUIRE(decodeToCover(png(3), 268, 100, px));
  REQUIRE(px.width == 268);
  REQUIRE(px.height == 402);
  REQUIRE_FALSE(decodeToCover("not an image", 10, 10, px));
  REQUIRE(validId(routes::fake::imageId(1)));
  REQUIRE_FALSE(validId("../../etc/passwd"));
}

TEST_CASE("the cache decodes off the ui thread, keeps one copy per size, and evicts the least recently used") {
  auto fetcher = std::make_shared<Counting>();
  CacheOptions o;
  // room for three 136 x 208 images (snapped), not four
  o.budgetBytes = 136 * 208 * 4 * 3 + 100;
  ImageCache cache(fetcher, o);
  std::vector<std::string> evicted;
  cache.onEvict([&](const std::string& k) { evicted.push_back(k); });
  std::string k1;
  REQUIRE(cache.request(routes::fake::imageId(1), 134, 201, &k1) == State::Pending);
  REQUIRE(eventually([&] {
    cache.poll();
    return cache.state(k1) == State::Ready;
  }));
  // a second ask is a hit, not a fetch
  cache.request(routes::fake::imageId(1), 134, 201);
  REQUIRE(fetcher->calls == 1);
  Pixels px;
  REQUIRE(cache.take(k1, px));
  REQUIRE(cache.state(k1) == State::Resident);
  // shown: pinned
  cache.pin(k1);
  std::vector<std::string> keys;
  for (int i = 2; i <= 5; ++i) {
    std::string k;
    cache.request(routes::fake::imageId(i), 134, 201, &k);
    keys.push_back(k);
    REQUIRE(eventually([&] {
      cache.poll();
      return cache.state(k) != State::Pending;
    }));
    cache.take(k, px);
  }
  REQUIRE(cache.stats().bytes <= o.budgetBytes);
  // the pinned one stays; the oldest unpinned went, its texture released
  REQUIRE(cache.state(k1) == State::Resident);
  REQUIRE(cache.state(keys[0]) == State::Missing);
  REQUIRE(std::find(evicted.begin(), evicted.end(), keys[0]) != evicted.end());
  REQUIRE(cache.stats().evicted >= 2);
  // a failed fetch is remembered until the server is back
  std::string bad;
  REQUIRE(cache.request("unknownunknownunknown123", 10, 10, &bad) == State::Pending);
  REQUIRE(eventually([&] {
    cache.poll();
    return cache.state(bad) == State::Failed;
  }));
  cache.forgetFailures();
  REQUIRE(cache.state(bad) == State::Missing);
}

TEST_CASE("the binder puts a decoded image on its element at the element's size, and pins it while shown") {
  routes::test::Rig r;
  RouteArgs a;
  a.ref = "ref-tg-1";
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, a);
  r.h.step(500);
  // textures load when a frame is drawn
  r.h.capture();
  auto* poster = r.ui().page()->QuerySelector(".tposter .netimg");
  REQUIRE(poster != nullptr);
  const auto key = poster->GetAttribute<Rml::String>("data-img-key", "");
  // 360 x 509 dp at half scale, snapped to 8 px
  REQUIRE(key == routes::fake::imageId(100) + "@184x256");
  REQUIRE(r.s->images->state(key) == State::Resident);
  REQUIRE(r.ui().renderer()->stats().loadFailures == 0);
}

TEST_CASE("the http fetcher reads the image proxy: content-length, chunked, 404, refusals") {
  const std::string body = "\x89PNG-fake-bytes";
  TinyServer srv([&](const std::string& line) -> std::string {
    if (line.find("/v2/images/aaaaaaaaaaaaaaaaaaaaaaaa") != std::string::npos) {
      return "HTTP/1.1 200 OK\r\nContent-Type: image/png\r\nContent-Length: " + std::to_string(body.size()) + "\r\n\r\n" + body;
    }
    if (line.find("/v2/images/dddddddddddddddddddddddd") != std::string::npos) {
      return "HTTP/1.1 406 Not Acceptable\r\nContent-Length: 0\r\n\r\n";
    }
    if (line.find("/v2/images/bbbbbbbbbbbbbbbbbbbbbbbb") != std::string::npos) {
      return "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n4\r\n\x89PNG\r\n3\r\nabc\r\n0\r\n\r\n";
    }
    return "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n";
  });
  HttpFetcher f("http://127.0.0.1:" + std::to_string(srv.port) + "/some/path");
  std::string bytes, code;
  REQUIRE(f.fetch("aaaaaaaaaaaaaaaaaaaaaaaa", bytes, code));
  REQUIRE(bytes == body);
  {
    // the decoder's types alone: the server's proxy converts a webp or a gif
    std::lock_guard lock(srv.mu);
    CHECK(srv.last.find("\r\nAccept: image/jpeg, image/png\r\n") != std::string::npos);
  }
  REQUIRE_FALSE(f.fetch("dddddddddddddddddddddddd", bytes, code));
  CHECK(code == "not_acceptable");
  REQUIRE(f.fetch("bbbbbbbbbbbbbbbbbbbbbbbb", bytes, code));
  REQUIRE(bytes == "\x89PNGabc");
  REQUIRE_FALSE(f.fetch("cccccccccccccccccccccccc", bytes, code));
  REQUIRE(code == "not_found");
  // an id that is not one never leaves the screen
  REQUIRE_FALSE(f.fetch("../x", bytes, code));
  REQUIRE(code == "invalid");
  HttpFetcher none;
  REQUIRE_FALSE(none.fetch("aaaaaaaaaaaaaaaaaaaaaaaa", bytes, code));
  REQUIRE(code == "no_server");
  Url u;
  REQUIRE(parseUrl("https://tv.example:8443/base/", u));
  REQUIRE(u.host == "tv.example");
  REQUIRE(u.port == "8443");
  REQUIRE(u.path == "/base");
  REQUIRE_FALSE(parseUrl("ftp://x", u));
  REQUIRE_FALSE(parseUrl("http://user@host", u));
}

TEST_CASE("https goes through the platform's transport; the server falls back until the link names one") {
  std::vector<std::string> asked;
  Transport https = [&](const std::string& url, std::size_t maxBytes, int timeoutMs, std::string& body,
                        std::string& code) {
    asked.push_back(url);
    CHECK(maxBytes == (4u << 20));
    CHECK(timeoutMs == 10000);
    if (url.ends_with("/v2/images/cccccccccccccccccccccccc")) {
      code = "not_found";
      return false;
    }
    body = "\x89PNG";
    return true;
  };
  int fallbacks = 0;
  std::string configured;
  HttpFetcher f({}, https, [&] {
    fallbacks++;
    return configured;
  });
  std::string bytes, code;
  // nothing enrolled yet: no config, no server
  REQUIRE_FALSE(f.fetch("aaaaaaaaaaaaaaaaaaaaaaaa", bytes, code));
  CHECK(code == "no_server");
  CHECK(asked.empty());
  // enroll wrote the config: its url's origin, asked again at each fetch
  configured = "https://cadence.example/some/path";
  REQUIRE(f.fetch("aaaaaaaaaaaaaaaaaaaaaaaa", bytes, code));
  CHECK(bytes == "\x89PNG");
  REQUIRE(asked.size() == 1);
  CHECK(asked[0] == "https://cadence.example/v2/images/aaaaaaaaaaaaaaaaaaaaaaaa");
  REQUIRE_FALSE(f.fetch("cccccccccccccccccccccccc", bytes, code));
  CHECK(code == "not_found");
  CHECK(fallbacks == 3);
  // the link's url wins over the config, and the fallback is not asked again
  f.setServer("wss://tv.example:8443/v2/device");
  REQUIRE(f.fetch("bbbbbbbbbbbbbbbbbbbbbbbb", bytes, code));
  CHECK(asked.back() == "https://tv.example:8443/v2/images/bbbbbbbbbbbbbbbbbbbbbbbb");
  CHECK(fallbacks == 3);
  // an invalid id never reaches the transport
  const auto before = asked.size();
  REQUIRE_FALSE(f.fetch("../x", bytes, code));
  CHECK(asked.size() == before);
}

TEST_CASE("plain http stays the screen's own client with a transport set") {
  const std::string body = "\x89PNG-plain";
  TinyServer srv([&](const std::string&) -> std::string {
    return "HTTP/1.1 200 OK\r\nContent-Length: " + std::to_string(body.size()) + "\r\n\r\n" + body;
  });
  bool used = false;
  HttpFetcher f("ws://127.0.0.1:" + std::to_string(srv.port) + "/v2/device",
                [&](const std::string&, std::size_t, int, std::string&, std::string&) { return used = true; });
  std::string bytes, code;
  REQUIRE(f.fetch("aaaaaaaaaaaaaaaaaaaaaaaa", bytes, code));
  CHECK(bytes == body);
  CHECK_FALSE(used);
  CHECK(HttpFetcher::originOf("ftp://x/y").empty());
  CHECK(HttpFetcher::originOf("nothing").empty());
}

namespace {

// answers a fixed body for every id
struct Fixed final : Fetcher {
  std::string body;
  std::string fail;
  bool fetch(const std::string&, std::string& bytes, std::string& code) override {
    if (!fail.empty()) {
      code = fail;
      return false;
    }
    bytes = body;
    return true;
  }
};

}  // namespace

TEST_CASE("an image that fails to fetch or decode is logged by its code, never its id, once a minute") {
  using cadence::screen::ipc::Log;
  using cadence::screen::ipc::LogRecord;
  std::vector<LogRecord> got;
  Log::get().setSink([&](const LogRecord& r) { got.push_back(r); });
  auto fetcher = std::make_shared<Fixed>();
  // a webp the server did not convert (an old proxy): the decoder takes png and jpeg alone
  fetcher->body = std::string("RIFF\x10\x00\x00\x00WEBPVP8 ", 16) + std::string(8, '\0');
  CacheOptions o;
  o.inlineDecode = true;
  ImageCache cache(fetcher, o);
  const auto a = routes::fake::imageId(1);
  const auto b = routes::fake::imageId(2);
  CHECK(cache.request(a, 100, 150) == State::Failed);
  // the same failure again within the minute is held back
  CHECK(cache.request(b, 100, 150) == State::Failed);
  fetcher->body.clear();
  fetcher->fail = "unavailable";
  CHECK(cache.request(routes::fake::imageId(3), 100, 150) == State::Failed);
  Log::get().setSink(nullptr);
  std::vector<std::string> lines;
  for (const auto& r : got) {
    if (r.text.starts_with("image_")) lines.push_back(r.text);
  }
  REQUIRE(lines.size() == 2);
  CHECK(lines[0] == "image_decode_failed code=webp bytes=24 held=0");
  CHECK(lines[1] == "image_fetch_failed code=unavailable bytes=0 held=0");
  for (const auto& l : lines) {
    CHECK(l.find(a) == std::string::npos);
    CHECK(l.find(b) == std::string::npos);
  }
  CHECK(formatOf("\x89PNG\r\n") == "png");
  CHECK(formatOf("\xff\xd8\xff\xe0") == "jpeg");
  CHECK(formatOf("GIF89a") == "gif");
  CHECK(formatOf(std::string("\0\0\0\x1c" "ftypavif", 12)) == "avif");
  CHECK(formatOf("<html>") == "unknown");
}
