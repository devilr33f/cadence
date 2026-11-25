#pragma once

#include "ui/images/image-cache.h"

#include <cstddef>
#include <functional>
#include <mutex>
#include <string>

namespace cadence::screen::ui::images {

// what the image fetch accepts: the decoder's types alone, so the server's proxy converts a webp or a gif
// (android's ImageFetch sends the same)
inline constexpr const char* kAccept = "image/jpeg, image/png";

// a platform's own https (android: HttpURLConnection through jni, so the system's CA store and its updates decide
// trust): GET the full url with kAccept, the body of a 200 at most maxBytes, else false with a token (not_found,
// not_acceptable, unavailable, tls, timeout, too_large, ...). called on the cache's workers
using Transport = std::function<bool(const std::string& url, std::size_t maxBytes, int timeoutMs, std::string& body,
                                     std::string& code)>;

// GET <server>/v2/images/<id> from the server's image proxy (no credentials: the id is the capability). the server's
// address is LinkStatus.server_url (setServer); until cadenced has sent one, fallback is asked at each fetch (android:
// the serverUrl of cadenced's config.json, which enroll writes). plain http is spoken here (a LAN server, the dev
// stack); https goes through the platform's transport when there is one, else libcurl when built with it
// (CADENCE_SCREEN_CURL), else it answers tls_unsupported. 10 s per request, 4 MiB at most. nothing about a request is
// logged (ids are capabilities, rule 8)
class HttpFetcher final : public Fetcher {
 public:
  explicit HttpFetcher(std::string serverUrl = {}, Transport https = {}, std::function<std::string()> fallback = {})
      : base_(originOf(std::move(serverUrl))), https_(std::move(https)), fallback_(std::move(fallback)) {}

  // a link url (ws, wss, http, https, any path): its origin is the proxy's
  void setServer(std::string serverUrl);
  bool fetch(const std::string& id, std::string& bytes, std::string& code) override;

  // the origin of a link url: wss becomes https and ws http, the path dropped; empty for anything else
  static std::string originOf(std::string url);

 private:
  std::mutex mu_;
  std::string base_;
  const Transport https_;
  const std::function<std::string()> fallback_;
};

struct Url {
  std::string scheme;
  std::string host;
  std::string port;
  // the path prefix, without its trailing slash
  std::string path;
};
// http(s)://host[:port][/prefix]; false for anything else
bool parseUrl(const std::string& url, Url& out);

// one plain-http GET over tcp: the body of a 200 (content-length or chunked), else false with a token
bool httpGet(const Url& url, const std::string& path, std::size_t maxBytes, int timeoutMs, std::string& body,
             std::string& code);

}  // namespace cadence::screen::ui::images
