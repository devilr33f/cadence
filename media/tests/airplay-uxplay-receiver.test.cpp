// libairplay itself, started as audiod starts it: a sender's rtsp over loopback (GET /info, POST /pair-pin-start) and
// the dnssd records it advertises through a fake backend. no fairplay or pairing past the pin: those need a real
// apple sender (the owner-attended steps)
#include "audiod/airplay-uxplay-receiver.h"

extern "C" {
#include "cadence_dnssd.h"
}

#include <catch2/catch_test_macros.hpp>
#include <plist/plist.h>

#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <unistd.h>

#include <atomic>
#include <chrono>
#include <filesystem>
#include <map>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

using namespace cadence::audiod;
using namespace cadence::audiod::uxplay;
using namespace std::chrono_literals;

namespace {

class Scratch {
 public:
  Scratch() {
    static std::atomic<int> counter{0};
    path_ = std::filesystem::temp_directory_path() /
            ("cadence-uxplay-rx-" + std::to_string(::getpid()) + "-" + std::to_string(counter++));
    std::filesystem::create_directories(path_);
  }
  ~Scratch() {
    std::error_code ignored;
    std::filesystem::remove_all(path_, ignored);
  }
  [[nodiscard]] std::string file(const std::string& name) const { return (path_ / name).string(); }

 private:
  std::filesystem::path path_;
};

// what libairplay called, from its own threads
class Recorder final : public Events {
 public:
  void connectionOpened() override { ++opened; }
  void connectionClosed() override { ++closed; }
  void connectionReset(int /*reason*/) override {}
  void client(std::string_view, std::string_view, std::string_view, bool& admit) override { admit = true; }
  void audioFormat(AudioCodec, std::uint16_t, bool, bool) override {}
  void audio(const AudioPacket&) override {}
  void audioFlush() override {}
  void volume(float) override {}
  double clientVolume() override { return -12.0; }
  void metadata(std::span<const std::uint8_t>) override {}
  void cover(std::span<const std::uint8_t>) override {}
  void progress(std::uint32_t, std::uint32_t, std::uint32_t) override {}
  void mirrorRunning(bool) override {}
  bool videoCodec(bool) override { return true; }
  void videoSize(float, float) override {}
  void video(const VideoPacket&) override {}
  void videoFlush() override {}
  void pin(std::string_view value) override {
    std::scoped_lock lock(mutex);
    pins.emplace_back(value);
  }
  void registered(std::string_view, std::string_view) override {}
  bool known(std::string_view) override { return false; }
  void hlsPlay(std::string_view, float) override {}
  void hlsScrub(float) override {}
  void hlsRate(float) override {}
  void hlsStop() override {}
  HlsInfo hlsInfo() override { return {}; }

  std::atomic<int> opened{0};
  std::atomic<int> closed{0};
  std::mutex mutex;
  std::vector<std::string> pins;
};

// the backend audiod hands libairplay: what it was asked to advertise
struct FakeBackend {
  std::mutex mutex;
  std::map<std::string, std::pair<std::uint16_t, std::string>> services;
  std::vector<std::string> withdrawn;
  cadence_dnssd_backend_t backend{};

  FakeBackend() {
    backend.ctx = this;
    backend.register_service = [](void* ctx, const char* type, const char* name, uint16_t port,
                                  const unsigned char* txt, size_t length) {
      auto* self = static_cast<FakeBackend*>(ctx);
      std::scoped_lock lock(self->mutex);
      self->services[type] = {port, std::string(name) + "|" + std::string(reinterpret_cast<const char*>(txt), length)};
      return 0;
    };
    backend.unregister_service = [](void* ctx, const char* type) {
      auto* self = static_cast<FakeBackend*>(ctx);
      std::scoped_lock lock(self->mutex);
      self->withdrawn.emplace_back(type);
    };
  }
};

std::map<std::string, std::string> records(const std::string& txt) {
  std::map<std::string, std::string> out;
  std::size_t offset = 0;
  char key[64];
  const char* value = nullptr;
  std::size_t length = 0;
  while (cadence_dnssd_txt_next(reinterpret_cast<const unsigned char*>(txt.data()), txt.size(), &offset, key,
                                sizeof key, &value, &length) == 1)
    out[key] = value != nullptr ? std::string(value, length) : std::string();
  return out;
}

// one rtsp exchange on a fresh connection: the response's status line, headers and body
struct Response {
  int status = 0;
  std::string headers;
  std::string body;
};

Response rtsp(std::uint16_t port, const std::string& request) {
  const int fd = ::socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
  REQUIRE(fd >= 0);
  timeval timeout{2, 0};
  ::setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof timeout);
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_port = htons(port);
  address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  REQUIRE(::connect(fd, reinterpret_cast<sockaddr*>(&address), sizeof address) == 0);
  REQUIRE(::send(fd, request.data(), request.size(), MSG_NOSIGNAL) == static_cast<ssize_t>(request.size()));
  std::string got;
  char buffer[4096];
  Response out;
  for (;;) {
    const auto size = ::recv(fd, buffer, sizeof buffer, 0);
    if (size <= 0)
      break;
    got.append(buffer, static_cast<std::size_t>(size));
    const auto end = got.find("\r\n\r\n");
    if (end == std::string::npos)
      continue;
    out.headers = got.substr(0, end);
    std::size_t length = 0;
    if (const auto at = out.headers.find("Content-Length: "); at != std::string::npos)
      length = std::stoul(out.headers.substr(at + 16));
    if (got.size() >= end + 4 + length) {
      out.body = got.substr(end + 4, length);
      break;
    }
  }
  ::close(fd);
  if (out.headers.size() > 12)
    out.status = std::stoi(out.headers.substr(9, 3));
  return out;
}

ReceiverSettings settings(const Scratch& scratch, bool pin) {
  ReceiverSettings out;
  out.name = "Living Room TV";
  out.hwAddress = {0x02, 0x11, 0x22, 0x33, 0x44, 0x55};
  out.keyFile = scratch.file("airplay.pem");
  out.port = 0;
  out.mirroring = true;
  out.maxWidth = 1280;
  out.maxHeight = 720;
  out.requirePin = pin;
  return out;
}

}  // namespace

TEST_CASE("the receiver advertises raop and airplay with the records a sender needs") {
  Scratch scratch;
  FakeBackend fake;
  setDnssdBackend(&fake.backend);
  Recorder events;
  const Log log([](std::string_view) {});
  {
    auto receiver = Receiver::start(log, settings(scratch, true), events);
    REQUIRE(receiver);
    REQUIRE(receiver->port() != 0);
    std::scoped_lock lock(fake.mutex);
    REQUIRE(fake.services.count("_raop._tcp") == 1);
    REQUIRE(fake.services.count("_airplay._tcp") == 1);
    const auto& [raopPort, raop] = fake.services["_raop._tcp"];
    const auto& [airplayPort, airplay] = fake.services["_airplay._tcp"];
    CHECK(raopPort == receiver->port());
    CHECK(airplayPort == receiver->port());
    CHECK(raop.starts_with("021122334455@Living Room TV|"));
    CHECK(airplay.starts_with("Living Room TV|"));
    const auto a = records(receiver->airplayTxt());
    CHECK(a.at("deviceid") == "02:11:22:33:44:55");
    CHECK(a.at("pw") == "true");
    CHECK(a.at("pk").size() == 64);
    // mirroring (bit 7) on, hls (bits 0 and 4) and h265 (bit 42) off
    const auto features = a.at("features");
    const auto comma = features.find(',');
    const auto low = std::stoul(features.substr(0, comma), nullptr, 16);
    const auto high = std::stoul(features.substr(comma + 1), nullptr, 16);
    CHECK((low & (1U << 7)) != 0);
    CHECK((low & 1U) == 0);
    CHECK((low & (1U << 4)) == 0);
    CHECK((high & (1U << 10)) == 0);
    const auto r = records(receiver->raopTxt());
    CHECK(r.at("pw") == "true");
    CHECK(r.at("pk") == a.at("pk"));
  }
  // stopping withdraws both
  std::scoped_lock lock(fake.mutex);
  CHECK(fake.withdrawn.size() == 2);
  setDnssdBackend(nullptr);
}

TEST_CASE("without mirroring or a pin the records say so, and h265 is offered only when asked") {
  Scratch scratch;
  Recorder events;
  const Log log([](std::string_view) {});
  auto plain = settings(scratch, false);
  plain.mirroring = false;
  auto receiver = Receiver::start(log, plain, events);
  REQUIRE(receiver);
  const auto a = records(receiver->airplayTxt());
  CHECK(a.at("pw") == "false");
  CHECK((std::stoul(a.at("features").substr(0, a.at("features").find(',')), nullptr, 16) & (1U << 7)) == 0);
  receiver.reset();
  auto hevc = settings(scratch, false);
  hevc.h265 = true;
  receiver = Receiver::start(log, hevc, events);
  REQUIRE(receiver);
  const auto features = records(receiver->airplayTxt()).at("features");
  CHECK((std::stoul(features.substr(features.find(',') + 1), nullptr, 16) & (1U << 10)) != 0);
}

TEST_CASE("a sender's GET /info is answered with the receiver's plist: its name, key, size and the initial volume") {
  Scratch scratch;
  Recorder events;
  const Log log([](std::string_view) {});
  auto receiver = Receiver::start(log, settings(scratch, false), events);
  REQUIRE(receiver);
  const auto response = rtsp(receiver->port(), "GET /info RTSP/1.0\r\nCSeq: 1\r\n\r\n");
  REQUIRE(response.status == 200);
  CHECK(response.headers.find("application/x-apple-binary-plist") != std::string::npos);
  plist_t root = nullptr;
  plist_from_bin(response.body.data(), static_cast<uint32_t>(response.body.size()), &root);
  REQUIRE(root != nullptr);
  const auto text = [&](const char* key) {
    char* value = nullptr;
    if (auto* node = plist_dict_get_item(root, key))
      plist_get_string_val(node, &value);
    std::string out = value != nullptr ? value : "";
    std::free(value);
    return out;
  };
  CHECK(text("name") == "Living Room TV");
  CHECK(text("deviceID") == "02:11:22:33:44:55");
  // the ed25519 public key, as bytes
  char* pk = nullptr;
  std::uint64_t pkLength = 0;
  plist_get_data_val(plist_dict_get_item(root, "pk"), &pk, &pkLength);
  CHECK(pkLength == 32);
  std::free(pk);
  double volume = 1;
  plist_get_real_val(plist_dict_get_item(root, "initialVolume"), &volume);
  CHECK(volume == -12.0);
  auto* displays = plist_dict_get_item(root, "displays");
  REQUIRE(displays != nullptr);
  auto* display = plist_array_get_item(displays, 0);
  REQUIRE(display != nullptr);
  std::uint64_t width = 0;
  plist_get_uint_val(plist_dict_get_item(display, "widthPixels"), &width);
  CHECK(width == 1280);
  plist_free(root);
  CHECK(events.opened.load() >= 1);
  // the key is libairplay's, kept under the scratch: the same pk on the next start
  const auto before = records(receiver->airplayTxt()).at("pk");
  receiver.reset();
  receiver = Receiver::start(log, settings(scratch, false), events);
  REQUIRE(receiver);
  CHECK(records(receiver->airplayTxt()).at("pk") == before);
  CHECK(std::filesystem::exists(scratch.file("airplay.pem")));
}

TEST_CASE("pair-pin-start asks the screen to show a 4-digit pin") {
  Scratch scratch;
  Recorder events;
  const Log log([](std::string_view) {});
  auto receiver = Receiver::start(log, settings(scratch, true), events);
  REQUIRE(receiver);
  const auto response = rtsp(receiver->port(), "POST /pair-pin-start RTSP/1.0\r\nCSeq: 2\r\nContent-Length: 0\r\n\r\n");
  CHECK(response.status == 200);
  std::scoped_lock lock(events.mutex);
  REQUIRE(events.pins.size() == 1);
  CHECK(events.pins[0].size() == 4);
  CHECK(events.pins[0].find_first_not_of("0123456789") == std::string::npos);
}

TEST_CASE("a receiver that cannot start says so instead of running half") {
  Scratch scratch;
  Recorder events;
  const Log log([](std::string_view) {});
  auto bad = settings(scratch, false);
  bad.name.clear();
  CHECK_FALSE(Receiver::start(log, bad, events));
  // the port is taken
  auto first = Receiver::start(log, settings(scratch, false), events);
  REQUIRE(first);
  auto taken = settings(scratch, false);
  taken.port = first->port();
  CHECK_FALSE(Receiver::start(log, taken, events));
}
