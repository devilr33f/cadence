#include "ipc/client.h"
#include "support/env.h"
#include "support/fake-cadenced.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <thread>
#include <unistd.h>

using namespace cadence::screen;
using cadence::screen::test::FakeCadenced;

namespace {

ipc::OutEnvelope hello() {
  ipc::OutEnvelope e(cadence_screen_v1_Envelope_hello_tag);
  e->body.hello.protocol = 1;
  ipc::copyField(e->body.hello.version, "test");
  return e;
}

// drains the client until an event of that kind, up to timeoutMs
std::optional<ipc::ScreenClient::Event> waitFor(ipc::ScreenClient& c, ipc::ScreenClient::Event::Kind kind,
                                                int timeoutMs) {
  const auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(timeoutMs);
  while (std::chrono::steady_clock::now() < deadline) {
    for (auto& e : c.drain()) {
      if (e.kind == kind) return std::move(e);
    }
    std::this_thread::sleep_for(std::chrono::milliseconds(5));
  }
  return std::nullopt;
}

void linkAndReconnect(const std::string& path) {
  FakeCadenced server(path);
  ipc::ClientConfig config;
  config.path = path;
  config.backoff = ipc::Backoff(20, 100, 10000);
  config.hello = hello;
  config.pollMs = 20;
  int wakes = 0;
  ipc::ScreenClient client(std::move(config), [&] { wakes++; });
  client.start();

  REQUIRE(server.accept(3000));
  auto first = server.next(3000);
  REQUIRE(first);
  CHECK(first->which_body == cadence_screen_v1_Envelope_hello_tag);
  CHECK(std::string(first->body.hello.version) == "test");
  REQUIRE(waitFor(client, ipc::ScreenClient::Event::Kind::Connected, 3000));
  CHECK(client.connected());

  ipc::OutEnvelope ready(cadence_screen_v1_Envelope_ready_tag);
  ready->body.ready.gate_open = true;
  ready->body.ready.device_name = ready.keep("TV");
  REQUIRE(server.send(ready.get()));
  auto got = waitFor(client, ipc::ScreenClient::Event::Kind::Message, 3000);
  REQUIRE(got);
  REQUIRE(got->envelope);
  CHECK(got->envelope->which_body == cadence_screen_v1_Envelope_ready_tag);
  CHECK(got->envelope->body.ready.gate_open);
  CHECK(std::string(got->envelope->body.ready.device_name) == "TV");
  CHECK(wakes > 0);

  ipc::OutEnvelope status(cadence_screen_v1_Envelope_screen_status_tag);
  status->body.screen_status.power = cadence_screen_v1_ScreenPower_SCREEN_POWER_ON;
  REQUIRE(client.send(status.get()));
  auto echoed = server.nextOf(cadence_screen_v1_Envelope_screen_status_tag, 3000);
  REQUIRE(echoed);

  // cadenced restarts: the client sees it go, and comes back with a fresh hello
  server.drop();
  REQUIRE(waitFor(client, ipc::ScreenClient::Event::Kind::Disconnected, 3000));
  REQUIRE(server.accept(3000));
  auto again = server.next(3000);
  REQUIRE(again);
  CHECK(again->which_body == cadence_screen_v1_Envelope_hello_tag);
  REQUIRE(waitFor(client, ipc::ScreenClient::Event::Kind::Connected, 3000));

  const auto t0 = std::chrono::steady_clock::now();
  client.stop();
  CHECK(std::chrono::steady_clock::now() - t0 < std::chrono::seconds(2));
  CHECK_FALSE(client.connected());
}

}  // namespace

TEST_CASE("the link over a socket path: hello first, messages, reconnect") {
  linkAndReconnect(test::socketPath("link"));
}

TEST_CASE("the link over an abstract socket (android)") {
  linkAndReconnect("@cadence-screen-test-" + std::to_string(::getpid()));
}

TEST_CASE("with no cadenced the client retries and sends nothing") {
  ipc::ClientConfig config;
  config.path = test::socketPath("absent");
  config.backoff = ipc::Backoff(10, 20, 10000);
  config.hello = hello;
  ipc::ScreenClient client(std::move(config), nullptr);
  client.start();
  std::this_thread::sleep_for(std::chrono::milliseconds(100));
  ipc::OutEnvelope status(cadence_screen_v1_Envelope_screen_status_tag);
  CHECK_FALSE(client.send(status.get()));
  CHECK(client.drain().empty());
  client.stop();
}

TEST_CASE("a frame that does not decode is counted and skipped") {
  const auto path = test::socketPath("bad");
  FakeCadenced server(path);
  ipc::ClientConfig config;
  config.path = path;
  config.hello = hello;
  config.pollMs = 20;
  ipc::ScreenClient client(std::move(config), nullptr);
  client.start();
  REQUIRE(server.accept(3000));
  REQUIRE(server.next(3000));
  REQUIRE(waitFor(client, ipc::ScreenClient::Event::Kind::Connected, 3000));
  // a raw frame nanopb refuses, then a good one
  const std::uint8_t junk[] = {0xff, 0xff, 0xff, 0xff, 0x0f};
  REQUIRE(server.sendRaw(junk));
  {
    ipc::OutEnvelope gate(cadence_screen_v1_Envelope_playback_gate_tag);
    gate->body.playback_gate.open = true;
    REQUIRE(server.send(gate.get()));
  }
  auto got = waitFor(client, ipc::ScreenClient::Event::Kind::Message, 3000);
  REQUIRE(got);
  CHECK(got->envelope->which_body == cadence_screen_v1_Envelope_playback_gate_tag);
  CHECK(client.badFrames() == 1);
  client.stop();
}
