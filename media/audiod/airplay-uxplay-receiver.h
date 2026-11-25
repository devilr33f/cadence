#pragma once

#include "audiod/airplay-uxplay-events.h"
#include "audiod/log.h"

#include <array>
#include <cstdint>
#include <memory>
#include <optional>
#include <string>
#include <string_view>

extern "C" {
typedef struct cadence_dnssd_backend cadence_dnssd_backend_t;
// the embedder's hook (android: the app's jni sets cadence_android_dnssd_backend() before audiod runs); the backend
// must outlive every receiver
void cadence_audiod_airplay_dnssd(const cadence_dnssd_backend_t* backend);
}

namespace cadence::audiod::uxplay {

// what the receiver advertises and offers
struct ReceiverSettings {
  // the advertised name; the raop instance is "<hw address hex>@<name>"
  std::string name;
  // 6 bytes, the receiver's id: the deviceid record and pairing's identity
  std::array<std::uint8_t, 6> hwAddress{};
  // the ed25519 key libairplay makes once and keeps (mode 0600 under the scratch dir)
  std::string keyFile;
  // 0 = an ephemeral port (tests), else the rtsp port; airplay's default is 7000
  std::uint16_t port = 7000;
  bool mirroring = true;
  // the largest mirror size offered (/info's displays); 0 = 1920x1080
  std::uint32_t maxWidth = 0;
  std::uint32_t maxHeight = 0;
  std::uint32_t maxFps = 60;
  bool h265 = false;
  // a sender must enter a pin shown on screen (pair-pin-start); a sender that paired once is known by its key
  bool requirePin = false;
  // hls casts (an app sending a url): advertised only when set
  bool hls = false;
};

// the dnssd backend the receiver advertises through: avahi on linux (when built with it), android's NsdManager bridge
// (the app sets it with setDnssdBackend before audiod runs), else none (nothing advertised; /info still answers)
void setDnssdBackend(const cadence_dnssd_backend_t* backend);
[[nodiscard]] const cadence_dnssd_backend_t* dnssdBackend();

// libairplay started with every raop callback set (a NULL one crashes /info), in the order it needs: dnssd_init,
// raop_init, raop_init2, raop_start_httpd, raop_set_dnssd, dnssd_register_raop / _airplay. every callback goes to
// events on libairplay's own threads; events must outlive the receiver. stop (and the destructor) withdraws the
// records, stops the httpd and joins libairplay's threads, so no callback runs after it returns. last to connect wins
// (nohold): a second sender displaces the first
class Receiver {
 public:
  // nullptr, logged, when libairplay will not start (a bad name, the key file, the port)
  static std::unique_ptr<Receiver> start(const Log& log, const ReceiverSettings& settings, Events& events);
  ~Receiver();

  Receiver(const Receiver&) = delete;
  Receiver& operator=(const Receiver&) = delete;

  [[nodiscard]] std::uint16_t port() const { return port_; }
  // drops every sender's connection (displaced, stopped): the sender sees the receiver go
  void disconnect();
  // the records as advertised (rfc 6763 wire format), for the tests
  [[nodiscard]] std::string raopTxt() const;
  [[nodiscard]] std::string airplayTxt() const;

  struct Impl;

 private:
  explicit Receiver(std::unique_ptr<Impl> impl, std::uint16_t port);

  std::unique_ptr<Impl> impl_;
  std::uint16_t port_ = 0;
};

}  // namespace cadence::audiod::uxplay
